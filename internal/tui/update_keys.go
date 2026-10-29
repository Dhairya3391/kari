package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/scrobble"
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

	// Anime status modal captures all input while open
	if m.animeStatusPickerOpen {
		switch msg.String() {
		case "esc":
			m.animeStatusPickerOpen = false
			m.clearStatus()
			return nil, true
		case "up", "k":
			if m.animeStatusPickerIndex > 0 {
				m.animeStatusPickerIndex--
			}
			return nil, true
		case "down", "j":
			if m.animeStatusPickerIndex < len(DefaultAnimeStatusChoices)-1 {
				m.animeStatusPickerIndex++
			}
			return nil, true
		case "enter":
			return m.executeAnimeStatusAction(m.animeStatusPickerIndex)
		}
		return nil, true
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
				toastCmd := m.setToast("downloads paused — press p on the downloads screen to resume", ToastInfo)
				m.loading = false
				m.loadingText = ""
				return toastCmd, true
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
			m.autoPlayAfterResolve = false
		}
		return nil, true
	case msg.String() == "?" || msg.String() == "f1" || key.Matches(msg, m.keys.Help):
		if m.queryInput.Focused() {
			return nil, false
		}
		m.showHelp = !m.showHelp
		m.helpScroll = 0
		return nil, true
	case key.Matches(msg, m.keys.Status):
		if cmd, ok := m.triggerAnimeStatusModal(); ok {
			return cmd, true
		}
	}
	return nil, false
}

func (m *modelImpl) triggerAnimeStatusModal() (tea.Cmd, bool) {
	var title string
	var aniID int
	var grpKey history.GroupKey

	switch m.activeView {
	case viewPreview:
		if m.appMode != provider.ModeAnime || m.selectedSeries == nil {
			return nil, false
		}
		title = m.selectedSeries.Title
		if m.resolved != nil && m.resolved.SeriesTitle != "" {
			title = m.resolved.SeriesTitle
		}
		aniID = anilistIDFor(m.appMode, m.selectedSeries)
		grpKey = m.findHistoryGroupKeyForAnime(title)
	case viewEpisodes:
		if m.appMode != provider.ModeAnime || m.selectedSeries == nil {
			return nil, false
		}
		title = m.selectedSeries.Title
		aniID = anilistIDFor(m.appMode, m.selectedSeries)
		grpKey = m.findHistoryGroupKeyForAnime(title)
	case viewHistory:
		item, ok := m.historyList.SelectedItem().(rowItem)
		if !ok {
			return nil, false
		}
		var grp *history.Group
		for i := range m.historyGroups {
			if m.historyGroups[i].Key.String() == item.key {
				grp = &m.historyGroups[i]
				break
			}
		}
		if grp == nil || (grp.Mode != "anime" && grp.Mode != string(provider.ModeAnime)) {
			return nil, false
		}
		title = grp.Title
		aniID = grp.LastPlayed.AniListID
		if aniID == 0 {
			aniID = grp.ContinueEntry.AniListID
		}
		grpKey = grp.Key
	default:
		return nil, false
	}

	m.animeStatusTargetTitle = title
	m.animeStatusTargetAniListID = aniID
	m.animeStatusTargetKey = grpKey
	m.animeStatusPickerOpen = true
	m.animeStatusLoading = true
	m.animeStatusPickerIndex = 0
	m.animeStatusEntry = nil
	m.animeStatusTotalEpisodes = 0
	opID := m.newOpID()
	m.animeStatusOpID = opID
	return m.fetchAnimeStatusCmd(opID, aniID, title), true
}

func (m *modelImpl) findHistoryGroupKeyForAnime(title string) history.GroupKey {
	if m.historyStore != nil {
		normTitle := strings.ToLower(strings.TrimSpace(title))
		for _, entry := range m.historyStore.All() {
			if strings.ToLower(strings.TrimSpace(entry.Title)) == normTitle || strings.ToLower(strings.TrimSpace(entry.Key.Title)) == normTitle {
				return history.GroupKey{
					Title:     entry.Title,
					Mode:      entry.Mode,
					MediaType: entry.MediaType,
				}
			}
		}
	}
	return history.GroupKey{Title: title, Mode: "anime", MediaType: "tv"}
}

