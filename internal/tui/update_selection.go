package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/downloader"
	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/service"
)

func (m *modelImpl) selectSeries(idx int) (tea.Model, tea.Cmd) {
	tuiLog.Debug("series selected", "index", idx, "resultsLen", len(m.seriesResults))
	if idx < 0 || idx >= len(m.seriesResults) {
		m.setStatus(statusError, "Series selection out of range")
		return m, nil
	}
	// One pipeline at a time: a series pick while anything loads waits with
	// visible feedback instead of orphaning the active operation.
	if !m.guardLoad() {
		return m, nil
	}
	m.selectedSeries = &m.seriesResults[idx]
	m.searchIndex = idx
	m.selectedEpisode = nil // Reset episode selection for new series
	m.selectedEpisodes = make(map[int]struct{})
	m.episodeIndex = 0

	// Titles from manga/comic providers flow into the chapter listing
	// and the fullscreen reader instead of episode resolution. The
	// branch is on the MangaSource capability, never on names or modes.
	if m.isMangaProvider(m.selectedSeries.Provider) {
		tuiLog.Debug("manga result; loading chapters", "title", m.selectedSeries.Title)
		return m.selectMangaSeries(idx)
	}

	// Movie-titled and live results skip the episode listing and resolve directly,
	// unless the selected provider declares (via provider.MovieEpisodeFlow)
	// that it needs a fetched episode ID even for movies.
	if (m.selectedSeries.MediaType == provider.MediaTypeMovie || m.selectedSeries.MediaType == provider.MediaTypeLive || m.selectedSeries.Type == provider.ModeLive || m.appMode == provider.ModeLive) && !m.registry.RequiresEpisodeListForMovies(m.selectedSeries.Provider) {
		tuiLog.Debug("direct result; resolving directly", "title", m.selectedSeries.Title)
		m.selectedEpisode = &provider.Episode{
			Title: m.selectedSeries.Title,
			ID:    m.selectedSeries.ID,
		}
		m.loadingText = "Preparing playback..."
		m.resolved = nil
		m.rawSubtitles = nil
		m.subtitleResolverUsed = ""
		m.subtitleLangUsed = ""
		m.subtitleSourceUsed = ""
		m.manualPlaybackSelected = false
		m.resolveAttempts = 0
		m.preferredRepairAttempts = 0
		m.clearPreviewPoster()
		m.beginProviderWait(m.appMode)
		m.autoPlayAfterResolve = false
		opID := m.newOpID()
		m.resolveOpID = opID
		m.pushView(viewPreview)
		return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, *m.selectedEpisode, nil, nil), m.prefetchPreviewPoster())
	}

	tuiLog.Debug("loading episodes", "title", m.selectedSeries.Title)
	m.loading = true
	m.loadingText = "Loading episodes..."
	m.resolved = nil
	m.rawSubtitles = nil
	m.clearPreviewPoster()
	m.setStatus(statusInfo, "")
	opID := m.newOpID()
	m.episodesOpID = opID
	return m, tea.Batch(m.spinner.Tick, m.episodesCmd(opID, *m.selectedSeries))
}

func (m *modelImpl) selectEpisode(idx int) (tea.Model, tea.Cmd) {
	return m.startEpisodeResolution(idx, false)
}

func (m *modelImpl) selectedSeriesIndex() int {
	if item, ok := m.seriesList.SelectedItem().(rowItem); ok {
		return item.index
	}
	return m.seriesList.Index()
}

// visibleSeriesResults narrows the search results by the / filter query,
// keeping each row's original index aligned. An empty query returns the
// full list unchanged. The list widget and the renderer share it so the
// cursor position always addresses the same rows on screen.
func (m *modelImpl) visibleSeriesResults() ([]provider.SearchResult, []int) {
	q := strings.ToLower(strings.TrimSpace(m.resultsFilter))
	if q == "" {
		idxs := make([]int, len(m.seriesResults))
		for i := range idxs {
			idxs[i] = i
		}
		return m.seriesResults, idxs
	}
	var out []provider.SearchResult
	var outIdx []int
	for i, r := range m.seriesResults {
		hay := strings.ToLower(r.Title) + " " + strings.ToLower(strings.TrimSpace(r.Year))
		if strings.Contains(hay, q) {
			out = append(out, r)
			outIdx = append(outIdx, i)
		}
	}
	return out, outIdx
}

