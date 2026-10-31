package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/util"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type authDoneMsg struct {
	service string
	err     error
}

// historyImportMsg carries the result of a one-way tracker import
// (AniList/Trakt watched lists into the local history store). Quiet
// results come from the automatic startup sync: they toast only when
// something actually arrived and never surface errors.
type historyImportMsg struct {
	source             string
	imported, upgraded int
	skipped            int
	opID               int
	quiet              bool
	err                error
}
type traktCodeMsg struct {
	userCode, verificationURL, deviceCode string
	interval, expiresIn                   int
}

func (m *modelImpl) startTraktAuth() (tea.Model, tea.Cmd) {
	if m.traktClient == nil {
		return m, m.setStatusTimed(statusError, "Trakt integration not configured")
	}
	m.loading = true
	m.loadingText = "Requesting code..."
	return m, func() tea.Msg {
		userCode, verificationURL, deviceCode, interval, expiresIn, err := m.traktClient.StartDeviceAuth(m.appCtx)
		if err != nil {
			return authDoneMsg{service: "Trakt", err: err}
		}
		return traktCodeMsg{
			userCode:        userCode,
			verificationURL: verificationURL,
			deviceCode:      deviceCode,
			interval:        interval,
			expiresIn:       expiresIn,
		}
	}
}

func (m *modelImpl) onTraktCode(msg traktCodeMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingText = ""
	m.traktAuthActive = true
	m.traktUserCode = msg.userCode
	m.traktVerifyURL = msg.verificationURL
	m.traktDeviceCode = msg.deviceCode
	_ = util.OpenBrowser(msg.verificationURL)
	statusCmd := m.setStatusTimed(statusInfo, fmt.Sprintf("Enter code %s at %s", msg.userCode, msg.verificationURL))
	parentCtx := m.appCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(parentCtx)
	m.traktCancel = cancel
	pollCmd := m.pollTraktAuthCmd(ctx, msg.deviceCode, msg.interval, msg.expiresIn)
	return m, tea.Batch(statusCmd, pollCmd)
}

func (m *modelImpl) pollTraktAuthCmd(ctx context.Context, deviceCode string, interval, expiresIn int) tea.Cmd {
	return func() tea.Msg {
		err := m.traktClient.PollDeviceAuth(ctx, deviceCode, interval, expiresIn)
		return authDoneMsg{service: "Trakt", err: err}
	}
}

func (m *modelImpl) onAuthDone(msg authDoneMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingText = ""
	switch msg.service {
	case "Trakt":
		m.traktAuthActive = false
		m.traktUserCode = ""
		m.traktVerifyURL = ""
		m.traktDeviceCode = ""
		if m.traktCancel != nil {
			m.traktCancel()
			m.traktCancel = nil
		}
	case "AniList":
		m.anilistAuthURL = ""
		m.authInput.Blur()
	}
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		return m, m.setStatusTimed(statusError, fmt.Sprintf("%s auth failed: %s", msg.service, cleanErrorForUI(msg.err)))
	}
	m.saveSettings()
	return m, m.setStatusTimed(statusSuccess, fmt.Sprintf("%s connected successfully", msg.service))
}

// startHistoryImport fetches the focused tracker's watched list and
// merges it into the local history store: one-way, additive, and async
// so the UI never blocks on the network.
func (m *modelImpl) startHistoryImport(source string) (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, m.setStatusTimed(statusError, "Watch history unavailable")
	}
	switch source {
	case "anilist":
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			return m, m.setStatusTimed(statusWarn, "Connect AniList first (c)")
		}
	case "trakt":
		if m.traktClient == nil || !m.traktClient.IsAuthenticated() {
			return m, m.setStatusTimed(statusWarn, "Connect Trakt first (c)")
		}
	default:
		return m, nil
	}
	m.loading = true
	m.loadingText = fmt.Sprintf("Importing watched history from %s...", source)
	opID := m.newOpID()
	m.historyImportOpID = opID
	return m, tea.Batch(m.spinner.Tick, m.historyImportCmd(opID, source))
}

func (m *modelImpl) startHistoryExport() (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, m.setStatusTimed(statusError, "Watch history unavailable")
	}
	if (m.anilistClient == nil || !m.anilistClient.IsAuthenticated()) &&
		(m.traktClient == nil || !m.traktClient.IsAuthenticated()) {
		return m, m.setStatusTimed(statusWarn, "Connect AniList or Trakt first (c)")
	}
	entries := m.historyStore.All()
	anilistClient, traktClient := m.anilistClient, m.traktClient
	parentCtx := m.appCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	m.loading = true
	m.loadingText = "Syncing local history to connected accounts..."
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(parentCtx, 2*time.Minute)
		defer cancel()
		synced, skipped := exportHistory(ctx, entries, anilistClient, traktClient)
		return historyExportMsg{synced: synced, skipped: skipped}
	})
}

