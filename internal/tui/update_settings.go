package tui

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/settings"
)

func (m *modelImpl) updateSettings(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case startupAddonLoadedMsg:
		if msg.err != nil {
			host := msg.manifestURL
			if parsed, parseErr := url.Parse(msg.manifestURL); parseErr == nil && parsed.Host != "" {
				host = parsed.Host
			}
			tuiLog.Warn("configured addon unavailable", "host", host, "err", msg.err)
			// Startup warnings are set before addon loads resolve; don't replace
			// a more important one (e.g. unavailable watch history) with this.
			if m.statusText != "" {
				return m, nil
			}
			return m, m.setStatusTimed(statusError, "Configured addon unavailable: "+host)
		}
		if msg.addon == nil || m.registry == nil {
			return m, nil
		}
		m.registry.Register(msg.addon)
		m.updateEffectiveModes()
		m.syncDisabledProvidersToService()
		return m, nil
	case addonTestDoneMsg:
		if !m.addonTesting || msg.opID != m.addonTestOpID || msg.manifestURL != m.addonPendingURL {
			return m, nil
		}
		m.addonTesting = false
		m.addonTestCancel = nil
		m.addonPendingURL = ""
		// Keep the rebind target on failure: the input stays open, so a retry
		// with Enter must still replace the addon rather than add a duplicate.
		if msg.err != nil {
			prefix := "Addon add failed: "
			if msg.replacing != "" {
				prefix = "Addon update failed: "
			}
			return m, m.setStatusTimed(statusError, prefix+msg.err.Error())
		}
		if msg.addon == nil || m.registry == nil {
			return m, m.setStatusTimed(statusError, "Addon add returned no provider")
		}
		if msg.replacing != "" {
			oldName, removed := m.registry.UnregisterConfigured(msg.replacing)
			if removed {
				m.rebindDisabledProviderKeys(oldName, msg.addon.Name())
			}
		}
		m.registry.Register(msg.addon)
		m.modes = m.registry.AllModes()
		m.updateEffectiveModes()
		m.syncDisabledProvidersToService()
		status := "Addon added, saved, and enabled"
		if msg.replacing != "" {
			oldIndex := slices.Index(m.stremioAddonManifests, msg.replacing)
			if oldIndex >= 0 {
				m.stremioAddonManifests = slices.Delete(m.stremioAddonManifests, oldIndex, oldIndex+1)
			}
			if oldIndex >= 0 {
				m.stremioAddonManifests = slices.Insert(m.stremioAddonManifests, min(oldIndex, len(m.stremioAddonManifests)), msg.manifestURL)
			} else {
				m.stremioAddonManifests = append(m.stremioAddonManifests, msg.manifestURL)
			}
			m.settingsIndex = slices.Index(m.stremioAddonManifests, msg.manifestURL) + 1
			status = "Addon updated, saved, and enabled"
		} else {
			m.stremioAddonManifests = append(m.stremioAddonManifests, msg.manifestURL)
			m.settingsIndex = len(m.stremioAddonManifests)
		}
		m.addonInput.SetValue("")
		m.addonInputOpen = false
		m.addonInput.Blur()
		m.addonRebindingURL = ""
		m.saveSettings()
		return m, m.setStatusTimed(statusSuccess, status)
	case tea.KeyMsg:
		if m.addonInputOpen {
			if m.addonTesting {
				return m, nil
			}
			switch msg.String() {
			case "enter":
				manifest := strings.TrimSpace(m.addonInput.Value())
				u, err := url.Parse(manifest)
				if err != nil || u.Host == "" {
					return m, m.setStatusTimed(statusError, "Enter a valid http or https manifest URL")
				}
				scheme := strings.ToLower(u.Scheme)
				if scheme != "http" && scheme != "https" {
					return m, m.setStatusTimed(statusError, "Enter a valid http or https manifest URL")
				}
				for _, existing := range m.stremioAddonManifests {
					if existing == m.addonRebindingURL {
						continue
					}
					if canonicalAddonURL(existing) == canonicalAddonURL(manifest) {
						m.addonInputOpen = false
						m.addonInput.Blur()
						m.addonRebindingURL = ""
						return m, m.setStatusTimed(statusInfo, "Addon already configured")
					}
				}
				if m.addonManifestTester == nil {
					return m, m.setStatusTimed(statusError, "Addon checks are unavailable")
				}
				return m, m.startAddonCheck(manifest, m.addonRebindingURL)
			case "esc":
				if m.addonTestCancel != nil {
					m.addonTestCancel()
				}
				m.addonTesting = false
				m.addonPendingURL = ""
				m.addonRebindingURL = ""
				m.addonTestCancel = nil
				m.addonInputOpen = false
				m.addonInput.Blur()
				return m, nil
			default:
				var cmd tea.Cmd
				m.addonInput, cmd = m.addonInput.Update(msg)
				return m, cmd
			}
		}
		if m.providerPriorityOpen {
			m.updateProviderPriorityPicker(msg.String())
			return m, nil
		}
		// 1. Audio / Subtitle Language Picker Modals
		if m.audioPickerOpen {
			allLangs := m.availableLanguages()
			if index, ok := movePickerCursor(msg.String(), m.audioPickerIndex, len(allLangs), m.pageStep()); ok {
				m.audioPickerIndex = index
				return m, nil
			}
			switch msg.String() {
			case " ", "space":
				if m.audioPickerIndex < len(allLangs) {
					m.toggleAudioLanguage(allLangs[m.audioPickerIndex])
					m.saveSettings()
				}
			case "enter", "esc":
				m.audioPickerOpen = false
				m.saveSettings()
				m.clearStatus()
			}
			return m, nil
		}
		if m.subtitlePickerOpen {
			options := subtitlePickerOptions()
			if index, ok := movePickerCursor(msg.String(), m.subtitlePickerIndex, len(options), m.pageStep()); ok {
				m.subtitlePickerIndex = index
				return m, nil
			}
			switch msg.String() {
			case "enter", " ", "space":
				if m.subtitlePickerIndex < len(options) {
					m.setSubtitlePickerChoice(m.subtitlePickerMode, options[m.subtitlePickerIndex])
				}
				m.subtitlePickerOpen = false
				m.saveSettings()
				m.clearStatus()
				m.refreshRanking()
				return m, m.triggerSubtitleSync()
			case "esc":
				m.subtitlePickerOpen = false
				m.clearStatus()
			}
			return m, nil
		}

		// 2. Custom Hex / AniList input
		if m.editingAccentHex {
			if msg.String() == "enter" {
				if normalized, ok := normalizeHexColor(m.hexInput.Value()); ok {
					m.customAccentHex = normalized
					m.accentIndex = len(accentPresets)
					m.editingAccentHex = false
					m.hexInput.Blur()
					m.applyAccent(normalized)
					return m, nil
				}
				return m, m.setStatusTimed(statusError, "Invalid hex color — use 6 hex digits, e.g. be95ff")
			}
			var cmd tea.Cmd
			m.hexInput, cmd = m.hexInput.Update(msg)
			return m, cmd
		}

		if m.anilistAuthURL != "" {
			if msg.String() == "enter" {
				code := strings.TrimSpace(m.authInput.Value())
				if code == "" {
					return m, m.setStatusTimed(statusWarn, "Please paste the token or code")
				}
				m.anilistAuthURL = ""
				m.authInput.Blur()
				m.loading = true
				m.loadingText = "Exchanging AniList token..."
				return m, func() tea.Msg {
					err := m.anilistClient.ExchangeCode(m.appCtx, code)
					return authDoneMsg{service: "AniList", err: err}
				}
			}
			if msg.String() == "esc" {
				m.anilistAuthURL = ""
				m.authInput.Blur()
				m.clearStatus()
				return m, nil
			}
			var cmd tea.Cmd
			m.authInput, cmd = m.authInput.Update(msg)
			return m, cmd
		}

		if m.traktAuthActive {
			if msg.String() == "esc" {
				m.traktAuthActive = false
				m.traktUserCode = ""
				m.traktVerifyURL = ""
				m.traktDeviceCode = ""
				if m.traktCancel != nil {
					m.traktCancel()
					m.traktCancel = nil
				}
				m.clearStatus()
				return m, nil
			}
		}
		// 3. Category & Row Navigation
		if msg.Type == tea.KeyTab {
			m.settingsCategory = (m.settingsCategory + 1) % SettingsCategory(len(SettingsCategoryNames))
			m.settingsIndex = 0
			return m, nil
		}
		if msg.Type == tea.KeyShiftTab {
			m.settingsCategory = (m.settingsCategory - 1 + SettingsCategory(len(SettingsCategoryNames))) % SettingsCategory(len(SettingsCategoryNames))
			m.settingsIndex = 0
			return m, nil
		}

		switch msg.String() {
		// Direct category jumps via number keys.
		case "1":
			m.settingsCategory = CategoryPlayback
			m.settingsIndex = 0
			return m, nil
		case "2":
			m.settingsCategory = CategoryLanguages
			m.settingsIndex = 0
			return m, nil
		case "3":
			m.settingsCategory = CategoryAnime
			m.settingsIndex = 0
			return m, nil
		case "4":
			m.settingsCategory = CategoryModes
			m.settingsIndex = 0
			return m, nil
		case "5":
			m.settingsCategory = CategoryProviders
			m.settingsIndex = 0
			return m, nil
		case "6":
			m.settingsCategory = CategoryAccounts
			m.settingsIndex = 0
			return m, nil
		case "7":
			m.settingsCategory = CategoryInterface
			m.settingsIndex = 0
			return m, nil
		case "8":
			m.settingsCategory = CategoryAddons
			m.settingsIndex = 0
			return m, nil
		case "9":
			m.settingsCategory = CategoryHistory
			m.settingsIndex = 0
			return m, nil
		case "e":
			if m.settingsCategory == CategoryAddons && m.settingsIndex > 0 &&
				m.settingsIndex <= len(m.stremioAddonManifests) && !m.addonTesting {
				old := m.stremioAddonManifests[m.settingsIndex-1]
				m.addonRebindingURL = old
				m.addonInput.SetValue(old)
				m.addonInputOpen = true
				m.addonInput.Focus()
				return m, nil
			}
		case "d":
			if m.settingsCategory == CategoryAddons && m.settingsIndex > 0 &&
				m.settingsIndex <= len(m.stremioAddonManifests) && !m.addonTesting {
				idx := m.settingsIndex - 1
				manifestURL := m.stremioAddonManifests[idx]
				m.stremioAddonManifests = append(m.stremioAddonManifests[:idx], m.stremioAddonManifests[idx+1:]...)
				if m.settingsIndex > len(m.stremioAddonManifests) {
					m.settingsIndex = len(m.stremioAddonManifests)
				}
				if m.registry != nil {
					providerName, removed := m.registry.UnregisterConfigured(manifestURL)
					if removed {
						providerName = strings.ToLower(strings.TrimSpace(providerName))
						for key := range m.disabledProviders {
							if key == providerName || strings.HasSuffix(key, ":"+providerName) {
								delete(m.disabledProviders, key)
							}
						}
						m.modes = m.registry.AllModes()
						m.updateEffectiveModes()
						m.syncDisabledProvidersToService()
					}
				}
				m.saveSettings()
				if len(m.modes) > 0 && !m.isModeAvailable(string(m.appMode)) {
					modeCmd := m.switchToMode(m.modes[0])
					statusCmd := m.setStatusTimed(statusInfo, "Addon removed")
					return m, tea.Batch(modeCmd, statusCmd)
				}
				return m, m.setStatusTimed(statusInfo, "Addon removed")
			}
		case "K", "shift+up":
			if m.settingsCategory == CategoryModes && m.settingsIndex > 0 {
				return m.reorderMode(-1)
			}
		case "J", "shift+down":
			if m.settingsCategory == CategoryModes && m.settingsIndex > 0 {
				return m.reorderMode(1)
			}
		case "up", "k":
			if m.settingsIndex > 0 {
				m.settingsIndex--
			}
			return m, nil

		case "down", "j":
			maxRows := m.maxRowsForCategory(m.settingsCategory)
			if m.settingsIndex < maxRows-1 {
				m.settingsIndex++
			}
			return m, nil
		case "g", "home":
			m.settingsIndex = 0
			return m, nil
		case "G", "end":
			maxRows := m.maxRowsForCategory(m.settingsCategory)
			if maxRows > 0 {
				m.settingsIndex = maxRows - 1
			}
			return m, nil
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			step := m.pageStep()
			switch msg.String() {
			case "ctrl+u", "ctrl+d":
				step = max(1, step/2)
			}
			delta := step
			if msg.String() == "pgup" || msg.String() == "ctrl+u" {
				delta = -delta
			}
			m.settingsIndex = clampIndex(m.settingsIndex+delta, m.maxRowsForCategory(m.settingsCategory))
			return m, nil

		case "left", "h":
			if m.settingsCategory == CategoryPlayback && m.settingsIndex == 3 {
				m.openProviderPriorityEditor()
				return m, nil
			}
			if m.settingsCategory == CategoryProviders {
				return m.toggleProviderAtIndex(m.settingsIndex)
			}
			m.cycleCurrentSetting(-1)
			m.saveSettings()
			m.refreshRanking()
			return m, m.triggerSubtitleSync()

		case "right", "l":
			if m.settingsCategory == CategoryPlayback && m.settingsIndex == 3 {
				m.openProviderPriorityEditor()
				return m, nil
			}
			if m.settingsCategory == CategoryProviders {
				return m.toggleProviderAtIndex(m.settingsIndex)
			}
			m.cycleCurrentSetting(1)
			m.saveSettings()
			m.refreshRanking()
			return m, m.triggerSubtitleSync()
		case "enter", "space", " ":
			if m.settingsCategory == CategoryPlayback && m.settingsIndex == 3 {
				m.openProviderPriorityEditor()
				return m, nil
			}
			if m.settingsCategory == CategoryAddons {
				if m.settingsIndex == 0 {
					m.addonRebindingURL = ""
					m.addonInputOpen = true
					m.addonInput.Focus()
					m.addonInput.SetValue("")
				} else if m.settingsIndex <= len(m.stremioAddonManifests) && !m.addonTesting {
					old := m.stremioAddonManifests[m.settingsIndex-1]
					return m, m.startAddonCheck(old, old)
				}
				return m, nil
			}
			if m.settingsCategory == CategoryModes {
				if m.settingsIndex == 0 {
					m.cycleDefaultMode(1)
					return m, nil
				}
				return m.toggleModeAtIndex(m.settingsIndex - 1)
			}
			if m.settingsCategory == CategoryProviders {
				return m.toggleProviderAtIndex(m.settingsIndex)
			}
			if m.settingsCategory == CategoryLanguages && m.settingsIndex == 0 {
				// Open Audio language picker
				m.audioPickerOpen = true
				m.audioPickerIndex = 0
				return m, nil
			}
			if m.settingsCategory == CategoryLanguages && m.settingsIndex == 1 {
				m.openSubtitlePicker(m.appMode)
				return m, nil
			}
			if m.settingsCategory == CategoryAnime && m.settingsIndex == 2 {
				m.openSubtitlePicker(provider.ModeAnime)
				return m, nil
			}
			if m.settingsCategory == CategoryAccounts {
				if m.settingsIndex == 0 {
					return m.startAniListAuth()
				}
				if m.settingsIndex == 1 {
					return m.startTraktAuth()
				}
				if m.settingsIndex == 2 {
					m.startupSync = !m.startupSync
					m.saveSettings()
					return m, nil
				}
				return m.toggleDiscordPresence()
			}
			m.cycleCurrentSetting(1)
			m.saveSettings()
			m.refreshRanking()
			return m, m.triggerSubtitleSync()

		case "c":
			if m.settingsCategory == CategoryAccounts {
				if m.settingsIndex == 0 {
					return m.startAniListAuth()
				}
				if m.settingsIndex == 1 {
					return m.startTraktAuth()
				}
			}
		case "r":
			if m.settingsCategory == CategoryAddons && m.settingsIndex > 0 &&
				m.settingsIndex <= len(m.stremioAddonManifests) && !m.addonTesting {
				old := m.stremioAddonManifests[m.settingsIndex-1]
				return m, m.startAddonCheck(old, old)
			}
			if m.settingsCategory == CategoryAccounts {
				if m.settingsIndex == 0 && m.anilistClient != nil {
					_ = m.anilistClient.Revoke()
					return m, m.setStatusTimed(statusSuccess, "AniList disconnected")
				}
				if m.settingsIndex == 1 && m.traktClient != nil {
					_ = m.traktClient.Revoke()
					return m, m.setStatusTimed(statusSuccess, "Trakt disconnected")
				}
			}

		case "i":
			if m.settingsCategory == CategoryAccounts {
				if m.settingsIndex == 0 {
					return m.startHistoryImport("anilist")
				}
				if m.settingsIndex == 1 {
					return m.startHistoryImport("trakt")
				}
			}

		case "esc":
			m.goBackOne()
			return m, nil
		}
	}
	return m, nil
}

