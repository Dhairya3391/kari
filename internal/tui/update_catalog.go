package tui

import (
	"context"
	"errors"
	"fmt"
	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/provider"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

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
			m.episodeCache.valid = false
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
	m.episodeCache.valid = false
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
		historyEntries := episodeHistorySnapshot(m.historyStore)
		for i, it := range msg.results {
			entry, ok := historyEntries[history.EntryKey{
				Title:     seriesTitle,
				Mode:      string(m.appMode),
				MediaType: mediaType,
				Season:    it.Season,
				Episode:   it.Episode,
			}.String()]
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
	if m.selectedEpisode == nil && targetIdx == 0 {
		m.activeSeason = 0
		m.episodeIndex = 0
	} else if m.episodeIndex >= 0 && m.episodeIndex < len(m.episodeResults) {
		ep := m.episodeResults[m.episodeIndex]
		if ep.Season > 0 {
			seasons := distinctSeasonNumbers(m.episodeResults)
			m.activeSeason = 0
			for sIdx, sNum := range seasons {
				if sNum == ep.Season {
					m.activeSeason = sIdx
					break
				}
			}
		} else {
			m.activeSeason = 0
		}
	} else {
		m.activeSeason = 0
	}
	if m.episodeIndex >= 0 && m.episodeIndex < len(m.episodeList.Items()) {
		m.episodeList.Select(m.episodeIndex)
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
		m.episodeCache.valid = false
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
	m.episodeCache.valid = false
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
	return m
}
