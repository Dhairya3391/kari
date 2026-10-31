package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
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
		// Direct-resolve skips the episode list, but anime providers still
		// need the picked sub/dub track: search-result IDs alone do not say
		// which audio lane to open.
		if m.appMode == provider.ModeAnime {
			m.selectedEpisode.Audio = m.audioMode
		}
		m.loadingText = "Preparing playback..."
		m.resolved = nil
		m.resetPreviewSkipInfo()
		m.rawSubtitles = nil
		m.subtitleResolverUsed = ""
		m.subtitleLangUsed = ""
		m.subtitleSourceUsed = ""
		m.manualPlaybackSelected = false
		m.resolveAttempts = 0
		m.preferredRepairAttempts = 0
		// Drop the previous content's ranking so Preview never flashes
		// stale sources before the first snapshot arrives (mirrors
		// startEpisodeResolution lines 252-253).
		m.rankedSources = nil
		m.previewSelectedIndex = 0
		m.clearPreviewPoster()
		m.beginProviderWait(m.appMode)
		m.autoPlayAfterResolve = false
		opID := m.newOpID()
		m.resolveOpID = opID
		m.pushView(viewPreview)
		skipInfoCmd := m.triggerPreviewSkipInfoFor(previewSkipMedia(*m.selectedSeries, *m.selectedEpisode, m.appMode))
		return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, *m.selectedEpisode, nil, nil), m.prefetchPreviewPoster(), skipInfoCmd)
	}

	tuiLog.Debug("loading episodes", "title", m.selectedSeries.Title)
	m.loading = true
	m.loadingText = "Loading episodes..."
	m.resolved = nil
	m.resetPreviewSkipInfo()
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
	return service.FilterSearchResults(m.seriesResults, m.resultsFilter)
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

// selectedEpisodeIndex resolves the 0-based index of the currently active
// episode within episodeResults. Custom grid navigation and autoplay advance
// episodeIndex and selectedEpisode directly rather than driving the legacy
// episodeList widget cursor.
func (m *modelImpl) selectedEpisodeIndex() int {
	if m.selectedEpisode != nil && len(m.episodeResults) > 0 {
		if m.episodeIndex >= 0 && m.episodeIndex < len(m.episodeResults) {
			ep := &m.episodeResults[m.episodeIndex]
			if matchesEpisode(ep, m.selectedEpisode) {
				return m.episodeIndex
			}
		}
		for i := range m.episodeResults {
			if matchesEpisode(&m.episodeResults[i], m.selectedEpisode) {
				m.episodeIndex = i
				return i
			}
		}
	}
	if m.episodeIndex >= 0 && m.episodeIndex < len(m.episodeResults) {
		return m.episodeIndex
	}
	if item, ok := m.episodeList.SelectedItem().(rowItem); ok {
		return item.index
	}
	return m.episodeList.Index()
}

func matchesEpisode(a, b *provider.Episode) bool {
	if a == nil || b == nil {
		return false
	}
	if a == b {
		return true
	}
	if a.ID != "" && b.ID != "" && a.ID == b.ID {
		return true
	}
	if a.Season > 0 && b.Season > 0 && a.Season == b.Season && a.Episode == b.Episode {
		return true
	}
	if a.Season == 0 && b.Season == 0 && a.Episode > 0 && a.Episode == b.Episode {
		return true
	}
	if a.Title != "" && b.Title != "" && a.Title == b.Title && a.Season == b.Season && a.Episode == b.Episode {
		return true
	}
	return false
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
	if idx >= 0 && idx < len(m.episodeList.Items()) {
		m.episodeList.Select(idx)
	}
	if m.selectedEpisode.Season > 0 {
		seasons := distinctSeasonNumbers(m.episodeResults)
		for sIdx, sNum := range seasons {
			if sNum == m.selectedEpisode.Season {
				m.activeSeason = sIdx
				break
			}
		}
	}
	_, origIndices := m.currentSeasonEpisodes()
	for i, origIdx := range origIndices {
		if origIdx == idx {
			m.seasonEpisodeIndex = i
			break
		}
	}
	m.loading = true
	m.setStatus(statusInfo, "")
	if autoPlay {
		m.loadingText = "Preparing next episode..."
	} else {
		m.loadingText = "Preparing playback..."
	}
	if src, ok := m.selectedPlaybackSource(); ok {
		m.prevSourceResolver = src.Resolver
		m.prevSourceLanguage = src.Language
		m.prevSourceQuality = service.SourceQuality(src.Quality)
	}
	m.resolved = nil
	m.resetPreviewSkipInfo()
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
	skipInfoCmd := m.triggerPreviewSkipInfoFor(previewSkipMedia(series, *m.selectedEpisode, m.appMode))
	return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, series, *m.selectedEpisode, nil, nil), m.prefetchPreviewPoster(), skipInfoCmd)
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
	case viewHistory:
		if m.historyFiltering {
			m.historyFiltering = false
			m.clearStatus()
			return true
		}
		if strings.TrimSpace(m.historyFilter) != "" {
			m.historyFilter = ""
			m.refreshHistoryItems()
			m.historyList.Select(0)
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
