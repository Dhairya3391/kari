package tui

import (
	"errors"
	"fmt"
	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *modelImpl) onResolveDone(msg resolveDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.resolveOpID {
		tuiLog.Debug("stale resolve ignored", "got", msg.opID, "want", m.resolveOpID)
		return m, nil
	}
	m.resolveOpID = 0
	m.endProviderWait()
	if msg.err != nil {
		if m.playOpID == 0 {
			// Verified-missing track (e.g. unreleased dub): retrying
			// cannot help, so drop the episode instead of looping.
			if m.resolved == nil && errors.Is(msg.err, provider.ErrAudioUnavailable) {
				m.loading = false
				m.loadingText = ""
				m.autoPlayAfterResolve = false
				return m.handleUnavailableAudio(), nil
			}
			// No sources at all: retry the whole resolve automatically,
			// bounded by maxResolveAttempts, so a flaky provider doesn't
			// force a manual retry. Partial results stay playable and are
			// never retried over.
			if m.resolved == nil && m.selectedSeries != nil && m.selectedEpisode != nil && m.resolveAttempts+1 < maxResolveAttempts {
				m.resolveAttempts++
				m.loading = true
				m.loadingText = fmt.Sprintf("Retrying streams (%d/%d)...", m.resolveAttempts+1, maxResolveAttempts)
				m.beginProviderWait(m.appMode)
				opID := m.newOpID()
				m.resolveOpID = opID
				return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, *m.selectedEpisode, nil, nil))
			}
			m.loading = false
			m.loadingText = ""
			m.autoPlayAfterResolve = false
			if m.resolved == nil {
				logging.Error("resolve failed", "provider", selectedSeriesProvider(m.selectedSeries), "series", selectedSeriesTitle(m.selectedSeries), "episode", selectedEpisodeTitle(m.selectedEpisode), "err", msg.err)
				statusMsg := cleanErrorForUI(msg.err)
				if (m.appMode == provider.ModeLive || (m.selectedSeries != nil && m.selectedSeries.Type == provider.ModeLive)) && errors.Is(msg.err, provider.ErrNoSources) {
					if m.selectedSeries != nil && m.selectedSeries.Year != "" && m.selectedSeries.Year != "24/7" && m.selectedSeries.Year != "Live" {
						statusMsg = "Stream not live yet — scheduled for " + m.selectedSeries.Year
					} else {
						statusMsg = "Stream is not live yet — check back closer to match time"
					}
				}
				m.setStatus(statusError, statusMsg)
			}
		}
		return m, m.triggerSubtitleSync()
	}
	m.resolveAttempts = 0
	m.mergeResolved(msg.resolved)
	skipInfoCmd := m.triggerPreviewSkipInfo()

	// If playback is already active, don't re-trigger playback or override player status
	if m.playOpID != 0 {
		return m, skipInfoCmd
	}

	// Partial success with a preferred provider down (movy-first): one
	// quiet background repair so the favorite doesn't need a manual R.
	// Bounded to one attempt per pick; the merge dedupes anything the
	// retry re-delivers.
	if m.preferredRepairAttempts < 1 && m.preferredRepairNeeded() {
		m.preferredRepairAttempts++
		m.loading = true
		m.loadingText = "Retrying preferred provider…"
		exclude := m.retryExclude()
		retry := m.mediaService.LastFailures()
		m.beginRetryWait(m.appMode, exclude)
		opID := m.newOpID()
		m.resolveOpID = opID
		return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, *m.selectedEpisode, exclude, retry), skipInfoCmd)
	}

	// The subtitle fetch started on the first progress snapshot, so by
	// now it is usually done: only wait when a fresh fetch just started
	// or one is still in flight, keeping the soft-subtitle guarantee
	// for players without hot-add.
	subCmd := m.triggerSubtitleSync()
	if m.autoPlayAfterResolve && (subCmd != nil || m.subtitleOpID != 0) {
		m.loading = true
		m.loadingText = "Loading subtitles..."
		return m, tea.Batch(m.spinner.Tick, subCmd, skipInfoCmd)
	}
	mdl, cmd := m.finalizeResolved()
	return mdl, tea.Batch(cmd, subCmd, skipInfoCmd)
}

// onSubtitleDone attaches the fetched track to the resolved media. When
// autoPlayAfterResolve is set, playback begins once this completes so soft
// subtitles are guaranteed to be passed to the player; if playback was
// already launched manually, the track is hot-added to the running instance.
func (m *modelImpl) onSubtitleDone(msg subtitleDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.subtitleOpID {
		return m, nil
	}
	m.subtitleOpID = 0
	if m.resolved != nil {
		m.resolved.SelectedSubtitle = nil
		if msg.err == nil && strings.TrimSpace(msg.track.Path) != "" {
			track := msg.track
			m.resolved.SelectedSubtitle = &track
		}
	}
	if msg.err != nil {
		tuiLog.Debug("subtitle unavailable; playing without", "err", msg.err)
	}
	var retryCmd tea.Cmd
	if m.resolved == nil || m.resolved.SelectedSubtitle == nil {
		retryCmd = m.triggerSubtitleSync()
	}
	if m.autoPlayAfterResolve {
		if retryCmd != nil {
			return m, tea.Batch(m.spinner.Tick, retryCmd)
		}
		return m.finalizeResolved()
	}
	if m.playOpID != 0 && msg.err == nil && strings.TrimSpace(msg.track.Path) != "" {
		path := msg.track.Path
		go func() {
			if err := player.HotAddSubtitle(path); err != nil {
				tuiLog.Debug("failed to hot-add subtitle to active player", "err", err)
			}
		}()
	}
	return m, retryCmd
}

