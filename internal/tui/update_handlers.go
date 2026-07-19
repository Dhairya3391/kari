package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"errors"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/settings"
	"kari/internal/util"
)

func cleanErrorForUI(err error) string {
	if err == nil {
		return "Unknown error"
	}
	msg := err.Error()

	if strings.Contains(msg, "no sources found") {
		return "No sources found"
	}
	if strings.Contains(msg, "context deadline") {
		return "Request timed out"
	}
	if strings.Contains(msg, "connection") {
		return "Connection failed"
	}

	parts := strings.Split(msg, "; ")
	if len(parts) > 1 {
		var cleanParts []string
		for _, p := range parts {
			name := strings.Split(p, ":")[0]
			cleanParts = append(cleanParts, title(name))
		}
		return "No sources: " + strings.Join(cleanParts, ", ")
	}

	short := strings.Split(msg, ":")[0]
	if len(short) > 50 {
		short = short[:50] + "..."
	}
	return title(short)
}

func title(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 32
	}
	return string(r)
}

func (m *modelImpl) onSearchDone(msg searchDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.searchOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		logging.Errorf("onSearchDone failed opID=%d err=%v", msg.opID, msg.err)
		m.setStatus(statusError, fmt.Sprintf("Search failed: %v", msg.err))
		m.queryInput.Focus()
		return m, nil
	}

	logging.Infof("onSearchDone success opID=%d results_count=%d used_query=%q", msg.opID, len(msg.results), msg.usedQuery)
	m.allSeriesResults = msg.results
	m.usedQuery = msg.usedQuery
	m.seriesResults = msg.results
	m.seriesList.SetItems(seriesToItems(m.seriesResults))
	if len(m.seriesResults) == 0 {
		m.setStatus(statusWarn, "No series results found")
		m.queryInput.Focus()
		return m, nil
	}
	if m.searchIndex >= 0 && m.searchIndex < len(m.seriesResults) {
		m.seriesList.Select(m.searchIndex)
	}
	if m.queryInput.Focused() {
		m.queryInput.Blur()
	}
	m.setStatus(statusInfo, "")
	return m, nil
}

func (m *modelImpl) onEpisodesDone(msg episodesDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.episodesOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		logging.Errorf("onEpisodesDone failed opID=%d err=%v", msg.opID, msg.err)
		m.setStatus(statusError, fmt.Sprintf("Episodes load failed: %v", msg.err))
		return m, nil
	}

	seriesTitle := ""
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
	}

	logging.Infof("onEpisodesDone success opID=%d episodes_count=%d", msg.opID, len(msg.results))
	m.episodeResults = msg.results
	m.episodeList.SetItems(episodesToItems(msg.results, m.historyStore, seriesTitle, m.selectedEpisodes))

	// Auto-resolve for movies to skip the episode list screen
	if m.selectedSeries != nil && m.selectedSeries.MediaType == "movie" && len(msg.results) > 0 {
		idx := 0
		logging.Debugf("onEpisodesDone: auto-selecting movie episode for %q", m.selectedSeries.Title)
		return m.selectEpisode(idx)
	}

	// Auto-move cursor to first incomplete episode
	targetIdx := 0
	if m.historyStore != nil && len(msg.results) > 0 {
		found := false
		lastCompleteIdx := -1
		for i, it := range msg.results {
			entry, ok := m.historyStore.Get(history.EntryKey{
				Provider: it.Provider,
				Title:    seriesTitle,
				Season:   it.Season,
				Episode:  it.Number,
			})
			if !ok || !entry.Complete {
				targetIdx = i
				found = true
				break
			}
			lastCompleteIdx = i
		}
		if !found && lastCompleteIdx != -1 {
			targetIdx = lastCompleteIdx
		}
	}

	// Try to find current episode index if it's not set
	if m.selectedEpisode != nil {
		for i, it := range m.episodeResults {
			if it.URL != "" && m.selectedEpisode.URL != "" && it.URL == m.selectedEpisode.URL {
				m.episodeIndex = i
				break
			}
			if it.Number > 0 && it.Number == m.selectedEpisode.Number && it.Season == m.selectedEpisode.Season {
				m.episodeIndex = i
				break
			}
		}
	}

	if m.episodeIndex < 0 {
		m.episodeIndex = targetIdx
	}

	if m.episodeIndex >= 0 && m.episodeIndex < len(m.episodeResults) {
		m.episodeList.Select(m.episodeIndex)
	} else {
		m.episodeList.Select(targetIdx)
	}

	if m.selectedEpisode == nil {
		m.pushView(viewEpisodes)
	}
	m.setStatus(statusInfo, "")
	return m, nil
}

func (m *modelImpl) onHistoryContinueEpisodes(msg historyContinueEpisodesMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.historyContinueOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		logging.Errorf("history continue episode load failed title=%q err=%v", msg.group.Title, msg.err)
		m.setStatus(statusWarn, "Could not find next episode, opening last watched")
		return m.resolveHistoryEntry(msg.group.ContinueEntry)
	}

	m.episodeResults = msg.results
	seriesTitle := msg.group.Title
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
	}
	m.episodeList.SetItems(episodesToItems(msg.results, m.historyStore, seriesTitle, m.selectedEpisodes))

	idx, ok := nextEpisodeAfterEntry(msg.results, msg.group.FarthestComplete)
	if !ok {
		m.setStatus(statusWarn, "No next episode found, opening last watched")
		return m.resolveHistoryEntry(msg.group.ContinueEntry)
	}
	return m.startEpisodeResolution(idx, false)
}

func (m *modelImpl) onResolveDone(msg resolveDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.resolveOpID {
		logging.Debugf("onResolveDone: ignoring old opID %d (current %d)", msg.opID, m.resolveOpID)
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		m.autoPlayAfterResolve = false
		m.pendingAutoPlay = false
		if m.resolved == nil {
			logging.Errorf("resolve failed provider=%s series=%q episode=%q err=%v", selectedSeriesProvider(m.selectedSeries), selectedSeriesTitle(m.selectedSeries), selectedEpisodeTitle(m.selectedEpisode), msg.err)
			m.setStatus(statusError, cleanErrorForUI(msg.err))
		}
		return m, nil
	}

	m.mergeResolved(msg.resolved)
	return m.finalizeResolved()
}

func (m *modelImpl) onSubtitleDone(msg subtitleDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.subtitleOpID {
		return m, nil
	}
	m.subtitleOpID = 0
	if msg.err == nil && len(msg.tracks) > 0 {
		m.mergeResolved(model.ResolvedMedia{Subtitles: msg.tracks})
	}
	if m.pendingManualPlay {
		m.pendingManualPlay = false
		m.loading = true
		m.loadingText = "Opening player..."
		opID := m.newOpID()
		m.playOpID = opID
		return m, tea.Batch(m.spinner.Tick, m.playCmd(opID), m.playStartedTimeoutCmd(opID))
	}
	if m.pendingAutoPlay {
		m.pendingAutoPlay = false
		return m.finalizeResolved()
	}
	return m, nil
}

