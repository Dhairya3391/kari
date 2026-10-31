package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/termimg"
)

func (m *modelImpl) currentKind() model.Kind {
	switch m.activeView {
	case viewSearch:
		return model.FromKey(string(m.appMode))
	case viewEpisodes, viewPreview:
		if m.selectedSeries != nil && m.selectedSeries.MediaType != "" {
			return model.FromKey(m.selectedSeries.MediaType)
		}
		return model.FromKey(string(m.appMode))
	case viewChapters, viewReader:
		return model.KindManga
	default: // History, Settings, Downloads, Help
		return model.FromKey(string(m.appMode))
	}
}

// accentOverride returns the user's accent selection in the form
// ThemeFor expects: "auto", a preset name, or a hex color.
func (m *modelImpl) accentOverride() string {
	if m.accentIndex > 0 && m.accentIndex < len(accentPresets) {
		return accentPresets[m.accentIndex].name
	}
	if m.customAccentHex != "" {
		return m.customAccentHex
	}
	return ""
}

func (m *modelImpl) currentModeTheme() ModeTheme {
	override := m.accentOverride()
	target := ThemeFor(m.currentKind(), override)

	if !m.crossfadeActive {
		return target
	}

	// Crossfade lerp in RGB over 16 steps (0..16 / 16.0) with smootherstep easing
	frac := float64(m.crossfadeStep) / 16.0
	if frac > 1.0 {
		frac = 1.0
	}

	dest := target
	if m.crossfadeTo.Accent.Dark != "" {
		dest = m.crossfadeTo
	}

	fromDark := HexToRGBA(m.crossfadeFrom.Accent.Dark)
	toDark := HexToRGBA(dest.Accent.Dark)
	fromLight := HexToRGBA(m.crossfadeFrom.Accent.Light)
	toLight := HexToRGBA(dest.Accent.Light)

	curDark := SmoothMixRGB(fromDark, toDark, frac)
	curLight := SmoothMixRGB(fromLight, toLight, frac)

	fromDimDark := HexToRGBA(m.crossfadeFrom.AccentDim.Dark)
	toDimDark := HexToRGBA(dest.AccentDim.Dark)
	fromDimLight := HexToRGBA(m.crossfadeFrom.AccentDim.Light)
	toDimLight := HexToRGBA(dest.AccentDim.Light)

	curDimDark := SmoothMixRGB(fromDimDark, toDimDark, frac)
	curDimLight := SmoothMixRGB(fromDimLight, toDimLight, frac)

	return ModeTheme{
		Accent: lipgloss.AdaptiveColor{
			Dark:  RGBToHex(curDark),
			Light: RGBToHex(curLight),
		},
		AccentDim: lipgloss.AdaptiveColor{
			Dark:  RGBToHex(curDimDark),
			Light: RGBToHex(curDimLight),
		},
	}
}

func (m *modelImpl) currentAccent() lipgloss.AdaptiveColor {
	return m.currentModeTheme().Accent
}

func (m *modelImpl) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	var output string
	if m.width < 36 || m.height < 10 {
		output = m.renderSmallTerminalNotice()
	} else if m.activeView == viewReader {
		output = m.renderReaderFullscreen()
	} else {
		output = m.renderMainView()
	}
	if m.imgProtocol == termimg.ProtocolKitty && m.historyPosterVisible() {
		m.historyPosterDrawn = true
	} else if !m.historyPosterVisible() {
		m.historyPosterDrawn = false
	}

	// Pixel-protocol images (Kitty) are persistent overlays: clean up hidden slots
	if m.imgProtocol.NeedsCleanup() {
		var cleanup strings.Builder
		if !m.searchPosterVisible() {
			cleanup.WriteString(m.imgProtocol.Cleanup(kittySearchImageID))
		}
		if !m.historyPosterVisible() {
			cleanup.WriteString(m.imgProtocol.Cleanup(kittyHistoryImageID))
		}
		if !m.previewPosterVisible() {
			cleanup.WriteString(m.imgProtocol.Cleanup(kittyPreviewImageID))
		}
		if !m.readerPageVisible() {
			cleanup.WriteString(m.imgProtocol.Cleanup(kittyReaderImageID))
		}
		if !m.searchPosterVisible() && !m.previewPosterVisible() && !m.readerPageVisible() {
			cleanup.WriteString(m.imgProtocol.DeleteAll())
		}
		output = cleanup.String() + output
	}

	return output
}