type animeHistoryProgress struct {
	media model.ResolvedMedia
	max   int
}

func exportHistory(ctx context.Context, entries []history.Entry, ani *scrobble.AniListClient, trakt *scrobble.TraktClient) (int, int) {
	synced, skipped := 0, 0
	type syncJob struct {
		name      string
		skipOnErr int
		run       func(context.Context) (int, error)
	}
	var jobs []syncJob
	var traktCandidates []scrobble.WatchedItem
	traktReady := trakt != nil && trakt.IsAuthenticated()
	traktRemote := make(map[string]struct{})
	if traktReady {
		watched, err := trakt.FetchWatchedHistory(ctx)
		if err != nil {
			tuiLog.Error("trakt history check failed; skipping export to avoid duplicates", "err", err)
			traktReady = false
		} else {
			for _, item := range watched {
				for _, key := range traktHistoryKeys(item) {
					traktRemote[key] = struct{}{}
				}
			}
		}
	}
	animeProgress := make(map[string]animeHistoryProgress)
	mangaProgress := make(map[string]scrobble.MangaProgress)
	for _, entry := range entries {
		if !historyEntryComplete(entry) || strings.TrimSpace(entry.Title) == "" {
			skipped++
			continue
		}
		switch entry.Mode {
		case string(provider.ModeAnime):
			if entry.Episode > 0 {
				idPart := strconv.Itoa(entry.AniListID)
				key := idPart
				if entry.AniListID <= 0 {
					key = strings.ToLower(strings.TrimSpace(entry.Title))
				}
				progress := animeProgress[key]
				if progress.max < entry.Episode {
					progress.max = entry.Episode
					progress.media = model.ResolvedMedia{SeriesTitle: entry.Title, AniListID: entry.AniListID, EpisodeNumber: entry.Episode}
				}
				animeProgress[key] = progress
			}
		case string(provider.ModeManga):
			chapter, ok := mangaScrobbleChapter(entry.MangaChapter)
			if !ok {
				chapter, ok = mangaScrobbleChapter(entry.EpisodeTitle)
			}
			if ok && entry.MangaPages > 0 && entry.MangaPage >= entry.MangaPages {
				key := strconv.Itoa(entry.AniListID)
				if entry.AniListID <= 0 {
					key = strings.ToLower(strings.TrimSpace(entry.Title))
				}
				if old, found := mangaProgress[key]; !found || old.Chapter < chapter {
					mangaProgress[key] = scrobble.MangaProgress{Title: entry.Title, AniListID: entry.AniListID, Chapter: chapter}
				}
			}
		}
		if !traktReady || entry.Mode == string(provider.ModeManga) || entry.Mode == string(provider.ModeLive) {
			if !traktReady && trakt != nil && trakt.IsAuthenticated() &&
				entry.Mode != string(provider.ModeManga) && entry.Mode != string(provider.ModeLive) &&
				(entry.MediaType == provider.MediaTypeMovie || entry.Episode > 0) {
				skipped++
			}
			continue
		}
		item := scrobble.WatchedItem{
			Title: entry.Title, Mode: entry.Mode, MediaType: entry.MediaType,
			Season: entry.Season, Episode: entry.Episode, TMDBID: entry.TMDBID,
			Complete: true, WatchedAt: entry.WatchedAt,
		}
		if entry.MediaType != provider.MediaTypeMovie && entry.Episode <= 0 {
			skipped++
			continue
		}
		alreadySynced := false
		for _, key := range traktHistoryKeys(item) {
			if _, ok := traktRemote[key]; ok {
				alreadySynced = true
				break
			}
		}
		if alreadySynced {
			skipped++
			continue
		}
		traktCandidates = append(traktCandidates, item)
		for _, key := range traktHistoryKeys(item) {
			traktRemote[key] = struct{}{}
		}
	}
	if trakt != nil && trakt.IsAuthenticated() && !traktReady {
		skipped += len(traktCandidates)
		traktCandidates = nil
	}
	if ani != nil && ani.IsAuthenticated() {
		for _, progress := range animeProgress {
			progress.media.EpisodeNumber = progress.max
			media := progress.media
			jobs = append(jobs, syncJob{
				name:      media.SeriesTitle,
				skipOnErr: 1,
				run: func(ctx context.Context) (int, error) {
					if err := ani.UpdateProgress(ctx, media); err != nil {
						return 0, err
					}
					return 1, nil
				},
			})
		}
		for _, progress := range mangaProgress {
			jobs = append(jobs, syncJob{
				name:      progress.Title,
				skipOnErr: 1,
				run: func(ctx context.Context) (int, error) {
					if err := ani.UpdateMangaProgress(ctx, progress); err != nil {
						return 0, err
					}
					return 1, nil
				},
			})
		}
	} else {
		skipped += len(animeProgress) + len(mangaProgress)
	}
	if len(traktCandidates) > 0 {
		candidates := traktCandidates
		jobs = append(jobs, syncJob{
			name:      "Trakt history",
			skipOnErr: len(candidates),
			run: func(ctx context.Context) (int, error) {
				return trakt.SyncWatchedHistory(ctx, candidates)
			},
		})
	}
	jobQueue := make(chan syncJob, len(jobs))
	for _, job := range jobs {
		jobQueue <- job
	}
	close(jobQueue)
	var mu sync.Mutex
	var workers sync.WaitGroup
	for range min(4, len(jobs)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobQueue {
				count, err := job.run(ctx)
				if err != nil {
					tuiLog.Error("connected account history sync failed", "title", job.name, "err", err)
					mu.Lock()
					synced += count
					skipped += max(0, job.skipOnErr-count)
					mu.Unlock()
					continue
				}
				mu.Lock()
				synced += count
				mu.Unlock()
			}
		}()
	}
	workers.Wait()
	return synced, skipped
}

