package tui

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"errors"

	"github.com/charmbracelet/bubbles/textinput"
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

	lower := strings.ToLower(msg)
	if strings.Contains(lower, "rate limited") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "429") {
		return "Rate limited by Pengu, use your own token for better rate limits"
	}
	if strings.Contains(lower, "no results found") || strings.Contains(lower, "no results") {
		return "No results found"
	}
	if strings.Contains(lower, "no sources found") {
		return "No sources found"
	}
	if strings.Contains(lower, "deadline") || strings.Contains(lower, "timeout") {
		return "Request timed out — please try again"
	}
	if strings.Contains(lower, "connection") || strings.Contains(lower, "no such host") {
		return "Network connection failed — check your connection"
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
		if errors.Is(msg.err, provider.ErrNoResults) {
			m.allSeriesResults = nil
			m.usedQuery = msg.usedQuery
			m.seriesResults = nil
			m.seriesList.SetItems(nil)
			m.clearSearchPoster()
			m.setStatus(statusWarn, fmt.Sprintf("No results found for %q — press Tab to switch categories", msg.usedQuery))
			m.queryInput.Focus()
			return m, textinput.Blink
		}
		logging.Error("onSearchDone failed", "opID", msg.opID, "err", msg.err)
		m.setStatus(statusError, cleanErrorForUI(msg.err))
		m.queryInput.Focus()
		return m, textinput.Blink
	}

	logging.Info("onSearchDone success", "opID", msg.opID, "results_count", len(msg.results), "used_query", msg.usedQuery)
	m.allSeriesResults = msg.results
	m.usedQuery = msg.usedQuery
	m.seriesResults = msg.results
	// A fresh result list drops any stale / text filter.
	m.resultsFilter = ""
	m.resultsFiltering = false
	m.seriesList.SetItems(seriesToItems(m.seriesResults))
	if len(m.seriesResults) == 0 {
		m.setStatus(statusWarn, fmt.Sprintf("No results found for %q — press Tab to switch categories", msg.usedQuery))
		m.queryInput.Focus()
		return m, textinput.Blink
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
		if m.appMode == provider.ModeAnime && strings.EqualFold(strings.TrimSpace(m.audioMode), provider.AudioDub) && errors.Is(msg.err, provider.ErrNoEpisodes) {
			m.episodeResults = nil
			m.episodeList.SetItems(nil)
			if m.selectedEpisode == nil {
				m.pushView(viewEpisodes)
			}
			m.setStatus(statusWarn, "No dub episodes are available yet — press a for sub")
			return m, nil
		}
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

	// If opening a series fresh (targetIdx == 0), start on Season 1 (index 0)
	if targetIdx == 0 {
		m.activeSeason = 0
		m.episodeIndex = 0
	} else if m.episodeIndex >= 0 && m.episodeIndex < len(m.episodeResults) {
		ep := m.episodeResults[m.episodeIndex]
		if ep.Season > 0 {
			m.activeSeason = ep.Season - 1
		} else {
			m.activeSeason = 0
		}
	} else {
		m.activeSeason = 0
	}
	// A fresh episode list drops any stale text filter.
	m.episodeFilter = ""
	m.episodeFiltering = false
	_, origIndices := m.currentSeasonEpisodes()
	m.seasonEpisodeIndex = 0
	for i, origIdx := range origIndices {
		if origIdx == m.episodeIndex {
			m.seasonEpisodeIndex = i
			break
		}
	}

	if m.selectedEpisode == nil {
		m.pushView(viewEpisodes)
	}
	if m.appMode == provider.ModeAnime && strings.EqualFold(strings.TrimSpace(m.audioMode), provider.AudioDub) && len(msg.results) == 0 {
		m.setStatus(statusWarn, "No dub episodes are available yet — press a for sub")
	} else {
		m.setStatus(statusInfo, "")
	}
	return m, m.maybeFetchEpisodeTitles()
}

// placeholderEpisodeRE matches provider placeholder titles ("Episode 12",
// "EP 3", "Ep. 5") that carry no real episode name.
var placeholderEpisodeRE = regexp.MustCompile(`(?i)^\s*(?:ep|episode|eps|e)\.?\s*\d+\s*$`)

