package service

// Pins for the remote-import merge policy: additive only, upgrades for
// newly-completed episodes, never a clobber of local progress.

import (
	"path/filepath"
	"testing"
	"time"

	"kari/internal/history"
	"kari/internal/scrobble"
)

func importTestStore(t *testing.T) *history.Store {
	t.Helper()
	store, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestImportWatchedInsertsMissing(t *testing.T) {
	store := importTestStore(t)

	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "Frieren", Mode: "anime", MediaType: "anime", Season: 1, Episode: 1, Complete: true, WatchedAt: time.Now()},
		{Title: "Frieren", Mode: "anime", MediaType: "anime", Season: 1, Episode: 2, Complete: false, WatchedAt: time.Now()},
	})
	if imported != 2 || upgraded != 0 || skipped != 0 {
		t.Fatalf("got %d/%d/%d, want 2/0/0", imported, upgraded, skipped)
	}
	if _, ok := store.Get(history.EntryKey{Title: "Frieren", Mode: "anime", MediaType: "anime", Season: 1, Episode: 2}); !ok {
		t.Error("in-progress episode must be stored as the continue point")
	}
	// In-progress imports stay incomplete after the Upsert normalization.
	entry, _ := store.Get(history.EntryKey{Title: "Frieren", Mode: "anime", MediaType: "anime", Season: 1, Episode: 2})
	if entry.Complete {
		t.Error("in-progress import must not be marked complete")
	}
}

func TestImportWatchedIsIdempotent(t *testing.T) {
	store := importTestStore(t)
	items := []scrobble.WatchedItem{
		{Title: "The Boys", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1, Complete: true, WatchedAt: time.Now()},
	}

	if imported, _, _ := ImportWatched(store, items); imported != 1 {
		t.Fatalf("first import = %d, want 1", imported)
	}
	imported, upgraded, skipped := ImportWatched(store, items)
	if imported != 0 || upgraded != 0 || skipped != 1 {
		t.Fatalf("second import = %d/%d/%d, want 0/0/1", imported, upgraded, skipped)
	}
}

func TestImportWatchedUpgradesIncomplete(t *testing.T) {
	store := importTestStore(t)
	key := history.EntryKey{Title: "The Boys", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1}
	if err := store.Upsert(history.Entry{
		Key: key, Title: "The Boys", Season: 1, Episode: 1,
		PositionSecs: 600, DurationSecs: 3600,
		WatchedAt: time.Now().Add(-time.Hour),
		Mode:      "tv", MediaType: "tv",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "The Boys", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1, Complete: true, WatchedAt: time.Now()},
	})
	if imported != 0 || upgraded != 1 || skipped != 0 {
		t.Fatalf("got %d/%d/%d, want 0/1/0", imported, upgraded, skipped)
	}
	entry, _ := store.Get(key)
	if !entry.Complete {
		t.Errorf("remote-complete must upgrade local in-progress, got %+v", entry)
	}
}

func TestImportWatchedNeverClobbersComplete(t *testing.T) {
	store := importTestStore(t)
	key := history.EntryKey{Title: "Dune", Mode: "movies", MediaType: "movie"}
	before := history.Entry{
		Key: key, Title: "Dune", Complete: true,
		WatchedAt: time.Now().Add(-time.Hour),
		Mode:      "movies", MediaType: "movie",
	}
	if err := store.Upsert(before); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// A remote record for the same key changes nothing.
	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "Dune", Mode: "movies", MediaType: "movie", Complete: true, WatchedAt: time.Now()},
	})
	if imported != 0 || upgraded != 0 || skipped != 1 {
		t.Fatalf("got %d/%d/%d, want 0/0/1", imported, upgraded, skipped)
	}
	after, _ := store.Get(key)
	if !after.WatchedAt.Equal(before.WatchedAt) {
		t.Error("complete local entry must keep its timestamp")
	}
}

func TestImportWatchedNilStore(t *testing.T) {
	items := []scrobble.WatchedItem{
		{Title: "X", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1, Complete: true},
	}
	imported, upgraded, skipped := ImportWatched(nil, items)
	if imported != 0 || upgraded != 0 || skipped != 1 {
		t.Fatalf("got %d/%d/%d, want 0/0/1", imported, upgraded, skipped)
	}
}