func (m *modelImpl) renderSmallTerminalNotice() string {
	accent := m.currentAccent()
	st := NewStyles(accent)
	notice := st.Err.Render(fmt.Sprintf(
		"Kari needs at least 36 × 10 cells\nCurrent terminal: %d × %d\nResize the terminal to continue.",
		m.width,
		m.height,
	))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, notice)
}

func (m *modelImpl) searchPosterVisible() bool {
	return (m.activeView == viewSearch || m.activeView == viewEpisodes) && !m.showHelp && !m.animeStatusPickerOpen && m.imagesEnabled && m.searchPoster != ""
}

func (m *modelImpl) previewPosterVisible() bool {
	// No resolved check: retries nil out m.resolved while re-resolving
	// the same episode, and the nil window emitted a Kitty delete for
	// a still-valid poster every frame (blank art after refresh). The
	// poster belongs to the selected series/episode, which retries
	// never change; title switches already clear the slot explicitly.
	return m.activeView == viewPreview && !m.showHelp && !m.animeStatusPickerOpen && m.imagesEnabled && m.previewPoster != ""
}

func (m *modelImpl) historyPosterVisible() bool {
	return m.activeView == viewHistory && !m.showHelp && !m.animeStatusPickerOpen && m.imagesEnabled && m.historyPoster != "" && m.width >= 100
}

func (m *modelImpl) readerPageVisible() bool {
	if m.activeView != viewReader || m.showHelp {
		return false
	}
	_, ok := m.readerRender[m.readerPage]
	return ok
}

func (m *modelImpl) activeDownloadBadge(st Styles) string {
	if m.batchInProgress && m.batchTotal > 0 {
		pct := int(m.batchEpisodeProgress * 100)
		if pct < 0 {
			pct = 0
		} else if pct > 100 {
			pct = 100
		}
		if m.downloadPaused {
			return st.Dim.Render(fmt.Sprintf("⏸ %d/%d · %d%% · paused", m.batchCurrent, m.batchTotal, pct))
		}
		info := fmt.Sprintf("%d/%d · %d%%", m.batchCurrent, m.batchTotal, pct)
		if m.downloadSpeed != "" {
			info += " · " + m.downloadSpeed
		}
		return st.Current.Render("↓ ") + st.Dim.Render(info)
	}

	if (m.cancelDownload != nil || m.downloadOpID != 0) && !m.batchInProgress {
		pct := int(m.downloadProgress)
		if pct < 0 {
			pct = 0
		} else if pct > 100 {
			pct = 100
		}
		if m.downloadPaused {
			return st.Dim.Render(fmt.Sprintf("⏸ %d%% · paused", pct))
		}
		info := fmt.Sprintf("%d%%", pct)
		if m.downloadSpeed != "" {
			info += " · " + m.downloadSpeed
		}
		return st.Current.Render("↓ ") + st.Dim.Render(info)
	}

	if m.pendingDownload != nil && m.downloadPaused && !m.batchInProgress && m.downloadOpID == 0 {
		return st.Dim.Render("⏸ paused download · press d")
	}

	return ""
}

