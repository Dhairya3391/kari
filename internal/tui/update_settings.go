package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/util"
)

func (m *modelImpl) updateSettings(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		defer m.ensureSettingsVisible()
		// Esc while either of these text inputs is focused is handled by
		// exitInputMode (called from handleGlobalKeys' Back case, which
		// runs before this function ever sees the key) rather than here —
		// that's what closes just the input instead of leaving the whole
		// Settings screen.
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
				code := m.authInput.Value()
				m.anilistAuthURL = ""
				m.authInput.Blur()
				m.loading = true
				m.loadingText = "Exchanging code..."
				return m, func() tea.Msg {
					err := m.anilistClient.ExchangeCode(m.appCtx, code)
					return authDoneMsg{err: err}
				}
			}
			var cmd tea.Cmd
			m.authInput, cmd = m.authInput.Update(msg)
			return m, cmd
		}

		switch msg.String() {
		case "up", "k":
			m.moveSettings(-1)
		case "down", "j":
			m.moveSettings(1)
		case "left":
			switch m.settingsIndex {
			case 2:
				// Value rows wrap instead of hard-stopping at the ends.
				m.qualityMode--
				if m.qualityMode < 0 {
					m.qualityMode = qualityLowest
				}
				m.selectedPlayback = 0
				if filtered := m.filteredPlayback(); len(filtered) > 0 {
					m.selectedPlayback = filtered[0]
				}
				m.saveSettings()
			case 3:
				languages := m.availableLanguages()
				if len(languages) == 0 {
					break
				}
				m.languageIndex--
				if m.languageIndex < 0 {
					m.languageIndex = len(languages) - 1
				}
			case 4:
				m.subtitleLanguageIndex--
				if m.subtitleLanguageIndex < 0 {
					m.subtitleLanguageIndex = len(lang.SubtitleOptions) - 1
				}
				m.subtitleLanguage = lang.SubtitleOptions[m.subtitleLanguageIndex]
				m.saveSettings()
			case 5:
				return m, tea.Batch(m.setImagesEnabled(false), m.triggerSubtitleSync())
			case 6:
				m.accentIndex--
				if m.accentIndex < 0 {
					m.accentIndex = len(accentPresets)
				}
				m.setAccent(m.accentIndex)
			case settingsSkipProviderIndex:
				m.skipProvider = cycleSkipProvider(m.skipProvider, true)
				m.saveSettings()
			case settingsAutoIntroIndex, settingsAutoEndingIndex, settingsAutoRecapIndex, settingsAutoPreviewIndex:
				m.toggleAutoSkip(m.settingsIndex)
				m.saveSettings()
			}
			return m, m.triggerSubtitleSync()
		case "right":
			switch m.settingsIndex {
			case 2:
				m.qualityMode++
				if m.qualityMode > qualityLowest {
					m.qualityMode = 0
				}
				m.selectedPlayback = 0
				if filtered := m.filteredPlayback(); len(filtered) > 0 {
					m.selectedPlayback = filtered[0]
				}
				m.saveSettings()
			case 3:
				languages := m.availableLanguages()
				if len(languages) == 0 {
					break
				}
				m.languageIndex++
				if m.languageIndex >= len(languages) {
					m.languageIndex = 0
				}
			case 4:
				m.subtitleLanguageIndex++
				if m.subtitleLanguageIndex >= len(lang.SubtitleOptions) {
					m.subtitleLanguageIndex = 0
				}
				m.subtitleLanguage = lang.SubtitleOptions[m.subtitleLanguageIndex]
				m.saveSettings()
			case 5:
				return m, tea.Batch(m.setImagesEnabled(true), m.triggerSubtitleSync())
			case 6:
				m.accentIndex++
				if m.accentIndex > len(accentPresets) {
					m.accentIndex = 0
				}
				m.setAccent(m.accentIndex)
			case settingsSkipProviderIndex:
				m.skipProvider = cycleSkipProvider(m.skipProvider, false)
				m.saveSettings()
			case settingsAutoIntroIndex, settingsAutoEndingIndex, settingsAutoRecapIndex, settingsAutoPreviewIndex:
				m.toggleAutoSkip(m.settingsIndex)
				m.saveSettings()
			}
			return m, m.triggerSubtitleSync()
		case " ":
			if m.settingsIndex == 3 && m.languageFilter != nil {
				languages := m.availableLanguages()
				if len(languages) > 0 {
					if m.languageIndex >= len(languages) {
						m.languageIndex = 0
					}
					selected := languages[m.languageIndex].Code
					m.languageFilter[selected] = !m.languageEnabled(selected)
					if !m.hasEnabledLanguage() {
						m.languageFilter[selected] = true
					}
					m.selectedPlayback = 0
					if filtered := m.filteredPlayback(); len(filtered) > 0 {
						m.selectedPlayback = filtered[0]
					}
					m.saveSettings()
				}
			} else if m.settingsIndex == settingsSkipProviderIndex {
				m.skipProvider = cycleSkipProvider(m.skipProvider, false)
				m.saveSettings()
			} else if m.toggleAutoSkip(m.settingsIndex) {
				m.saveSettings()
			}
			return m, m.triggerSubtitleSync()
		case "c", "C":
			switch m.settingsIndex {
			case 0:
				return m.startTraktAuth()
			case 1:
				return m.startAniListAuth()
			case 6:
				if m.accentIndex == len(accentPresets) {
					return m.startCustomAccentInput()
				}
			}
		case "r", "R":
			switch m.settingsIndex {
			case 0:
				if m.traktClient != nil {
					_ = m.traktClient.Revoke()
				}
			case 1:
				if m.anilistClient != nil {
					_ = m.anilistClient.Revoke()
				}
			}
		}
	case authDoneMsg:
		m.loading = false
		m.traktAuthCode = ""
		m.traktAuthURL = ""
		m.traktAuthDeviceCode = ""
		var cmd tea.Cmd
		if msg.err != nil {
			cmd = m.setStatusTimed(statusError, fmt.Sprintf("Auth failed: %v", msg.err))
		} else {
			cmd = m.setStatusTimed(statusSuccess, "Authenticated successfully")
		}
		return m, cmd
	case traktCodeMsg:
		m.traktAuthCode = msg.userCode
		m.traktAuthURL = msg.verificationURL
		m.traktAuthDeviceCode = msg.deviceCode
		return m, m.pollTraktAuth(msg.deviceCode, msg.interval, msg.expiresIn)
	}
	return m, nil
}