func (m *modelImpl) playStartedTimeoutCmd(opID int) tea.Cmd {
	return tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
		return playStartedMsg{opID: opID}
	})
}

func (m *modelImpl) finalizeResolved() (tea.Model, tea.Cmd) {
	if m.autoPlayAfterResolve {
		if m.subtitleOpID != 0 {
			m.pendingAutoPlay = true
			m.pushView(viewPreview)
			m.setStatus(statusInfo, "")
			return m, nil
		}
		if len(m.orderedPlaybackSources()) == 0 {
			m.autoPlayAfterResolve = false
			m.pushView(viewPreview)
			m.setStatus(statusWarn, "No playback source matches the current filters")
			return m, nil
		}
		m.autoPlayAfterResolve = false
		m.loading = true
		m.loadingText = "Opening player..."
		opID := m.newOpID()
		m.playOpID = opID
		return m, tea.Batch(m.spinner.Tick, m.playCmd(opID), m.playStartedTimeoutCmd(opID))
	}
	m.pushView(viewPreview)
	m.setStatus(statusInfo, "")
	return m, nil
}

func (m *modelImpl) applyResumeFromHistory(resolved *model.ResolvedMedia) {
	if m.historyStore == nil || resolved == nil {
		return
	}

	providerName := resolved.Resolver
	if m.selectedSeries != nil && m.selectedSeries.Provider != "" {
		providerName = m.selectedSeries.Provider
	}

	entry, ok := m.historyStore.Get(history.EntryKey{
		Provider: providerName,
		Title:    resolved.SeriesTitle,
		Season:   resolved.SeasonNumber,
		Episode:  resolved.EpisodeNumber,
	})

	if ok && !entry.Complete && entry.PositionSecs > 5 {
		resolved.StartTime = entry.PositionSecs
		logging.Infof("applyResumeFromHistory: found resume point at %.2fs for %q", entry.PositionSecs, resolved.SeriesTitle)
	}
}

func (m *modelImpl) onResolveProgress(msg resolveProgressMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.resolveOpID {
		return m, m.resolveSubscription()
	}

	wasNil := m.resolved == nil
	m.mergeResolved(msg.resolved)
	m.pushView(viewPreview)

	if wasNil && m.subtitleService != nil {
		opID := m.newOpID()
		m.subtitleOpID = opID
		return m, tea.Batch(m.resolveSubscription(), m.subtitleFetchCmd(opID, *m.resolved))
	}

	return m, m.resolveSubscription()
}

func hasDownloadedSubtitles(tracks []model.SubtitleTrack) bool {
	for _, t := range tracks {
		if t.Path != "" {
			return true
		}
	}
	return false
}

func (m *modelImpl) mergeResolved(resolved model.ResolvedMedia) {
	if m.resolved == nil {
		m.resolved = &model.ResolvedMedia{
			SeriesTitle:   resolved.SeriesTitle,
			SeriesURL:     resolved.SeriesURL,
			EpisodeTitle:  resolved.EpisodeTitle,
			EpisodeURL:    resolved.EpisodeURL,
			MediaType:     resolved.MediaType,
			Year:          resolved.Year,
			TMDBID:        resolved.TMDBID,
			SeasonNumber:  resolved.SeasonNumber,
			EpisodeNumber: resolved.EpisodeNumber,
			Resolver:      resolved.Resolver,
			Playback:      append([]model.PlaybackSource{}, resolved.Playback...),
			Subtitles:     append([]model.SubtitleTrack{}, resolved.Subtitles...),
		}
		m.selectedPlayback = 0
		m.ensurePlaybackSelection()
		m.applyResumeFromHistory(m.resolved)
		return
	}

	// Append new sources, avoiding duplicates
	seen := make(map[string]struct{})
	for _, p := range m.resolved.Playback {
		seen[p.URL] = struct{}{}
	}

	for _, p := range resolved.Playback {
		if _, ok := seen[p.URL]; !ok {
			m.resolved.Playback = append(m.resolved.Playback, p)
			seen[p.URL] = struct{}{}
		}
	}

	// Only replace subtitles from resolve phase if we don't already have downloaded ones
	if len(resolved.Subtitles) > 0 && !hasDownloadedSubtitles(m.resolved.Subtitles) {
		m.resolved.Subtitles = append([]model.SubtitleTrack{}, resolved.Subtitles...)
	}
	m.ensurePlaybackSelection()
}

func (m *modelImpl) onPlayDone(msg playDoneMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingText = ""
	m.autoPlayAfterResolve = false

	if msg.opID != m.playOpID {
		logging.Warnf("onPlayDone: opID mismatch (got %d, want %d)", msg.opID, m.playOpID)
		return m, nil
	}

	var needsConfirm *player.NeedsCompletionConfirmError
	isConfirmErr := errors.As(msg.err, &needsConfirm)

	if msg.err != nil && !isConfirmErr {
		logging.Errorf("playback failed opID=%d provider=%q err=%v", msg.opID, msg.provider, msg.err)
		m.setStatus(statusError, fmt.Sprintf("Playback failed: %v", msg.err))
		m.autoplay = false
		return m, nil
	}

	// Update history
	if m.historyStore != nil && m.resolved != nil {
		providerName := m.resolved.Resolver
		if m.selectedSeries != nil && m.selectedSeries.Provider != "" {
			providerName = m.selectedSeries.Provider
		}

		entry := history.Entry{
			Key: history.EntryKey{
				Provider: providerName,
				Title:    m.resolved.SeriesTitle,
				Season:   m.resolved.SeasonNumber,
				Episode:  m.resolved.EpisodeNumber,
			},
			ProviderName:    providerName,
			Title:           m.resolved.SeriesTitle,
			EpisodeTitle:    m.resolved.EpisodeTitle,
			Season:          m.resolved.SeasonNumber,
			Episode:         m.resolved.EpisodeNumber,
			WatchedAt:       time.Now(),
			PositionSecs:    msg.result.FinalPositionSecs,
			DurationSecs:    msg.result.DurationSecs,
			PercentComplete: 0, // Upsert will compute this
			Complete:        msg.result.Completed,

			// Metadata for re-play
			Mode:       string(m.appMode),
			SeriesURL:  m.resolved.SeriesURL,
			EpisodeURL: m.resolved.EpisodeURL,
			MediaType:  m.resolved.MediaType,
			TMDBID:     m.resolved.TMDBID,
		}
		if err := m.historyStore.Upsert(entry); err != nil {
			logging.Errorf("failed to upsert history: %v", err)
		}

		// Refresh episode list markers if it exists
		if len(m.episodeResults) > 0 {
			seriesTitle := ""
			if m.selectedSeries != nil {
				seriesTitle = m.selectedSeries.Title
			}
			m.episodeList.SetItems(episodesToItems(m.episodeResults, m.historyStore, seriesTitle, m.selectedEpisodes))
		}

		// Get updated entry to have correct PercentComplete for scrobbling
		if updated, ok := m.historyStore.Get(entry.Key); ok {
			m.triggerScrobble(updated)
		} else {
			m.triggerScrobble(entry)
		}
	}

	if isConfirmErr {
		m.confirmCompletion = true
		logging.Infof("playback finished on Android, needs confirmation")
		m.setStatus(statusInfo, "Finished playback?")
	} else {
		logging.Infof("playback finished opID=%d provider=%q result=%+v", msg.opID, msg.provider, msg.result)
		m.setStatus(statusSuccess, "Playback finished")
	}

	m.activeView = viewPreview

	if m.autoplay && m.resolved != nil && (m.resolved.MediaType == "anime" || m.resolved.MediaType == "tv" || m.resolved.MediaType == "cartoon") {
		if idx, ok := m.nextEpisodeIndex(); ok {
			logging.Infof("autoplay: starting next episode index=%d", idx)
			return m.startEpisodeResolution(idx, true)
		}
		m.autoplay = false
		m.setStatus(statusWarn, "Autoplay: No more episodes")
	}

	return m, nil
}