func (m *modelImpl) renderMainView() string {
	accent := m.currentAccent()
	dims := ComputeDims(m.width, m.height)
	st := NewStyles(accent)

	// Breadcrumb header with active download badge in top-right corner
	crumbs := m.activeCrumbs()
	badge := m.activeDownloadBadge(st)
	header := RenderHeaderWithStatus(crumbs, accent, dims.ContentWidth, badge)
	var body string
	if m.showHelp {
		body = m.renderHelpOverlayContent(dims.ContentWidth, accent)
	} else if m.animeStatusPickerOpen {
		body = m.renderAnimeStatusOverlayContent(dims.ContentWidth, accent)
	} else {
		body = m.renderScreenBody(dims, accent)
	}

	// Status block above the footer: the loading indicator lives here
	// in a fixed slot so its appearance never shifts body content up
	// or down a line. A live toast stacks below it; both are budgeted
	// out of the body, which stays top-aligned.
	var statusRows []string
	if m.loading {
		text := strings.TrimSpace(m.loadingText)
		if text == "" {
			text = "Loading…"
		}
		sp := m.spinner.View()
		if sp == "" {
			sp = "⠋"
		}
		st := NewStyles(accent)
		loadingLine := st.Current.Render(sp) + " " + st.Dim.Render(text)
		statusRows = append(statusRows, lipgloss.PlaceHorizontal(dims.ContentWidth, lipgloss.Center, loadingLine))
	}
	now := time.Now()
	if m.activeToast != nil && !m.activeToast.IsExpired(now) {
		statusRows = append(statusRows, lipgloss.PlaceHorizontal(dims.ContentWidth, lipgloss.Center, m.activeToast.RenderWithTime(accent, now)))
	} else if text := strings.TrimSpace(m.statusText); text != "" && (m.statusExpiresAt.IsZero() || time.Now().Before(m.statusExpiresAt)) {
		statusStyle := st.Dim
		switch m.statusType {
		case statusError:
			statusStyle = st.Err
		case statusInfo, statusSuccess, statusWarn:
			statusStyle = st.Current
		}
		statusRows = append(statusRows, lipgloss.PlaceHorizontal(dims.ContentWidth, lipgloss.Center, statusStyle.Render(text)))
	} else if m.playOpID != 0 && !m.loading {
		statusRows = append(statusRows, lipgloss.PlaceHorizontal(dims.ContentWidth, lipgloss.Center, st.Current.Render("▶ Now Playing")))
	}

	// Footer
	bindings := m.activeKeyBindings()
	status := m.activeIntegrationStatus()
	footer := RenderFooter(bindings, status, accent, dims.ContentWidth)

	return RenderFrame(dims, header, body, strings.Join(statusRows, "\n"), footer)
}

func (m *modelImpl) activeCrumbs() []string {
	if m.showHelp {
		base := m.activeCrumbsForView(m.activeView)
		return append(base, "help")
	}
	if m.animeStatusPickerOpen {
		base := m.activeCrumbsForView(m.activeView)
		return append(base, "status")
	}
	return m.activeCrumbsForView(m.activeView)
}

func (m *modelImpl) activeCrumbsForView(v viewState) []string {
	switch v {
	case viewSearch:
		if len(m.seriesResults) > 0 {
			return []string{"kari", strings.ToLower(string(m.appMode)), "search"}
		}
		return []string{"kari", strings.ToLower(string(m.appMode))}
	case viewEpisodes:
		title := "episodes"
		if m.selectedSeries != nil && m.selectedSeries.Title != "" {
			title = m.selectedSeries.Title
		}
		return []string{"kari", strings.ToLower(string(m.appMode)), strings.ToLower(title)}
	case viewChapters:
		title := "chapters"
		if m.selectedSeries != nil && m.selectedSeries.Title != "" {
			title = m.selectedSeries.Title
		}
		return []string{"kari", "manga", strings.ToLower(title)}
	case viewPreview:
		seriesTitle := "preview"
		if m.selectedSeries != nil && m.selectedSeries.Title != "" {
			seriesTitle = m.selectedSeries.Title
		} else if m.resolved != nil && m.resolved.SeriesTitle != "" {
			seriesTitle = m.resolved.SeriesTitle
		}

		epCode := ""
		if m.selectedEpisode != nil {
			if m.selectedEpisode.Season > 0 && m.selectedEpisode.Episode > 0 {
				epCode = fmt.Sprintf("s%02de%02d", m.selectedEpisode.Season, m.selectedEpisode.Episode)
			} else if m.selectedEpisode.Episode > 0 {
				epCode = fmt.Sprintf("ep%02d", m.selectedEpisode.Episode)
			}
		}

		crumbs := []string{"kari", strings.ToLower(string(m.appMode)), strings.ToLower(seriesTitle)}
		if epCode != "" {
			if m.appMode == provider.ModeAnime && m.audioMode != "" {
				crumbs = append(crumbs, fmt.Sprintf("%s [%s]", epCode, strings.ToLower(m.audioMode)))
			} else {
				crumbs = append(crumbs, epCode)
			}
		}
		return crumbs
	case viewHistory:
		return []string{"kari", "history"}
	case viewSettings:
		return []string{"kari", "settings"}
	case viewDownloads:
		return []string{"kari", "downloads"}
	default:
		return []string{"kari"}
	}
}