// movePickerCursor applies a list-navigation key to a modal picker cursor.
// It returns handled=false for keys that are not navigation.
func movePickerCursor(key string, index, length, pageStep int) (int, bool) {
	switch key {
	case "up", "k":
		return max(0, index-1), true
	case "down", "j":
		return min(max(0, length-1), index+1), true
	case "home", "g":
		return 0, true
	case "end", "G":
		return max(0, length-1), true
	case "pgup", "ctrl+u":
		return max(0, index-pageStep), true
	case "pgdown", "ctrl+d":
		return min(max(0, length-1), index+pageStep), true
	}
	return index, false
}

func (m *modelImpl) maxRowsForCategory(cat SettingsCategory) int {
	switch cat {
	case CategoryPlayback:
		return 6
	case CategoryLanguages:
		return 2
	case CategoryAnime:
		return 8
	case CategoryModes:
		return len(m.configuredModes) + 1
	case CategoryHistory:
		return len(m.configuredModes) + 1
	case CategoryProviders:
		return len(m.providerRows())
	case CategoryAddons:
		return len(m.stremioAddonManifests) + 1
	case CategoryAccounts:
		return 4
	case CategoryInterface:
		return 5
	default:
		return 1
	}
}

func (m *modelImpl) cycleCurrentSetting(delta int) {
	switch m.settingsCategory {
	case CategoryPlayback:
		switch m.settingsIndex {
		case 0: // Preferred player
			if len(m.availablePlayers) > 1 {
				m.selectedPlayer = (m.selectedPlayer + delta + len(m.availablePlayers)) % len(m.availablePlayers)
			}
		case 1: // Stream quality (active mode)
			m.cycleQuality(m.appMode, delta)
		case 2: // Download quality
			m.downloadQuality = (m.downloadQuality + delta + 5) % 5
		case 4: // Autoplay next episode
			m.autoplay = !m.autoplay
		case 5: // Verify downloads
			m.disableDownloadVerification = !m.disableDownloadVerification
			if m.downloadService != nil {
				m.downloadService.SetVerifyDownloads(!m.disableDownloadVerification)
			}
		}

	case CategoryLanguages:
		switch m.settingsIndex {
		case 0: // Audio
			m.audioPickerOpen = true
			m.audioPickerIndex = 0
		case 1: // Subtitle language (active mode)
			m.cycleSubtitleLanguage(m.appMode, delta)
		}

	case CategoryAnime:
		switch m.settingsIndex {
		case 0: // Anime audio track
			m.cycleAudioTrack(delta)
		case 1: // Anime subtitles
			m.toggleAnimeSubtitles()
		case 2: // Subtitle language
			m.cycleSubtitleLanguage(provider.ModeAnime, delta)
		case 3: // Skip source
			m.skipProvider = cycleSkipProvider(m.skipProvider, delta < 0)
		case 4: // Intro
			m.autoSkipIntro = !m.autoSkipIntro
		case 5: // Outro
			m.autoSkipEnding = !m.autoSkipEnding
		case 6: // Recap
			m.skipRecap = !m.skipRecap
		case 7: // Preview
			m.skipPreview = !m.skipPreview
		}
	case CategoryModes:
		if m.settingsIndex == 0 {
			m.cycleDefaultMode(delta)
		} else if m.settingsIndex > 0 {
			m.reorderMode(delta)
		}
	case CategoryHistory:
		if m.settingsIndex == 0 {
			m.toggleAllHistoryModes()
		} else {
			modeIndex := m.settingsIndex - 1
			if modeIndex < len(m.configuredModes) {
				if m.disabledHistoryModes == nil {
					m.disabledHistoryModes = make(map[string]bool)
				}
				mode := m.configuredModes[modeIndex]
				if m.disableHistory {
					m.disabledHistoryModes = make(map[string]bool, len(m.configuredModes))
					for _, configuredMode := range m.configuredModes {
						m.disabledHistoryModes[configuredMode] = true
					}
					m.disableHistory = false
				}
				if m.disabledHistoryModes[mode] {
					delete(m.disabledHistoryModes, mode)
				} else {
					m.disabledHistoryModes[mode] = true
				}
			}
		}
	case CategoryAccounts:
		switch m.settingsIndex {
		case 2:
			m.startupSync = !m.startupSync
		case 3:
			m.discordPresence = !m.discordPresence
			m.applyPresenceToggle()
		}

	case CategoryInterface:
		switch m.settingsIndex {
		case 0: // Poster artwork
			m.imagesEnabled = !m.imagesEnabled
		case 1: // Accent
			m.accentIndex = (m.accentIndex + delta + len(accentPresets)) % len(accentPresets)
			m.setAccent(m.accentIndex)
		case 2: // Transitions
			m.transitions = !m.transitions
		case 3: // Startup search active
			m.startupSearchActive = !m.startupSearchActive
		case 4: // Auto update
			m.autoUpdate = !m.autoUpdate
		}
	}
}

