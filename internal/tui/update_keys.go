package tui

import (
	"strings"
	"time"

	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/settings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
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
			m.clearStatus()
			return nil, true
		}
		switch msg.String() {
		case "up", "k":
			m.helpScroll--
		case "down", "j":
			m.helpScroll++
		case "pgup":
			m.helpScroll -= max(1, m.height-8)
		case "pgdown":
			m.helpScroll += max(1, m.height-8)
		}
		return nil, true
	}
	if m.activeView == viewSettings || m.activeView == viewPreview {
		switch msg.String() {
		case "ctrl+u":
			m.scrollBody(-max(1, m.bodyHeight()-1))
			return nil, true
		case "ctrl+d":
			m.scrollBody(max(1, m.bodyHeight()-1))
			return nil, true
		}
	}

	// While a / filter is being typed, single-rune keys belong to the
	// filter query, not to global bindings — typing "q" must not quit,
	// "h" must not open history, "s" must not open settings.
	if m.resultsFiltering || m.episodeFiltering {
		if s := msg.String(); len(s) == 1 && s != "?" {
			return nil, false
		}
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
		if m.activeView == viewChapters && m.chapterList.SettingFilter() {
			return nil, false
		}

		// Nothing running: quit immediately — no nagging.
		if m.batchCancel == nil && m.cancelDownload == nil {
			return tea.Quit, true
		}

		// Downloads are running: first q announces them; second q pauses
		// (partials stay on disk for the next session's resume) and quits.
		if m.confirmQuit {
			if m.batchCancel != nil {
				m.batchCancel()
				m.batchCancel = nil
			}
			if m.cancelDownload != nil {
				m.cancelDownload()
				m.cancelDownload = nil
			}
			m.drainDownloadChan()
			return tea.Quit, true
		}
		m.confirmQuit = true
		m.setStatus(statusWarn, "Downloads running — q again to pause & quit (resume next launch)")
		return tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
			return resetConfirmQuitMsg{}
		}), true
	case key.Matches(msg, m.keys.History):
		// Reachable from any list screen, not just the search home —
		// hiding navigation behind an empty state was unintuitive.
		switch m.activeView {
		case viewSearch:
			if m.queryInput.Focused() {
				return nil, false
			}
			m.loading = true
			m.loadingText = "Loading history..."
			_, historyCmd := m.refreshHistory()
			return tea.Batch(m.spinner.Tick, historyCmd, func() tea.Msg {
				return historyLoadedMsg{}
			}), true
		case viewEpisodes, viewPreview, viewDownloads, viewSettings, viewChapters:
			m.loading = true
			m.loadingText = "Loading history..."
			_, historyCmd := m.refreshHistory()
			return tea.Batch(m.spinner.Tick, historyCmd, func() tea.Msg {
				return historyLoadedMsg{}
			}), true
		}
	case key.Matches(msg, m.keys.Settings):
		switch m.activeView {
		case viewSearch:
			if m.queryInput.Focused() {
				return nil, false
			}
			m.pushView(viewSettings)
			return nil, true
		case viewEpisodes, viewPreview, viewDownloads, viewChapters:
			m.pushView(viewSettings)
			return nil, true
		}
	case key.Matches(msg, m.keys.Stop):
		if m.queryInput.Focused() {
			return nil, false
		}
		if m.activeView == viewDownloads {
			// The downloads screen owns x: with the cursor on a specific
			// row, x targets that item (double-press confirmed), not the
			// global pause. The screen handler also offers X for all.
			return nil, false
		}
		if m.activeView == viewSearch && m.seriesList.SettingFilter() {
			return nil, false
		}
		if m.activeView == viewEpisodes && m.episodeList.SettingFilter() {
			return nil, false
		}
		if m.activeView == viewChapters && m.chapterList.SettingFilter() {
			return nil, false
		}

		if m.batchCancel != nil || m.cancelDownload != nil {
			if m.confirmStop {
				// Double-x pauses (not cancels): partial files stay on disk
				// and the downloads screen's p resumes from the checkpoint.
				if m.batchCancel != nil {
					m.batchCancel()
					m.batchCancel = nil
				}
				if m.cancelDownload != nil {
					m.cancelDownload()
					m.cancelDownload = nil
				}
				m.downloadOpID = 0
				m.drainDownloadChan()
				m.downloadPaused = true
				m.setToast("downloads paused — press p on the downloads screen to resume", ToastInfo)
				m.loading = false
				m.loadingText = ""
				return nil, true
			}
			m.confirmStop = true
			m.setStatus(statusWarn, "Press x again to pause downloads")
			return tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
				return resetConfirmStopMsg{}
			}), true
		}
		return nil, false
	case key.Matches(msg, m.keys.Home):
		if m.activeView == viewPreview {
			m.clearPreviewPoster()
		}
		m.activeView = viewSearch
		m.backStack = nil
		m.loading = false
		m.clearStatus()
		return nil, true
	case msg.String() == "ctrl+p":
		if m.queryInput.Focused() {
			return nil, false
		}
		if len(m.availablePlayers) <= 1 {
			return nil, true
		}
		m.selectedPlayer = (m.selectedPlayer + 1) % len(m.availablePlayers)
		m.saveSettings()
		return nil, true
	case key.Matches(msg, m.keys.Back):
		m.clearStatus()
		if m.clearActiveFilter() {
			return nil, true
		}
		if m.exitInputMode() {
			return nil, true
		}
		m.goBackOne()
		if m.loading || m.resolveOpID != 0 || m.playOpID != 0 {
			tuiLog.Debug("ESC cancelled in-flight operations", "resolveOpID", m.resolveOpID, "playOpID", m.playOpID)
			m.loading = false
			m.loadingText = ""
			m.resolveOpID = 0
			m.playOpID = 0
			m.subtitleOpID = 0
		}
		return nil, true
	case msg.String() == "?":
		if m.queryInput.Focused() {
			return nil, false
		}
		m.showHelp = !m.showHelp
		m.helpScroll = 0
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