// refilterSeriesList rebuilds the results widget from the current /
// filter, preserving original indices so selection and posters still
// resolve into the full result list.
func (m *modelImpl) refilterSeriesList() {
	visible, idxs := m.visibleSeriesResults()
	m.seriesList.SetItems(seriesToItemsIndexed(visible, idxs))
	if len(visible) > 0 {
		m.seriesList.Select(0)
	}
}

func (m *modelImpl) selectedEpisodeIndex() int {
	if item, ok := m.episodeList.SelectedItem().(rowItem); ok {
		return item.index
	}
	return m.episodeList.Index()
}

func (m *modelImpl) playNextEpisode() (tea.Model, tea.Cmd) {
	idx, ok := m.nextEpisodeIndex()
	if !ok {
		m.setStatus(statusWarn, "No next episode available")
		return m, nil
	}
	return m.startEpisodeResolution(idx, true)
}

func (m *modelImpl) startEpisodeResolution(idx int, autoPlay bool) (tea.Model, tea.Cmd) {
	// Completion-chained callers (history resume steps, movie
	// auto-select, autoplay) arrive with settled state; user picks
	// while anything loads wait with visible feedback instead of
	// abandoning the running pipeline.
	if !m.guardLoad() {
		return m, nil
	}
	tuiLog.Debug("episode selected", "index", idx, "resultsLen", len(m.episodeResults))
	if idx < 0 || idx >= len(m.episodeResults) {
		m.setStatus(statusError, "Episode selection out of range")
		return m, nil
	}
	m.selectedEpisode = &m.episodeResults[idx]
	m.episodeIndex = idx
	m.loading = true
	m.setStatus(statusInfo, "")
	if autoPlay {
		m.loadingText = "Preparing next episode..."
	} else {
		m.loadingText = "Preparing playback..."
	}
	if src, ok := m.selectedPlaybackSource(); ok {
		m.prevSourceLanguage = src.Language
		m.prevSourceQuality = service.SourceQuality(src.Quality)
	}
	m.resolved = nil
	m.rawSubtitles = nil
	m.manualPlaybackSelected = false
	m.resolveAttempts = 0
	m.preferredRepairAttempts = 0
	// Drop the previous episode's ranking immediately so Preview never
	// flashes stale sources before the first snapshot re-ranks.
	m.rankedSources = nil
	m.previewSelectedIndex = 0
	m.subtitleResolverUsed = ""
	m.subtitleLangUsed = ""
	m.subtitleSourceUsed = ""
	m.clearPreviewPoster()
	m.beginProviderWait(m.appMode)
	m.autoPlayAfterResolve = autoPlay
	series := provider.SearchResult{}
	if m.selectedSeries != nil {
		series = *m.selectedSeries
	}
	opID := m.newOpID()
	m.resolveOpID = opID
	m.pushView(viewPreview)
	tuiLog.Debug("resolving playback", "series", series.Title, "episode", m.selectedEpisode.Title, "autoPlay", autoPlay)
	return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, series, *m.selectedEpisode, nil, nil), m.prefetchPreviewPoster())
}
func (m *modelImpl) searchCmd(opID int, query string) tea.Cmd {
	if m.searchQueryCancel != nil {
		m.searchQueryCancel()
		m.searchQueryCancel = nil
	}
	parent := m.appCtx
	if parent == nil {
		parent = context.Background()
	}
	searchCtx, cancel := context.WithCancel(parent)
	m.searchQueryCancel = cancel

	mode := m.appMode
	cacheable := !m.modeFeatures().NoCachedSearches
	return func() tea.Msg {
		cacheKey := fmt.Sprintf("%s:%s", mode, query)
		if cacheable {
			if entry, ok := m.searchCache.Get(cacheKey); ok {
				logging.Debug("search cache hit", "mode", mode, "query", query)
				return searchDoneMsg{results: entry.results, usedQuery: entry.usedQuery, warnings: entry.warnings, opID: opID, err: nil}
			}
		}

		logging.Debug("search start", "mode", mode, "query", query)
		results, usedQuery, warnings, err := m.mediaService.Search(searchCtx, mode, query)
		if err == nil && cacheable {
			m.searchCache.Set(cacheKey, searchCacheEntry{
				results:   results,
				usedQuery: usedQuery,
				warnings:  warnings,
			})
		}

		return searchDoneMsg{results: results, usedQuery: usedQuery, warnings: warnings, opID: opID, err: err}
	}
}