func (m *modelImpl) providerPriorityModes() []provider.ContentType {
	if m.registry == nil {
		return nil
	}
	return m.registry.AllModes()
}

func (m *modelImpl) providerPriorityRows() []ProviderPriorityRow {
	modes := m.providerPriorityModes()
	rows := make([]ProviderPriorityRow, 0, len(modes))
	for _, mode := range modes {
		modeName := providerPriorityModeLabel(mode)
		providerName := "Auto"
		for _, option := range m.preferredProviderOptions(mode) {
			if option.ID == m.preferredProviderForMode(mode) {
				providerName = option.Name
				break
			}
		}
		rows = append(rows, ProviderPriorityRow{ModeName: modeName, ProviderName: providerName})
	}
	return rows
}

func providerPriorityModeLabel(mode provider.ContentType) string {
	switch mode {
	case provider.ModeAnime:
		return "Anime"
	case provider.ModeCartoon:
		return "Cartoon"
	case provider.ModeStremio:
		return "Stremio"
	case provider.ModeMovies:
		return "Movies"
	case provider.ModeTV:
		return "TV Shows"
	case provider.ModeJellyfin:
		return "Jellyfin"
	case provider.ModeLive:
		return "Live"
	case provider.ModeManga:
		return "Manga"
	default:
		return string(mode)
	}
}