func TestImportWatchedRepeatPlaysCollapse(t *testing.T) {
	store := importTestStore(t)
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-time.Hour)
	imported, _, _ := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "The Boys", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1, Complete: true, WatchedAt: newer},
		{Title: "The Boys", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1, Complete: true, WatchedAt: older},
	})
	if imported != 1 {
		t.Fatalf("repeat plays must collapse to one entry, imported = %d", imported)
	}
	entry, _ := store.Get(history.EntryKey{Title: "The Boys", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1})
	if !entry.WatchedAt.Equal(newer) {
		t.Errorf("latest play must win, got %v", entry.WatchedAt)
	}
}

func mangaItem(title, chapter string, complete bool) scrobble.WatchedItem {
	return scrobble.WatchedItem{
		Title: title, Mode: "manga", MediaType: "manga",
		Chapter: chapter, Complete: complete, WatchedAt: time.Now(),
	}
}

func mangaKey(title string) history.EntryKey {
	return history.EntryKey{Title: title, Mode: "manga", MediaType: "manga"}
}

func TestImportMangaInsertsPosition(t *testing.T) {
	store := importTestStore(t)
	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		mangaItem("One Piece", "12", false),
	})
	if imported != 1 || upgraded != 0 || skipped != 0 {
		t.Fatalf("got %d/%d/%d, want 1/0/0", imported, upgraded, skipped)
	}
	entry, ok := store.Get(mangaKey("One Piece"))
	if !ok {
		t.Fatal("manga position must be stored")
	}
	if entry.MangaChapter != "12" || entry.Complete {
		t.Errorf("entry = %+v, want chapter 12 in-progress", entry)
	}
}

func TestImportMangaMovesForward(t *testing.T) {
	store := importTestStore(t)
	if err := store.Upsert(history.Entry{
		Key: mangaKey("One Piece"), Title: "One Piece",
		EpisodeTitle: "10", MangaChapter: "10", MangaPage: 30, MangaPages: 50,
		PositionSecs: 30, DurationSecs: 50,
		Mode: "manga", MediaType: "manga",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		mangaItem("One Piece", "12", false),
	})
	if imported != 0 || upgraded != 1 || skipped != 0 {
		t.Fatalf("got %d/%d/%d, want 0/1/0", imported, upgraded, skipped)
	}
	entry, _ := store.Get(mangaKey("One Piece"))
	if entry.MangaChapter != "12" {
		t.Errorf("chapter = %q, want 12", entry.MangaChapter)
	}
	if entry.MangaPage != 0 || entry.Complete {
		t.Errorf("moved marker must restart at page one in-progress, got %+v", entry)
	}
}

func TestImportMangaKeepsLocalPage(t *testing.T) {
	store := importTestStore(t)
	if err := store.Upsert(history.Entry{
		Key: mangaKey("One Piece"), Title: "One Piece",
		EpisodeTitle: "12", MangaChapter: "12", MangaPage: 30, MangaPages: 50,
		PositionSecs: 30, DurationSecs: 50,
		Mode: "manga", MediaType: "manga",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Same chapter: local page precision wins, even when older.
	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		mangaItem("One Piece", "12", false),
		mangaItem("One Piece", "10", true),
	})
	if imported != 0 || upgraded != 0 || skipped != 2 {
		t.Fatalf("got %d/%d/%d, want 0/0/2", imported, upgraded, skipped)
	}
	entry, _ := store.Get(mangaKey("One Piece"))
	if entry.MangaPage != 30 {
		t.Errorf("page = %d, want local 30", entry.MangaPage)
	}
}

func TestImportMangaCompletesSameChapter(t *testing.T) {
	store := importTestStore(t)
	if err := store.Upsert(history.Entry{
		Key: mangaKey("Berserk"), Title: "Berserk",
		EpisodeTitle: "50", MangaChapter: "50", MangaPage: 20, MangaPages: 40,
		PositionSecs: 20, DurationSecs: 40,
		Mode: "manga", MediaType: "manga",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		mangaItem("Berserk", "50", true),
	})
	if imported != 0 || upgraded != 1 || skipped != 0 {
		t.Fatalf("got %d/%d/%d, want 0/1/0", imported, upgraded, skipped)
	}
	entry, _ := store.Get(mangaKey("Berserk"))
	if !entry.Complete || entry.MangaPage != 40 {
		t.Errorf("must close the chapter at page 40, got %+v", entry)
	}
}

