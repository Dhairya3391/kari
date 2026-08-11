package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/settings"
)

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
				m.drainDownloadChan()
				if m.downloadOutputDir != "" {
					m.downloadService.CleanupPartial(m.downloadOutputDir, m.downloadTitle)
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
			m.refreshHistory()
			return tea.Batch(m.spinner.Tick, func() tea.Msg {
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
				return nil, true
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
				m.downloadProgress = 0
				m.downloadTotalSize = ""
				m.downloadSpeed = ""
				m.downloadDownloaded = ""
				m.downloadETA = ""
				m.drainDownloadChan()
				if m.downloadOutputDir != "" {
					m.downloadService.CleanupPartial(m.downloadOutputDir, m.downloadTitle)
				}
				m.loading = false
				m.loadingText = ""
				return nil, true
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
		return nil, true
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

func (m *modelImpl) applyAccent(hex string) {
	SetAccentColor(hex)
	m.downloadBar = newDownloadBar()
	m.saveSettings()
}

func (m *modelImpl) setAccent(idx int) {
	m.accentIndex = idx
	if idx < len(accentPresets) {
		m.applyAccent(accentPresets[idx].hex)
		return
	}
	if m.customAccentHex != "" {
		m.applyAccent(m.customAccentHex)
	}
}

// startCustomAccentInput opens the hex-entry field for the Custom accent
// slot, pre-filled with the last custom color if one was set.
func (m *modelImpl) startCustomAccentInput() (tea.Model, tea.Cmd) {
	m.hexInput.SetValue(strings.TrimPrefix(m.customAccentHex, "#"))
	m.hexInput.CursorEnd()
	m.hexInput.Focus()
	m.editingAccentHex = true
	return m, textinput.Blink
}

// saveSettings persists every setting the settings screen can change.
// Centralized so adding a new setting can't accidentally blank out an
// existing one by building a settings.Data literal that omits it.
// AccentColor is read from colorPrimary itself (not accentPresets[idx],
// which would panic once idx can point at the Custom slot) since that's
// always the actual color in effect, whether from a preset or custom hex.
func (m *modelImpl) saveSettings() {
	settings.Save(&settings.Data{
		QualityMode:      m.qualityMode,
		LanguageFilter:   m.languageFilter,
		SubtitleLanguage: m.subtitleLanguage,
		DisableImages:    !m.imagesEnabled,
		AccentColor:      string(colorPrimary),
	})
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
	m.updateQueryPlaceholder()

	// Clear current results and selection to avoid cross-mode provider errors
	m.allSeriesResults = nil
	m.seriesResults = nil
	m.seriesList.SetItems(nil)
	m.selectedSeries = nil
	m.searchQuery = ""
	m.usedQuery = ""
	m.searchIndex = 0
	m.clearSearchPoster()

	return nil
}

// updateQueryPlaceholder adjusts the search prompt hint for the active mode.
func (m *modelImpl) updateQueryPlaceholder() {
	if m.appMode == provider.ModeJellyfin {
		m.queryInput.Placeholder = "Search… (Enter on empty = browse library)"
		return
	}
	m.queryInput.Placeholder = "Search… (Esc for controls)"
}