func (m *modelImpl) openProviderPriorityEditor() {
	m.providerPriorityOpen = true
	m.providerPriorityIndex = 0
	for i, mode := range m.providerPriorityModes() {
		if mode == m.appMode {
			m.providerPriorityIndex = i
			break
		}
	}
}

func (m *modelImpl) updateProviderPriorityPicker(key string) {
	modes := m.providerPriorityModes()
	if len(modes) == 0 {
		m.providerPriorityOpen = false
		return
	}
	last := len(modes) - 1
	switch key {
	case "up", "k":
		m.providerPriorityIndex = max(0, m.providerPriorityIndex-1)
	case "down", "j":
		m.providerPriorityIndex = min(last, m.providerPriorityIndex+1)
	case "home", "g":
		m.providerPriorityIndex = 0
	case "end", "G":
		m.providerPriorityIndex = last
	case "left", "h":
		m.cycleProviderPriority(modes[m.providerPriorityIndex], -1)
		m.saveSettings()
	case "right", "l":
		m.cycleProviderPriority(modes[m.providerPriorityIndex], 1)
		m.saveSettings()
	case "enter", "esc":
		m.providerPriorityOpen = false
	}
}

func (m *modelImpl) cycleProviderPriority(mode provider.ContentType, delta int) {
	m.cyclePreferredProvider(mode, delta)
}

