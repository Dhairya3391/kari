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
	logging.Debugf("selectSeries: index=%d results_len=%d", idx, len(m.seriesResults))
	if idx < 0 || idx >= len(m.seriesResults) {
		m.setStatus(statusError, "Series selection out of range")
		return m, nil
	}
	m.selectedSeries = &m.seriesResults[idx]
	m.searchIndex = idx
	m.selectedEpisode = nil // Reset episode selection for new series
	m.selectedEpisodes = make(map[int]struct{})
	m.episodeIndex = 0

	if m.selectedSeries.MediaType == "movie" && m.selectedSeries.Provider != "miruro" {
		logging.Debugf("selectSeries: movie detected, resolving playback directly for %q", m.selectedSeries.Title)
		m.selectedEpisode = &model.EpisodeResult{
			Title: m.selectedSeries.Title,
			Kind:  "movie",
		}
		m.loading = true
		m.loadingText = "Preparing playback..."
		m.resolved = nil
		m.rawSubtitles = nil
		m.clearPreviewPoster()
		opID := m.newOpID()
		m.resolveOpID = opID
		m.pushView(viewPreview)
		return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, *m.selectedEpisode))
	}

	if direct, ok := directEpisodeForResult(*m.selectedSeries); ok {
		logging.Debugf("selectSeries: found direct episode for %q", m.selectedSeries.Title)
		m.selectedEpisode = &direct
		m.loading = true
		m.loadingText = "Preparing playback..."
		m.resolved = nil
		m.rawSubtitles = nil
		m.clearPreviewPoster()
		opID := m.newOpID()
		m.resolveOpID = opID
		m.pushView(viewPreview)
		return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, direct))
	}

	logging.Debugf("selectSeries: loading episodes for %q", m.selectedSeries.Title)
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
	if m.loading {
		return m, nil
	}
	logging.Debugf("selectEpisode: index=%d results_len=%d", idx, len(m.episodeResults))
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
		m.prevSourceQuality = service.SourceQuality(src.Label)
	} else {
		m.prevSourceLanguage = ""
		m.prevSourceQuality = 0
	}
	m.resolved = nil
	m.rawSubtitles = nil
	m.clearPreviewPoster()
	m.autoPlayAfterResolve = autoPlay
	series := model.SearchResult{}
	if m.selectedSeries != nil {
		series = *m.selectedSeries
	}
	opID := m.newOpID()
	m.resolveOpID = opID
	m.pushView(viewPreview)
	logging.Debugf("selectEpisode: resolving playback for series=%q episode=%q autoPlay=%t", series.Title, m.selectedEpisode.Title, autoPlay)
	return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, series, *m.selectedEpisode))
}
func (m *modelImpl) searchCmd(opID int, query string) tea.Cmd {
	mode := m.appMode
	return func() tea.Msg {
		modeKey := string(mode)
		cacheKey := fmt.Sprintf("%s:%s", modeKey, query)
		cacheable := mode != provider.ModeJellyfin
		if cacheable {
			if entry, ok := m.searchCache.Get(cacheKey); ok {
				logging.Debugf("search cache hit mode=%s query=%q", modeKey, query)
				return searchDoneMsg{results: entry.results, usedQuery: entry.usedQuery, warnings: entry.warnings, opID: opID, err: nil}
			}
		}

		logging.Debugf("search start mode=%s query=%q", modeKey, query)
		results, usedQuery, warnings, err := m.mediaService.Search(m.appCtx, mode, query)
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

func (m *modelImpl) episodesCmd(opID int, series model.SearchResult) tea.Cmd {
	mode := m.appMode
	audioMode := m.audioMode
	return func() tea.Msg {
		results, err := m.mediaService.FetchEpisodes(m.appCtx, mode, series, audioMode)
		return episodesDoneMsg{results: results, opID: opID, err: err}
	}
}

func (m *modelImpl) historyContinueEpisodesCmd(opID int, group history.Group, series model.SearchResult, mode provider.ContentType) tea.Cmd {
	audioMode := m.audioMode
	return func() tea.Msg {
		results, err := m.mediaService.FetchEpisodes(m.appCtx, mode, series, audioMode)
		return historyContinueEpisodesMsg{group: group, results: results, opID: opID, err: err}
	}
}

func (m *modelImpl) resolveCmd(opID int, series model.SearchResult, episode model.EpisodeResult) tea.Cmd {
	logging.Debugf("resolveCmd: opID=%d series=%q episode=%q", opID, series.Title, episode.Title)
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

			resolved, err := m.mediaService.Resolve(ctx, mode, series, episode, onResult)

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

// triggerSubtitleSync (re-)fetches subtitles if either the currently
// selected playback source's provider, or the preferred subtitle language,
// no longer matches what the last fetch targeted — e.g. the user switched
// sources with tab/shift+tab, a quality/language filter change moved the
// default selection to a different provider, or the subtitle language
// setting itself changed. It's a no-op if nothing changed, so it's safe to
// call after any selectedPlayback/subtitleLanguage update without spamming
// fetches.
func (m *modelImpl) triggerSubtitleSync() tea.Cmd {
	if m.resolved == nil || m.subtitleService == nil {
		return nil
	}
	src, ok := m.selectedPlaybackSource()
	if !ok {
		return nil
	}
	if src.Resolver == m.subtitleResolverUsed && m.subtitleLanguage == m.subtitleLangUsed {
		return nil
	}

	m.subtitleResolverUsed = src.Resolver
	m.subtitleLangUsed = m.subtitleLanguage
	opID := m.newOpID()
	m.subtitleOpID = opID
	mediaForFetch := *m.resolved
	if len(m.rawSubtitles) > 0 {
		mediaForFetch.Subtitles = append([]model.SubtitleTrack{}, m.rawSubtitles...)
	}
	return m.subtitleFetchCmd(opID, mediaForFetch)
}

func (m *modelImpl) subtitleFetchCmd(opID int, resolved model.ResolvedMedia) tea.Cmd {
	preferredLang := m.subtitleLanguage
	preferredResolver := ""
	if src, ok := m.selectedPlaybackSource(); ok {
		preferredResolver = src.Resolver
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.appCtx, 30*time.Second)
		defer cancel()
		tracks, err := m.subtitleService.Fetch(ctx, resolved, preferredLang, preferredResolver)
		if err != nil {
			logging.Debugf("subtitle fetch failed: %v", err)
		}
		return subtitleDoneMsg{tracks: tracks, opID: opID, err: err}
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
	provider := ""
	if src, ok := m.selectedPlaybackSource(); ok {
		provider = src.Label
	}
	return func() tea.Msg {
		logging.Debugf("playCmd: opID=%d media=%q provider=%q sources_count=%d startTime=%.2f", opID, resolved.DisplayTitle(), provider, len(sources), startTime)
		subPaths := resolved.SubtitlePaths()
		logging.Debugf("playCmd: launching playback for %q using player=%s subs=%d paths=%v", resolved.DisplayTitle(), playerName, len(subPaths), subPaths)
		result, err := m.players.PlayWithSources(sources, resolved, playerName)
		return playDoneMsg{opID: opID, provider: provider, result: result, err: err}
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

		resolver := resolved.Resolver
		if len(resolved.Playback) > 0 && resolved.Playback[0].Resolver != "" {
			resolver = resolved.Playback[0].Resolver
		}

		return downloadStartedMsg{
			opID:      opID,
			cancel:    cancel,
			outputDir: outputDir,
			title:     title,
			provider:  resolver,
		}
	}
}

func selectedSeriesTitle(series *model.SearchResult) string {
	if series == nil {
		return ""
	}
	return series.Title
}

func selectedSeriesProvider(series *model.SearchResult) string {
	if series == nil {
		return ""
	}
	return series.Provider
}

func selectedEpisodeTitle(episode *model.EpisodeResult) string {
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
	if strings.EqualFold(strings.TrimSpace(group.MediaType), "movie") {
		return false
	}
	return group.FarthestComplete.Episode > 0
}

func modeForHistoryEntry(entry history.Entry) provider.ContentType {
	if entry.Mode != "" {
		return provider.ContentType(entry.Mode)
	}
	switch strings.ToLower(strings.TrimSpace(entry.MediaType)) {
	case "movie", "movies":
		return provider.ModeMovies
	case "anime":
		return provider.ModeAnime
	case "cartoon":
		return provider.ModeCartoon
	default:
		return provider.ModeTV
	}
}

func nextEpisodeAfterEntry(episodes []model.EpisodeResult, entry history.Entry) (int, bool) {
	for idx, episode := range episodes {
		if episodeAfterHistoryEntry(episode, entry) {
			return idx, true
		}
	}
	return 0, false
}

func episodeAfterHistoryEntry(episode model.EpisodeResult, entry history.Entry) bool {
	if entry.Season > 0 {
		if episode.Season > entry.Season {
			return true
		}
		if episode.Season > 0 && episode.Season < entry.Season {
			return false
		}
	}
	if entry.Episode > 0 && episode.Number > entry.Episode {
		if entry.Season <= 0 || episode.Season == entry.Season || episode.Season <= 0 {
			return true
		}
	}
	return false
}

func (m *modelImpl) startSearchFromInput() (tea.Model, tea.Cmd) {
	if m.loading {
		return m, nil
	}
	q := strings.TrimSpace(m.queryInput.Value())
	if q == "" && m.appMode != provider.ModeJellyfin {
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
		if m.seriesList.SettingFilter() || m.seriesList.IsFiltered() || strings.TrimSpace(m.seriesList.FilterValue()) != "" {
			m.seriesList.ResetFilter()
			m.setStatus(statusInfo, "")
			return true
		}
	case viewEpisodes:
		if m.episodeList.SettingFilter() || m.episodeList.IsFiltered() || strings.TrimSpace(m.episodeList.FilterValue()) != "" {
			m.episodeList.ResetFilter()
			m.setStatus(statusInfo, "")
			return true
		}
	}
	return false
}

func (m *modelImpl) exitInputMode() bool {
	if m.activeView == viewSearch && m.queryInput.Focused() {
		m.queryInput.Blur()
		m.setStatus(statusInfo, "")
		return true
	}
	// Settings-screen text inputs (custom accent hex, AniList auth code)
	// need the same treatment: handleGlobalKeys' Back case runs before
	// updateSettings ever sees the key, so without this, Esc while typing
	// here falls through to goBackOne() and exits the whole Settings
	// screen instead of just closing the input.
	if m.activeView == viewSettings {
		if m.editingAccentHex {
			m.editingAccentHex = false
			m.hexInput.Blur()
			return true
		}
		if m.anilistAuthURL != "" {
			m.anilistAuthURL = ""
			m.authInput.Blur()
			return true
		}
	}
	return false
}