// isPlaceholderEpisodeTitle reports whether an episode title is empty or
// a bare "Episode N" placeholder rather than a real name.
func isPlaceholderEpisodeTitle(title string) bool {
	t := strings.TrimSpace(title)
	return t == "" || placeholderEpisodeRE.MatchString(t)
}

// maybeFetchEpisodeTitles starts a best-effort AniList lookup for real
// episode titles when the anime provider only sent placeholders. It
// returns nil unless there is something to enrich: anime mode, a numeric
// (AniList) series id, and at least one placeholder title.
func (m *modelImpl) maybeFetchEpisodeTitles() tea.Cmd {
	if m.appMode != provider.ModeAnime || m.posterClient == nil || m.selectedSeries == nil {
		return nil
	}
	anilistID, err := strconv.Atoi(strings.TrimSpace(m.selectedSeries.ID))
	if err != nil || anilistID <= 0 {
		return nil
	}
	needed := make(map[int]struct{})
	for i, ep := range m.episodeResults {
		if !isPlaceholderEpisodeTitle(ep.Title) {
			continue
		}
		num := ep.Episode
		if num <= 0 {
			num = i + 1
		}
		needed[num] = struct{}{}
	}
	if len(needed) == 0 {
		return nil
	}
	opID := m.newOpID()
	m.episodeTitlesOpID = opID
	client := m.posterClient
	seriesTitle := m.selectedSeries.Title
	seriesYear, _ := strconv.Atoi(strings.TrimSpace(m.selectedSeries.Year))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.appCtx, 20*time.Second)
		defer cancel()
		titles, err := client.FetchEpisodeTitles(ctx, anilistID)
		if err != nil {
			tuiLog.Debug("anilist episode titles fetch failed", "anilist_id", anilistID, "err", err)
		}
		// AniList's streamingEpisodes only name the episodes it has licensed
		// (e.g. the first 26 of Naruto's 220), so backfill the still-missing
		// rows from TMDB keyed by absolute episode number. AniList wins on
		// conflicts.
		missing := false
		for num := range needed {
			if _, ok := titles[num]; !ok {
				missing = true
				break
			}
		}
		if missing {
			tmdbTitles, tmdbErr := client.FetchEpisodeTitlesTMDB(ctx, seriesTitle, seriesYear)
			if tmdbErr != nil {
				tuiLog.Debug("tmdb episode titles fetch failed", "title", seriesTitle, "err", tmdbErr)
			}
			for num, title := range tmdbTitles {
				if _, ok := titles[num]; ok {
					continue
				}
				if titles == nil {
					titles = make(map[int]string)
				}
				titles[num] = title
			}
		}
		return episodeTitlesMsg{titles: titles, opID: opID}
	}
}

// onEpisodeTitles patches placeholder episode titles with the real names
// looked up on AniList. Provider-sent real titles are never overwritten;
// entries AniList has no name for keep their placeholder.
func (m *modelImpl) onEpisodeTitles(msg episodeTitlesMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.episodeTitlesOpID || len(msg.titles) == 0 {
		return m, nil
	}
	patched := false
	for i, ep := range m.episodeResults {
		if !isPlaceholderEpisodeTitle(ep.Title) {
			continue
		}
		num := ep.Episode
		if num <= 0 {
			num = i + 1
		}
		if title, ok := msg.titles[num]; ok && strings.TrimSpace(title) != "" {
			m.episodeResults[i].Title = title
			patched = true
		}
	}
	if patched {
		m.refreshEpisodeList()
	}
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
	titleCmd := m.maybeFetchEpisodeTitles()

	if idx, ok := nextEpisodeAfterEntry(msg.results, msg.group.FarthestComplete); ok {
		mdl, cmd := m.startEpisodeResolution(idx, false)
		return mdl, tea.Batch(cmd, titleCmd)
	}
	if idx, ok := episodeIndexForEntry(msg.results, msg.group.ContinueEntry); ok {
		m.setStatus(statusWarn, "No next episode found, opening last watched")
		mdl, cmd := m.startEpisodeResolution(idx, false)
		return mdl, tea.Batch(cmd, titleCmd)
	}
	if m.selectedEpisode == nil {
		m.pushView(viewEpisodes)
	}
	m.setStatus(statusWarn, "No next episode found")
	return m, nil
}

