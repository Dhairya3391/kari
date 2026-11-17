package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/provider"
	"kari/internal/service"
)

func (m *modelImpl) updateSearch(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.queryInput.Focused() {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			if key.Matches(keyMsg, m.keys.Type) {
				return m, m.cycleMode(keyMsg.Type == tea.KeyShiftTab)
			}
			switch keyMsg.String() {
			case "enter":
				return m.startSearchFromInput()
			case "esc":
				m.queryInput.Blur()
				m.clearStatus()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.queryInput, cmd = m.queryInput.Update(msg)
		if cleaned := scrubTerminalResponses(m.queryInput.Value()); cleaned != m.queryInput.Value() {
			m.queryInput.SetValue(cleaned)
		}

		if strings.TrimSpace(m.queryInput.Value()) == "" {
			m.seriesResults = nil
			m.seriesList.SetItems(nil)
			m.allSeriesResults = nil
			m.clearSearchPoster()
		}

		return m, cmd
	}

	// If results are empty (Search Home screen)
	if len(m.seriesResults) == 0 {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			switch {
			case keyMsg.String() == "enter":
				if m.modeFeatures().AllowEmptyQuery {
					return m.startSearchFromInput()
				}
				m.queryInput.Focus()
				m.setStatus(statusInfo, "")
				return m, textinput.Blink
			case keyMsg.String() == "space" || key.Matches(keyMsg, m.keys.Search):
				m.queryInput.SetValue("")
				m.queryInput.Focus()
				m.setStatus(statusInfo, "")
				return m, textinput.Blink
			case keyMsg.String() == "h":
				m.loading = true
				m.loadingText = "Loading history..."
				_, historyCmd := m.refreshHistory()
				return m, tea.Batch(m.spinner.Tick, historyCmd, func() tea.Msg {
					return historyLoadedMsg{}
				})
			case keyMsg.String() == "d":
				m.pushView(viewDownloads)
				return m, nil
			case keyMsg.String() == "s":
				m.pushView(viewSettings)
				return m, nil
			case key.Matches(keyMsg, m.keys.Type):
				cmd := m.cycleMode(keyMsg.Type == tea.KeyShiftTab)
				return m, cmd
			default:
				// In control mode, digits 1..9 jump to the nth effective mode
				if s := keyMsg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
					digitIdx := int(s[0] - '1')
					if digitIdx < len(m.modes) {
						cmd := m.switchToMode(m.modes[digitIdx])
						return m, cmd
					}
				}
			}
		}
		return m, nil
	}

	// If results exist (Results screen)
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if m.resultsFiltering {
			switch keyMsg.String() {
			case "enter":
				m.resultsFiltering = false
				return m, nil
			case "backspace":
				if len(m.resultsFilter) > 0 {
					m.resultsFilter = m.resultsFilter[:len(m.resultsFilter)-1]
					m.refilterSeriesList()
				}
				return m, nil
			case "up", "k":
				return m, m.moveSeriesSelection(m.seriesList.CursorUp)
			case "down", "j":
				return m, m.moveSeriesSelection(m.seriesList.CursorDown)
			}
			if len(keyMsg.String()) == 1 {
				m.resultsFilter += keyMsg.String()
				m.refilterSeriesList()
				return m, nil
			}
			return m, nil
		}
		if m.appMode == provider.ModeLive {
			switch keyMsg.String() {
			case "r":
				return m, m.refreshLiveCmd()
			case "1", "2", "3":
				return m, m.jumpLiveSection(int(keyMsg.String()[0] - '1'))
			}
		}
		if keyMsg.String() == "left" || keyMsg.String() == "right" {
			return m, nil
		}
		switch {
		case key.Matches(keyMsg, m.keys.Select) || keyMsg.String() == "enter":
			return m.selectSeries(m.selectedSeriesIndex())
		case keyMsg.String() == " " || keyMsg.String() == "space" || key.Matches(keyMsg, m.keys.Search):
			m.resultsFilter = ""
			m.resultsFiltering = false
			m.queryInput.SetValue("")
			m.queryInput.Focus()
			m.setStatus(statusInfo, "")
			return m, textinput.Blink
		case keyMsg.String() == "/" || keyMsg.String() == "ctrl+f" || key.Matches(keyMsg, m.keys.Filter):
			// / filters the current results by title.
			m.resultsFiltering = true
			m.resultsFilter = ""
			m.refilterSeriesList()
		case keyMsg.String() == "d":
			m.pushView(viewDownloads)
			return m, nil
		case key.Matches(keyMsg, m.keys.Type):
			cmd := m.cycleMode(keyMsg.Type == tea.KeyShiftTab)
			return m, cmd
		case keyMsg.String() == "esc":
			m.seriesResults = nil
			m.seriesList.SetItems(nil)
			m.allSeriesResults = nil
			m.resultsFilter = ""
			m.resultsFiltering = false
			m.clearSearchPoster()
			m.clearStatus()
			return m, nil
		case keyMsg.String() == "up" || keyMsg.String() == "k":
			return m, m.moveSeriesSelection(m.seriesList.CursorUp)
		case keyMsg.String() == "down" || keyMsg.String() == "j":
			return m, m.moveSeriesSelection(m.seriesList.CursorDown)
		case keyMsg.String() == "g" || keyMsg.String() == "home":
			return m, m.moveSeriesSelection(func() { m.seriesList.Select(0) })
		case keyMsg.String() == "G" || keyMsg.String() == "end":
			return m, m.moveSeriesSelection(func() {
				if items := m.seriesList.Items(); len(items) > 0 {
					m.seriesList.Select(len(items) - 1)
				}
			})
		case keyMsg.String() == "pgup" || keyMsg.String() == "ctrl+u":
			step := m.pageStep()
			if keyMsg.String() == "ctrl+u" {
				step = max(1, step/2)
			}
			return m, m.moveSeriesSelection(func() { m.pageSeriesList(-step) })
		case keyMsg.String() == "pgdown" || keyMsg.String() == "ctrl+d":
			step := m.pageStep()
			if keyMsg.String() == "ctrl+d" {
				step = max(1, step/2)
			}
			return m, m.moveSeriesSelection(func() { m.pageSeriesList(step) })
		default:
			if s := keyMsg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
				digitIdx := int(s[0] - '1')
				if digitIdx < len(m.modes) {
					cmd := m.switchToMode(m.modes[digitIdx])
					return m, cmd
				}
			}
		}
	}

	prevSel := m.selectedSeriesIndex()
	var cmd tea.Cmd
	m.seriesList, cmd = m.seriesList.Update(msg)
	cmds := []tea.Cmd{cmd}
	if newSel := m.selectedSeriesIndex(); newSel != prevSel {
		cmds = append(cmds, m.triggerSearchPoster(newSel))
		cmds = append(cmds, m.prefetchSelectedReadingChapters())
	}
	return m, tea.Batch(cmds...)
}