func (m *modelImpl) episodesCmd(opID int, series provider.SearchResult) tea.Cmd {
	mode := m.appMode
	audioMode := m.audioMode
	return func() tea.Msg {
		results, err := m.mediaService.FetchEpisodes(m.appCtx, mode, series, audioMode)
		return episodesDoneMsg{results: results, opID: opID, err: err}
	}
}

func (m *modelImpl) historyContinueEpisodesCmd(opID int, group history.Group, series provider.SearchResult, mode provider.ContentType) tea.Cmd {
	audioMode := m.audioMode
	return func() tea.Msg {
		results, err := m.mediaService.FetchEpisodes(m.appCtx, mode, series, audioMode)
		return historyContinueEpisodesMsg{group: group, results: results, opID: opID, err: err}
	}
}

// resolveCmd resolves sources, skipping exclude providers that already
// delivered: retries only ask the failed ones again, never the
// successful. Fresh loads pass nil.
func (m *modelImpl) resolveCmd(opID int, series provider.SearchResult, episode provider.Episode, exclude, retry []string) tea.Cmd {
	tuiLog.Debug("resolve starting", "opID", opID, "series", series.Title, "episode", episode.Title, "exclude", exclude, "retry", retry)
	mode := m.appMode

	return tea.Batch(
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.appCtx, 60*time.Second)
			defer cancel()

			onResult := func(resolved model.ResolvedMedia) {
				select {
				case m.resolveChan <- resolveProgressMsg{resolved: resolved, opID: opID}:
				case <-ctx.Done():
				}
			}

			resolved, err := m.mediaService.Resolve(ctx, mode, series, episode, onResult, service.ResolveOptions{Exclude: exclude, Retry: retry})

			// Deliver the completion marker through the same channel the
			// subscription reads (mirroring download/batch) so the
			// subscription goroutine that consumes it terminates instead of
			// blocking forever on <-resolveChan after the last progress
			// message. Returning the marker directly here would orphan that
			// goroutine, and stale ones would then steal the first message
			// of the next resolve.
			select {
			case m.resolveChan <- resolveDoneMsg{resolved: resolved, opID: opID, err: err}:
			case <-ctx.Done():
			}

			return resolveWorkerDoneMsg{}
		},
		m.resolveSubscription(),
	)
}

func (m *modelImpl) invalidateSubtitleSync() {
	m.subtitleOpID = 0
	m.subtitleResolverUsed = ""
	m.subtitleLangUsed = ""
	m.subtitleSourceUsed = ""
	if m.resolved != nil {
		m.resolved.SelectedSubtitle = nil
	}
}

// triggerSubtitleSync fetches subtitles in the background whenever the
// selected source, its URL, or the preferred language changed. The fetch
// never gates playback: play starts from sources alone and the track
// attaches when it arrives (onSubtitleDone).
func (m *modelImpl) triggerSubtitleSync() tea.Cmd {
	if m.resolved == nil {
		return nil
	}
	if !m.subtitlesWanted() {
		m.invalidateSubtitleSync()
		return nil
	}
	if m.subtitleService == nil {
		return nil
	}
	src, ok := m.selectedPlaybackSource()
	if !ok {
		return nil
	}

	targetChanged := src.Resolver != m.subtitleResolverUsed ||
		m.subtitleLanguage != m.subtitleLangUsed ||
		src.URL != m.subtitleSourceUsed
	if !targetChanged {
		return nil
	}

	m.subtitleOpID = 0
	m.resolved.SelectedSubtitle = nil
	m.subtitleResolverUsed = src.Resolver
	m.subtitleLangUsed = m.subtitleLanguage
	m.subtitleSourceUsed = src.URL
	opID := m.newOpID()
	m.subtitleOpID = opID
	mediaForFetch := *m.resolved
	mediaForFetch.Subtitles = append([]model.SubtitleTrack{}, m.rawSubtitles...)
	return m.subtitleFetchCmd(opID, mediaForFetch, src.Resolver)
}