func (m *modelImpl) onDownloadProgress(msg downloadProgressMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, nil
	}
	m.downloadProgress = msg.progress
	m.loadingText = fmt.Sprintf("Downloading... %.1f%%", msg.progress)
	return m, m.downloadSubscription()
}

func (m *modelImpl) onDownloadDone(msg downloadDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	m.downloadProgress = 0
	m.cancelDownload = nil
	m.downloadOpID = 0
	if msg.err != nil {
		logging.Errorf("download failed opID=%d err=%v", msg.opID, msg.err)
		errMsg := fmt.Sprintf("Download failed: %v", msg.err)
		if errors.Is(msg.err, exec.ErrNotFound) || strings.Contains(msg.err.Error(), "executable file not found") {
			errMsg = "Download failed: yt-dlp is not installed"
		}
		m.setStatus(statusError, errMsg)
		return m, m.clearStatusAfter(7 * time.Second)
	}

	m.setStatus(statusSuccess, "Download complete")
	return m, m.clearStatusAfter(7 * time.Second)
}

func (m *modelImpl) downloadSubscription() tea.Cmd {
	return func() tea.Msg {
		return <-m.downloadChan
	}
}

func (m *modelImpl) resolveSubscription() tea.Cmd {
	return func() tea.Msg {
		return <-m.resolveChan
	}
}

func (m *modelImpl) updateActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewSearch:
		return m.updateSearch(msg)
	case viewEpisodes:
		return m.updateEpisodes(msg)
	case viewPreview:
		return m.updatePreview(msg)
	case viewHistory:
		return m.updateHistory(msg)
	case viewSettings:
		return m.updateSettings(msg)
	default:
		return m, nil
	}
}

func (m *modelImpl) updateEpisodes(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(keyMsg, m.keys.ToggleSelect):
			if m.episodeList.SettingFilter() {
				break
			}
			if len(m.episodeResults) == 0 {
				return m, nil
			}
			idx := m.selectedEpisodeIndex()
			if _, ok := m.selectedEpisodes[idx]; ok {
				delete(m.selectedEpisodes, idx)
			} else {
				m.selectedEpisodes[idx] = struct{}{}
			}
			m.refreshEpisodeList()
			return m, nil
		case key.Matches(keyMsg, m.keys.SelectAll):
			if m.episodeList.SettingFilter() {
				break
			}
			m.selectAllEpisodes()
			return m, nil
		case key.Matches(keyMsg, m.keys.DeselectAll):
			if m.episodeList.SettingFilter() {
				break
			}
			m.selectedEpisodes = make(map[int]struct{})
			m.refreshEpisodeList()
			return m, nil
		case key.Matches(keyMsg, m.keys.BatchDownload):
			if len(m.selectedEpisodes) == 0 {
				m.setStatus(statusWarn, "No episodes selected — press space to select")
				return m, nil
			}
			if m.batchInProgress || m.downloadOpID != 0 {
				m.setStatus(statusWarn, "A download is already in progress")
				return m, nil
			}
			return m.startBatchDownload()
		case key.Matches(keyMsg, m.keys.Select):
			return m.selectEpisode(m.selectedEpisodeIndex())
		case key.Matches(keyMsg, m.keys.Audio):
			if m.selectedSeries != nil {
				if m.audioMode == "sub" {
					m.audioMode = "dub"
				} else {
					m.audioMode = "sub"
				}
				m.selectedEpisodes = make(map[int]struct{})
				m.setStatus(statusInfo, "Audio: "+strings.ToUpper(m.audioMode))
				opID := m.newOpID()
				m.episodesOpID = opID
				return m, tea.Batch(m.spinner.Tick, m.episodesCmd(opID, *m.selectedSeries), m.clearStatusAfter(3*time.Second))
			}
		case keyMsg.String() == "g":
			m.episodeList.Select(0)
			return m, nil
		case keyMsg.String() == "G":
			if visible := m.episodeList.VisibleItems(); len(visible) > 0 {
				m.episodeList.Select(len(visible) - 1)
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.episodeList, cmd = m.episodeList.Update(msg)
	return m, cmd
}

func (m *modelImpl) selectAllEpisodes() {
	m.selectedEpisodes = make(map[int]struct{})
	for i := range m.episodeResults {
		m.selectedEpisodes[i] = struct{}{}
	}
	m.refreshEpisodeList()
}

func (m *modelImpl) refreshEpisodeList() {
	seriesTitle := ""
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
	}
	m.episodeList.SetItems(episodesToItems(m.episodeResults, m.historyStore, seriesTitle, m.selectedEpisodes))
}

func (m *modelImpl) startBatchDownload() (tea.Model, tea.Cmd) {
	selected := m.orderedSelectedEpisodes()
	if len(selected) == 0 {
		m.setStatus(statusWarn, "No episodes selected")
		return m, nil
	}

	m.batchInProgress = true
	m.batchCurrent = 0
	m.batchTotal = len(selected)
	m.loading = true
	m.loadingText = fmt.Sprintf("Downloading 0/%d...", m.batchTotal)

	opID := m.newOpID()
	m.downloadOpID = opID

	var series model.SearchResult
	var mode provider.ContentType
	var hasSeries bool
	if m.selectedSeries != nil {
		series = *m.selectedSeries
		mode = m.appMode
		hasSeries = true
	}

	cmd := m.batchDownloadCmd(opID, selected, series, mode, hasSeries)
	return m, tea.Batch(m.spinner.Tick, cmd)
}