func (m *modelImpl) activeKeyBindings() []KeyBinding {
	if m.showHelp {
		return []KeyBinding{
			{Key: "esc", Action: "close help"},
		}
	}
	if m.animeStatusPickerOpen {
		return []KeyBinding{
			{Key: "↑/↓", Action: "move"},
			{Key: "enter", Action: "select"},
			{Key: "esc", Action: "cancel"},
		}
	}
	switch m.activeView {
	case viewSearch:
		if len(m.seriesResults) > 0 {
			return []KeyBinding{
				{Key: "enter", Action: "open"},
				{Key: "space", Action: "search"},
				{Key: "tab/⇧tab", Action: "mode"},
				{Key: "?", Action: "help"},
				{Key: "esc", Action: "back"},
			}
		}
		return []KeyBinding{
			{Key: "space", Action: "search"},
			{Key: "tab/⇧tab", Action: "mode"},
			{Key: "h", Action: "history"},
			{Key: "d", Action: "downloads"},
			{Key: "s", Action: "settings"},
			{Key: "?", Action: "help"},
			{Key: "q", Action: "quit"},
		}
	case viewEpisodes:
		if m.selectMode {
			return []KeyBinding{
				{Key: "space", Action: "toggle"},
				{Key: "m", Action: "mark complete"},
				{Key: "D", Action: "download"},
				{Key: "?", Action: "help"},
				{Key: "esc", Action: "cancel"},
			}
		}
		keys := []KeyBinding{
			{Key: "enter", Action: "open"},
			{Key: "space", Action: "select"},
			{Key: "m", Action: "mark complete"},
		}
		if len(distinctSeasonNumbers(m.episodeResults)) > 1 {
			keys = append(keys, KeyBinding{Key: "←/→", Action: "season"})
		}
		if m.appMode == provider.ModeAnime {
			keys = append(keys, KeyBinding{Key: "t", Action: "status"})
			keys = append(keys, KeyBinding{Key: "a", Action: "sub/dub"})
		}
		keys = append(keys,
			KeyBinding{Key: "?", Action: "help"},
			KeyBinding{Key: "esc", Action: "back"},
		)
		return keys
	case viewChapters:
		return []KeyBinding{
			{Key: "enter", Action: "read"},
			{Key: "/", Action: "filter"},
			{Key: "g/G", Action: "top/bottom"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}
	case viewPreview:
		keys := []KeyBinding{
			{Key: "enter", Action: "play"},
			{Key: "m", Action: "mark complete"},
		}
		if m.resolved != nil && model.IsEpisodeBased(m.resolved.MediaType) {
			keys = append(keys, KeyBinding{Key: "n", Action: "next"})
		}
		keys = append(keys, KeyBinding{Key: "D", Action: "download"})
		if m.appMode == provider.ModeAnime {
			keys = append(keys, KeyBinding{Key: "t", Action: "status"})
			keys = append(keys, KeyBinding{Key: "a", Action: "sub/dub"})
		}
		if m.hasResumePosition() {
			keys = append(keys, KeyBinding{Key: "r", Action: "restart"})
		}
		if m.failedProviderName != "" {
			keys = append(keys, KeyBinding{Key: "R", Action: "retry"})
		}
		keys = append(keys,
			KeyBinding{Key: "?", Action: "help"},
			KeyBinding{Key: "esc", Action: "back"})
		return keys
	case viewHistory:
		if m.historyFiltering {
			return []KeyBinding{
				{Key: "enter", Action: "apply filter"},
				{Key: "esc", Action: "close filter"},
				{Key: "alt+backspace / ctrl+w", Action: "delete word"},
				{Key: "ctrl+u", Action: "clear query"},
				{Key: "F1", Action: "help"},
			}
		}
		return []KeyBinding{
			{Key: "enter", Action: "open"},
			{Key: "tab/⇧tab", Action: "section"},
			{Key: "←/→", Action: "mode"},
			{Key: "/", Action: "filter"},
			{Key: "d", Action: "delete"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}
	case viewSettings:
		if m.audioPickerOpen {
			return []KeyBinding{
				{Key: "↑/↓", Action: "move"},
				{Key: "space", Action: "toggle"},
				{Key: "enter", Action: "done"},
				{Key: "esc", Action: "cancel"},
			}
		}
		return []KeyBinding{
			{Key: "↑↓", Action: "move"},
			{Key: "←→", Action: "change"},
			{Key: "enter", Action: "edit"},
			{Key: "tab/⇧tab", Action: "category"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}
	case viewDownloads:
		data := m.collectDownloadsData(m.width, m.height, m.currentAccent())
		onActive := len(data.Active) > 0 && m.downloadsIndex < len(data.Active)
		pAction := "pause"
		if m.downloadPaused {
			pAction = "resume"
		}
		if onActive {
			return []KeyBinding{
				{Key: "p", Action: pAction},
				{Key: "enter", Action: "open folder"},
				{Key: "x", Action: "cancel"},
				{Key: "X", Action: "cancel all"},
				{Key: "?", Action: "help"},
				{Key: "esc", Action: "back"},
			}
		}

		var bindings []KeyBinding
		if len(data.Active) > 0 || m.downloadPaused {
			bindings = append(bindings, KeyBinding{Key: "p", Action: pAction})
		}
		if len(data.Done) > 0 {
			bindings = append(bindings, KeyBinding{Key: "enter", Action: "play"})
		}
		bindings = append(bindings, KeyBinding{Key: "o", Action: "open folder"})
		if len(data.Done) > 0 {
			bindings = append(bindings, KeyBinding{Key: "x", Action: "delete"})
		}
		bindings = append(bindings,
			KeyBinding{Key: "?", Action: "help"},
			KeyBinding{Key: "esc", Action: "back"},
		)
		return bindings
	default:
		return []KeyBinding{
			{Key: "esc", Action: "back"},
			{Key: "?", Action: "help"},
		}
	}
}

func (m *modelImpl) activeIntegrationStatus() *IntegrationStatus {
	playerName := m.selectedPlayerName()
	trackerName := ""
	trackerConnected := false

	if m.appMode == provider.ModeAnime {
		trackerName = "anilist"
		if m.anilistClient != nil && m.anilistClient.IsAuthenticated() {
			trackerConnected = true
		}
	} else {
		trackerName = "trakt"
		if m.traktClient != nil && m.traktClient.IsAuthenticated() {
			trackerConnected = true
		}
	}

	return &IntegrationStatus{
		PlayerName:       playerName,
		Playing:          m.playOpID != 0,
		TrackerName:      trackerName,
		TrackerConnected: trackerConnected,
	}
}

func (m *modelImpl) renderScreenBody(dims Dims, accent lipgloss.AdaptiveColor) string {
	switch m.activeView {
	case viewSearch:
		if len(m.seriesResults) > 0 {
			// Render Results view
			return RenderResultsScreen(m.collectResultsData(dims.ContentWidth, dims.BodyHeight, accent))
		}
		// Render Search Home view
		return RenderSearchScreen(m.collectSearchData(dims.ContentWidth, accent))

	case viewEpisodes:
		return RenderEpisodesScreen(m.collectEpisodesData(dims.ContentWidth, dims.BodyHeight, accent))

	case viewChapters:
		return RenderChaptersScreen(m.collectChaptersData(dims.ContentWidth, dims.BodyHeight, accent))
	case viewPreview:
		return RenderPreviewScreen(m.collectPreviewData(dims.ContentWidth, dims.BodyHeight, accent))

	case viewHistory:
		return RenderHistoryScreen(m.collectHistoryData(dims.ContentWidth, dims.BodyHeight, accent))

	case viewSettings:
		return RenderSettingsScreen(m.collectSettingsData(dims.ContentWidth, dims.BodyHeight))

	case viewDownloads:
		return RenderDownloadsScreen(m.collectDownloadsData(dims.ContentWidth, dims.BodyHeight, accent))

	default:
		return ""
	}
}
func (m *modelImpl) renderHelpOverlayContent(width int, accent lipgloss.AdaptiveColor) string {
	screenName := "Global"
	var contextKeys []KeyBinding

	switch m.activeView {
	case viewSearch:
		if len(m.seriesResults) > 0 {
			screenName = "Results"
			contextKeys = []KeyBinding{
				{Key: "enter", Action: "open series"},
				{Key: "↑↓ j k", Action: "navigate results"},
				{Key: "tab/⇧tab", Action: "cycle mode"},
				{Key: "space", Action: "new search"},
				{Key: "/", Action: "filter results"},
				{Key: "d", Action: "downloads"},
			}
		} else {
			screenName = "Search"
			contextKeys = []KeyBinding{
				{Key: "space/enter", Action: "focus search"},
				{Key: "tab/⇧tab", Action: "cycle mode"},
				{Key: "1..9", Action: "jump to mode"},
				{Key: "h", Action: "history"},
				{Key: "d", Action: "downloads"},
				{Key: "s", Action: "settings"},
			}
		}
	case viewEpisodes:
		screenName = "Episodes"
		contextKeys = []KeyBinding{
			{Key: "enter", Action: "open source preview"},
			{Key: "space", Action: "toggle select mode"},
			{Key: "m", Action: "toggle watched"},
			{Key: "[ ]", Action: "switch season tabs"},
			{Key: "/ / ctrl+f", Action: "filter by title or number"},
			{Key: "g / G", Action: "first / last episode"},
			{Key: "pgup / pgdn", Action: "page up / down"},
			{Key: "ctrl+u / ctrl+d", Action: "half page (browse mode)"},
			{Key: "D", Action: "batch download selected"},
			{Key: "ctrl+a", Action: "select all episodes"},
			{Key: "ctrl+d", Action: "deselect all"},
			{Key: "d", Action: "downloads"},
			{Key: "a", Action: "toggle sub/dub (anime)"},
			{Key: "t", Action: "anime status / rewatch"},
		}
	case viewPreview:
		screenName = "Preview"
		contextKeys = []KeyBinding{
			{Key: "enter / space", Action: "play selected source"},
			{Key: "↑↓ j k", Action: "choose playback source"},
			{Key: "g / G", Action: "top / lowest source"},
			{Key: "m", Action: "toggle watched"},
			{Key: "n / ]", Action: "next episode"},
			{Key: "p / [", Action: "previous episode"},
			{Key: "D", Action: "download selected source"},
			{Key: "d", Action: "open downloads screen"},
			{Key: "A", Action: "toggle autoplay"},
			{Key: "a", Action: "toggle sub/dub (anime)"},
			{Key: "t", Action: "anime status / rewatch"},
			{Key: "r", Action: "restart from 00:00"},
			{Key: "ctrl+p", Action: "switch external player"},
		}
	case viewHistory:
		screenName = "History"
		if m.historyFiltering {
			screenName = "Filter history"
			contextKeys = []KeyBinding{
				{Key: "type", Action: "search titles, episodes, and modes"},
				{Key: "backspace", Action: "delete character"},
				{Key: "alt+backspace / ctrl+w", Action: "delete previous word"},
				{Key: "ctrl+u", Action: "clear query"},
				{Key: "enter", Action: "keep filter"},
				{Key: "esc", Action: "close filter; esc again clears it"},
			}
			break
		}
		contextKeys = []KeyBinding{
			{Key: "enter", Action: "resume from timestamp"},
			{Key: "tab/⇧tab", Action: "switch Continue / Finished tab"},
			{Key: "[/]", Action: "switch content mode"},
			{Key: "/", Action: "filter titles and episodes"},
			{Key: "d", Action: "delete title from history"},
			{Key: "D", Action: "clear all history"},
			{Key: "t", Action: "anime status / rewatch"},
		}
	case viewSettings:
		screenName = "Settings"
		contextKeys = []KeyBinding{
			{Key: "↑↓ j k", Action: "navigate setting rows"},
			{Key: "←→ h l", Action: "change option value"},
			{Key: "tab/⇧tab", Action: "switch settings category"},
			{Key: "enter", Action: "edit / open picker"},
			{Key: "c / r / i", Action: "connect / revoke / import"},
		}
	case viewChapters:
		screenName = "Chapters"
		contextKeys = []KeyBinding{
			{Key: "enter", Action: "open / read chapter"},
			{Key: "↑↓ j k", Action: "navigate chapters"},
			{Key: "/", Action: "filter chapters"},
			{Key: "g / G", Action: "first / last chapter"},
			{Key: "pgup / pgdn", Action: "page up / down"},
			{Key: "ctrl+u / ctrl+d", Action: "half page scroll"},
			{Key: "d", Action: "downloads"},
		}
	case viewReader:
		screenName = "Reader"
		contextKeys = []KeyBinding{
			{Key: "←→ h l", Action: "previous / next page"},
			{Key: "space", Action: "next page"},
			{Key: "[ ]", Action: "previous / next chapter"},
			{Key: "g / G", Action: "first / last page"},
			{Key: "esc", Action: "exit reader"},
		}
	case viewDownloads:
		screenName = "Downloads"
		contextKeys = []KeyBinding{
			{Key: "enter", Action: "play completed download"},
			{Key: "p", Action: "pause / resume download"},
			{Key: "o", Action: "open download folder"},
			{Key: "x", Action: "cancel item / delete file (2×)"},
			{Key: "X", Action: "cancel all downloads (2×)"},
			{Key: "↑↓ j k", Action: "navigate items"},
			{Key: "g / G", Action: "first / last item"},
			{Key: "pgup / pgdn", Action: "page up / down"},
			{Key: "ctrl+u / ctrl+d", Action: "half page scroll"},
		}
	}

	globalKeys := []KeyBinding{
		{Key: "esc", Action: "back / close"},
		{Key: "?", Action: "help overlay"},
		{Key: "h", Action: "history"},
		{Key: "s", Action: "settings"},
		{Key: "d", Action: "downloads"},
		{Key: "ctrl+c", Action: "force quit"},
		{Key: "q", Action: "quit (2× if downloading)"},
	}
	return RenderHelpOverlay(HelpData{
		ScreenName:  screenName,
		ContextKeys: contextKeys,
		GlobalKeys:  globalKeys,
		Width:       width,
		Accent:      accent,
	})
}

func (m *modelImpl) renderAnimeStatusOverlayContent(width int, accent lipgloss.AdaptiveColor) string {
	connected := m.anilistClient != nil && m.anilistClient.IsAuthenticated()
	return RenderAnimeStatusOverlay(AnimeStatusOverlayData{
		Title:         m.animeStatusTargetTitle,
		AniListID:     m.animeStatusTargetAniListID,
		Connected:     connected,
		Loading:       m.animeStatusLoading,
		CurrentEntry:  m.animeStatusEntry,
		TotalEpisodes: m.animeStatusTotalEpisodes,
		SelectedIndex: m.animeStatusPickerIndex,
		Accent:        accent,
		Width:         width,
	})
}

func formatTimeMMSS(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	d := time.Duration(seconds) * time.Second
	hrs := int(d.Hours())
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60

	if hrs > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hrs, mins, secs)
	}
	return fmt.Sprintf("%02d:%02d", mins, secs)
}