func (m *modelImpl) subtitleFetchCmd(opID int, resolved model.ResolvedMedia, preferredResolver string) tea.Cmd {
	preferredLang := m.subtitleLanguage
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.appCtx, 10*time.Second)
		defer cancel()
		track, err := m.subtitleService.Fetch(ctx, resolved, preferredLang, preferredResolver)
		if err != nil {
			tuiLog.Warn("subtitle fetch failed", "err", err)
		}
		return subtitleDoneMsg{track: track, opID: opID, err: err}
	}
}

func (m *modelImpl) playCmd(opID int) tea.Cmd {
	if m.resolved == nil {
		return m.playCmdWithStartTime(opID, 0)
	}
	return m.playCmdWithStartTime(opID, m.resolved.StartTime)
}

func (m *modelImpl) playCmdWithStartTime(opID int, startTime float64) tea.Cmd {
	sources := m.orderedPlaybackSources()
	if m.resolved == nil || len(sources) == 0 {
		return func() tea.Msg {
			return playDoneMsg{opID: opID, err: fmt.Errorf("no playback source matches the current filters")}
		}
	}
	resolved := *m.resolved
	resolved.StartTime = startTime
	playerName := m.selectedPlayerName()
	providerName := ""
	if src, ok := m.selectedPlaybackSource(); ok {
		providerName = src.Resolver
		if providerName == "" {
			providerName = src.Quality
		}
	}
	return func() tea.Msg {
		tuiLog.Debug("play starting", "opID", opID, "media", resolved.DisplayTitle(), "provider", providerName, "sourcesCount", len(sources), "startTime", startTime)
		tuiLog.Debug("launching playback", "media", resolved.DisplayTitle(), "player", playerName, "hasSubtitles", resolved.SubtitlePath() != "")
		result, err := m.players.PlayWithSources(sources, resolved, playerName)
		return playDoneMsg{opID: opID, provider: providerName, result: result, err: err}
	}
}

func (m *modelImpl) downloadCmd(opID int, resolved model.ResolvedMedia) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(m.appCtx)
		outputDir, title := m.downloadService.OrganizedPath(resolved)

		go func() {
			defer cancel()
			err := m.downloadService.Download(ctx, resolved, func(dp downloader.DownloadProgress) {
				select {
				case m.downloadChan <- downloadProgressMsg{
					opID:       opID,
					progress:   dp.Percent,
					totalSize:  dp.TotalSize,
					speed:      dp.Speed,
					downloaded: dp.Downloaded,
					eta:        dp.ETA,
				}:
				default:
				}
			})
			// Unlike progress ticks above, the completion message must
			// never be dropped: nothing else clears m.loading or
			// re-enables starting another download, so a non-blocking
			// send here (channel full from a backlog of progress
			// updates the UI hasn't drained yet) would leave the app
			// stuck showing "Downloading..." forever. It only sends
			// once per download, so blocking briefly is harmless. The
			// ctx.Done() case only fires if the user explicitly
			// cancelled (Stop/Quit) — in that case the UI already reset
			// itself synchronously, nobody will read this value, and
			// without this escape the goroutine would block forever.
			select {
			case m.downloadChan <- downloadDoneMsg{opID: opID, err: err}:
			case <-ctx.Done():
			}
		}()

		return downloadStartedMsg{
			opID:      opID,
			cancel:    cancel,
			outputDir: outputDir,
			title:     title,
		}
	}
}