func (m *modelImpl) orderedSelectedEpisodes() []model.EpisodeResult {
	out := make([]model.EpisodeResult, 0, len(m.selectedEpisodes))
	for i := range m.episodeResults {
		if _, ok := m.selectedEpisodes[i]; ok {
			out = append(out, m.episodeResults[i])
		}
	}
	return out
}

func (m *modelImpl) batchDownloadCmd(opID int, episodes []model.EpisodeResult, series model.SearchResult, mode provider.ContentType, hasSeries bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(m.appCtx)

		if !hasSeries {
			cancel()
			return batchDoneMsg{opID: opID, completed: 0, total: len(episodes)}
		}

		go func() {
			defer cancel()

			onProgress := func(current, total int, epTitle string, epProgress float64) {
				select {
				case m.batchChan <- batchProgressMsg{
					opID:            opID,
					current:         current,
					total:           total,
					episodeTitle:    epTitle,
					episodeProgress: epProgress,
				}:
				default:
				}
			}

			results := m.downloadService.BatchDownload(
				ctx,
				series,
				episodes,
				mode,
				m.qualityMode,
				m.languageFilter,
				onProgress,
			)

			completed := 0
			for _, r := range results {
				if r.Err == nil {
					completed++
				}
			}

			select {
			case m.batchChan <- batchDoneMsg{
				opID:      opID,
				completed: completed,
				total:     len(episodes),
			}:
			default:
			}
		}()

		return batchStartedMsg{opID: opID, cancel: cancel, total: len(episodes)}
	}
}

func (m *modelImpl) onBatchProgress(msg batchProgressMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, nil
	}
	m.batchCurrent = msg.current
	m.batchTotal = msg.total
	m.loadingText = fmt.Sprintf("Downloading %d/%d: %s - %.0f%%",
		msg.current, msg.total, msg.episodeTitle, msg.episodeProgress*100)
	return m, m.batchSubscription()
}

func (m *modelImpl) onBatchDone(msg batchDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	m.batchInProgress = false
	m.batchCancel = nil
	m.batchCurrent = 0
	m.batchTotal = 0
	m.downloadOpID = 0

	if msg.completed == 0 {
		m.setStatus(statusError, "Batch download failed")
	} else {
		m.setStatus(statusSuccess, fmt.Sprintf("Downloaded %d/%d episodes", msg.completed, msg.total))
	}

	return m, nil
}

func (m *modelImpl) batchSubscription() tea.Cmd {
	return func() tea.Msg {
		return <-m.batchChan
	}
}

func (m *modelImpl) updateHistory(msg tea.Msg) (tea.Model, tea.Cmd) {

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.confirmDelete || m.confirmClearHistory {
			switch msg.String() {
			case "y", "Y":
				if m.confirmDelete {
					if item, ok := m.historyList.SelectedItem().(rowItem); ok {
						_ = m.historyStore.DeleteGroup(historyGroupKeyByString(m.historyStore.All(), item.key))
					}
					m.confirmDelete = false
					return m.refreshHistory()
				}
				if m.confirmClearHistory {
					_ = m.historyStore.Clear()
					m.confirmClearHistory = false
					return m.refreshHistory()
				}
			case "n", "N", "esc":
				m.confirmDelete = false
				m.confirmClearHistory = false
				return m, nil
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, m.keys.Back):
			m.goBackOne()
			return m, nil
		case key.Matches(msg, m.keys.Delete):
			if len(m.historyList.Items()) > 0 {
				m.confirmDelete = true
			}
			return m, nil
		case key.Matches(msg, m.keys.ClearHistory):
			if len(m.historyList.Items()) > 0 {
				m.confirmClearHistory = true
			}
			return m, nil
		case key.Matches(msg, m.keys.Select):
			if item, ok := m.historyList.SelectedItem().(rowItem); ok {
				return m.playHistoryGroup(item.key)
			}
		}
	}

	var cmd tea.Cmd
	m.historyList, cmd = m.historyList.Update(msg)
	return m, cmd
}

func (m *modelImpl) refreshHistory() (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, nil
	}
	groups := history.BuildGroups(m.historyStore.All())
	items := historyGroupsToItems(groups)
	m.historyList.SetItems(items)
	return m, nil
}

func (m *modelImpl) playHistoryGroup(keyStr string) (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, nil
	}
	groups := history.BuildGroups(m.historyStore.All())
	var group *history.Group
	for i := range groups {
		if groups[i].Key.String() == keyStr {
			group = &groups[i]
			break
		}
	}
	if group == nil {
		return m, nil
	}

	if shouldFetchNextEpisode(*group) {
		return m.fetchHistoryNextEpisode(*group)
	}

	return m.resolveHistoryEntry(group.ContinueEntry)
}

func (m *modelImpl) fetchHistoryNextEpisode(group history.Group) (tea.Model, tea.Cmd) {
	entry := group.ContinueEntry
	series := searchResultFromHistoryEntry(entry)
	targetMode := modeForHistoryEntry(entry)
	m.appMode = targetMode
	if series.URL == "" {
		logging.Infof("fetchHistoryNextEpisode: missing series URL, falling back to search for %q via %s", entry.Title, entry.ProviderName)
		m.loading = true
		m.loadingText = fmt.Sprintf("Searching %s...", entry.Title)
		opID := m.newOpID()
		m.searchOpID = opID
		return m, m.searchCmd(opID, entry.Title)
	}

	m.selectedSeries = &series
	m.selectedEpisode = nil
	m.resolved = nil
	m.selectedPlayback = 0
	m.episodeResults = nil
	m.episodeIndex = -1
	m.autoPlayAfterResolve = false
	m.loading = true
	m.loadingText = "Finding next episode..."
	opID := m.newOpID()
	m.historyContinueOpID = opID
	logging.Infof("fetchHistoryNextEpisode: loading episodes for %q after S%dE%d", group.Title, group.FarthestComplete.Season, group.FarthestComplete.Episode)
	return m, tea.Batch(m.spinner.Tick, m.historyContinueEpisodesCmd(opID, group, series, targetMode))
}

func (m *modelImpl) resolveHistoryEntry(entry history.Entry) (tea.Model, tea.Cmd) {
	// Reconstruct results to call Resolve directly
	series := searchResultFromHistoryEntry(entry)
	episode := episodeResultFromHistoryEntry(entry)
	targetMode := modeForHistoryEntry(entry)

	// If missing critical URLs, fallback to search using the CORRECT provider and mode
	if series.URL == "" || (episode.URL == "" && series.MediaType != "movie") {
		logging.Infof("playHistoryEntry: missing URLs, falling back to search for %q via %s", entry.Title, entry.ProviderName)

		m.appMode = targetMode

		m.loading = true
		m.loadingText = fmt.Sprintf("Searching %s...", entry.Title)
		opID := m.newOpID()
		m.searchOpID = opID
		return m, m.searchCmd(opID, entry.Title)
	}

	// URLs are present, set the mode correctly before resolving
	m.appMode = targetMode

	m.selectedSeries = &series
	m.selectedEpisode = &episode
	m.resolved = nil
	m.selectedPlayback = 0
	m.episodeResults = nil
	m.episodeIndex = -1
	m.autoPlayAfterResolve = false
	m.loading = true
	m.loadingText = "Resolving..."
	opID := m.newOpID()
	m.resolveOpID = opID

	// Also fetch episodes in background to enable "Play Next"
	epOpID := m.newOpID()
	m.episodesOpID = epOpID

	logging.Infof("playHistoryEntry: re-resolving %q S%dE%d from history for preview", entry.Title, entry.Season, entry.Episode)

	m.pushView(viewPreview)
	return m, tea.Batch(
		m.spinner.Tick,
		m.resolveCmd(opID, series, episode),
		m.episodesCmd(epOpID, series),
	)
}