func TestImportMangaSkipsUnorderable(t *testing.T) {
	store := importTestStore(t)
	if err := store.Upsert(history.Entry{
		Key: mangaKey("Weird"), Title: "Weird",
		EpisodeTitle: "12a", MangaChapter: "12a",
		Mode: "manga", MediaType: "manga",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Fractional suffixes providers don't share: never move backwards.
	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		mangaItem("Weird", "13", false),
	})
	if imported != 0 || upgraded != 0 || skipped != 1 {
		t.Fatalf("got %d/%d/%d, want 0/0/1", imported, upgraded, skipped)
	}
}

func TestImportSkipsLive(t *testing.T) {
	store := importTestStore(t)
	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "Match", Mode: "live", MediaType: "live", Complete: true, WatchedAt: time.Now()},
		{Title: "Match", Mode: "tv", MediaType: "live", Season: 1, Episode: 1, Complete: true, WatchedAt: time.Now()},
		{Title: "Match", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1, Complete: true, WatchedAt: time.Now()},
	})
	if imported != 1 || upgraded != 0 || skipped != 2 {
		t.Fatalf("got %d/%d/%d, want 1/0/2", imported, upgraded, skipped)
	}
	if n := len(store.All()); n != 1 {
		t.Fatalf("store has %d entries, want 1", n)
	}
}

func TestImportMatchesAniListIDAcrossTitles(t *testing.T) {
	store := importTestStore(t)
	// Local watch recorded under the provider title, with the catalog
	// id new watches now persist.
	if err := store.Upsert(history.Entry{
		Key:   history.EntryKey{Title: "Frieren", Mode: "anime", MediaType: "anime", Season: 1, Episode: 1},
		Title: "Frieren", Season: 1, Episode: 1, Complete: true,
		Mode: "anime", MediaType: "anime", AniListID: 154587,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Remote uses the romaji title for the same catalog id: must merge,
	// not duplicate.
	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "Sousou no Frieren", Mode: "anime", MediaType: "anime", Season: 1, Episode: 1, AniListID: 154587, Complete: true, WatchedAt: time.Now()},
	})
	if imported != 0 || upgraded != 0 || skipped != 1 {
		t.Fatalf("got %d/%d/%d, want 0/0/1", imported, upgraded, skipped)
	}
	if n := len(store.All()); n != 1 {
		t.Fatalf("store has %d entries, want 1 (no duplicate)", n)
	}
}

func TestImportMatchesTMDBAcrossTitles(t *testing.T) {
	store := importTestStore(t)
	if err := store.Upsert(history.Entry{
		Key:   history.EntryKey{Title: "The Boys (2024)", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1},
		Title: "The Boys (2024)", Season: 1, Episode: 1, Complete: true,
		Mode: "tv", MediaType: "tv", TMDBID: 123,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "The Boys", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1, TMDBID: 123, Complete: true, WatchedAt: time.Now()},
	})
	if imported != 0 || upgraded != 0 || skipped != 1 {
		t.Fatalf("got %d/%d/%d, want 0/0/1", imported, upgraded, skipped)
	}
	if n := len(store.All()); n != 1 {
		t.Fatalf("store has %d entries, want 1 (no duplicate)", n)
	}
}

func TestImportTMDBNamespacesMedia(t *testing.T) {
	store := importTestStore(t)
	// TMDB movie 123 and TV show 123 are different entities sharing a
	// number: the media type keeps them apart.
	imported, _, _ := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "Dune", Mode: "movies", MediaType: "movie", TMDBID: 123, Complete: true, WatchedAt: time.Now()},
		{Title: "Some Show", Mode: "tv", MediaType: "tv", Season: 2, Episode: 3, TMDBID: 123, Complete: true, WatchedAt: time.Now()},
	})
	if imported != 2 {
		t.Fatalf("imported = %d, want 2 (no cross-media merge)", imported)
	}
}