// saveSettings persists every setting the settings screen can change.
// Centralized so adding a new setting can't accidentally blank out an
// existing one by building a settings.Data literal that omits it.
// AccentColor is read from colorPrimary itself (not accentPresets[idx],
func (m *modelImpl) saveSettings() {
	disabledList := make([]string, 0)
	for k, v := range m.disabledModes {
		if v {
			disabledList = append(disabledList, k)
		}
	}

	accentSetting := "auto"
	if m.accentIndex > 0 && m.accentIndex < len(accentPresets) {
		accentSetting = accentPresets[m.accentIndex].name
	} else if m.customAccentHex != "" {
		accentSetting = m.customAccentHex
	}

	modes := m.configuredModes
	if len(modes) == 0 {
		modes = settings.DefaultModes
	}

	langFilter := m.languageFilter
	if langFilter == nil {
		langFilter = make(map[string]bool)
	}

	defaultMode := m.defaultMode
	if defaultMode == "" {
		defaultMode = "last"
	}

	audioMode := m.audioMode
	if audioMode == "" {
		audioMode = "sub"
	}

	skipProvider := m.skipProvider
	if skipProvider == "" {
		skipProvider = "hybrid"
	}

	subLang := m.subtitleLanguage
	if subLang == "" {
		subLang = "en"
	}

	transitionsVal := m.transitions

	settings.Save(&settings.Data{
		QualityMode:           m.qualityMode,
		DownloadQuality:       m.downloadQuality,
		LanguageFilter:        langFilter,
		SubtitleLanguage:      subLang,
		DisableAnimeSubtitles: m.disableAnimeSubtitles,
		DefaultAnimeAudio:     audioMode,
		PreferredPlayer:       m.selectedPlayerName(),
		Autoplay:              m.autoplay,
		DisableImages:         !m.imagesEnabled,
		AccentColor:           accentSetting,
		SkipProvider:          skipProvider,
		AutoSkipIntro:         m.autoSkipIntro,
		AutoSkipEnding:        m.autoSkipEnding,
		SkipRecap:             m.skipRecap,
		SkipPreview:           m.skipPreview,
		StartupSync:           m.startupSync,
		DefaultMode:           defaultMode,
		Modes:                 modes,
		ModesDisabled:         disabledList,
		LastMode:              string(m.appMode),
		Transitions:           &transitionsVal,
	})
	if m.players != nil {
		m.players.SetSkipSettings(player.SkipSettings{
			Provider:       m.skipProvider,
			AutoSkipIntro:  m.autoSkipIntro,
			AutoSkipEnding: m.autoSkipEnding,
			SkipRecap:      m.skipRecap,
			SkipPreview:    m.skipPreview,
		})
	}
}
func (m *modelImpl) cycleMode(reverse bool) tea.Cmd {
	if len(m.modes) <= 1 {
		return nil
	}
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
	return m.switchToMode(m.modes[idx])
}
func (m *modelImpl) switchToMode(target provider.ContentType) tea.Cmd {
	if target == m.appMode {
		return nil
	}

	m.clearStatus()
	oldMode := m.appMode
	m.appMode = target
	m.updateQueryPlaceholder()
	m.saveSettings()

	// Start the accent crossfade when transitions are enabled.
	var fadeCmd tea.Cmd
	if m.transitions {
		fadeCmd = m.startThemeCrossfade(model.FromKey(string(oldMode)), model.FromKey(string(target)))
	}

	// Clean search bar, results, and query on mode change
	m.queryInput.SetValue("")
	m.queryInput.Blur()
	m.searchQuery = ""
	m.usedQuery = ""
	m.searchIndex = 0
	m.allSeriesResults = nil
	m.seriesResults = nil
	if m.seriesList.Items() != nil {
		m.seriesList.SetItems(nil)
	}
	m.selectedSeries = nil
	m.resultsFilter = ""
	m.resultsFiltering = false
	m.clearSearchPoster()
	m.loading = false
	m.loadingText = ""

	return fadeCmd
}

// updateQueryPlaceholder adjusts the search prompt hint for the active mode,
// preferring the placeholder declared by the mode's providers.
func (m *modelImpl) updateQueryPlaceholder() {
	if ph := m.modeFeatures().SearchPlaceholder; ph != "" {
		m.queryInput.Placeholder = strings.ToLower(ph)
		return
	}
	m.queryInput.Placeholder = "search " + strings.ToLower(string(m.appMode)) + "…"
}