func (m *modelImpl) updateSearch(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.queryInput.Focused() {
		if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "enter" {
			return m.startSearchFromInput()
		}
		var cmd tea.Cmd
		m.queryInput, cmd = m.queryInput.Update(msg)

		if strings.TrimSpace(m.queryInput.Value()) == "" {
			m.seriesResults = nil
			m.seriesList.SetItems(nil)
			m.allSeriesResults = nil
		}

		return m, cmd
	}

	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(keyMsg, m.keys.Select):
			if len(m.seriesResults) == 0 {
				return m.startSearchFromInput()
			}
			return m.selectSeries(m.selectedSeriesIndex())
		case key.Matches(keyMsg, m.keys.Search):
			m.queryInput.Focus()
			m.setStatus(statusInfo, "")
			return m, nil
		case key.Matches(keyMsg, m.keys.Type):
			cmd := m.cycleMode(keyMsg.String() == "shift+tab")
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.seriesList, cmd = m.seriesList.Update(msg)
	return m, cmd
}

func (m *modelImpl) updatePreview(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || m.resolved == nil {
		return m, nil
	}

	if m.confirmCompletion {
		switch keyMsg.String() {
		case "y", "Y":
			if m.historyStore != nil && m.resolved != nil {
				providerName := m.resolved.Resolver
				if m.selectedSeries != nil && m.selectedSeries.Provider != "" {
					providerName = m.selectedSeries.Provider
				}

				entry := history.Entry{
					Key: history.EntryKey{
						Provider: providerName,
						Title:    m.resolved.SeriesTitle,
						Season:   m.resolved.SeasonNumber,
						Episode:  m.resolved.EpisodeNumber,
					},
					ProviderName: providerName,
					Title:        m.resolved.SeriesTitle,
					EpisodeTitle: m.resolved.EpisodeTitle,
					Season:       m.resolved.SeasonNumber,
					Episode:      m.resolved.EpisodeNumber,
					WatchedAt:    time.Now(),
					PositionSecs: 1, // Set to 1 to satisfy >85% if duration 1
					DurationSecs: 1,
					Complete:     true,

					// Metadata
					Mode:       string(m.appMode),
					SeriesURL:  m.resolved.SeriesURL,
					EpisodeURL: m.resolved.EpisodeURL,
					MediaType:  m.resolved.MediaType,
					TMDBID:     m.resolved.TMDBID,
				}
				_ = m.historyStore.Upsert(entry)

				// Refresh episode list markers
				if len(m.episodeResults) > 0 {
					seriesTitle := ""
					if m.selectedSeries != nil {
						seriesTitle = m.selectedSeries.Title
					}
					m.episodeList.SetItems(episodesToItems(m.episodeResults, m.historyStore, seriesTitle, m.selectedEpisodes))
				}

				if updated, ok := m.historyStore.Get(entry.Key); ok {
					m.triggerScrobble(updated)
				} else {
					m.triggerScrobble(entry)
				}
			}
			m.confirmCompletion = false
			m.setStatus(statusSuccess, "Marked as complete")
			return m, nil
		case "n", "N":
			m.confirmCompletion = false
			m.setStatus(statusInfo, "Marked as in-progress")
			return m, m.clearStatusAfter(3 * time.Second)
		case "esc":
			m.confirmCompletion = false
			m.setStatus(statusInfo, "")
			return m, nil
		}
		return m, nil
	}

	switch keyMsg.String() {
	case "p", "enter":
		if len(m.orderedPlaybackSources()) == 0 {
			m.setStatus(statusWarn, "No playback source matches the current filters")
			return m, nil
		}
		if m.subtitleOpID != 0 {
			m.pendingManualPlay = true
			m.loadingText = "Downloading subtitles..."
			return m, nil
		}
		m.loading = true
		m.loadingText = "Opening player..."
		opID := m.newOpID()
		m.playOpID = opID
		return m, tea.Batch(m.spinner.Tick, m.playCmd(opID), m.playStartedTimeoutCmd(opID))
	case "r":
		if len(m.orderedPlaybackSources()) == 0 {
			m.setStatus(statusWarn, "No playback source matches the current filters")
			return m, nil
		}
		if m.subtitleOpID != 0 {
			m.pendingManualPlay = true
			m.resolved.StartTime = 0
			m.loadingText = "Downloading subtitles..."
			return m, nil
		}
		if m.resolved != nil {
			m.resolved.StartTime = 0
		}
		m.loading = true
		m.loadingText = "Starting from beginning..."
		opID := m.newOpID()
		m.playOpID = opID
		return m, tea.Batch(m.spinner.Tick, m.playCmd(opID), m.playStartedTimeoutCmd(opID))
	case "n":
		return m.playNextEpisode()
	case "A":
		if m.resolved != nil && (m.resolved.MediaType == "anime" || m.resolved.MediaType == "tv" || m.resolved.MediaType == "cartoon") {
			m.autoplay = !m.autoplay
			status := "OFF"
			if m.autoplay {
				status = "ON"
			}
			m.setStatus(statusInfo, fmt.Sprintf("Autoplay: %s", status))
		}
		return m, nil
	case "tab", "shift+tab":
		filtered := m.filteredPlayback()
		if len(filtered) <= 1 {
			return m, nil
		}
		step := 1
		if keyMsg.String() == "shift+tab" {
			step = -1
		}
		pos := 0
		for i, idx := range filtered {
			if idx == m.selectedPlayback {
				pos = i
				break
			}
		}
		pos = (pos + step + len(filtered)) % len(filtered)
		m.selectedPlayback = filtered[pos]
		m.setStatus(statusInfo, "")
		return m, nil
	case "d":
		if m.resolved == nil || len(m.resolved.Playback) == 0 {
			if m.loading {
				m.setStatus(statusWarn, "Preparing playback, please wait...")
			} else {
				m.setStatus(statusWarn, "No playback source available to download")
			}
			return m, nil
		}
		if len(m.orderedPlaybackSources()) == 0 {
			m.setStatus(statusWarn, "No playback source matches the current filters")
			return m, nil
		}
		if m.downloadOpID != 0 {
			m.setStatus(statusWarn, "A download is already in progress")
			return m, nil
		}
		m.loading = true
		m.loadingText = "Downloading..."
		opID := m.newOpID()
		m.downloadOpID = opID
		resolved := *m.resolved
		resolved.Playback = m.orderedPlaybackSources()
		return m, tea.Batch(m.spinner.Tick, m.downloadCmd(opID, resolved))
	}
	return m, nil
}