func (m *modelImpl) preferredProviderForMode(mode provider.ContentType) string {
	return m.filterScope.For(mode).PreferredProvider
}

func (m *modelImpl) preferredProviderOptions(mode provider.ContentType) []ProviderRow {
	options := []ProviderRow{{ID: "", Name: "Auto"}}
	seen := map[string]bool{"": true}
	if m.registry == nil {
		return options
	}
	for _, source := range m.registry.ProvidersForMode(mode) {
		id := strings.TrimSpace(source.Name())
		key := strings.ToLower(id)
		if id == "" || seen[key] || m.isProviderDisabledKey(mode, id) {
			continue
		}
		if id == "jellyfin" && !m.isModeAvailable("jellyfin") {
			continue
		}
		seen[key] = true
		options = append(options, ProviderRow{ID: id, Name: m.registry.DisplayName(id)})
	}
	return options
}

func (m *modelImpl) toggleAllHistoryModes() {
	if m.historyAllModesEnabled() {
		m.disableHistory = true
		return
	}
	m.disableHistory = false
	m.disabledHistoryModes = make(map[string]bool)
}

func (m *modelImpl) historyAllModesEnabled() bool {
	return !m.disableHistory && len(m.disabledHistoryModes) == 0
}

type startupModeOption struct {
	Key   string
	Label string
}