// moveSeriesSelection applies a cursor move and refreshes the poster only when
// the selection actually changed. At a list boundary the move is a no-op, so
// redundant scroll/key events no longer cancel and refetch the same artwork.
func (m *modelImpl) moveSeriesSelection(move func()) tea.Cmd {
	before := m.selectedSeriesIndex()
	move()
	if m.selectedSeriesIndex() == before {
		return nil
	}
	return tea.Batch(m.triggerSearchPoster(m.selectedSeriesIndex()), m.prefetchSelectedReadingChapters())
}

func (m *modelImpl) updatePreview(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "enter":
		if !m.mediaReady() {
			if m.loadInFlight() {
				m.setStatus(statusInfo, "Resolving playback streams...")
				return m, nil
			}
			m.setStatus(statusWarn, "No playback source available")
			return m, nil
		}
		if m.playOpID != 0 {
			return m, nil
		}

		// Store sticky provider choice for this title
		if src, ok := m.selectedPlaybackSource(); ok && m.resolved != nil {
			if m.stickyProviders == nil {
				m.stickyProviders = make(map[string]string)
			}
			m.stickyProviders[m.resolved.SeriesTitle] = src.Resolver
		}

		m.loading = true
		m.loadingText = "Opening player..."
		opID := m.newOpID()
		m.playOpID = opID
		m.pushPlayingView()
		return m, tea.Batch(m.spinner.Tick, m.playCmd(opID), m.playStartedTimeoutCmd(opID))

	case "up", "k":
		if m.previewSelectedIndex > 0 {
			m.previewSelectedIndex--
			m.manualPlaybackSelected = true
			m.invalidateSubtitleSync()
			return m, m.triggerSubtitleSync()
		}
		return m, nil

	case "down", "j":
		if m.previewSelectedIndex < len(m.rankedSources)-1 {
			m.previewSelectedIndex++
			m.manualPlaybackSelected = true
			m.invalidateSubtitleSync()
			return m, m.triggerSubtitleSync()
		}
		return m, nil

	case "g", "home":
		m.previewSelectedIndex = 0
		m.manualPlaybackSelected = true
		m.invalidateSubtitleSync()
		return m, m.triggerSubtitleSync()

	case "G", "end":
		if len(m.rankedSources) > 0 {
			m.previewSelectedIndex = len(m.rankedSources) - 1
			m.manualPlaybackSelected = true
			m.invalidateSubtitleSync()
			return m, m.triggerSubtitleSync()
		}
		return m, nil
	case "pgup", "pgdown", "ctrl+u", "ctrl+d":
		step := m.pageStep()
		switch keyMsg.String() {
		case "ctrl+u", "ctrl+d":
			step = max(1, step/2)
		}
		delta := step
		if keyMsg.String() == "pgup" || keyMsg.String() == "ctrl+u" {
			delta = -delta
		}
		next := clampIndex(m.previewSelectedIndex+delta, len(m.rankedSources))
		if next != m.previewSelectedIndex {
			m.previewSelectedIndex = next
			m.manualPlaybackSelected = true
			m.invalidateSubtitleSync()
			return m, m.triggerSubtitleSync()
		}
		return m, nil

	case "r":
		// Restart the current episode from the beginning.
		if m.resolved == nil || len(m.orderedPlaybackSources()) == 0 || m.playOpID != 0 {
			return m, nil
		}
		m.loading = true
		m.loadingText = "Starting from beginning..."
		opID := m.newOpID()
		m.playOpID = opID
		m.pushPlayingView()
		return m, tea.Batch(m.spinner.Tick, m.playCmdWithStartTime(opID, 0), m.playStartedTimeoutCmd(opID))

	case "R":
		if m.selectedSeries != nil && m.selectedEpisode != nil {
			if m.resolveOpID != 0 || m.playOpID != 0 {
				m.setStatus(statusInfo, "Please wait for the current operation")
				return m, nil
			}
			exclude := m.retryExclude()
			retry := []string(nil)
			if m.mediaService != nil {
				retry = m.mediaService.LastFailures()
			}
			m.invalidateSubtitleSync()
			if m.subtitleService != nil {
				m.subtitleService.ClearCache()
			}
			m.pruneResolvedToResolvers(exclude)
			if len(exclude) > 0 {
				m.refreshRanking()
			} else {
				m.rawSubtitles = nil
				m.rankedSources = nil
				m.previewSelectedIndex = 0
			}
			m.manualPlaybackSelected = false
			m.loading = false
			m.loadingText = ""
			m.setStatus(statusInfo, "")
			if !m.guardLoad() {
				return m, nil
			}
			m.loading = len(m.orderedPlaybackSources()) == 0
			m.loadingText = "Retrying streams..."
			m.beginRetryWait(m.appMode, exclude)
			opID := m.newOpID()
			m.resolveOpID = opID
			return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, *m.selectedEpisode, exclude, retry))
		}
		return m, nil

	case "a":
		return m.toggleAnimeAudio()

	case "m":
		if m.selectedEpisode != nil || m.resolved != nil {
			var ep provider.Episode
			if m.selectedEpisode != nil {
				ep = *m.selectedEpisode
			} else {
				ep = provider.Episode{
					Season:  m.resolved.SeasonNumber,
					Episode: m.resolved.EpisodeNumber,
					Title:   m.resolved.EpisodeTitle,
					TMDBID:  m.resolved.TMDBID,
				}
			}
			marked, err := m.toggleEpisodeComplete(ep)
			if err != nil {
				m.setStatus(statusWarn, fmt.Sprintf("Failed to update progress: %v", err))
				return m, nil
			}
			var toastCmd tea.Cmd
			if marked {
				toastCmd = m.setToast("marked complete", ToastSuccess)
			} else {
				toastCmd = m.setToast("unmarked", ToastInfo)
			}
			if m.resolved != nil {
				m.applyResumeFromHistory(m.resolved)
			}
			m.refreshEpisodeList()
			return m, toastCmd
		}
		return m, nil

	case "n":
		return m.playNextEpisode()

	case "[":
		if m.episodeIndex > 0 {
			return m.startEpisodeResolution(m.episodeIndex-1, false)
		}
		return m, nil

	case "]":
		// Next episode, mirroring [
		return m.playNextEpisode()

	case "p":
		return m.updatePreview(tea.KeyMsg{Type: tea.KeyEnter})

	case "A":
		m.autoplay = !m.autoplay
		m.saveSettings()
		if m.autoplay {
			return m, m.setToast("autoplay on", ToastSuccess)
		}
		return m, m.setToast("autoplay off", ToastInfo)

	case "ctrl+p":
		if len(m.availablePlayers) <= 1 {
			return m, nil
		}
		m.selectedPlayer = (m.selectedPlayer + 1) % len(m.availablePlayers)
		m.saveSettings()
		return m, nil

	case "d":
		m.pushView(viewDownloads)
		return m, nil

	case "D":
		sources := m.orderedPlaybackSources()
		if m.resolved == nil || len(sources) == 0 {
			if m.loading {
				m.setStatus(statusWarn, "Preparing playback, please wait...")
			} else {
				m.setStatus(statusWarn, "No playback source available to download")
			}
			return m, nil
		}
		if m.downloadOpID != 0 {
			m.setStatus(statusWarn, "A download is already in progress")
			return m, nil
		}
		opID := m.newOpID()
		m.downloadOpID = opID
		resolved := *m.resolved
		resolved.Playback = sources
		m.singleResolved = &resolved
		if m.downloadService != nil && m.selectedSeries != nil && m.selectedEpisode != nil {
			job := service.DownloadJob{Series: *m.selectedSeries, Episode: *m.selectedEpisode, Mode: m.appMode}
			if err := m.downloadService.SavePendingJob(job); err != nil {
				tuiLog.Warn("save pending download failed", "err", err)
			} else {
				m.pendingDownload = &job
			}
		}
		m.downloadPaused = false
		toastCmd := m.setToast("episode queued for download", ToastSuccess)
		return m, tea.Batch(toastCmd, m.downloadCmd(opID, resolved))
	}
	return m, nil
}

