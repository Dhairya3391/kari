package tui

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
)

// reURL matches http(s) URLs so cleanErrorForUI can scrub them.
var reURL = regexp.MustCompile(`https?://\S+`)

func cleanErrorForUI(err error) string {
	if err == nil {
		return "Unknown error"
	}
	msg := err.Error()

	// Scrub transport detail: URLs embed upstream hostnames (i.e. provider
	// identities) and add noise without helping the user.
	for _, m := range reURL.FindAllString(msg, -1) {
		msg = strings.ReplaceAll(msg, m, "…")
	}

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

	short := strings.TrimSpace(msg)
	if len(short) > 60 {
		short = short[:60] + "..."
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
		logging.Error("onSearchDone failed", "opID", msg.opID, "err", msg.err)
		m.setStatus(statusError, "Search failed: "+cleanErrorForUI(msg.err))
		m.queryInput.Focus()
		return m, nil
	}

	logging.Info("onSearchDone success", "opID", msg.opID, "results_count", len(msg.results), "used_query", msg.usedQuery)
	m.allSeriesResults = msg.results
	m.usedQuery = msg.usedQuery
	m.seriesResults = msg.results
	m.seriesList.SetItems(seriesToItems(m.seriesResults))
	if len(m.seriesResults) == 0 {
		m.setStatus(statusWarn, "No results for "+msg.usedQuery+" — try another mode (tab to switch)")
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
	return m, m.triggerSearchPoster(m.selectedSeriesIndex())
}

func (m *modelImpl) onEpisodesDone(msg episodesDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.episodesOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		logging.Error("onEpisodesDone failed", "opID", msg.opID, "err", msg.err)
		m.setStatus(statusError, "Episodes load failed: "+cleanErrorForUI(msg.err))
		return m, nil
	}

	seriesTitle := ""
	mediaType := ""
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
		mediaType = m.selectedSeries.MediaType
	}

	logging.Info("onEpisodesDone success", "opID", msg.opID, "episodes_count", len(msg.results))
	m.episodeResults = msg.results
	m.episodeList.SetItems(episodesToItems(msg.results, m.historyStore, seriesTitle, m.appMode, mediaType, m.selectedEpisodes))

	if target := m.pendingHistoryTarget; target != nil {
		m.pendingHistoryTarget = nil
		if idx, ok := episodeIndexForEntry(msg.results, *target); ok {
			return m.startEpisodeResolution(idx, false)
		}
		m.setStatus(statusWarn, "Saved episode no longer available, opening series")
	}

	// Auto-resolve for movies to skip the episode list screen
	if m.selectedSeries != nil && m.selectedSeries.MediaType == provider.MediaTypeMovie && len(msg.results) > 0 {
		idx := 0
		tuiLog.Debug("movie result; auto-selecting episode flow", "title", m.selectedSeries.Title)
		return m.selectEpisode(idx)
	}

	// Auto-move cursor to first incomplete episode
	targetIdx := 0
	if m.historyStore != nil && len(msg.results) > 0 {
		found := false
		lastCompleteIdx := -1
		for i, it := range msg.results {
			entry, ok := m.historyStore.Get(history.EntryKey{
				Title:     seriesTitle,
				Mode:      string(m.appMode),
				MediaType: mediaType,
				Season:    it.Season,
				Episode:   it.Episode,
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
			if it.ID != "" && m.selectedEpisode.ID != "" && it.ID == m.selectedEpisode.ID {
				m.episodeIndex = i
				break
			}
			if it.Episode > 0 && it.Episode == m.selectedEpisode.Episode && it.Season == m.selectedEpisode.Season {
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
		logging.Error("history continue episode load failed", "title", msg.group.Title, "err", msg.err)
		m.setStatus(statusWarn, "Could not load episodes for "+msg.group.Title)
		if m.selectedEpisode == nil {
			m.pushView(viewEpisodes)
		}
		return m, nil
	}

	m.episodeResults = msg.results
	seriesTitle, mediaType := msg.group.Title, msg.group.MediaType
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
		mediaType = m.selectedSeries.MediaType
	}
	m.episodeList.SetItems(episodesToItems(msg.results, m.historyStore, seriesTitle, m.appMode, mediaType, m.selectedEpisodes))

	if idx, ok := nextEpisodeAfterEntry(msg.results, msg.group.FarthestComplete); ok {
		return m.startEpisodeResolution(idx, false)
	}
	if idx, ok := episodeIndexForEntry(msg.results, msg.group.ContinueEntry); ok {
		m.setStatus(statusWarn, "No next episode found, opening last watched")
		return m.startEpisodeResolution(idx, false)
	}
	if m.selectedEpisode == nil {
		m.pushView(viewEpisodes)
	}
	m.setStatus(statusWarn, "No next episode found")
	return m, nil
}

func (m *modelImpl) onResolveDone(msg resolveDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.resolveOpID {
		tuiLog.Debug("stale resolve ignored", "got", msg.opID, "want", m.resolveOpID)
		return m, nil
	}
	m.resolveOpID = 0
	if msg.err != nil {
		m.loading = false
		m.loadingText = ""
		m.autoPlayAfterResolve = false
		m.pendingAutoPlay = false
		if m.resolved == nil {
			logging.Error("resolve failed", "provider", selectedSeriesProvider(m.selectedSeries), "series", selectedSeriesTitle(m.selectedSeries), "episode", selectedEpisodeTitle(m.selectedEpisode), "err", msg.err)
			m.setStatus(statusError, cleanErrorForUI(msg.err))
		}
		return m, nil
	}
	m.mergeResolved(msg.resolved)

	// All providers have now reported in, so this is the first point where
	// every provider's subtitles are actually known — fetch now rather than
	// on the first (possibly incomplete) progress update.
	subCmd := m.triggerSubtitleSync()
	mdl, cmd := m.finalizeResolved()
	return mdl, tea.Batch(cmd, subCmd)
}

func (m *modelImpl) onSubtitleDone(msg subtitleDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.subtitleOpID {
		return m, nil
	}
	m.subtitleOpID = 0
	if msg.err == nil && len(msg.tracks) > 0 && m.resolved != nil {
		// Set directly rather than through mergeResolved: its subtitle merge
		// deliberately refuses to replace an already-downloaded subtitle
		// (to protect against a stale/duplicate progress update undoing a
		// real fetch), but this IS a deliberate replacement — the whole
		// point of a re-sync triggered by switching sources is to swap out
		// whatever subtitle was already there for the new source's own one.
		m.resolved.Subtitles = msg.tracks
	}
	if m.pendingManualPlay {
		m.pendingManualPlay = false
		m.loading = true
		opID := m.newOpID()
		m.playOpID = opID
		if m.pendingPlayFromStart {
			m.pendingPlayFromStart = false
			m.loadingText = "Starting from beginning..."
			return m, tea.Batch(m.spinner.Tick, m.playCmdWithStartTime(opID, 0), m.playStartedTimeoutCmd(opID))
		}
		m.loadingText = "Opening player..."
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
			m.loading = true
			m.loadingText = "Downloading subtitles..."
			m.pushView(viewPreview)
			m.setStatus(statusInfo, "")
			return m, m.spinner.Tick
		}
		if len(m.orderedPlaybackSources()) == 0 {
			m.autoPlayAfterResolve = false
			m.loading = false
			m.loadingText = ""
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
	m.loading = false
	m.loadingText = ""
	m.pushView(viewPreview)
	m.setStatus(statusInfo, "")
	return m, nil
}

func (m *modelImpl) applyResumeFromHistory(resolved *model.ResolvedMedia) {
	if m.historyStore == nil || resolved == nil {
		return
	}

	entry, ok := m.historyStore.Get(history.EntryKey{
		Title:     resolved.SeriesTitle,
		Mode:      string(m.appMode),
		MediaType: resolved.MediaType,
		Season:    resolved.SeasonNumber,
		Episode:   resolved.EpisodeNumber,
	})

	if ok && !entry.Complete && entry.PositionSecs > 5 {
		resolved.StartTime = entry.PositionSecs
		tuiLog.Info("resume point found", "positionSecs", entry.PositionSecs, "title", resolved.SeriesTitle)
	} else {
		resolved.StartTime = 0
	}
}

func (m *modelImpl) onResolveProgress(msg resolveProgressMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.resolveOpID {
		return m, m.resolveSubscription()
	}

	wasNil := m.resolved == nil
	m.mergeResolved(msg.resolved)
	m.pushView(viewPreview)

	subCmd := m.triggerSubtitleSync()

	var finalizeCmd tea.Cmd
	if m.autoPlayAfterResolve && len(m.orderedPlaybackSources()) > 0 {
		var mdl tea.Model
		mdl, finalizeCmd = m.finalizeResolved()
		m = mdl.(*modelImpl)
	}

	if wasNil {
		return m, tea.Batch(m.resolveSubscription(), m.triggerPreviewPoster(), m.triggerPreviewDetails(), subCmd, finalizeCmd)
	}

	return m, tea.Batch(m.resolveSubscription(), subCmd, finalizeCmd)
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
			Playback:      append([]provider.MediaSource{}, resolved.Playback...),
			Subtitles:     append([]model.SubtitleTrack{}, resolved.Subtitles...),
		}
		m.rawSubtitles = append([]model.SubtitleTrack{}, resolved.Subtitles...)
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

	// Accumulate raw subtitles from all provider updates
	seenSub := make(map[string]struct{})
	for _, s := range m.rawSubtitles {
		seenSub[s.URL] = struct{}{}
	}
	for _, s := range resolved.Subtitles {
		if s.URL != "" {
			if _, ok := seenSub[s.URL]; !ok {
				m.rawSubtitles = append(m.rawSubtitles, s)
				seenSub[s.URL] = struct{}{}
			}
		}
	}

	// Only replace subtitles from resolve phase if we don't already have downloaded ones
	if len(resolved.Subtitles) > 0 && !hasDownloadedSubtitles(m.resolved.Subtitles) {
		m.resolved.Subtitles = append([]model.SubtitleTrack{}, m.rawSubtitles...)
	}
	m.ensurePlaybackSelection()
}

func (m *modelImpl) onPlayDone(msg playDoneMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingText = ""
	m.autoPlayAfterResolve = false

	if msg.opID != m.playOpID {
		tuiLog.Warn("play result opID mismatch", "got", msg.opID, "want", m.playOpID)
		return m, nil
	}
	m.playOpID = 0

	var needsConfirm *player.NeedsCompletionConfirmError
	isConfirmErr := errors.As(msg.err, &needsConfirm)

	if msg.err != nil && !isConfirmErr {
		logging.Error("playback failed", "opID", msg.opID, "provider", msg.provider, "err", msg.err)
		m.setStatus(statusError, "Playback failed: "+cleanErrorForUI(msg.err))
		m.autoplay = false
		return m, nil
	}

	// Update history
	if m.historyStore != nil && m.resolved != nil {
		audioMode := m.audioMode
		if m.selectedEpisode != nil && m.selectedEpisode.Audio != "" {
			audioMode = m.selectedEpisode.Audio
		}
		lang := ""
		if src, ok := m.selectedPlaybackSource(); ok && src.Language != "" {
			lang = src.Language
		} else if m.prevSourceLanguage != "" {
			lang = m.prevSourceLanguage
		}

		entry := history.Entry{
			Key: history.EntryKey{
				Title:     m.resolved.SeriesTitle,
				Mode:      string(m.appMode),
				MediaType: m.resolved.MediaType,
				Season:    m.resolved.SeasonNumber,
				Episode:   m.resolved.EpisodeNumber,
			},
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
			Mode:      string(m.appMode),
			MediaType: m.resolved.MediaType,
			TMDBID:    m.resolved.TMDBID,
			AudioMode: audioMode,
			Language:  lang,
		}
		if err := m.historyStore.Upsert(entry); err != nil {
			tuiLog.Error("history upsert failed", "err", err)
		}

		// Update resolved StartTime to reflect updated resume point or completion
		m.applyResumeFromHistory(m.resolved)
		// Refresh episode list markers if it exists
		if len(m.episodeResults) > 0 {
			seriesTitle, mediaType := "", ""
			if m.selectedSeries != nil {
				seriesTitle = m.selectedSeries.Title
				mediaType = m.selectedSeries.MediaType
			}
			m.episodeList.SetItems(episodesToItems(m.episodeResults, m.historyStore, seriesTitle, m.appMode, mediaType, m.selectedEpisodes))
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
		logging.Info("playback finished on Android, needs confirmation")
	} else {
		logging.Info("playback finished", "opID", msg.opID, "provider", msg.provider, "result", msg.result)
		m.setStatus(statusSuccess, "Playback finished")
	}

	m.activeView = viewPreview

	if m.autoplay && m.resolved != nil && model.IsEpisodeBased(m.resolved.MediaType) {
		if idx, ok := m.nextEpisodeIndex(); ok {
			logging.Info("autoplay: starting next episode", "index", idx)
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
	m.downloadProgress = msg.progress * 100
	m.downloadTotalSize = msg.totalSize
	m.downloadSpeed = msg.speed
	m.downloadDownloaded = msg.downloaded
	m.downloadETA = msg.eta
	m.loadingText = downloadLoadingText(m.downloadProgress, m.downloadTotalSize, m.downloadSpeed, m.downloadDownloaded, m.downloadETA)
	return m, m.downloadSubscription()
}

func downloadLoadingText(progress float64, totalSize, speed, downloaded, eta string) string {
	if progress < 0 {
		return "Downloading..."
	}
	if totalSize != "" && speed != "" && downloaded != "" {
		text := fmt.Sprintf("Downloading %.1f%% — %s / %s at %s", progress, downloaded, totalSize, speed)
		if eta != "" {
			text += fmt.Sprintf(", ETA %s", eta)
		}
		return text
	}
	if progress >= 100 && totalSize != "" {
		return fmt.Sprintf("Downloaded %s", totalSize)
	}
	return fmt.Sprintf("Downloading... %.1f%%", progress)
}

func (m *modelImpl) onDownloadDone(msg downloadDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""

	statusMsg := "Download complete"
	if m.downloadTotalSize != "" {
		statusMsg = fmt.Sprintf("Downloaded %s", m.downloadTotalSize)
	}

	m.downloadProgress = 0
	m.downloadTotalSize = ""
	m.downloadSpeed = ""
	m.downloadDownloaded = ""
	m.downloadETA = ""
	m.cancelDownload = nil
	m.downloadOpID = 0
	if msg.err != nil {
		logging.Error("download failed", "opID", msg.opID, "err", msg.err)
		errMsg := fmt.Sprintf("Download failed: %v", msg.err)
		if errors.Is(msg.err, exec.ErrNotFound) || strings.Contains(msg.err.Error(), "executable file not found") {
			errMsg = "Download failed: yt-dlp is not installed"
		}
		return m, m.setStatusTimed(statusError, errMsg)
	}

	return m, m.setStatusTimed(statusSuccess, statusMsg)
}

func (m *modelImpl) downloadSubscription() tea.Cmd {
	return func() tea.Msg {
		return <-m.downloadChan
	}
}

func (m *modelImpl) drainDownloadChan() {
	for {
		select {
		case <-m.downloadChan:
		default:
			return
		}
	}
}

func (m *modelImpl) resolveSubscription() tea.Cmd {
	return func() tea.Msg {
		return <-m.resolveChan
	}
}