func (m *modelImpl) handleGlobalKeys(msg tea.KeyMsg) (tea.Cmd, bool) {
	if !key.Matches(msg, m.keys.Quit) {
		m.confirmQuit = false
	}
	if !key.Matches(msg, m.keys.Stop) {
		m.confirmStop = false
	}

	// Help overlay captures all input except ? to dismiss and esc
	if m.showHelp {
		if msg.String() == "?" || msg.String() == "esc" {
			m.showHelp = false
			return nil, true
		}
		return nil, true
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		if m.queryInput.Focused() {
			return nil, false
		}
		if m.activeView == viewSearch && m.seriesList.SettingFilter() {
			return nil, false
		}
		if m.activeView == viewEpisodes && m.episodeList.SettingFilter() {
			return nil, false
		}

		if m.batchInProgress && m.batchCancel != nil {
			if m.confirmQuit {
				m.batchCancel()
				m.batchCancel = nil
				m.batchInProgress = false
				m.downloadOpID = 0
				m.loading = false
				m.loadingText = ""
			drainBatchQuit:
				for {
					select {
					case <-m.batchChan:
					default:
						break drainBatchQuit
					}
				}
				return tea.Quit, true
			}
			m.confirmQuit = true
			m.setStatus(statusWarn, "Press q again to quit")
			return tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
				return resetConfirmQuitMsg{}
			}), true
		}

		if m.cancelDownload != nil {
			if m.confirmQuit {
				m.cancelDownload()
				m.cancelDownload = nil
				if m.downloadOutputDir != "" {
					m.downloadService.CleanupPartial(m.downloadOutputDir, m.downloadProvider, m.downloadTitle)
				}
				return tea.Quit, true
			}
			m.confirmQuit = true
			m.setStatus(statusWarn, "Press q again to quit")
			return tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
				return resetConfirmQuitMsg{}
			}), true
		}
		return tea.Quit, true
	case key.Matches(msg, m.keys.History):
		if m.activeView == viewSearch && !m.queryInput.Focused() {
			m.loading = true
			m.loadingText = "Loading history..."
			return tea.Batch(m.spinner.Tick, func() tea.Msg {
				m.refreshHistory()
				return historyLoadedMsg{}
			}), true
		}
	case key.Matches(msg, m.keys.Settings):
		if m.activeView == viewSearch && !m.queryInput.Focused() {
			m.pushView(viewSettings)
			return nil, true
		}
	case key.Matches(msg, m.keys.Stop):
		if m.queryInput.Focused() {
			return nil, false
		}
		if m.activeView == viewSearch && m.seriesList.SettingFilter() {
			return nil, false
		}
		if m.activeView == viewEpisodes && m.episodeList.SettingFilter() {
			return nil, false
		}

		if m.batchInProgress && m.batchCancel != nil {
			if m.confirmStop {
				m.batchCancel()
				m.batchCancel = nil
				m.batchInProgress = false
				m.downloadOpID = 0
				m.loading = false
				m.loadingText = ""
			drainBatchStop:
				for {
					select {
					case <-m.batchChan:
					default:
						break drainBatchStop
					}
				}
				m.setStatus(statusInfo, "Batch download stopped")
				return m.clearStatusAfter(3 * time.Second), true
			}
			m.confirmStop = true
			m.setStatus(statusWarn, "Press x again to stop")
			return tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
				return resetConfirmStopMsg{}
			}), true
		}

		if m.cancelDownload != nil {
			if m.confirmStop {
				m.cancelDownload()
				m.cancelDownload = nil
				m.downloadOpID = 0
				if m.downloadOutputDir != "" {
					m.downloadService.CleanupPartial(m.downloadOutputDir, m.downloadProvider, m.downloadTitle)
				}
				m.loading = false
				m.loadingText = ""
				m.setStatus(statusInfo, "Download stopped")
				return m.clearStatusAfter(3 * time.Second), true
			}
			m.confirmStop = true
			m.setStatus(statusWarn, "Press x again to stop")
			return tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
				return resetConfirmStopMsg{}
			}), true
		}
		return nil, false
	case key.Matches(msg, m.keys.Home):
		m.activeView = viewSearch
		m.backStack = nil
		m.loading = false
		m.setStatus(statusInfo, "")
		return nil, true
	case msg.String() == "ctrl+p":
		if m.queryInput.Focused() {
			return nil, false
		}
		if len(m.availablePlayers) <= 1 {
			return nil, true
		}
		m.selectedPlayer = (m.selectedPlayer + 1) % len(m.availablePlayers)
		m.setStatus(statusInfo, "Player: "+strings.ToUpper(m.selectedPlayerName()))
		return m.clearStatusAfter(3 * time.Second), true
	case key.Matches(msg, m.keys.Back):
		if m.clearActiveFilter() {
			return nil, true
		}
		if m.exitInputMode() {
			return nil, true
		}
		m.goBackOne()
		if m.loading {
			logging.Debugf("handleGlobalKeys: ESC cancelled in-flight resolve (opID=%d)", m.resolveOpID)
			m.loading = false
			m.loadingText = ""
			m.resolveOpID = m.newOpID()
		}
		return nil, true
	case msg.String() == "?":
		if m.queryInput.Focused() {
			return nil, false
		}
		m.showHelp = !m.showHelp
		return nil, true
	}
	return nil, false
}

func (m *modelImpl) cycleMode(reverse bool) tea.Cmd {
	idx := 0
	for i, v := range m.modes {
		if v == m.appMode {
			idx = i
			break
		}
	}
	if reverse {
		idx = (idx - 1 + len(m.modes)) % len(m.modes)
	} else {
		idx = (idx + 1) % len(m.modes)
	}
	m.appMode = m.modes[idx]
	m.setStatus(statusInfo, "Mode: "+strings.ToUpper(string(m.modes[idx])))
	m.updateQueryPlaceholder()

	// Clear current results and selection to avoid cross-mode provider errors
	m.allSeriesResults = nil
	m.seriesResults = nil
	m.seriesList.SetItems(nil)
	m.selectedSeries = nil
	m.searchQuery = ""
	m.usedQuery = ""
	m.searchIndex = 0

	return m.clearStatusAfter(3 * time.Second)
}