type authDoneMsg struct{ err error }
type traktCodeMsg struct {
	userCode, verificationURL, deviceCode string
	interval, expiresIn                   int
}

func (m *modelImpl) startTraktAuth() (tea.Model, tea.Cmd) {
	if m.traktClient == nil {
		return m, nil
	}
	m.loading = true
	m.loadingText = "Requesting code..."
	return m, func() tea.Msg {
		userCode, verURL, devCode, interval, expires, err := m.traktClient.StartDeviceAuth(m.appCtx)
		if err != nil {
			return authDoneMsg{err: err}
		}
		return traktCodeMsg{userCode, verURL, devCode, interval, expires}
	}
}

func (m *modelImpl) pollTraktAuth(deviceCode string, interval, expiresIn int) tea.Cmd {
	return func() tea.Msg {
		err := m.traktClient.PollDeviceAuth(m.appCtx, deviceCode, interval, expiresIn)
		return authDoneMsg{err: err}
	}
}

func (m *modelImpl) startAniListAuth() (tea.Model, tea.Cmd) {
	if m.anilistClient == nil {
		return m, nil
	}
	url := m.anilistClient.AuthURL()
	m.anilistAuthURL = url
	m.authInput.Focus()
	_ = util.OpenBrowser(url)
	return m, textinput.Blink
}