func TestImportMatchesNormalizedTitle(t *testing.T) {
	store := importTestStore(t)
	if err := store.Upsert(history.Entry{
		Key:   history.EntryKey{Title: "One-Piece", Mode: "anime", MediaType: "anime", Season: 1, Episode: 1},
		Title: "One-Piece", Season: 1, Episode: 1, Complete: true,
		Mode: "anime", MediaType: "anime",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Punctuation and case differ, letters don't: same show.
	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "One Piece", Mode: "anime", MediaType: "anime", Season: 1, Episode: 1, Complete: true, WatchedAt: time.Now()},
	})
	if imported != 0 || upgraded != 0 || skipped != 1 {
		t.Fatalf("got %d/%d/%d, want 0/0/1", imported, upgraded, skipped)
	}
	if n := len(store.All()); n != 1 {
		t.Fatalf("store has %d entries, want 1 (no duplicate)", n)
	}
}

func TestImportBackfillsIDs(t *testing.T) {
	store := importTestStore(t)
	// Insert carries the remote ids along.
	imported, _, _ := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "Frieren", Mode: "anime", MediaType: "anime", Season: 1, Episode: 1, AniListID: 154587, Complete: true, WatchedAt: time.Now()},
		{Title: "Dune", Mode: "movies", MediaType: "movie", TMDBID: 456, Complete: true, WatchedAt: time.Now()},
	})
	if imported != 2 {
		t.Fatalf("imported = %d, want 2", imported)
	}
	ep, _ := store.Get(history.EntryKey{Title: "Frieren", Mode: "anime", MediaType: "anime", Season: 1, Episode: 1})
	if ep.AniListID != 154587 {
		t.Errorf("AniListID = %d, want 154587", ep.AniListID)
	}
	mv, _ := store.Get(history.EntryKey{Title: "Dune", Mode: "movies", MediaType: "movie"})
	if mv.TMDBID != 456 {
		t.Errorf("TMDBID = %d, want 456", mv.TMDBID)
	}

	// Upgrade backfills ids the local entry lacks.
	if err := store.Upsert(history.Entry{
		Key:   history.EntryKey{Title: "The Boys", Mode: "tv", MediaType: "tv", Season: 1, Episode: 2},
		Title: "The Boys", Season: 1, Episode: 2,
		PositionSecs: 100, DurationSecs: 3600,
		Mode: "tv", MediaType: "tv",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	_, upgraded, _ := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "The Boys", Mode: "tv", MediaType: "tv", Season: 1, Episode: 2, TMDBID: 123, Complete: true, WatchedAt: time.Now()},
	})
	if upgraded != 1 {
		t.Fatalf("upgraded = %d, want 1", upgraded)
	}
	entry, _ := store.Get(history.EntryKey{Title: "The Boys", Mode: "tv", MediaType: "tv", Season: 1, Episode: 2})
	if entry.TMDBID != 123 || !entry.Complete {
		t.Errorf("entry = %+v, want tmdb 123 complete", entry)
	}
}

func TestImportSkipDoesNotChurn(t *testing.T) {
	store := importTestStore(t)
	before := history.Entry{
		Key:   history.EntryKey{Title: "Dune", Mode: "movies", MediaType: "movie"},
		Title: "Dune", Complete: true, WatchedAt: time.Now().Add(-time.Hour),
		Mode: "movies", MediaType: "movie",
	}
	if err := store.Upsert(before); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Skipped entries are left byte-identical: no id backfill, no
	// timestamp touch, so repeated syncs stay write-quiet.
	_, _, skipped := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "Dune", Mode: "movies", MediaType: "movie", TMDBID: 456, Complete: true, WatchedAt: time.Now()},
	})
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	after, _ := store.Get(history.EntryKey{Title: "Dune", Mode: "movies", MediaType: "movie"})
	if after.TMDBID != 0 || !after.WatchedAt.Equal(before.WatchedAt) {
		t.Errorf("skipped entry must be untouched, got %+v", after)
	}
}