func traktHistoryKeys(item scrobble.WatchedItem) []string {
	if item.MediaType == provider.MediaTypeMovie {
		keys := []string{"movie:title:" + strings.ToLower(strings.TrimSpace(item.Title))}
		if item.TMDBID > 0 {
			keys = append(keys, "movie:tmdb:"+strconv.Itoa(item.TMDBID))
		}
		return keys
	}
	coordinate := strconv.Itoa(item.Season) + ":" + strconv.Itoa(item.Episode)
	keys := []string{"episode:title:" + strings.ToLower(strings.TrimSpace(item.Title)) + ":" + coordinate}
	if item.TMDBID > 0 {
		keys = append(keys, "episode:tmdb:"+strconv.Itoa(item.TMDBID)+":"+coordinate)
	}
	return keys
}

func historyEntryComplete(entry history.Entry) bool {
	return entry.Complete || entry.PercentComplete >= 0.95 ||
		(entry.DurationSecs > 0 && entry.PositionSecs/entry.DurationSecs >= 0.95)
}

// historyImportCmd runs the fetch+merge off the UI thread: tracker calls
// take their own timeouts, and the merge is plain store writes.
func (m *modelImpl) historyImportCmd(opID int, source string) tea.Cmd {
	return func() tea.Msg {
		parentCtx := m.appCtx
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(parentCtx, 90*time.Second)
		defer cancel()

		items, err := m.fetchSourceItems(ctx, source)
		if err != nil {
			return historyImportMsg{source: source, opID: opID, err: err}
		}
		imported, upgraded, skipped := service.ImportWatched(m.historyStore, items)
		return historyImportMsg{source: source, opID: opID, imported: imported, upgraded: upgraded, skipped: skipped}
	}
}

// fetchSourceItems pulls every watch list one tracker owns: anime and
// manga from AniList, episodes and movies from Trakt.
func (m *modelImpl) fetchSourceItems(ctx context.Context, source string) ([]scrobble.WatchedItem, error) {
	switch source {
	case "anilist":
		anime, err := m.anilistClient.FetchWatchedList(ctx)
		if err != nil {
			return nil, err
		}
		manga, err := m.anilistClient.FetchMangaList(ctx)
		if err != nil {
			return nil, err
		}
		return append(anime, manga...), nil
	case "trakt":
		return m.traktClient.FetchWatchedHistory(ctx)
	default:
		return nil, fmt.Errorf("unknown import source %q", source)
	}
}

// autoImportSources lists the trackers able to feed the startup sync:
// connected clients only, so the sync never prompts or errors.
func (m *modelImpl) autoImportSources() []string {
	var out []string
	if m.anilistClient != nil && m.anilistClient.IsAuthenticated() {
		out = append(out, "anilist")
	}
	if m.traktClient != nil && m.traktClient.IsAuthenticated() {
		out = append(out, "trakt")
	}
	return out
}