func (m *modelImpl) handleUnavailableAudio() *modelImpl {
	if m.selectedEpisode == nil {
		m.setStatus(statusWarn, "The selected audio track is unavailable — choose another episode")
		return m
	}

	audio := strings.ToLower(strings.TrimSpace(m.selectedEpisode.Audio))
	if audio == "" {
		audio = "audio"
	}
	message := fmt.Sprintf("No %s track for Episode %d", audio, m.selectedEpisode.Episode)
	if m.appMode == provider.ModeAnime {
		alternate := provider.AudioSub
		if audio == provider.AudioSub {
			alternate = provider.AudioDub
		}
		message += " — press a to switch to " + alternate
	}
	m.setStatus(statusWarn, message)
	m.setToast(message, ToastInfo)
	return m
}

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
		return m, nil
	}
	m.resolveAttempts = 0
	m.mergeResolved(msg.resolved)

	// If playback is already active, don't re-trigger playback or override player status
	if m.playOpID != 0 {
		return m, nil
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
		return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, *m.selectedEpisode, exclude, retry))
	}

	// All providers have now reported in, so this is the first point where
	// every provider's subtitles are actually known — fetch now rather than
	// on the first (possibly incomplete) progress update.
	subCmd := m.triggerSubtitleSync()
	mdl, cmd := m.finalizeResolved()
	return mdl, tea.Batch(cmd, subCmd)
}

// onSubtitleDone attaches the fetched track to the resolved media. Playback
// never waits for this: sources alone start play, and the subtitle joins the
// next launch (or this one if the fetch beats the player handshake).
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
	return m, nil
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

	if wasNil {
		return m, tea.Batch(m.resolveSubscription(), m.triggerPreviewPoster(), m.triggerPreviewDetails())
	}

	// Progressive snapshots can carry the TMDBID after the first one;
	// retry the details fetch once it is known so movies don't stay
	// blank. Guarded to the TMDB path so anime/movies without an ID
	// don't spam the AniList fallback on every snapshot.
	if m.previewOverview == "" && msg.resolved.TMDBID != 0 {
		return m, tea.Batch(m.resolveSubscription(), m.triggerPreviewDetails())
	}

	return m, m.resolveSubscription()
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
	return strings.Join([]string{
		strings.TrimSpace(track.URL),
		strings.TrimSpace(track.SourceID),
		strings.TrimSpace(track.SourceURL),
		strings.TrimSpace(track.Referer),
	}, "\x00")
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

	var needsConfirm *player.NeedsCompletionConfirmError
	if msg.err != nil && !errors.As(msg.err, &needsConfirm) {
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
			AniListID: anilistIDFor(m.appMode, m.selectedSeries),
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

	logging.Info("playback finished", "opID", msg.opID, "provider", msg.provider, "result", msg.result)
	m.setStatus(statusSuccess, "Playback finished")

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
	return m, m.downloadSubscription()
}

func (m *modelImpl) onDownloadDone(msg downloadDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, nil
	}
	// A user-initiated pause cancels the context. Keep the progress readout
	// and partial files so resume continues from the checkpoint.
	if errors.Is(msg.err, context.Canceled) {
		logging.Info("download paused", "opID", msg.opID)
		m.cancelDownload = nil
		m.downloadOpID = 0
		return m, nil
	}
	statusMsg := "download complete"
	if m.downloadTotalSize != "" {
		statusMsg = fmt.Sprintf("downloaded %s", m.downloadTotalSize)
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
		if errors.Is(msg.err, exec.ErrNotFound) {
			errMsg = "Download failed: yt-dlp is not installed"
		}
		if m.pendingDownload != nil {
			m.downloadPaused = true
		}
		return m, m.setStatusTimed(statusError, errMsg)
	}

	if m.downloadService != nil {
		if err := m.downloadService.ClearPendingJob(); err != nil {
			tuiLog.Warn("clear pending download failed", "err", err)
		}
	}
	m.pendingDownload = nil
	m.downloadPaused = false
	m.setToast(statusMsg, ToastSuccess)
	return m, nil
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