// updateQueryPlaceholder adjusts the search prompt hint for the active mode.
func (m *modelImpl) updateQueryPlaceholder() {
	if m.appMode == provider.ModeJellyfin {
		m.queryInput.Placeholder = "Search… (Enter on empty = browse library)"
		return
	}
	m.queryInput.Placeholder = "Search… (Esc for controls)"
}

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
		opID := m.newOpID()
		m.resolveOpID = opID
		m.pushView(viewPreview)
		return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, direct))
	}

	logging.Debugf("selectSeries: loading episodes for %q", m.selectedSeries.Title)
	m.loading = true
	m.loadingText = "Loading episodes..."
	m.resolved = nil
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
	m.resolved = nil
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
	return func() tea.Msg {
		mode := string(m.appMode)
		cacheKey := fmt.Sprintf("%s:%s", mode, query)
		// Jellyfin searches are ranked locally against the provider's own
		// library cache, so caching here would only serve stale results.
		cacheable := m.appMode != provider.ModeJellyfin
		if cacheable {
			if entry, ok := m.searchCache[cacheKey]; ok {
				logging.Debugf("search cache hit mode=%s query=%q", mode, query)
				return searchDoneMsg{results: entry.results, usedQuery: entry.usedQuery, warnings: entry.warnings, opID: opID, err: nil}
			}
		}

		logging.Debugf("search start mode=%s query=%q", mode, query)
		results, usedQuery, warnings, err := m.mediaService.Search(m.appCtx, m.appMode, query)
		if err == nil && cacheable {
			m.searchCache[cacheKey] = searchCacheEntry{
				results:   results,
				usedQuery: usedQuery,
				warnings:  warnings,
			}
		}

		return searchDoneMsg{results: results, usedQuery: usedQuery, warnings: warnings, opID: opID, err: err}
	}
}

func (m *modelImpl) episodesCmd(opID int, series model.SearchResult) tea.Cmd {
	return func() tea.Msg {
		results, err := m.mediaService.FetchEpisodes(m.appCtx, m.appMode, series, m.audioMode)
		return episodesDoneMsg{results: results, opID: opID, err: err}
	}
}

func (m *modelImpl) historyContinueEpisodesCmd(opID int, group history.Group, series model.SearchResult, mode provider.ContentType) tea.Cmd {
	return func() tea.Msg {
		results, err := m.mediaService.FetchEpisodes(m.appCtx, mode, series, m.audioMode)
		return historyContinueEpisodesMsg{group: group, results: results, opID: opID, err: err}
	}
}

func (m *modelImpl) resolveCmd(opID int, series model.SearchResult, episode model.EpisodeResult) tea.Cmd {
	logging.Debugf("resolveCmd: opID=%d series=%q episode=%q", opID, series.Title, episode.Title)

	return tea.Batch(
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.appCtx, 60*time.Second)
			defer cancel()

			onResult := func(resolved model.ResolvedMedia) {
				m.resolveChan <- resolveProgressMsg{resolved: resolved, opID: opID}
			}

			resolved, err := m.mediaService.Resolve(ctx, m.appMode, series, episode, onResult)
			if err != nil {
				return resolveDoneMsg{resolved: resolved, opID: opID, err: err}
			}

			logging.Debugf("resolveCmd: resolved successfully with %d sources", len(resolved.Playback))
			return resolveDoneMsg{resolved: resolved, opID: opID, err: nil}
		},
		m.resolveSubscription(),
	)
}

func (m *modelImpl) subtitleFetchCmd(opID int, resolved model.ResolvedMedia) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.appCtx, 30*time.Second)
		defer cancel()
		tracks, err := m.subtitleService.Fetch(ctx, resolved)
		if err != nil {
			logging.Debugf("subtitle fetch failed: %v", err)
		}
		return subtitleDoneMsg{tracks: tracks, opID: opID, err: err}
	}
}