// refreshLiveCmd re-runs the live search for the current query. Live modes
// disable search caching, so this always reaches the upstream catalogs.
func (m *modelImpl) refreshLiveCmd() tea.Cmd {
	if !m.guardLoad() {
		return nil
	}
	m.loading = true
	m.loadingText = "Refreshing live guide..."
	m.setStatus(statusInfo, "")
	opID := m.newOpID()
	m.searchOpID = opID
	return tea.Batch(m.spinner.Tick, m.searchCmd(opID, m.searchQuery))
}

// jumpLiveSection moves the cursor to the first result of the requested
// section: 0 live now, 1 upcoming, 2 channels.
func (m *modelImpl) jumpLiveSection(section int) tea.Cmd {
	visible, _ := m.visibleSeriesResults()
	if len(visible) == 0 {
		return nil
	}
	guide := service.BuildLiveGuide(visible, m.selectedSeriesIndex(), time.Now())
	want := []service.LiveSectionKind{
		service.LiveSectionNow,
		service.LiveSectionUpcoming,
		service.LiveSectionChannels,
	}
	if section < 0 || section >= len(want) {
		return nil
	}
	for _, s := range guide.Sections {
		if s.Kind == want[section] && s.Start >= 0 && s.Start < len(visible) {
			m.seriesList.Select(s.Start)
			return m.triggerSearchPoster(m.selectedSeriesIndex())
		}
	}
	m.setStatus(statusInfo, "That section is empty")
	return nil
}