func (m *modelImpl) availableStartupModes() []startupModeOption {
	modes := []startupModeOption{
		{"last", "Last active"},
		{"first", "First in list"},
	}
	for _, modeKey := range m.configuredModes {
		if !m.disabledModes[modeKey] && m.isModeAvailable(modeKey) {
			kind := model.FromKey(modeKey)
			label := modeKey
			if s := kind.Spec(); s != nil && s.Label != "" {
				label = s.Label
			}
			modes = append(modes, startupModeOption{Key: modeKey, Label: label})
		}
	}
	return modes
}

func (m *modelImpl) cycleDefaultMode(delta int) {
	options := m.availableStartupModes()
	if len(options) == 0 {
		m.defaultMode = "last"
		m.saveSettings()
		return
	}

	cur := strings.ToLower(strings.TrimSpace(m.defaultMode))
	if cur == "" {
		cur = "last"
	}
	curIdx := 0
	found := false
	for i, opt := range options {
		if opt.Key == cur {
			curIdx = i
			found = true
			break
		}
	}
	if !found {
		curIdx = 0
	}
	nextIdx := (curIdx + delta + len(options)) % len(options)
	m.defaultMode = options[nextIdx].Key
	m.saveSettings()
}
func (m *modelImpl) reorderMode(delta int) (tea.Model, tea.Cmd) {
	if len(m.configuredModes) <= 1 {
		return m, nil
	}
	idx := m.settingsIndex - 1
	if idx < 0 || idx >= len(m.configuredModes) {
		return m, nil
	}
	target := idx + delta
	if target < 0 || target >= len(m.configuredModes) {
		return m, nil
	}
	m.configuredModes[idx], m.configuredModes[target] = m.configuredModes[target], m.configuredModes[idx]
	m.settingsIndex = target + 1
	m.updateEffectiveModes()
	m.saveSettings()
	return m, nil
}