func (m *modelImpl) playCmd(opID int) tea.Cmd {
	sources := m.orderedPlaybackSources()
	if m.resolved == nil || len(sources) == 0 {
		return func() tea.Msg {
			return playDoneMsg{opID: opID, err: fmt.Errorf("no playback source matches the current filters")}
		}
	}
	resolved := *m.resolved
	playerName := m.selectedPlayerName()
	provider := ""
	if src, ok := m.selectedPlaybackSource(); ok {
		provider = src.Label
	}
	return func() tea.Msg {
		logging.Debugf("playCmd: opID=%d media=%q provider=%q sources_count=%d", opID, resolved.DisplayTitle(), provider, len(sources))
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
			err := m.downloadService.Download(ctx, resolved, func(progress float64) {
				m.downloadChan <- downloadProgressMsg{opID: opID, progress: progress}
			})
			m.downloadChan <- downloadDoneMsg{opID: opID, err: err}
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

func searchResultFromHistoryEntry(entry history.Entry) model.SearchResult {
	return model.SearchResult{
		Title:     entry.Title,
		URL:       entry.SeriesURL,
		Provider:  history.FirstNonEmpty(entry.ProviderName, entry.Key.Provider),
		MediaType: entry.MediaType,
		TMDBID:    entry.TMDBID,
	}
}

func episodeResultFromHistoryEntry(entry history.Entry) model.EpisodeResult {
	return model.EpisodeResult{
		Title:    entry.EpisodeTitle,
		URL:      entry.EpisodeURL,
		Number:   entry.Episode,
		Season:   entry.Season,
		Provider: history.FirstNonEmpty(entry.ProviderName, entry.Key.Provider),
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
	return false
}

func (m *modelImpl) updateSettings(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.anilistAuthURL != "" {
			if msg.String() == "enter" {
				code := m.authInput.Value()
				m.anilistAuthURL = ""
				m.authInput.Blur()
				m.loading = true
				m.loadingText = "Exchanging code..."
				return m, func() tea.Msg {
					err := m.anilistClient.ExchangeCode(m.appCtx, code)
					return authDoneMsg{err: err}
				}
			}
			if msg.String() == "esc" {
				m.anilistAuthURL = ""
				m.authInput.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.authInput, cmd = m.authInput.Update(msg)
			return m, cmd
		}

		switch msg.String() {
		case "up", "k":
			if m.settingsIndex > 0 {
				m.settingsIndex--
			}
		case "down", "j":
			if m.settingsIndex == 2 {
				m.settingsIndex++
				m.languageIndex = 0
			} else if m.settingsIndex < 3 {
				m.settingsIndex++
			}
		case "left":
			switch m.settingsIndex {
			case 2:
				if m.qualityMode > 0 {
					m.qualityMode--
					m.selectedPlayback = 0
					if filtered := m.filteredPlayback(); len(filtered) > 0 {
						m.selectedPlayback = filtered[0]
					}
					m.setStatus(statusInfo, "Quality: "+qualityLabel(m.qualityMode))
					settings.Save(&settings.Data{QualityMode: m.qualityMode, LanguageFilter: m.languageFilter})
				}
			case 3:
				if m.languageIndex > 0 {
					m.languageIndex--
				}
			}
		case "right":
			switch m.settingsIndex {
			case 2:
				if m.qualityMode < qualityLowest {
					m.qualityMode++
					m.selectedPlayback = 0
					if filtered := m.filteredPlayback(); len(filtered) > 0 {
						m.selectedPlayback = filtered[0]
					}
					m.setStatus(statusInfo, "Quality: "+qualityLabel(m.qualityMode))
					settings.Save(&settings.Data{QualityMode: m.qualityMode, LanguageFilter: m.languageFilter})
				}
			case 3:
				languages := m.availableLanguages()
				if m.languageIndex < len(languages)-1 {
					m.languageIndex++
				}
			}
		case " ":
			if m.settingsIndex == 3 && m.languageFilter != nil {
				languages := m.availableLanguages()
				if len(languages) > 0 {
					lang := languages[m.languageIndex]
					m.languageFilter[lang] = !m.languageEnabled(lang)
					if !m.hasEnabledLanguage() {
						m.languageFilter[lang] = true
					}
					m.selectedPlayback = 0
					if filtered := m.filteredPlayback(); len(filtered) > 0 {
						m.selectedPlayback = filtered[0]
					}
					settings.Save(&settings.Data{QualityMode: m.qualityMode, LanguageFilter: m.languageFilter})
				}
			}
		case "c", "C":
			switch m.settingsIndex {
			case 0:
				return m.startTraktAuth()
			case 1:
				return m.startAniListAuth()
			}
		case "r", "R":
			switch m.settingsIndex {
			case 0:
				if m.traktClient != nil {
					_ = m.traktClient.Revoke()
				}
			case 1:
				if m.anilistClient != nil {
					_ = m.anilistClient.Revoke()
				}
			}
		}
	case authDoneMsg:
		m.loading = false
		var cmd tea.Cmd
		if msg.err != nil {
			m.setStatus(statusError, fmt.Sprintf("Auth failed: %v", msg.err))
			cmd = m.clearStatusAfter(5 * time.Second)
		} else {
			m.setStatus(statusSuccess, "Authenticated successfully")
			cmd = m.clearStatusAfter(5 * time.Second)
		}
		return m, cmd
	case traktCodeMsg:
		m.traktAuthCode = msg.userCode
		m.traktAuthURL = msg.verificationURL
		m.traktAuthDeviceCode = msg.deviceCode
		return m, m.pollTraktAuth(msg.deviceCode, msg.interval)
	}
	return m, nil
}

type authDoneMsg struct{ err error }
type traktCodeMsg struct {
	userCode, verificationURL, deviceCode string
	interval, expiresIn                   int
}

func (m *modelImpl) startTraktAuth() (tea.Model, tea.Cmd) {
	if m.traktClient == nil {
		return m, nil
	}
	m.loading = true
	m.loadingText = "Requesting code..."
	return m, func() tea.Msg {
		userCode, verURL, devCode, interval, expires, err := m.traktClient.StartDeviceAuth(m.appCtx)
		if err != nil {
			return authDoneMsg{err: err}
		}
		return traktCodeMsg{userCode, verURL, devCode, interval, expires}
	}
}

func (m *modelImpl) pollTraktAuth(deviceCode string, interval int) tea.Cmd {
	return func() tea.Msg {
		err := m.traktClient.PollDeviceAuth(m.appCtx, deviceCode, interval)
		m.traktAuthCode = ""
		m.traktAuthURL = ""
		return authDoneMsg{err: err}
	}
}

func (m *modelImpl) startAniListAuth() (tea.Model, tea.Cmd) {
	if m.anilistClient == nil {
		return m, nil
	}
	url := m.anilistClient.AuthURL()
	m.anilistAuthURL = url
	m.authInput.Focus()
	_ = util.OpenBrowser(url)
	return m, textinput.Blink
}

func (m *modelImpl) triggerScrobble(entry history.Entry) {
	if m.resolved == nil {
		return
	}

	// Capture values to avoid race conditions
	resolved := *m.resolved
	appMode := m.appMode
	trakt := m.traktClient
	anilist := m.anilistClient
	appCtx := m.appCtx
	if appCtx == nil {
		appCtx = context.Background()
	}

	logging.Debugf("triggerScrobble: entry=%+v appMode=%s", entry, appMode)

	go func() {
		// Use a local copy of entry to ensure idempotency update doesn't affect other goroutines
		e := entry

		ctx, cancel := context.WithTimeout(appCtx, 15*time.Second)
		defer cancel()

		progress := e.PercentComplete
		if progress == 0 && e.DurationSecs > 0 {
			progress = e.PositionSecs / e.DurationSecs
		}

		// Idempotency check: only scrobble if progress changed significantly (>1%) or is 100%
		diff := progress - e.LastScrobbledPercent
		if diff < 0 {
			diff = -diff
		}
		if diff < 0.01 && e.LastScrobbledPercent != 0 && progress < 0.99 {
			logging.Debugf("triggerScrobble: skipping redundant update (diff=%.4f)", diff)
			return
		}

		logging.Debugf("triggerScrobble: media_type=%q tmdb_id=%d season=%d ep=%d progress=%.2f", resolved.MediaType, resolved.TMDBID, resolved.SeasonNumber, resolved.EpisodeNumber, progress)

		success := false
		if appMode == provider.ModeAnime {
			if anilist != nil && anilist.IsAuthenticated() {
				logging.Debugf("triggerScrobble: scrobbling %q to anilist (progress=%.2f)", e.Title, progress)
				if err := anilist.UpdateProgress(ctx, resolved); err != nil {
					logging.Errorf("anilist scrobble failed: %v", err)
				} else {
					logging.Infof("anilist scrobble successful for %q", e.Title)
					success = true
				}
			}
		} else {
			if trakt != nil && trakt.IsAuthenticated() {
				logging.Debugf("triggerScrobble: scrobbling %q to trakt (progress=%.2f)", e.Title, progress)
				_ = trakt.RefreshIfNeeded(ctx)
				var err error
				if resolved.MediaType == "movie" {
					err = trakt.ScrobbleMovie(ctx, resolved, progress)
				} else {
					err = trakt.ScrobbleEpisode(ctx, resolved, progress)
				}
				if err != nil {
					logging.Errorf("trakt scrobble failed: %v", err)
				} else {
					logging.Infof("trakt scrobble successful for %q", e.Title)
					success = true
				}
			}
		}

		if success {
			// Update the record with the last scrobbled percentage to prevent duplicates
			e.LastScrobbledPercent = progress
			// We need to re-fetch the latest entry from the store to avoid overwriting other updates
			if latest, ok := m.historyStore.Get(e.Key); ok {
				latest.LastScrobbledPercent = progress
				_ = m.historyStore.Upsert(latest)
			} else {
				_ = m.historyStore.Upsert(e)
			}
		}
	}()
}