// autoHistoryImportCmd syncs every connected tracker into the local
// store at startup, quietly: one background command, one result. It
// returns nil when there is nothing to sync from.
func (m *modelImpl) autoHistoryImportCmd() tea.Cmd {
	if m.historyStore == nil {
		return nil
	}
	sources := m.autoImportSources()
	if len(sources) == 0 {
		return nil
	}
	opID := m.newOpID()
	m.historyImportOpID = opID
	return func() tea.Msg {
		parentCtx := m.appCtx
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(parentCtx, 180*time.Second)
		defer cancel()

		var (
			imported, upgraded int
			skipped            int
		)
		for _, source := range sources {
			items, err := m.fetchSourceItems(ctx, source)
			if err != nil {
				// A source that errored simply contributes nothing;
				// the next startup (or a manual import) retries it.
				tuiLog.Debug("startup sync fetch failed", "source", source, "err", err)
				continue
			}
			i, u, s := service.ImportWatched(m.historyStore, items)
			imported, upgraded, skipped = imported+i, upgraded+u, skipped+s
		}
		return historyImportMsg{source: "sync", opID: opID, quiet: true, imported: imported, upgraded: upgraded, skipped: skipped}
	}
}

// onHistoryImport reports the import outcome: a toast with counts on
// success, a status line on failure. Stale results are ignored. Quiet
// (startup) results stay silent unless something arrived, and never
// surface errors — an offline start must not nag.
func (m *modelImpl) onHistoryImport(msg historyImportMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.historyImportOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		if msg.quiet {
			tuiLog.Debug("startup sync failed", "source", msg.source, "err", msg.err)
			return m, nil
		}
		logging.Error("history import failed", "source", msg.source, "err", msg.err)
		return m, m.setStatusTimed(statusError, fmt.Sprintf("%s import failed: %s", msg.source, cleanErrorForUI(msg.err)))
	}
	tuiLog.Info("history import done", "source", msg.source, "imported", msg.imported, "upgraded", msg.upgraded, "skipped", msg.skipped)
	if msg.quiet && msg.imported+msg.upgraded == 0 {
		return m, nil
	}
	parts := []string{fmt.Sprintf("%d new", msg.imported)}
	if msg.upgraded > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", msg.upgraded))
	}
	if msg.skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d already here", msg.skipped))
	}
	return m, m.setToast(fmt.Sprintf("Imported from %s: %s", msg.source, strings.Join(parts, ", ")), ToastSuccess)
}

func (m *modelImpl) startAniListAuth() (tea.Model, tea.Cmd) {
	if m.anilistClient == nil {
		return m, m.setStatusTimed(statusError, "AniList integration not configured")
	}
	url := m.anilistClient.AuthURL()
	m.anilistAuthURL = url
	m.authInput.Focus()
	m.authInput.SetValue("")
	_ = util.OpenBrowser(url)
	return m, tea.Batch(m.setStatusTimed(statusInfo, "Browser opened — authorize Kari and paste code"), textinput.Blink)
}

func (m *modelImpl) triggerScrobble(entry history.Entry, media *model.ResolvedMedia) {
	if !trackableEntry(entry) || media == nil {
		return
	}
	if entry.Complete {
		if m.anilistClient != nil && entry.Mode == string(provider.ModeAnime) {
			go func() {
				if err := m.anilistClient.UpdateProgress(context.Background(), *media); err != nil {
					tuiLog.Error("anilist episode progress sync failed", "title", media.SeriesTitle, "episode", media.EpisodeNumber, "err", err)
					return
				}
				tuiLog.Info("anilist episode progress synced", "title", media.SeriesTitle, "episode", media.EpisodeNumber)
			}()
		}
		if m.traktClient != nil {
			go func() {
				var err error
				if entry.MediaType == provider.MediaTypeMovie {
					err = m.traktClient.ScrobbleMovie(context.Background(), *media, 100)
				} else {
					err = m.traktClient.ScrobbleEpisode(context.Background(), *media, 100)
				}
				if err != nil {
					tuiLog.Error("trakt watch sync failed", "title", media.SeriesTitle, "episode", media.EpisodeNumber, "err", err)
					return
				}
				tuiLog.Info("trakt watch synced", "title", media.SeriesTitle, "episode", media.EpisodeNumber)
			}()
		}
	}
}

var skipProviderOptions = []string{"hybrid", "skipdb", "introdb", "anime-skip", "aniskip", "off"}

// trackableEntry reports whether a history entry belongs in tracker
// sync at all. Live has no tracker equivalent (ephemeral schedules, no
// progress model), so it stays out of both directions: never scrobbled
// out, never imported in (the merge layer skips it too).
func trackableEntry(entry history.Entry) bool {
	return entry.Mode != string(provider.ModeLive) && entry.MediaType != provider.MediaTypeLive
}

func cycleSkipProvider(current string, reverse bool) string {
	idx := 0
	for i, opt := range skipProviderOptions {
		if strings.EqualFold(opt, current) {
			idx = i
			break
		}
	}
	step := 1
	if reverse {
		step = -1
	}
	next := (idx + step + len(skipProviderOptions)) % len(skipProviderOptions)
	return skipProviderOptions[next]
}