func (m *modelImpl) toggleModeAtIndex(idx int) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.configuredModes) {
		return m, nil
	}
	modeKey := m.configuredModes[idx]
	// Check if available
	if !m.isModeAvailable(modeKey) {
		return m, nil
	}

	// If currently enabled, check if it's the last effective mode
	if !m.disabledModes[modeKey] {
		effective := m.effectiveModesList()
		if len(effective) <= 1 {
			return m, m.setToast("✗ keep at least one mode on", ToastError)
		}
		m.disabledModes[modeKey] = true
	} else {
		delete(m.disabledModes, modeKey)
	}
	if m.disabledModes[m.defaultMode] {
		m.defaultMode = "last"
	}

	m.updateEffectiveModes()
	m.saveSettings()
	// If the disabled mode was the active mode, switch to the first effective mode
	var cmd tea.Cmd
	if m.disabledModes[string(m.appMode)] {
		effective := m.effectiveModesList()
		if len(effective) > 0 {
			cmd = m.switchToMode(effective[0])
		}
	}

	return m, cmd
}

// isModeAvailable reports whether any provider currently serves the mode.
func (m *modelImpl) isModeAvailable(modeKey string) bool {
	return len(m.registry.ProvidersForMode(provider.ContentType(modeKey))) > 0
}

func (m *modelImpl) effectiveModesList() []provider.ContentType {
	var effective []provider.ContentType
	for _, k := range m.configuredModes {
		if !m.disabledModes[k] && m.isModeAvailable(k) {
			effective = append(effective, provider.ContentType(k))
		}
	}
	return effective
}

func (m *modelImpl) updateEffectiveModes() {
	m.modes = m.effectiveModesList()
}

// providerSection is one Settings > Providers group.
type providerSection struct {
	Section string
	Mode    provider.ContentType
}

// settingsSectionLabel returns the longer label Settings uses for modes whose
// KindSpec label is abbreviated, or "" when the canonical label applies.
func settingsSectionLabel(mode provider.ContentType) string {
	switch mode {
	case provider.ModeTV:
		return "TV Shows"
	case provider.ModeStremio:
		return "Stremio Add-ons"
	default:
		return ""
	}
}

// providerSections derives the Settings > Providers sections from the
// configured mode order so newly added modes cannot be omitted here.
func (m *modelImpl) providerSections() []providerSection {
	modes := m.configuredModes
	if len(modes) == 0 {
		modes = settings.DefaultModes
	}
	sections := make([]providerSection, 0, len(modes))
	for _, key := range modes {
		mode := provider.ContentType(key)
		label := settingsSectionLabel(mode)
		if label == "" {
			if spec := model.FromKey(key).Spec(); spec != nil && spec.Label != "" {
				label = spec.Label
			}
		}
		sections = append(sections, providerSection{Section: label, Mode: mode})
	}
	return sections
}

func (m *modelImpl) isProviderDisabledKey(mode provider.ContentType, id string) bool {
	if m.disabledProviders == nil {
		return false
	}
	p := strings.ToLower(strings.TrimSpace(id))
	if m.disabledProviders[p] {
		return true
	}
	if m.isConfiguredAddon(p) {
		// An addon appears under several modes; disabling it anywhere must
		// hide it everywhere, including Stremio browsing and playback.
		for key := range m.disabledProviders {
			if strings.HasSuffix(key, ":"+p) {
				return true
			}
		}
		return false
	}
	return m.disabledProviders[string(mode)+":"+p]
}

// isConfiguredAddon reports whether id is a user-configured Stremio addon.
func (m *modelImpl) isConfiguredAddon(id string) bool {
	return m.registry != nil && m.registry.IsConfiguredAddon(id)
}

// deleteModeScopedKeys removes every mode-scoped disable key for one provider.
func deleteModeScopedKeys(disabled map[string]bool, id string) {
	if disabled == nil {
		return
	}
	suffix := ":" + strings.ToLower(strings.TrimSpace(id))
	for key := range disabled {
		if strings.HasSuffix(key, suffix) {
			delete(disabled, key)
		}
	}
}

// rebindDisabledProviderKeys carries disable state from an addon's old identity
// to its new one so rebinding the URL does not silently re-enable a disabled
// addon. Refresh keeps the same identity and is a no-op.
func (m *modelImpl) rebindDisabledProviderKeys(oldName, newName string) {
	if m.disabledProviders == nil {
		return
	}
	oldName = strings.ToLower(strings.TrimSpace(oldName))
	newName = strings.ToLower(strings.TrimSpace(newName))
	if oldName == "" || newName == "" || oldName == newName {
		return
	}
	// Collect renames before mutating the map. Adding new keys while ranging
	// can make them visible to later iterations, which could re-rename a key
	// whose new identity still matches the old suffix.
	const sep = ":"
	var stale []string
	renamed := make(map[string]bool)
	for key, disabled := range m.disabledProviders {
		if !disabled {
			continue
		}
		if key == oldName {
			stale = append(stale, key)
			renamed[newName] = true
			continue
		}
		if strings.HasSuffix(key, sep+oldName) {
			stale = append(stale, key)
			renamed[strings.TrimSuffix(key, sep+oldName)+sep+newName] = true
		}
	}
	for _, key := range stale {
		delete(m.disabledProviders, key)
	}
	for key := range renamed {
		m.disabledProviders[key] = true
	}
}