func (m *modelImpl) executeAnimeStatusAction(index int) (tea.Cmd, bool) {
	if index < 0 || index >= len(DefaultAnimeStatusChoices) {
		return nil, true
	}
	choice := DefaultAnimeStatusChoices[index]
	switch choice.Action {
	case ActionAnimeWatching:
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			m.animeStatusPickerOpen = false
			return m.setStatusTimed(statusWarn, "Connect AniList in Settings (s) > Accounts"), true
		}
		m.animeStatusPickerOpen = false
		return m.updateAnimeStatusCmd(scrobble.StatusCurrent, nil, nil), true

	case ActionAnimeRewatching:
		if m.historyStore != nil {
			_ = m.historyStore.DeleteGroup(m.animeStatusTargetKey)
			m.historyGroups = history.BuildGroups(m.historyStore.All())
			m.refreshHistoryItems()
		}
		if m.anilistClient != nil && m.anilistClient.IsAuthenticated() {
			progress := 0
			repeat := 1
			if m.animeStatusEntry != nil && m.animeStatusEntry.Repeat > 0 {
				repeat = m.animeStatusEntry.Repeat + 1
			}
			m.animeStatusPickerOpen = false
			return tea.Batch(
				m.updateAnimeStatusCmd(scrobble.StatusRepeating, &progress, &repeat),
				m.setStatusTimed(statusSuccess, "Rewatch started: reset AniList & cleared local history"),
			), true
		}
		m.animeStatusPickerOpen = false
		return m.setStatusTimed(statusSuccess, "Rewatch started: cleared local history"), true

	case ActionAnimeCompleted:
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			m.animeStatusPickerOpen = false
			return m.setStatusTimed(statusWarn, "Connect AniList in Settings (s) > Accounts"), true
		}
		var progress *int
		if m.animeStatusTotalEpisodes > 0 {
			progress = &m.animeStatusTotalEpisodes
		}
		m.animeStatusPickerOpen = false
		return m.updateAnimeStatusCmd(scrobble.StatusCompleted, progress, nil), true

	case ActionAnimePaused:
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			m.animeStatusPickerOpen = false
			return m.setStatusTimed(statusWarn, "Connect AniList in Settings (s) > Accounts"), true
		}
		m.animeStatusPickerOpen = false
		return m.updateAnimeStatusCmd(scrobble.StatusPaused, nil, nil), true

	case ActionAnimeDropped:
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			m.animeStatusPickerOpen = false
			return m.setStatusTimed(statusWarn, "Connect AniList in Settings (s) > Accounts"), true
		}
		m.animeStatusPickerOpen = false
		return m.updateAnimeStatusCmd(scrobble.StatusDropped, nil, nil), true

	case ActionAnimePlanning:
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			m.animeStatusPickerOpen = false
			return m.setStatusTimed(statusWarn, "Connect AniList in Settings (s) > Accounts"), true
		}
		progress := 0
		m.animeStatusPickerOpen = false
		return m.updateAnimeStatusCmd(scrobble.StatusPlanning, &progress, nil), true

	case ActionAnimeRemoveFromAniList:
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			m.animeStatusPickerOpen = false
			return m.setStatusTimed(statusWarn, "Connect AniList in Settings (s) > Accounts"), true
		}
		if m.animeStatusEntry == nil {
			m.animeStatusPickerOpen = false
			return m.setStatusTimed(statusWarn, "Anime not found in your AniList library"), true
		}
		m.animeStatusPickerOpen = false
		return m.deleteAnimeStatusCmd(m.animeStatusEntry.ID), true

	case ActionAnimeResetLocalHistory:
		if m.historyStore != nil {
			_ = m.historyStore.DeleteGroup(m.animeStatusTargetKey)
			m.historyGroups = history.BuildGroups(m.historyStore.All())
			m.refreshHistoryItems()
		}
		m.animeStatusPickerOpen = false
		return m.setStatusTimed(statusSuccess, "Local watch history reset for "+m.animeStatusTargetTitle), true
	}
	m.animeStatusPickerOpen = false
	return nil, true
}

