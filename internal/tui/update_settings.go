package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/util"
)

func (m *modelImpl) updateSettings(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// 1. Audio Language Picker Modal
		if m.audioPickerOpen {
			allLangs := m.availableLanguages()
			switch msg.String() {
			case "up", "k":
				if m.audioPickerIndex > 0 {
					m.audioPickerIndex--
				}
				return m, nil
			case "down", "j":
				if m.audioPickerIndex < len(allLangs)-1 {
					m.audioPickerIndex++
				}
			// Physical spacebar reports " ", not "space" — match
			// both, or the toggle silently never fires.
			case " ", "space":
				if m.audioPickerIndex < len(allLangs) {
					if m.languageFilter == nil {
						m.languageFilter = make(map[string]bool)
					}
					l := allLangs[m.audioPickerIndex]
					// Committed-code toggle scheme: store the flipped
					// value under both the code and display keys (an
					// explicit true re-enables), and refuse to leave
					// nothing enabled.
					wasEnabled := m.languageEnabled(l.Code) || m.languageEnabled(l.Display)
					m.languageFilter[l.Code] = !wasEnabled
					m.languageFilter[l.Display] = !wasEnabled
					if !m.hasEnabledLanguage() {
						m.languageFilter[l.Code] = true
						m.languageFilter[l.Display] = true
					}
					m.saveSettings()
				}
				return m, nil
			case "enter", "esc":
				m.audioPickerOpen = false
				m.saveSettings()
				m.clearStatus()
				return m, nil
			}
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
		switch msg.String() {
		case "tab":
			m.settingsCategory = (m.settingsCategory + 1) % 6
			m.settingsIndex = 0
			return m, nil
		case "shift+tab":
			m.settingsCategory = (m.settingsCategory - 1 + 6) % 6
			m.settingsIndex = 0
			return m, nil

		// Direct category jumps via 1-6 keys
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
			m.settingsCategory = CategoryAccounts
			m.settingsIndex = 0
			return m, nil
		case "6":
			m.settingsCategory = CategoryInterface
			m.settingsIndex = 0
			return m, nil

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

		case "left", "h":
			m.cycleCurrentSetting(-1)
			m.saveSettings()
			return m, m.triggerSubtitleSync()

		case "right", "l":
			m.cycleCurrentSetting(1)
			m.saveSettings()
			return m, m.triggerSubtitleSync()

		case "enter", "space", " ":
			if m.settingsCategory == CategoryModes {
				if m.settingsIndex == 0 {
					m.cycleDefaultMode(1)
					return m, nil
				}
				return m.toggleModeAtIndex(m.settingsIndex - 1)
			}
			if m.settingsCategory == CategoryLanguages && m.settingsIndex == 0 {
				// Open Audio language picker
				m.audioPickerOpen = true
				m.audioPickerIndex = 0
				return m, nil
			}
			if m.settingsCategory == CategoryAccounts {
				if m.settingsIndex == 0 {
					return m.startAniListAuth()
				}
				if m.settingsIndex == 1 {
					return m.startTraktAuth()
				}
				m.startupSync = !m.startupSync
				m.saveSettings()
				return m, nil
			}
			m.cycleCurrentSetting(1)
			m.saveSettings()
			return m, m.triggerSubtitleSync()

		case "c":
			if m.settingsCategory == CategoryAccounts {
				if m.settingsIndex == 0 {
					return m.startAniListAuth()
				}
				return m.startTraktAuth()
			}
		case "r":
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
			// Import the focused tracker's watched list into local
			// history. One-way and additive: local progress is never
			// overwritten, only missing entries filled in.
			if m.settingsCategory == CategoryAccounts {
				if m.settingsIndex == 0 {
					return m.startHistoryImport("anilist")
				}
				return m.startHistoryImport("trakt")
			}

		case "esc":
			m.goBackOne()
			return m, nil
		}
	}
	return m, nil
}