func (m *modelImpl) playStartedTimeoutCmd(opID int) tea.Cmd {
	return tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
		return playStartedMsg{opID: opID}
	})
}

func (m *modelImpl) finalizeResolved() (tea.Model, tea.Cmd) {
	if m.autoPlayAfterResolve {
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
		m.pushPlayingView()
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
	m.refreshProviderWait()
	m.pushView(viewPreview)
	if len(m.orderedPlaybackSources()) > 0 && !m.autoPlayAfterResolve {
		m.loading = false
		m.loadingText = ""
	}

	// Start the subtitle fetch on progress snapshots instead of waiting
	// for every provider: the first batch already carries usable tracks,
	// and the trigger re-fires only when the target changes or the pool
	// grows with nothing secured yet.
	subCmd := m.triggerSubtitleSync()
	skipInfoCmd := m.triggerPreviewSkipInfo()
	if wasNil {
		return m, tea.Batch(m.resolveSubscription(), m.triggerPreviewPoster(), m.triggerPreviewDetails(), subCmd, skipInfoCmd)
	}

	// Progressive snapshots can carry the TMDBID after the first one;
	// retry the details fetch once it is known so movies don't stay
	// blank. Guarded to the TMDB path so anime/movies without an ID
	// don't spam the AniList fallback on every snapshot.
	if m.previewOverview == "" && msg.resolved.TMDBID != 0 {
		return m, tea.Batch(m.resolveSubscription(), m.triggerPreviewDetails(), subCmd, skipInfoCmd)
	}

	return m, tea.Batch(m.resolveSubscription(), subCmd, skipInfoCmd)
}

// pruneResolvedToResolvers drops resolved rows for providers about to be
// re-queried, keeping rows from excluded (already-delivered) providers so
// a retry only adds sources, never removes a working one. An empty keep
// set is a full refresh: everything is dropped, matching a nil resolve.
func (m *modelImpl) pruneResolvedToResolvers(keep []string) {
	if m.resolved == nil {
		return
	}
	if len(keep) == 0 {
		m.resolved = nil
		return
	}
	keepSet := make(map[string]struct{}, len(keep))
	for _, name := range keep {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			keepSet[name] = struct{}{}
		}
	}
	kept := m.resolved.Playback[:0]
	for _, src := range m.resolved.Playback {
		if _, ok := keepSet[strings.ToLower(strings.TrimSpace(src.Resolver))]; ok {
			kept = append(kept, src)
		}
	}
	m.resolved.Playback = kept
	subs := m.resolved.Subtitles[:0]
	for _, sub := range m.resolved.Subtitles {
		if _, ok := keepSet[strings.ToLower(strings.TrimSpace(sub.Resolver))]; ok {
			subs = append(subs, sub)
		}
	}
	m.resolved.Subtitles = subs
	rawSubtitles := m.rawSubtitles[:0]
	for _, sub := range m.rawSubtitles {
		if _, ok := keepSet[strings.ToLower(strings.TrimSpace(sub.Resolver))]; ok {
			rawSubtitles = append(rawSubtitles, sub)
		}
	}
	m.rawSubtitles = rawSubtitles
}