func (m *modelImpl) fetchAnimeStatusCmd(opID int, anilistID int, title string) tea.Cmd {
	return func() tea.Msg {
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			return animeStatusLoadedMsg{opID: opID}
		}
		ctx, cancel := context.WithTimeout(m.appCtx, 10*time.Second)
		defer cancel()
		entry, total, err := m.anilistClient.GetMediaListEntry(ctx, anilistID, title)
		return animeStatusLoadedMsg{
			opID:          opID,
			entry:         entry,
			totalEpisodes: total,
			err:           err,
		}
	}
}

func (m *modelImpl) updateAnimeStatusCmd(status scrobble.MediaListStatus, progress *int, repeat *int) tea.Cmd {
	mediaID := m.animeStatusTargetAniListID
	title := m.animeStatusTargetTitle
	return func() tea.Msg {
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			return animeStatusUpdatedMsg{err: fmt.Errorf("anilist not authenticated")}
		}
		ctx, cancel := context.WithTimeout(m.appCtx, 10*time.Second)
		defer cancel()
		err := m.anilistClient.UpdateMediaListStatus(ctx, mediaID, title, status, progress, repeat)
		if err != nil {
			return animeStatusUpdatedMsg{err: err}
		}
		return animeStatusUpdatedMsg{statusText: fmt.Sprintf("AniList status updated: %s", status)}
	}
}

func (m *modelImpl) deleteAnimeStatusCmd(entryID int) tea.Cmd {
	return func() tea.Msg {
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			return animeStatusUpdatedMsg{err: fmt.Errorf("anilist not authenticated")}
		}
		ctx, cancel := context.WithTimeout(m.appCtx, 10*time.Second)
		defer cancel()
		err := m.anilistClient.DeleteMediaListEntry(ctx, entryID)
		if err != nil {
			return animeStatusUpdatedMsg{err: err}
		}
		return animeStatusUpdatedMsg{statusText: "Removed from AniList"}
	}
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
	disabledProvList := make([]string, 0)
	for k, v := range m.disabledProviders {
		if v {
			disabledProvList = append(disabledProvList, k)
		}
	}
	slices.Sort(disabledProvList)

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
	startupSearchActiveVal := m.startupSearchActive

	settings.Save(&settings.Data{
		QualityMode:                 m.qualityMode,
		DownloadQuality:             m.downloadQuality,
		DisableDownloadVerification: m.disableDownloadVerification,
		LanguageFilter:              langFilter,
		SubtitleLanguage:            subLang,
		DisableAnimeSubtitles:       m.disableAnimeSubtitles,
		DefaultAnimeAudio:           audioMode,
		PreferredPlayer:             m.selectedPlayerName(),
		Autoplay:                    m.autoplay,
		DisableImages:               !m.imagesEnabled,
		AccentColor:                 accentSetting,
		SkipProvider:                skipProvider,
		AutoSkipIntro:               m.autoSkipIntro,
		AutoSkipEnding:              m.autoSkipEnding,
		SkipRecap:                   m.skipRecap,
		SkipPreview:                 m.skipPreview,
		StartupSync:                 m.startupSync,
		DiscordPresence:             m.discordPresence,
		DefaultMode:                 defaultMode,
		Modes:                       modes,
		ModesDisabled:               disabledList,
		DisabledProviders:           disabledProvList,
		LastMode:                    string(m.appMode),
		Transitions:                 &transitionsVal,
		StartupSearchActive:         &startupSearchActiveVal,
		AutoUpdate:                  m.autoUpdate,
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
