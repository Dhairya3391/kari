package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/service"
)

func (m *modelImpl) updateSearch(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.queryInput.Focused() {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
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
		// Stray Kitty graphics-protocol responses (e.g. `_Gi=1;OK\`)
		// arrive on stdin as ordinary runes; a focused box would eat
		// them as typed text.
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
				// Fresh box for a fresh search (also purges any stale
				// value or leaked terminal chatter).
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
				cmd := m.cycleMode(keyMsg.String() == "shift+tab")
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
		// While a / filter is being typed, rune keys extend the query;
		// movement still works; enter applies, esc clears (globally).
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
				m.seriesList.CursorUp()
				return m, m.triggerSearchPoster(m.selectedSeriesIndex())
			case "down", "j":
				m.seriesList.CursorDown()
				return m, m.triggerSearchPoster(m.selectedSeriesIndex())
			}
			if len(keyMsg.String()) == 1 {
				m.resultsFilter += keyMsg.String()
				m.refilterSeriesList()
				return m, nil
			}
			return m, nil
		}
		switch {
		case key.Matches(keyMsg, m.keys.Select) || keyMsg.String() == "enter":
			return m.selectSeries(m.selectedSeriesIndex())
		case keyMsg.String() == " " || keyMsg.String() == "space" || key.Matches(keyMsg, m.keys.Search):
			// space starts a new search from the results list: fresh
			// empty box (typing blind into the stale query is what
			// the old behavior forced). The blink restarts so the
			// cursor visibly pulses — without it a refocused input
			// looks dead and typing feels lost.
			m.resultsFilter = ""
			m.resultsFiltering = false
			m.queryInput.SetValue("")
			m.queryInput.Focus()
			m.setStatus(statusInfo, "")
			return m, textinput.Blink
		case keyMsg.String() == "/":
			// / filters the current results by title.
			m.resultsFiltering = true
			m.resultsFilter = ""
			m.refilterSeriesList()
		case keyMsg.String() == "d":
			m.pushView(viewDownloads)
			return m, nil
		case key.Matches(keyMsg, m.keys.Type):
			cmd := m.cycleMode(keyMsg.String() == "shift+tab")
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
			m.seriesList.CursorUp()
			return m, m.triggerSearchPoster(m.selectedSeriesIndex())
		case keyMsg.String() == "down" || keyMsg.String() == "j":
			m.seriesList.CursorDown()
			return m, m.triggerSearchPoster(m.selectedSeriesIndex())
		case keyMsg.String() == "g":
			m.seriesList.Select(0)
			return m, m.triggerSearchPoster(m.selectedSeriesIndex())
		case keyMsg.String() == "G":
			if items := m.seriesList.Items(); len(items) > 0 {
				m.seriesList.Select(len(items) - 1)
			}
			return m, m.triggerSearchPoster(m.selectedSeriesIndex())
		case keyMsg.String() == "pgup":
			for range 5 {
				m.seriesList.CursorUp()
			}
			return m, m.triggerSearchPoster(m.selectedSeriesIndex())
		case keyMsg.String() == "pgdown":
			for range 5 {
				m.seriesList.CursorDown()
			}
			return m, m.triggerSearchPoster(m.selectedSeriesIndex())
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
	}
	return m, tea.Batch(cmds...)
}

func (m *modelImpl) updatePreview(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "enter":
		// Playback starts once the first source is in — subtitles are
		// fetched in the background and attach when ready. Until the
		// first source arrives enter only reports what is still loading.
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
		// Retry stream resolution for failed providers. Providers that
		// already delivered are skipped — only the failed ones are
		// asked again — and their rows are kept, so a retry only ever
		// adds rows, never drops a working source. With no failures
		// this is a full refresh (fresh signed URLs).
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
			m.pruneResolvedToResolvers(exclude)
			m.rawSubtitles = nil
			m.rankedSources = nil
			m.previewSelectedIndex = 0
			m.manualPlaybackSelected = false
			m.loading = false
			m.loadingText = ""
			m.setStatus(statusInfo, "")
			if !m.guardLoad() {
				return m, nil
			}
			m.loading = true
			m.loadingText = "Retrying streams..."
			m.beginRetryWait(m.appMode, exclude)
			opID := m.newOpID()
			m.resolveOpID = opID
			return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, *m.selectedEpisode, exclude, retry))
		}
		return m, nil

	case "a":
		return m.toggleAnimeAudio()

	case "n":
		return m.playNextEpisode()

	case "[":
		// Previous episode — the same step key as season tabs on the
		// episodes screen, one screen earlier in the flow.
		if m.episodeIndex > 0 {
			return m.startEpisodeResolution(m.episodeIndex-1, false)
		}
		return m, nil

	case "]":
		// Next episode, mirroring [
		return m.playNextEpisode()

	case "p":
		// Play is the single most expected action on this screen; give
		// it the key every user's instinct reaches for first.
		return m.updatePreview(tea.KeyMsg{Type: tea.KeyEnter})

	case "A":
		m.autoplay = !m.autoplay
		m.saveSettings()
		if m.autoplay {
			m.setToast("autoplay on", ToastSuccess)
		} else {
			m.setToast("autoplay off", ToastInfo)
		}
		return m, nil

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
		if m.resolved == nil || len(m.resolved.Playback) == 0 {
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
		resolved.Playback = m.orderedPlaybackSources()
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
		m.setToast("episode queued for download", ToastSuccess)
		return m, m.downloadCmd(opID, resolved)
	}
	return m, nil
}