func (m *modelImpl) resumePendingDownloadCmd(opID int, job service.DownloadJob) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(m.appCtx)
		meta := model.ResolvedMedia{
			SeriesTitle: job.Series.Title, EpisodeTitle: job.Episode.Title,
			MediaType: job.Series.MediaType, Year: job.Series.Year,
			TMDBID: job.Series.TMDBID, SeasonNumber: job.Episode.Season,
			EpisodeNumber: job.Episode.Episode,
		}
		outputDir, title := m.downloadService.OrganizedPath(meta)
		go func() {
			defer cancel()
			if job.SearchOnResume {
				results, _, _, err := m.mediaService.Search(ctx, job.Mode, job.Series.Title)
				if err == nil {
					wanted := strings.ToLower(strings.Join(strings.Fields(job.Series.Title), " "))
					matched := false
					for _, result := range results {
						if strings.ToLower(strings.Join(strings.Fields(result.Title), " ")) == wanted {
							job.Series = result
							job.SearchOnResume = false
							matched = true
							_ = m.downloadService.SavePendingJob(job)
							break
						}
					}
					if !matched {
						err = fmt.Errorf("could not find an exact provider match for %q", job.Series.Title)
					}
				}
				if err != nil {
					select {
					case m.downloadChan <- downloadDoneMsg{opID: opID, err: err}:
					case <-ctx.Done():
					}
					return
				}
			}
			resolved, err := m.mediaService.Resolve(ctx, job.Mode, job.Series, job.Episode, nil, service.ResolveOptions{})
			if err == nil {
				err = m.downloadService.Download(ctx, resolved, func(dp downloader.DownloadProgress) {
					select {
					case m.downloadChan <- downloadProgressMsg{opID: opID, progress: dp.Percent, totalSize: dp.TotalSize, speed: dp.Speed, downloaded: dp.Downloaded, eta: dp.ETA}:
					default:
					}
				})
			}
			select {
			case m.downloadChan <- downloadDoneMsg{opID: opID, err: err}:
			case <-ctx.Done():
			}
		}()
		return downloadStartedMsg{opID: opID, cancel: cancel, outputDir: outputDir, title: title}
	}
}

func selectedSeriesTitle(series *provider.SearchResult) string {
	if series == nil {
		return ""
	}
	return series.Title
}

func selectedSeriesProvider(series *provider.SearchResult) string {
	if series == nil {
		return ""
	}
	return series.Provider
}

func selectedEpisodeTitle(episode *provider.Episode) string {
	if episode == nil {
		return ""
	}
	return episode.Title
}

func historyGroupKeyByString(entries []history.Entry, keyStr string) history.GroupKey {
	if key, ok := history.BuildGroupLookup(entries)[keyStr]; ok {
		return key
	}
	return history.GroupKey{}
}

func shouldFetchNextEpisode(group history.Group) bool {
	if group.HasIncomplete || !group.HasComplete {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(group.MediaType), provider.MediaTypeMovie) || strings.EqualFold(strings.TrimSpace(group.MediaType), provider.MediaTypeLive) || group.Mode == string(provider.ModeLive) {
		return false
	}
	return group.FarthestComplete.Episode > 0
}

// modeForHistoryEntry maps a history entry to its content mode. Entries
// recorded before modes existed store only a MediaType, so legacy values
// (including the plural "movies" form) are matched here against the
// persisted vocabulary.
func modeForHistoryEntry(entry history.Entry) provider.ContentType {
	if entry.Mode != "" {
		return provider.ContentType(entry.Mode)
	}
	switch strings.ToLower(strings.TrimSpace(entry.MediaType)) {
	case provider.MediaTypeMovie, string(provider.ModeMovies):
		return provider.ModeMovies
	case string(provider.ModeAnime):
		return provider.ModeAnime
	case string(provider.ModeCartoon):
		return provider.ModeCartoon
	case provider.MediaTypeManga:
		return provider.ModeManga
	case provider.MediaTypeLive:
		return provider.ModeLive
	default:
		return provider.ModeTV
	}
}

func nextEpisodeAfterEntry(episodes []provider.Episode, entry history.Entry) (int, bool) {
	for idx, episode := range episodes {
		if episodeAfterHistoryEntry(episode, entry) {
			return idx, true
		}
	}
	return 0, false
}