func (m *modelImpl) mergeResolved(resolved model.ResolvedMedia) {
	if m.resolved == nil {
		m.resolved = &model.ResolvedMedia{
			SeriesTitle:        resolved.SeriesTitle,
			SeriesURL:          resolved.SeriesURL,
			EpisodeTitle:       resolved.EpisodeTitle,
			EpisodeURL:         resolved.EpisodeURL,
			MediaType:          resolved.MediaType,
			ContentMode:        resolved.ContentMode,
			Year:               resolved.Year,
			TMDBID:             resolved.TMDBID,
			SeasonNumber:       resolved.SeasonNumber,
			EpisodeNumber:      resolved.EpisodeNumber,
			Resolver:           resolved.Resolver,
			Playback:           append([]provider.MediaSource{}, resolved.Playback...),
			Subtitles:          append([]model.SubtitleTrack{}, resolved.Subtitles...),
			CompletedResolvers: append([]string{}, resolved.CompletedResolvers...),
			TotalResolvers:     resolved.TotalResolvers,
		}
		m.rawSubtitles = append([]model.SubtitleTrack{}, resolved.Subtitles...)
		m.selectedPlayback = 0
		m.refreshRanking()
		m.applyResumeFromHistory(m.resolved)
		return
	}
	// Update playback sources directly from the aggregated snapshot.
	// resolved.Playback is already sorted by MediaService with Movy.sx on top.
	var selectedURL string
	if src, ok := m.selectedPlaybackSource(); ok {
		selectedURL = src.URL
	}
	// Merge playback sources, deduplicating so retried or progressively
	// reporting providers add to existing sources instead of clobbering them.
	seenSources := make(map[string]struct{}, len(m.resolved.Playback)+len(resolved.Playback))
	for _, p := range m.resolved.Playback {
		key := p.TransportIdentity()
		if key != "" {
			seenSources[key] = struct{}{}
		}
	}
	for _, p := range resolved.Playback {
		key := p.TransportIdentity()
		if key != "" {
			if _, ok := seenSources[key]; !ok {
				m.resolved.Playback = append(m.resolved.Playback, p)
				seenSources[key] = struct{}{}
			}
		}
	}
	// Progressive snapshots may carry the TMDBID after the first one;
	// pick it up so the details/poster retry below queries TMDB and not
	// the AniList fallback.
	if m.resolved.TMDBID == 0 && resolved.TMDBID != 0 {
		m.resolved.TMDBID = resolved.TMDBID
	}
	if len(resolved.CompletedResolvers) > 0 {
		m.resolved.CompletedResolvers = resolved.CompletedResolvers
		m.resolved.TotalResolvers = resolved.TotalResolvers
		if resolved.TotalResolvers > 0 {
			m.totalProviders = resolved.TotalResolvers
		}
	}

	// If user manually switched sources with Tab, restore that specific source URL.
	// Otherwise default to the top-ranked source (index 0, e.g. Movy.sx).
	if m.manualPlaybackSelected {
		newSelected := 0
		if selectedURL != "" {
			for i, p := range m.resolved.Playback {
				if p.URL == selectedURL {
					newSelected = i
					break
				}
			}
		}
		m.selectedPlayback = newSelected
	} else {
		m.selectedPlayback = 0
	}
	// Accumulate raw subtitles from all provider updates
	seenSub := make(map[string]struct{})
	for _, subtitle := range m.rawSubtitles {
		seenSub[subtitleCandidateIdentity(subtitle)] = struct{}{}
	}
	for _, subtitle := range resolved.Subtitles {
		if subtitle.URL == "" {
			continue
		}
		key := subtitleCandidateIdentity(subtitle)
		if _, ok := seenSub[key]; ok {
			continue
		}
		m.rawSubtitles = append(m.rawSubtitles, subtitle)
		seenSub[key] = struct{}{}
	}

	if len(resolved.Subtitles) > 0 {
		m.resolved.Subtitles = append([]model.SubtitleTrack{}, m.rawSubtitles...)
	}
	m.refreshRanking()
}

func subtitleCandidateIdentity(track model.SubtitleTrack) string {
	u := strings.TrimSpace(track.URL)
	if u != "" {
		return strings.Join([]string{
			u,
			strings.TrimSpace(track.Resolver),
			strings.TrimSpace(track.SourceID),
			strings.TrimSpace(track.SourceURL),
			strings.TrimSpace(track.Referer),
		}, "\x00")
	}
	return strings.TrimSpace(track.Path)
}

func (m *modelImpl) onPlayDone(msg playDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.playOpID {
		tuiLog.Warn("play result opID mismatch", "got", msg.opID, "want", m.playOpID)
		return m, nil
	}
	m.playOpID = 0
	m.loading = false
	m.loadingText = ""
	m.autoPlayAfterResolve = false
	m.clearPresence()

	var needsConfirm *player.NeedsCompletionConfirmError
	if msg.err != nil && !errors.As(msg.err, &needsConfirm) {
		logging.Error("playback failed", "opID", msg.opID, "provider", msg.provider, "err", msg.err)
		m.setStatus(statusError, "Playback failed: "+cleanErrorForUI(msg.err))
		m.autoplay = false
		return m, nil
	}

	if msg.provider == "local" {
		tuiLog.Debug("local playback finished", "opID", msg.opID)
		return m, nil
	}
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
			AniListID: anilistIDFor(m.appMode, m.selectedSeries),
			AudioMode: audioMode,
			Language:  lang,
		}
		if err := m.historyStore.Upsert(entry); err != nil {
			tuiLog.Error("history upsert failed", "err", err)
		} else {
			m.invalidateEpisodeHistoryIndex()
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
			m.triggerScrobble(updated, m.resolved)
		} else {
			m.triggerScrobble(entry, m.resolved)
		}
	}

	logging.Info("playback finished", "opID", msg.opID, "provider", msg.provider, "result", msg.result)
	m.setStatus(statusSuccess, "Playback finished")

	m.activeView = viewPreview

	isFinished := msg.result.Completed || (msg.result.DurationSecs > 0 && (msg.result.FinalPositionSecs/msg.result.DurationSecs) >= 0.80)
	if m.autoplay && m.resolved != nil && model.IsEpisodeBased(m.resolved.MediaType) && isFinished {
		if idx, ok := m.nextEpisodeIndex(); ok {
			logging.Info("autoplay: starting next episode", "index", idx)
			return m.startEpisodeResolution(idx, true)
		}
		m.autoplay = false
		m.setStatus(statusWarn, "Autoplay: No more episodes")
	}

	return m, nil
}
