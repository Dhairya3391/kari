package service

// Remote watch-history import. Fetchers (internal/scrobble) translate
// tracker lists into WatchedItems; ImportWatched folds them into the
// local store without ever clobbering local progress:
//
//   - unknown key → inserted as-is (remote is the only record);
//   - local incomplete + remote complete → upgraded to complete, keeping
//     the local position/duration and the later timestamp;
//   - manga → one position entry per series (chapter numbers are
//     fractional strings providers don't share, so ranges are never
//     expanded); remote-newer chapters move the marker forward, the
//     local page within a chapter always wins;
//   - local complete → skipped, whatever the remote says;
//   - live → skipped entirely, both directions.
//
// Items arrive newest-first and repeat plays collapse onto the first
// (latest) record, so the batch is sorted defensively before merging.

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"kari/internal/history"
	"kari/internal/provider"
	"kari/internal/scrobble"
)

// ImportWatched merges remote watch items into the local history store,
// returning imported, upgraded, and skipped counts for the result toast.
//
// Matching is id-first and title-second: an AniList catalog id (anime/
// manga going forward), a TMDB id (movies/TV), the exact title key, then
// a punctuation-insensitive title. The first hit wins, so a renamed
// title still merges into the same local entry instead of duplicating
// it. Missing ids on the local side are backfilled on insert and
// upgrade, never on skip.
func ImportWatched(store *history.Store, items []scrobble.WatchedItem) (imported, upgraded, skipped int) {
	if store == nil {
		return 0, 0, len(items)
	}

	ordered := append([]scrobble.WatchedItem(nil), items...)
	slices.SortStableFunc(ordered, func(a, b scrobble.WatchedItem) int {
		if a.WatchedAt.After(b.WatchedAt) {
			return -1
		}
		if a.WatchedAt.Before(b.WatchedAt) {
			return 1
		}
		return 0
	})

	idx := newImportIndex(store.All())
	for _, item := range ordered {
		// Live has no tracker equivalent and stays out of history sync.
		if item.Mode == string(provider.ModeLive) || item.MediaType == provider.MediaTypeLive {
			skipped++
			continue
		}
		local, ok := idx.find(store, item)
		if item.Mode == string(provider.ModeManga) {
			i, u, s := importMangaItem(store, idx, item, local, ok)
			imported, upgraded, skipped = imported+i, upgraded+u, skipped+s
			continue
		}
		switch {
		case !ok:
			entry := watchedItemEntry(item)
			if err := store.Upsert(entry); err != nil {
				skipped++
				continue
			}
			idx.add(entry)
			imported++
		case !local.Complete && item.Complete:
			upgrade := local
			// Upsert derives Complete from position/duration, so a
			// bare Complete=true on a half-watched position would
			// flip back to false — pin the position at the end.
			if upgrade.DurationSecs > 0 {
				upgrade.PositionSecs = upgrade.DurationSecs
			}
			upgrade.Complete = true
			backfillIDs(&upgrade, item)
			if item.WatchedAt.After(upgrade.WatchedAt) {
				upgrade.WatchedAt = item.WatchedAt
			}
			if err := store.Upsert(upgrade); err != nil {
				skipped++
				continue
			}
			idx.add(upgrade)
			upgraded++
		default:
			skipped++
		}
	}
	return imported, upgraded, skipped
}

// importIndex matches remote items to local entries without trusting
// titles: catalog ids first, a normalized title second. First entry wins
// (the store lists newest first), and freshly inserted entries join the
// index so repeats inside one batch collapse.
type importIndex struct {
	byAniList map[aniKey]history.EntryKey
	byTMDB    map[tmdbKey]history.EntryKey
	byTitle   map[titleKey]history.EntryKey
	byBase    map[titleKey]history.EntryKey // year-stripped title fallback
}

type aniKey struct {
	id              int
	season, episode int
}

type tmdbKey struct {
	id              int
	media           string
	season, episode int
}

type titleKey struct {
	title           string
	mode, media     string
	season, episode int
}

func newImportIndex(entries []history.Entry) *importIndex {
	idx := &importIndex{
		byAniList: make(map[aniKey]history.EntryKey),
		byTMDB:    make(map[tmdbKey]history.EntryKey),
		byTitle:   make(map[titleKey]history.EntryKey),
		byBase:    make(map[titleKey]history.EntryKey),
	}
	for _, e := range entries {
		idx.add(e)
	}
	return idx
}