func (m *modelImpl) maxRowsForCategory(cat SettingsCategory) int {
	switch cat {
	case CategoryPlayback:
		return 4
	case CategoryLanguages:
		return 2
	case CategoryAnime:
		return 7
	case CategoryModes:
		return len(m.configuredModes) + 1
	case CategoryAccounts:
		return 3
	case CategoryInterface:
		return 3
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
		case 1: // Stream quality
			m.qualityMode = (m.qualityMode + delta + 4) % 4
		case 2: // Download quality
			m.downloadQuality = (m.downloadQuality + delta + 5) % 5
		case 3: // Autoplay next episode
			m.autoplay = !m.autoplay
		}

	case CategoryLanguages:
		switch m.settingsIndex {
		case 0: // Audio
			m.audioPickerOpen = true
			m.audioPickerIndex = 0
		case 1: // Subtitles
			if len(lang.SubtitleOptions) > 0 {
				m.subtitleLanguageIndex = (m.subtitleLanguageIndex + delta + len(lang.SubtitleOptions)) % len(lang.SubtitleOptions)
				m.subtitleLanguage = lang.SubtitleOptions[m.subtitleLanguageIndex]
			}
		}

	case CategoryAnime:
		switch m.settingsIndex {
		case 0: // Audio track
			if strings.EqualFold(m.audioMode, provider.AudioDub) {
				m.audioMode = provider.AudioSub
			} else {
				m.audioMode = provider.AudioDub
			}
		case 1: // Subtitles
			m.disableAnimeSubtitles = !m.disableAnimeSubtitles
			m.invalidateSubtitleSync()
			if m.subtitleService != nil {
				m.subtitleService.SetDisableAnimeSubtitles(m.disableAnimeSubtitles)
			}
		case 2: // Skip source
			m.skipProvider = cycleSkipProvider(m.skipProvider, delta < 0)
		case 3: // Intro
			m.autoSkipIntro = !m.autoSkipIntro
		case 4: // Outro
			m.autoSkipEnding = !m.autoSkipEnding
		case 5: // Recap
			m.skipRecap = !m.skipRecap
		case 6: // Preview
			m.skipPreview = !m.skipPreview
		}
	case CategoryModes:
		if m.settingsIndex == 0 {
			m.cycleDefaultMode(delta)
		} else if m.settingsIndex > 0 {
			m.reorderMode(delta)
		}
	case CategoryAccounts:
		if m.settingsIndex == 2 {
			m.startupSync = !m.startupSync
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
		}
	}
}

type authDoneMsg struct {
	service string
	err     error
}

// historyImportMsg carries the result of a one-way tracker import
// (AniList/Trakt watched lists into the local history store). Quiet
// results come from the automatic startup sync: they toast only when
// something actually arrived and never surface errors.
type historyImportMsg struct {
	source             string
	imported, upgraded int
	skipped            int
	opID               int
	quiet              bool
	err                error
}
type traktCodeMsg struct {
	userCode, verificationURL, deviceCode string
	interval, expiresIn                   int
}

func (m *modelImpl) startTraktAuth() (tea.Model, tea.Cmd) {
	if m.traktClient == nil {
		return m, m.setStatusTimed(statusError, "Trakt integration not configured")
	}
	m.loading = true
	m.loadingText = "Requesting code..."
	return m, func() tea.Msg {
		userCode, verificationURL, deviceCode, interval, expiresIn, err := m.traktClient.StartDeviceAuth(m.appCtx)
		if err != nil {
			return authDoneMsg{service: "Trakt", err: err}
		}
		return traktCodeMsg{
			userCode:        userCode,
			verificationURL: verificationURL,
			deviceCode:      deviceCode,
			interval:        interval,
			expiresIn:       expiresIn,
		}
	}
}

func (m *modelImpl) onTraktCode(msg traktCodeMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingText = ""
	m.traktAuthActive = true
	m.traktUserCode = msg.userCode
	m.traktVerifyURL = msg.verificationURL
	m.traktDeviceCode = msg.deviceCode
	_ = util.OpenBrowser(msg.verificationURL)
	statusCmd := m.setStatusTimed(statusInfo, fmt.Sprintf("Enter code %s at %s", msg.userCode, msg.verificationURL))
	parentCtx := m.appCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(parentCtx)
	m.traktCancel = cancel
	pollCmd := m.pollTraktAuthCmd(ctx, msg.deviceCode, msg.interval, msg.expiresIn)
	return m, tea.Batch(statusCmd, pollCmd)
}

func (m *modelImpl) pollTraktAuthCmd(ctx context.Context, deviceCode string, interval, expiresIn int) tea.Cmd {
	return func() tea.Msg {
		err := m.traktClient.PollDeviceAuth(ctx, deviceCode, interval, expiresIn)
		return authDoneMsg{service: "Trakt", err: err}
	}
}