func (m *modelImpl) providerRows() []ProviderRow {
	if m.registry == nil {
		return nil
	}
	var rows []ProviderRow
	for _, sec := range m.providerSections() {
		providers := m.registry.ProvidersForMode(sec.Mode)
		for _, p := range providers {
			id := p.Name()
			name := m.registry.DisplayName(id)
			key := string(sec.Mode) + ":" + strings.ToLower(id)
			avail := m.isModeAvailable(string(sec.Mode))
			rows = append(rows, ProviderRow{
				Section:   sec.Section,
				Mode:      sec.Mode,
				ID:        id,
				Name:      name,
				Key:       key,
				Enabled:   !m.isProviderDisabledKey(sec.Mode, id),
				Available: avail,
				Radio:     false,
			})
		}
	}
	return rows
}

func (m *modelImpl) toggleProviderAtIndex(index int) (tea.Model, tea.Cmd) {
	rows := m.providerRows()
	if index < 0 || index >= len(rows) {
		return m, nil
	}
	row := rows[index]
	if !row.Available {
		return m, nil
	}
	key := strings.ToLower(strings.TrimSpace(row.Key))
	id := strings.ToLower(strings.TrimSpace(row.ID))
	if m.disabledProviders == nil {
		m.disabledProviders = make(map[string]bool)
	}
	if m.isConfiguredAddon(id) {
		deleteModeScopedKeys(m.disabledProviders, id)
		if m.isProviderDisabledKey(row.Mode, id) {
			delete(m.disabledProviders, id)
		} else {
			m.disabledProviders[id] = true
		}
	} else if m.isProviderDisabledKey(row.Mode, id) {
		delete(m.disabledProviders, key)
	} else {
		m.disabledProviders[key] = true
	}
	m.syncDisabledProvidersToService()
	m.saveSettings()
	return m, nil
}

func (m *modelImpl) syncDisabledProvidersToService() {
	if m.searchQueryCancel != nil {
		m.searchQueryCancel()
		m.searchQueryCancel = nil
	}
	if m.searchOpID != 0 {
		m.searchOpID = m.newOpID()
		if m.activeView == viewSearch {
			m.loading = false
			m.loadingText = ""
		}
	}
	m.searchCacheGeneration.Add(1)
	if m.searchCache != nil {
		m.searchCache.Clear()
	}
	if m.mediaService == nil {
		return
	}
	var list []string
	seen := make(map[string]struct{})
	add := func(name string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		list = append(list, name)
	}
	for p, disabled := range m.disabledProviders {
		if !disabled {
			continue
		}
		add(p)
		// Addons from older settings files are disabled with mode-scoped keys
		// (e.g. "stremio:addon-xxxx"). The TUI already treats those as global;
		// mirror that here so every mode's service routes honor the choice.
		if mode, name, ok := strings.Cut(p, ":"); ok && mode != "" && m.isConfiguredAddon(name) {
			add(name)
		}
	}
	slices.Sort(list)
	m.mediaService.SetDisabledProviders(list)
}

// addonTestTimeout bounds a manifest probe so a dead addon cannot hang the
// settings screen.
const addonTestTimeout = 25 * time.Second

// startAddonCheck validates a manifest URL asynchronously. When replacing is
// non-empty, success swaps that addon for the newly tested one.
func (m *modelImpl) startAddonCheck(manifest, replacing string) tea.Cmd {
	if m.addonManifestTester == nil {
		return m.setStatusTimed(statusError, "Addon checks are unavailable")
	}
	parent := m.appCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, addonTestTimeout)
	m.addonTestOpID++
	m.addonTesting = true
	m.addonTestCancel = cancel
	m.addonPendingURL = manifest
	tester := m.addonManifestTester
	opID := m.addonTestOpID
	return func() tea.Msg {
		addon, err := tester(ctx, manifest)
		cancel()
		return addonTestDoneMsg{opID: opID, manifestURL: manifest, replacing: replacing, addon: addon, err: err}
	}
}

// canonicalAddonURL compares addon links without letting a trailing slash,
// a missing manifest suffix, or host case create a duplicate entry. Path and
// query stay verbatim: addon configuration identifiers are case-sensitive.
func canonicalAddonURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	u.Fragment = ""
	u.Path = strings.TrimSuffix(u.Path, "/")
	const suffix = "/manifest.json"
	if strings.HasSuffix(strings.ToLower(u.Path), suffix) {
		u.Path = u.Path[:len(u.Path)-len(suffix)]
	}
	u.RawPath = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String()
}