func (x *importIndex) add(e history.Entry) {
	key := history.EntryKey{
		Title:     e.Title,
		Mode:      e.Mode,
		MediaType: e.MediaType,
		Season:    e.Season,
		Episode:   e.Episode,
	}
	// History entries and their keys can disagree on the title slot
	// (legacy rows); the key is the identity, the entry the payload.
	if e.Key.Title != "" || e.Key.Mode != "" {
		key = e.Key
	}
	if e.AniListID > 0 {
		k := aniKey{id: e.AniListID, season: e.Season, episode: e.Episode}
		if _, seen := x.byAniList[k]; !seen {
			x.byAniList[k] = key
		}
	}
	if e.TMDBID > 0 {
		k := tmdbKey{id: e.TMDBID, media: e.MediaType, season: e.Season, episode: e.Episode}
		if _, seen := x.byTMDB[k]; !seen {
			x.byTMDB[k] = key
		}
	}
	k := titleKey{title: normTitle(e.Title), mode: e.Mode, media: e.MediaType, season: e.Season, episode: e.Episode}
	if k.title != "" {
		if _, seen := x.byTitle[k]; !seen {
			x.byTitle[k] = key
		}
	}
	// Year-stripped fallback for catalog disambiguation ("Show" ==
	// "Show (2011)"); the year check happens at lookup, so different
	// years still map to distinct slots here.
	if base, year := splitYear(e.Title); year != "" {
		bk := titleKey{title: normTitle(base), mode: e.Mode, media: e.MediaType, season: e.Season, episode: e.Episode}
		if bk.title != "" {
			if _, seen := x.byBase[bk]; !seen {
				x.byBase[bk] = key
			}
		}
	}
}

// find resolves a remote item to its local entry: exact title key first
// (live store view), then AniList id, TMDB id, normalized title.
func (x *importIndex) find(store *history.Store, item scrobble.WatchedItem) (history.Entry, bool) {
	if local, ok := store.Get(history.EntryKey{
		Title:     item.Title,
		Mode:      item.Mode,
		MediaType: item.MediaType,
		Season:    item.Season,
		Episode:   item.Episode,
	}); ok {
		return local, true
	}
	if item.AniListID > 0 {
		if key, ok := x.byAniList[aniKey{id: item.AniListID, season: item.Season, episode: item.Episode}]; ok {
			if local, ok := store.Get(key); ok {
				return local, true
			}
		}
	}
	if item.TMDBID > 0 {
		if key, ok := x.byTMDB[tmdbKey{id: item.TMDBID, media: item.MediaType, season: item.Season, episode: item.Episode}]; ok {
			if local, ok := store.Get(key); ok {
				return local, true
			}
		}
	}
	if key, ok := x.byTitle[titleKey{title: normTitle(item.Title), mode: item.Mode, media: item.MediaType, season: item.Season, episode: item.Episode}]; ok {
		if local, ok := store.Get(key); ok {
			return local, true
		}
	}
	// Year-stripped fallback for catalog disambiguation years: remote
	// "Show (2011)" still finds local "Show", and vice versa. Both
	// sides carrying different years never match, so remakes stay
	// apart; a yearless side always matches (nothing to conflict).
	if base, year := splitYear(item.Title); base != "" {
		stripped := titleKey{title: normTitle(base), mode: item.Mode, media: item.MediaType, season: item.Season, episode: item.Episode}
		if key, ok := x.byTitle[stripped]; ok {
			if local, ok := store.Get(key); ok {
				return local, true
			}
		}
		if key, ok := x.byBase[stripped]; ok {
			if local, ok := store.Get(key); ok {
				if _, localYear := splitYear(local.Title); year == "" || localYear == "" || year == localYear {
					return local, true
				}
			}
		}
	}
	return history.Entry{}, false
}

// backfillIDs copies catalog ids the local entry lacks from the remote
// item, so future imports match by id even when titles drift.
func backfillIDs(entry *history.Entry, item scrobble.WatchedItem) {
	if entry.TMDBID == 0 && item.TMDBID > 0 {
		entry.TMDBID = item.TMDBID
	}
	if entry.AniListID == 0 && item.AniListID > 0 {
		entry.AniListID = item.AniListID
	}
}

// normTitle folds a title for fuzzy matching: case and punctuation
// go away ("One-Piece" == "one piece"), letters and digits stay.
func normTitle(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// yearSuffixRE matches AniList-style disambiguation years: "Hunter x
// Hunter (2011)". Bare numbers are never stripped ("2012" stays whole).
var yearSuffixRE = regexp.MustCompile(`^(.*)\s+\(((?:19|20)\d{2})\)\s*$`)

// splitYear separates a trailing disambiguation year from the title.
// Base titles match when one side omits the year both carry the same
// one; different years ("Show (1999)" vs "Show (2011)") never match.
func splitYear(s string) (base, year string) {
	if m := yearSuffixRE.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		return strings.TrimSpace(m[1]), m[2]
	}
	return strings.TrimSpace(s), ""
}