func (m *modelImpl) onAuthDone(msg authDoneMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingText = ""
	switch msg.service {
	case "Trakt":
		m.traktAuthActive = false
		m.traktUserCode = ""
		m.traktVerifyURL = ""
		m.traktDeviceCode = ""
		if m.traktCancel != nil {
			m.traktCancel()
			m.traktCancel = nil
		}
	case "AniList":
		m.anilistAuthURL = ""
		m.authInput.Blur()
	}
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		return m, m.setStatusTimed(statusError, fmt.Sprintf("%s auth failed: %s", msg.service, cleanErrorForUI(msg.err)))
	}
	m.saveSettings()
	m.setToast(fmt.Sprintf("%s connected", msg.service), ToastSuccess)
	return m, m.setStatusTimed(statusSuccess, fmt.Sprintf("%s connected successfully", msg.service))
}

// startHistoryImport fetches the focused tracker's watched list and
// merges it into the local history store: one-way, additive, and async
// so the UI never blocks on the network.
func (m *modelImpl) startHistoryImport(source string) (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, m.setStatusTimed(statusError, "Watch history unavailable")
	}
	switch source {
	case "anilist":
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			return m, m.setStatusTimed(statusWarn, "Connect AniList first (c)")
		}
	case "trakt":
		if m.traktClient == nil || !m.traktClient.IsAuthenticated() {
			return m, m.setStatusTimed(statusWarn, "Connect Trakt first (c)")
		}
	default:
		return m, nil
	}
	m.loading = true
	m.loadingText = fmt.Sprintf("Importing watched history from %s...", source)
	opID := m.newOpID()
	m.historyImportOpID = opID
	return m, tea.Batch(m.spinner.Tick, m.historyImportCmd(opID, source))
}

// historyImportCmd runs the fetch+merge off the UI thread: tracker calls
// take their own timeouts, and the merge is plain store writes.
func (m *modelImpl) historyImportCmd(opID int, source string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.appCtx, 90*time.Second)
		defer cancel()

		items, err := m.fetchSourceItems(ctx, source)
		if err != nil {
			return historyImportMsg{source: source, opID: opID, err: err}
		}
		imported, upgraded, skipped := service.ImportWatched(m.historyStore, items)
		return historyImportMsg{source: source, opID: opID, imported: imported, upgraded: upgraded, skipped: skipped}
	}
}

// fetchSourceItems pulls every watch list one tracker owns: anime and
// manga from AniList, episodes and movies from Trakt.
func (m *modelImpl) fetchSourceItems(ctx context.Context, source string) ([]scrobble.WatchedItem, error) {
	switch source {
	case "anilist":
		anime, err := m.anilistClient.FetchWatchedList(ctx)
		if err != nil {
			return nil, err
		}
		manga, err := m.anilistClient.FetchMangaList(ctx)
		if err != nil {
			return nil, err
		}
		return append(anime, manga...), nil
	case "trakt":
		return m.traktClient.FetchWatchedHistory(ctx)
	default:
		return nil, fmt.Errorf("unknown import source %q", source)
	}
}

// autoImportSources lists the trackers able to feed the startup sync:
// connected clients only, so the sync never prompts or errors.
func (m *modelImpl) autoImportSources() []string {
	var out []string
	if m.anilistClient != nil && m.anilistClient.IsAuthenticated() {
		out = append(out, "anilist")
	}
	if m.traktClient != nil && m.traktClient.IsAuthenticated() {
		out = append(out, "trakt")
	}
	return out
}

// autoHistoryImportCmd syncs every connected tracker into the local
// store at startup, quietly: one background command, one result. It
// returns nil when there is nothing to sync from.
func (m *modelImpl) autoHistoryImportCmd() tea.Cmd {
	if m.historyStore == nil {
		return nil
	}
	sources := m.autoImportSources()
	if len(sources) == 0 {
		return nil
	}
	opID := m.newOpID()
	m.historyImportOpID = opID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.appCtx, 180*time.Second)
		defer cancel()

		var (
			imported, upgraded int
			skipped            int
		)
		for _, source := range sources {
			items, err := m.fetchSourceItems(ctx, source)
			if err != nil {
				// A source that errored simply contributes nothing;
				// the next startup (or a manual import) retries it.
				tuiLog.Debug("startup sync fetch failed", "source", source, "err", err)
				continue
			}
			i, u, s := service.ImportWatched(m.historyStore, items)
			imported, upgraded, skipped = imported+i, upgraded+u, skipped+s
		}
		return historyImportMsg{source: "sync", opID: opID, quiet: true, imported: imported, upgraded: upgraded, skipped: skipped}
	}
}

