package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
)

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
			m.clearSearchPoster()
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

	prevSel := m.selectedSeriesIndex()
	var cmd tea.Cmd
	m.seriesList, cmd = m.seriesList.Update(msg)
	cmds := []tea.Cmd{cmd}
	if newSel := m.selectedSeriesIndex(); newSel != prevSel {
		cmds = append(cmds, m.triggerSearchPoster(newSel))
	}
	return m, tea.Batch(cmds...)
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
				entry := history.Entry{
					Key: history.EntryKey{
						Title:     m.resolved.SeriesTitle,
						Mode:      string(m.appMode),
						MediaType: m.resolved.MediaType,
						Season:    m.resolved.SeasonNumber,
						Episode:   m.resolved.EpisodeNumber,
					},
					Title:        m.resolved.SeriesTitle,
					EpisodeTitle: m.resolved.EpisodeTitle,
					Season:       m.resolved.SeasonNumber,
					Episode:      m.resolved.EpisodeNumber,
					WatchedAt:    time.Now(),
					PositionSecs: 1, // Set to 1 to satisfy >85% if duration 1
					DurationSecs: 1,
					Complete:     true,

					// Metadata
					Mode:      string(m.appMode),
					MediaType: m.resolved.MediaType,
					TMDBID:    m.resolved.TMDBID,
				}
				_ = m.historyStore.Upsert(entry)

				// Refresh episode list markers
				if len(m.episodeResults) > 0 {
					seriesTitle, mediaType := "", ""
					if m.selectedSeries != nil {
						seriesTitle = m.selectedSeries.Title
						mediaType = m.selectedSeries.MediaType
					}
					m.episodeList.SetItems(episodesToItems(m.episodeResults, m.historyStore, seriesTitle, m.appMode, mediaType, m.selectedEpisodes))
				}

				if updated, ok := m.historyStore.Get(entry.Key); ok {
					m.triggerScrobble(updated)
				} else {
					m.triggerScrobble(entry)
				}
			}
			m.confirmCompletion = false
			return m, m.setStatusTimed(statusSuccess, "Marked as complete")
		case "n", "N":
			m.confirmCompletion = false
			return m, nil
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
		if m.loading || m.playOpID != 0 || m.pendingManualPlay {
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
		if m.loading || m.playOpID != 0 || m.pendingManualPlay {
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
		return m, m.triggerSubtitleSync()
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