// importMangaItem merges one remote manga position. Local history keeps a
// single entry per series (chapter in MangaChapter, page in
// MangaPage/MangaPages), so the merge compares chapters: a remote-newer
// chapter moves the marker to its first page, the same chapter keeps the
// local page (closing it when the remote says finished), and anything
// older or unorderable is skipped. The local entry arrives pre-resolved
// through the shared index, so id-matched and fuzzy titles work here too.
func importMangaItem(store *history.Store, idx *importIndex, item scrobble.WatchedItem, local history.Entry, ok bool) (imported, upgraded, skipped int) {
	if !ok {
		if strings.TrimSpace(item.Chapter) == "" && !item.Complete {
			return 0, 0, 1
		}
		entry := watchedItemEntry(item)
		entry.EpisodeTitle = strings.TrimSpace(item.Chapter)
		entry.MangaChapter = strings.TrimSpace(item.Chapter)
		if err := store.Upsert(entry); err != nil {
			return 0, 0, 1
		}
		idx.add(entry)
		return 1, 0, 0
	}

	localChapter := mangaProgressChapter(local)
	remoteChapter := strings.TrimSpace(item.Chapter)
	if localChapter == "" && remoteChapter != "" {
		// Blind local marker (chapter never recorded): adopt the
		// remote position — information, not a clobber.
		upgrade := local
		upgrade.EpisodeTitle = remoteChapter
		upgrade.MangaChapter = remoteChapter
		upgrade.MangaPage = 0
		upgrade.MangaPages = 0
		upgrade.PositionSecs = 0
		upgrade.DurationSecs = 0
		upgrade.Complete = item.Complete
		backfillIDs(&upgrade, item)
		if item.WatchedAt.After(upgrade.WatchedAt) {
			upgrade.WatchedAt = item.WatchedAt
		}
		if err := store.Upsert(upgrade); err != nil {
			return 0, 0, 1
		}
		idx.add(upgrade)
		return 0, 1, 0
	}
	switch cmp := compareChapters(localChapter, remoteChapter); {
	case cmp > 0:
		// Remote is ahead: move to its first page (or finished).
		upgrade := local
		upgrade.EpisodeTitle = strings.TrimSpace(item.Chapter)
		upgrade.MangaChapter = strings.TrimSpace(item.Chapter)
		upgrade.MangaPage = 0
		upgrade.MangaPages = 0
		upgrade.PositionSecs = 0
		upgrade.DurationSecs = 0
		upgrade.Complete = item.Complete
		backfillIDs(&upgrade, item)
		if item.WatchedAt.After(upgrade.WatchedAt) {
			upgrade.WatchedAt = item.WatchedAt
		}
		if err := store.Upsert(upgrade); err != nil {
			return 0, 0, 1
		}
		idx.add(upgrade)
		return 0, 1, 0
	case cmp == 0 && !local.Complete && item.Complete:
		// Same chapter, remote finished it: close the chapter. The
		// page advances to the end with it — completion means read,
		// and Upsert would otherwise flip Complete back to false
		// from the half-read position.
		upgrade := local
		if upgrade.MangaPages > 0 {
			upgrade.MangaPage = upgrade.MangaPages
			upgrade.PositionSecs = float64(upgrade.MangaPages)
			upgrade.DurationSecs = float64(upgrade.MangaPages)
		}
		upgrade.Complete = true
		backfillIDs(&upgrade, item)
		if item.WatchedAt.After(upgrade.WatchedAt) {
			upgrade.WatchedAt = item.WatchedAt
		}
		if err := store.Upsert(upgrade); err != nil {
			return 0, 0, 1
		}
		idx.add(upgrade)
		return 0, 1, 0
	default:
		return 0, 0, 1
	}
}

// mangaProgressChapter reads the chapter a manga entry points at,
// preferring the explicit v2 field over the legacy title slot.
func mangaProgressChapter(e history.Entry) string {
	if c := strings.TrimSpace(e.MangaChapter); c != "" {
		return c
	}
	return strings.TrimSpace(e.EpisodeTitle)
}

// compareChapters orders chapter numbers: +1 remote ahead, -1 local
// ahead, 0 equal or unorderable (fractional suffixes providers don't
// share must never move the marker backwards).
func compareChapters(local, remote string) int {
	local, remote = strings.TrimSpace(local), strings.TrimSpace(remote)
	if local == remote {
		return 0
	}
	l, lerr := strconv.ParseFloat(local, 64)
	r, rerr := strconv.ParseFloat(remote, 64)
	if lerr != nil || rerr != nil {
		return 0
	}
	switch {
	case r > l:
		return 1
	case r < l:
		return -1
	default:
		return 0
	}
}

// watchedItemEntry converts a remote watch item to a local history entry.
// Imported entries carry no playback position (only completion), so resume
// treats them as boundary markers: complete ones as watched, the
// in-progress one as the continue point. Catalog ids travel along so
// later imports match by id.
func watchedItemEntry(item scrobble.WatchedItem) history.Entry {
	watchedAt := item.WatchedAt
	if watchedAt.IsZero() {
		watchedAt = time.Now()
	}
	return history.Entry{
		Key: history.EntryKey{
			Title:     item.Title,
			Mode:      item.Mode,
			MediaType: item.MediaType,
			Season:    item.Season,
			Episode:   item.Episode,
		},
		Title:     item.Title,
		Season:    item.Season,
		Episode:   item.Episode,
		WatchedAt: watchedAt,
		Complete:  item.Complete,
		Mode:      item.Mode,
		MediaType: item.MediaType,
		TMDBID:    item.TMDBID,
		AniListID: item.AniListID,
	}
}