// onHistoryImport reports the import outcome: a toast with counts on
// success, a status line on failure. Stale results are ignored. Quiet
// (startup) results stay silent unless something arrived, and never
// surface errors — an offline start must not nag.
func (m *modelImpl) onHistoryImport(msg historyImportMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.historyImportOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		if msg.quiet {
			tuiLog.Debug("startup sync failed", "source", msg.source, "err", msg.err)
			return m, nil
		}
		logging.Error("history import failed", "source", msg.source, "err", msg.err)
		return m, m.setStatusTimed(statusError, fmt.Sprintf("%s import failed: %s", msg.source, cleanErrorForUI(msg.err)))
	}
	tuiLog.Info("history import done", "source", msg.source, "imported", msg.imported, "upgraded", msg.upgraded, "skipped", msg.skipped)
	if msg.quiet && msg.imported+msg.upgraded == 0 {
		return m, nil
	}
	parts := []string{fmt.Sprintf("%d new", msg.imported)}
	if msg.upgraded > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", msg.upgraded))
	}
	if msg.skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d already here", msg.skipped))
	}
	m.setToast(fmt.Sprintf("Imported from %s: %s", msg.source, strings.Join(parts, ", ")), ToastSuccess)
	return m, nil
}

func (m *modelImpl) startAniListAuth() (tea.Model, tea.Cmd) {
	if m.anilistClient == nil {
		return m, m.setStatusTimed(statusError, "AniList integration not configured")
	}
	url := m.anilistClient.AuthURL()
	m.anilistAuthURL = url
	m.authInput.Focus()
	m.authInput.SetValue("")
	util.OpenBrowser(url)
	return m, tea.Batch(m.setStatusTimed(statusInfo, "Browser opened — authorize Kari and paste code"), textinput.Blink)
}

func (m *modelImpl) triggerScrobble(entry history.Entry) {
	if !trackableEntry(entry) || m.resolved == nil {
		return
	}
	if entry.Complete {
		if m.anilistClient != nil && entry.Mode == string(provider.ModeAnime) {
			go func() {
				_ = m.anilistClient.UpdateProgress(context.Background(), *m.resolved)
			}()
		}
		if m.traktClient != nil {
			go func() {
				if entry.MediaType == provider.MediaTypeMovie {
					_ = m.traktClient.ScrobbleMovie(context.Background(), *m.resolved, 100)
				} else {
					_ = m.traktClient.ScrobbleEpisode(context.Background(), *m.resolved, 100)
				}
			}()
		}
	}
}

var skipProviderOptions = []string{"hybrid", "skipdb", "introdb", "anime-skip", "aniskip", "off"}

// trackableEntry reports whether a history entry belongs in tracker
// sync at all. Live has no tracker equivalent (ephemeral schedules, no
// progress model), so it stays out of both directions: never scrobbled
// out, never imported in (the merge layer skips it too).
func trackableEntry(entry history.Entry) bool {
	return entry.Mode != string(provider.ModeLive) && entry.MediaType != provider.MediaTypeLive
}

func cycleSkipProvider(current string, reverse bool) string {
	idx := 0
	for i, opt := range skipProviderOptions {
		if strings.EqualFold(opt, current) {
			idx = i
			break
		}
	}
	step := 1
	if reverse {
		step = -1
	}
	next := (idx + step + len(skipProviderOptions)) % len(skipProviderOptions)
	return skipProviderOptions[next]
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
			m.setToast("✗ keep at least one mode on", ToastError)
			return m, nil
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

func (m *modelImpl) isModeAvailable(modeKey string) bool {
	// Live needs live provider; jellyfin needs configured jellyfin
	switch modeKey {
	case "jellyfin":
		for _, p := range m.registry.ProvidersForMode(provider.ModeJellyfin) {
			if p != nil {
				return true
			}
		}
		return false
	case "live":
		for _, p := range m.registry.ProvidersForMode(provider.ModeLive) {
			if p != nil {
				return true
			}
		}
		return false
	default:
		return len(m.registry.ProvidersForMode(provider.ContentType(modeKey))) > 0
	}
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