func TestSplitYear(t *testing.T) {
	for _, tc := range []struct{ in, base, year string }{
		{"Hunter x Hunter (2011)", "Hunter x Hunter", "2011"},
		{"Show (1999)", "Show", "1999"},
		{"2012", "2012", ""},
		{"2001: A Space Odyssey", "2001: A Space Odyssey", ""},
		{"Show", "Show", ""},
		{"Show (11)", "Show (11)", ""},
	} {
		base, year := splitYear(tc.in)
		if base != tc.base || year != tc.year {
			t.Errorf("splitYear(%q) = (%q, %q), want (%q, %q)", tc.in, base, year, tc.base, tc.year)
		}
	}
}

func TestImportMatchesYearSuffixedTitle(t *testing.T) {
	store := importTestStore(t)
	// The real-data case: local "Hunter x Hunter" ep1 in progress,
	// AniList english "Hunter x Hunter (2011)" finished.
	if err := store.Upsert(history.Entry{
		Key:   history.EntryKey{Title: "Hunter x Hunter", Mode: "anime", MediaType: "anime", Season: 1, Episode: 1},
		Title: "Hunter x Hunter", Season: 1, Episode: 1,
		PositionSecs: 100, DurationSecs: 1400,
		Mode: "anime", MediaType: "anime",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "Hunter x Hunter (2011)", Mode: "anime", MediaType: "anime", Season: 1, Episode: 1, AniListID: 11061, Complete: true, WatchedAt: time.Now()},
	})
	if imported != 0 || upgraded != 1 || skipped != 0 {
		t.Fatalf("got %d/%d/%d, want 0/1/0", imported, upgraded, skipped)
	}
	if n := len(store.All()); n != 1 {
		t.Fatalf("store has %d entries, want 1 (no duplicate)", n)
	}
	entry, _ := store.Get(history.EntryKey{Title: "Hunter x Hunter", Mode: "anime", MediaType: "anime", Season: 1, Episode: 1})
	if !entry.Complete || entry.AniListID != 11061 {
		t.Errorf("entry = %+v, want complete with backfilled id", entry)
	}
}

func TestImportYearConflictStaysApart(t *testing.T) {
	store := importTestStore(t)
	if err := store.Upsert(history.Entry{
		Key:   history.EntryKey{Title: "Show (1999)", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1},
		Title: "Show (1999)", Season: 1, Episode: 1, Complete: true,
		Mode: "tv", MediaType: "tv",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Different year = different show: must not merge.
	imported, _, _ := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "Show (2011)", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1, Complete: true, WatchedAt: time.Now()},
	})
	if imported != 1 {
		t.Fatalf("imported = %d, want 1 (separate entry)", imported)
	}
	if n := len(store.All()); n != 2 {
		t.Fatalf("store has %d entries, want 2", n)
	}
}

func TestImportYearlessRemoteMatchesYearLocal(t *testing.T) {
	store := importTestStore(t)
	if err := store.Upsert(history.Entry{
		Key:   history.EntryKey{Title: "Show (2011)", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1},
		Title: "Show (2011)", Season: 1, Episode: 1, Complete: true,
		Mode: "tv", MediaType: "tv",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Remote without a year can't conflict: merges into the local row.
	imported, _, skipped := ImportWatched(store, []scrobble.WatchedItem{
		{Title: "Show", Mode: "tv", MediaType: "tv", Season: 1, Episode: 1, Complete: true, WatchedAt: time.Now()},
	})
	if imported != 0 || skipped != 1 {
		t.Fatalf("got %d/0/%d, want 0/0/1", imported, skipped)
	}
}

func TestImportMangaAdoptsBlindMarker(t *testing.T) {
	store := importTestStore(t)
	// Local manga entry that never recorded its chapter (the real
	// One Piece case): the remote position is information, not a
	// clobber, so it is adopted.
	if err := store.Upsert(history.Entry{
		Key: mangaKey("One Piece"), Title: "One Piece",
		Mode: "manga", MediaType: "manga",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	imported, upgraded, skipped := ImportWatched(store, []scrobble.WatchedItem{
		mangaItem("One Piece", "1100", false),
	})
	if imported != 0 || upgraded != 1 || skipped != 0 {
		t.Fatalf("got %d/%d/%d, want 0/1/0", imported, upgraded, skipped)
	}
	entry, _ := store.Get(mangaKey("One Piece"))
	if entry.MangaChapter != "1100" || entry.Complete {
		t.Errorf("entry = %+v, want chapter 1100 in-progress", entry)
	}
}