func (m *modelImpl) triggerScrobble(entry history.Entry) {
	if m.resolved == nil || m.historyStore == nil {
		return
	}

	// Capture values to avoid race conditions
	resolved := *m.resolved
	appMode := m.appMode
	trakt := m.traktClient
	anilist := m.anilistClient
	historyStore := m.historyStore
	appCtx := m.appCtx
	if appCtx == nil {
		appCtx = context.Background()
	}

	tuiLog.Debug("scrobble triggered", "entry", entry, "appMode", appMode)

	go func() {
		// Use a local copy of entry to ensure idempotency update doesn't affect other goroutines
		e := entry

		ctx, cancel := context.WithTimeout(appCtx, 15*time.Second)
		defer cancel()

		progress := e.PercentComplete
		if progress == 0 && e.DurationSecs > 0 {
			progress = e.PositionSecs / e.DurationSecs
		}

		// Idempotency check: only scrobble if progress changed significantly (>1%) or is 100%
		diff := progress - e.LastScrobbledPercent
		if diff < 0 {
			diff = -diff
		}
		if diff < 0.01 && e.LastScrobbledPercent != 0 && progress < 0.99 {
			tuiLog.Debug("scrobble skipped; redundant progress delta", "diff", diff)
			return
		}

		tuiLog.Debug("scrobble payload", "mediaType", resolved.MediaType, "tmdbID", resolved.TMDBID, "season", resolved.SeasonNumber, "episodeNumber", resolved.EpisodeNumber, "progress", progress)

		success := false
		if appMode == provider.ModeAnime {
			if anilist != nil && anilist.IsAuthenticated() {
				tuiLog.Debug("scrobbling to anilist", "episode", e.Title, "progress", progress)
				if err := anilist.UpdateProgress(ctx, resolved); err != nil {
					tuiLog.Error("anilist scrobble failed", "err", err)
				} else {
					tuiLog.Info("anilist scrobble succeeded", "episode", e.Title)
					success = true
				}
			}
		} else {
			if trakt != nil && trakt.IsAuthenticated() {
				if progress*100 < 1.0 {
					tuiLog.Debug("trakt scrobble skipped below 1% threshold", "progressPct", progress*100)
				} else {
					tuiLog.Debug("scrobbling to trakt", "episode", e.Title, "progress", progress)
					_ = trakt.RefreshIfNeeded(ctx)
					var err error
					if resolved.MediaType == provider.MediaTypeMovie {
						err = trakt.ScrobbleMovie(ctx, resolved, progress)
					} else {
						err = trakt.ScrobbleEpisode(ctx, resolved, progress)
					}
					if err != nil {
						tuiLog.Error("trakt scrobble failed", "err", err)
					} else {
						tuiLog.Info("trakt scrobble succeeded", "episode", e.Title)
						success = true
					}
				}
			}
		}

		if success {
			// Update the record with the last scrobbled percentage to prevent duplicates
			e.LastScrobbledPercent = progress
			// We need to re-fetch the latest entry from the store to avoid overwriting other updates
			if latest, ok := historyStore.Get(e.Key); ok {
				latest.LastScrobbledPercent = progress
				_ = historyStore.Upsert(latest)
			} else {
				_ = historyStore.Upsert(e)
			}
		}
	}()
}

var skipProviderOptions = []string{"hybrid", "anime-skip", "aniskip", "off"}

func (m *modelImpl) toggleAutoSkip(index int) bool {
	switch index {
	case settingsAutoIntroIndex:
		m.autoSkipIntro = !m.autoSkipIntro
	case settingsAutoEndingIndex:
		m.autoSkipEnding = !m.autoSkipEnding
	case settingsAutoRecapIndex:
		m.skipRecap = !m.skipRecap
	case settingsAutoPreviewIndex:
		m.skipPreview = !m.skipPreview
	default:
		return false
	}
	return true
}

func (m *modelImpl) moveSettings(direction int) {
	for index := m.settingsIndex + direction; index >= 0 && index <= settingsLastIndex; index += direction {
		if index == 3 && len(m.availableLanguages()) == 0 {
			continue
		}
		m.settingsIndex = index
		if index == 3 {
			m.languageIndex = 0
		}
		return
	}
}

func (m *modelImpl) ensureSettingsVisible() {
	selectedLine := 2 + m.settingsIndex*4
	visibleHeight := max(1, m.bodyHeight()-1)
	if selectedLine < m.bodyScroll {
		m.bodyScroll = selectedLine
		return
	}
	if selectedLine >= m.bodyScroll+visibleHeight {
		m.bodyScroll = selectedLine - visibleHeight + 1
	}
}

func cycleSkipProvider(current string, reverse bool) string {
	idx := 0
	for i, opt := range skipProviderOptions {
		if opt == current {
			idx = i
			break
		}
	}
	if reverse {
		idx = (idx - 1 + len(skipProviderOptions)) % len(skipProviderOptions)
	} else {
		idx = (idx + 1) % len(skipProviderOptions)
	}
	return skipProviderOptions[idx]
}