func episodeAfterHistoryEntry(episode provider.Episode, entry history.Entry) bool {
	if entry.Season > 0 {
		if episode.Season > entry.Season {
			return true
		}
		if episode.Season > 0 && episode.Season < entry.Season {
			return false
		}
	}
	if entry.Episode > 0 && episode.Episode > entry.Episode {
		if entry.Season <= 0 || episode.Season == entry.Season || episode.Season <= 0 {
			return true
		}
	}
	return false
}

func (m *modelImpl) startSearchFromInput() (tea.Model, tea.Cmd) {
	if !m.guardLoad() {
		return m, nil
	}
	q := strings.TrimSpace(m.queryInput.Value())
	if q == "" && !m.modeFeatures().AllowEmptyQuery {
		m.setStatus(statusWarn, "Enter a query")
		return m, nil
	}
	m.searchQuery = q
	m.loading = true
	if q == "" {
		m.loadingText = "Loading library..."
	} else {
		m.loadingText = "Searching..."
	}
	m.setStatus(statusInfo, "")
	m.resolved = nil
	m.clearPreviewPoster()
	m.selectedPlayback = 0
	opID := m.newOpID()
	m.searchOpID = opID
	return m, tea.Batch(m.spinner.Tick, m.searchCmd(opID, q))
}

func (m *modelImpl) clearActiveFilter() bool {
	switch m.activeView {
	case viewSearch:
		if m.resultsFiltering || strings.TrimSpace(m.resultsFilter) != "" {
			m.resultsFiltering = false
			m.resultsFilter = ""
			m.refilterSeriesList()
			m.clearStatus()
			return true
		}
		if m.seriesList.SettingFilter() || m.seriesList.IsFiltered() || strings.TrimSpace(m.seriesList.FilterValue()) != "" {
			m.seriesList.ResetFilter()
			m.clearStatus()
			return true
		}
	case viewEpisodes:
		if m.episodeFiltering || strings.TrimSpace(m.episodeFilter) != "" {
			m.episodeFiltering = false
			m.episodeFilter = ""
			m.clampEpisodeIndex()
			m.clearStatus()
			return true
		}
		if m.episodeList.SettingFilter() || m.episodeList.IsFiltered() || strings.TrimSpace(m.episodeList.FilterValue()) != "" {
			m.episodeList.ResetFilter()
			m.clearStatus()
			return true
		}
	case viewChapters:
		if m.chapterList.SettingFilter() || m.chapterList.IsFiltered() || strings.TrimSpace(m.chapterList.FilterValue()) != "" {
			m.chapterList.ResetFilter()
			m.clearStatus()
			return true
		}
	}
	return false
}

func (m *modelImpl) exitInputMode() bool {
	if m.activeView == viewSearch && m.queryInput.Focused() {
		m.queryInput.Blur()
		m.clearStatus()
		return true
	}
	if m.activeView == viewEpisodes && m.selectMode {
		m.selectMode = false
		m.clearStatus()
		return true
	}
	if m.activeView == viewHistory && (m.confirmDelete || m.confirmClearHistory) {
		m.confirmDelete = false
		m.confirmClearHistory = false
		m.clearStatus()
		return true
	}
	// Settings-screen text inputs (custom accent hex, AniList auth code)
	// need the same treatment: handleGlobalKeys' Back case runs before
	// updateSettings ever sees the key, so without this, Esc while typing
	// here falls through to goBackOne() and exits the whole Settings
	// screen instead of just closing the input.
	if m.activeView == viewSettings {
		if m.audioPickerOpen {
			m.audioPickerOpen = false
			m.saveSettings()
			m.clearStatus()
			return true
		}
		if m.editingAccentHex {
			m.editingAccentHex = false
			m.hexInput.Blur()
			m.clearStatus()
			return true
		}
		if m.anilistAuthURL != "" {
			m.anilistAuthURL = ""
			m.authInput.Blur()
			m.clearStatus()
			return true
		}
		if m.traktAuthActive {
			m.traktAuthActive = false
			m.traktUserCode = ""
			m.traktVerifyURL = ""
			m.traktDeviceCode = ""
			if m.traktCancel != nil {
				m.traktCancel()
				m.traktCancel = nil
			}
			m.clearStatus()
			return true
		}
	}
	return false
}
