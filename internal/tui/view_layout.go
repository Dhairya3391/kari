package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/lang"
	"kari/internal/model"
	"kari/internal/provider"
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

	// Crossfade lerp in RGB over 10 steps (0..10 / 10.0) with smoothstep easing
	frac := float64(m.crossfadeStep) / 10.0
	if frac > 1.0 {
		frac = 1.0
	}

	fromDark := HexToRGBA(m.crossfadeFrom.Accent.Dark)
	toDark := HexToRGBA(target.Accent.Dark)
	fromLight := HexToRGBA(m.crossfadeFrom.Accent.Light)
	toLight := HexToRGBA(target.Accent.Light)

	curDark := SmoothMixRGB(fromDark, toDark, frac)
	curLight := SmoothMixRGB(fromLight, toLight, frac)

	return ModeTheme{
		Accent: lipgloss.AdaptiveColor{
			Dark:  RGBToHex(curDark),
			Light: RGBToHex(curLight),
		},
		AccentDim: lipgloss.AdaptiveColor{
			Dark:  RGBToHex(curDark),
			Light: RGBToHex(curLight),
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
	return (m.activeView == viewSearch || m.activeView == viewEpisodes) && !m.showHelp && m.imagesEnabled && m.searchPoster != ""
}

func (m *modelImpl) previewPosterVisible() bool {
	// No resolved check: retries nil out m.resolved while re-resolving
	// the same episode, and the nil window emitted a Kitty delete for
	// a still-valid poster every frame (blank art after refresh). The
	// poster belongs to the selected series/episode, which retries
	// never change; title switches already clear the slot explicitly.
	return m.activeView == viewPreview && !m.showHelp && m.imagesEnabled && m.previewPoster != ""
}

func (m *modelImpl) historyPosterVisible() bool {
	return m.activeView == viewHistory && !m.showHelp && m.imagesEnabled && m.historyPoster != "" && m.width >= 100
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
	if text := strings.TrimSpace(m.statusText); text != "" && (m.statusExpiresAt.IsZero() || time.Now().Before(m.statusExpiresAt)) {
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
	if m.activeToast != nil && !m.activeToast.IsExpired(time.Now()) {
		statusRows = append(statusRows, m.activeToast.Render(accent))
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

	switch m.activeView {
	case viewSearch:
		if len(m.seriesResults) > 0 {
			return []KeyBinding{
				{Key: "enter", Action: "open"},
				{Key: "↑↓", Action: "move"},
				{Key: "tab", Action: "mode"},
				{Key: "space", Action: "search"},
				{Key: "/", Action: "filter"},
				{Key: "esc", Action: "back"},
				{Key: "?", Action: "help"},
			}
		}
		return []KeyBinding{
			{Key: "space", Action: "search"},
			{Key: "tab", Action: "mode"},
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
				{Key: "ctrl+a", Action: "all"},
				{Key: "ctrl+d", Action: "none"},
				{Key: "D", Action: "download"},
				{Key: "esc", Action: "cancel"},
			}
		}
		return []KeyBinding{
			{Key: "enter", Action: "open"},
			{Key: "space", Action: "select"},
			{Key: "[ ]", Action: "season"},
			{Key: "/", Action: "filter"},
			{Key: "a", Action: "sub/dub"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}
	case viewChapters:
		return []KeyBinding{
			{Key: "enter", Action: "open"},
			{Key: "/", Action: "filter"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}
	case viewPreview:
		keys := []KeyBinding{
			{Key: "enter/p", Action: "play"},
			{Key: "↑↓", Action: "source"},
			{Key: "n/]", Action: "next episode"},
			{Key: "[", Action: "prev episode"},
			{Key: "D", Action: "download"},
			{Key: "A", Action: "autoplay"},
		}
		if m.appMode == provider.ModeAnime {
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
		return []KeyBinding{
			{Key: "enter", Action: "open"},
			{Key: "tab", Action: "section"},
			{Key: "/", Action: "filter"},
			{Key: "d", Action: "delete"},
			{Key: "D", Action: "clear all"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}
	case viewSettings:
		if data := m.collectSettingsData(m.width, m.height); data.AudioPickerOpen {
			return []KeyBinding{
				{Key: "space", Action: "toggle"},
				{Key: "/", Action: "filter"},
				{Key: "enter", Action: "done"},
				{Key: "esc", Action: "cancel"},
			}
		}
		return []KeyBinding{
			{Key: "↑↓", Action: "move"},
			{Key: "←→", Action: "change"},
			{Key: "enter", Action: "edit"},
			{Key: "tab", Action: "category"},
			{Key: "i", Action: "import"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}
	case viewDownloads:
		pAction := "pause"
		if m.downloadPaused {
			pAction = "resume"
		}
		return []KeyBinding{
			{Key: "p", Action: pAction},
			{Key: "x", Action: "cancel item (2×)"},
			{Key: "X", Action: "cancel all (2×)"},
			{Key: "enter", Action: "open folder"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}
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
func (m *modelImpl) collectSearchData(width int, accent lipgloss.AdaptiveColor) SearchData {
	inputView := "› search " + strings.ToLower(string(m.appMode)) + "…"
	if m.queryInput.Focused() {
		inputView = m.queryInput.View()
	}

	return SearchData{
		Modes:        m.modes,
		ActiveMode:   m.appMode,
		InputView:    inputView,
		InputFocused: m.queryInput.Focused(),
		Loading:      m.loading,
		LoadingText:  m.loadingText,
		SpinnerFrame: m.spinner.View(),
		Width:        width,
		Accent:       accent,
	}
}

func (m *modelImpl) collectResultsData(width, height int, accent lipgloss.AdaptiveColor) ResultsData {
	posterBlock := posterBlock(m.searchPoster, m.searchPosterUnavailable, m.imgProtocol, kittySearchImageID, m.imagesEnabled)

	// The / filter narrows the widget, so the highlight is the widget
	// position — not the original index, which only selection uses.
	visible, _ := m.visibleSeriesResults()
	pos := m.seriesList.Index()
	if pos < 0 || pos >= len(visible) {
		pos = 0
	}

	inputView, inputFocused := "", false
	if m.queryInput.Focused() {
		inputView, inputFocused = m.queryInput.View(), true
	}

	return ResultsData{
		Query:         m.searchQuery,
		InputView:     inputView,
		InputFocused:  inputFocused,
		Mode:          m.appMode,
		Results:       visible,
		SelectedIndex: pos,
		FilterQuery:   m.resultsFilter,
		Filtering:     m.resultsFiltering,
		PosterBlock:   posterBlock,
		Overview:      m.previewOverview,
		Genres:        m.previewGenres,
		Rating:        m.previewRating,
		Loading:       m.loading,
		LoadingText:   m.loadingText,
		SpinnerFrame:  m.spinner.View(),
		Width:         width,
		TermWidth:     m.width,
		Height:        height,
		Accent:        accent,
	}
}

func (m *modelImpl) collectEpisodesData(width, height int, accent lipgloss.AdaptiveColor) EpisodesData {
	seriesTitle := "Episodes"
	if m.selectedSeries != nil && m.selectedSeries.Title != "" {
		seriesTitle = m.selectedSeries.Title
	}

	// Compute season count from distinct seasons in the episode list
	seasons := distinctSeasonNumbers(m.episodeResults)
	seasonCount := len(seasons)
	if seasonCount <= 0 {
		seasonCount = 1
	}

	seriesYear := ""
	seriesKind := ""
	if m.selectedSeries != nil {
		seriesYear = m.selectedSeries.Year
		seriesKind = m.selectedSeries.MediaType
	}

	selectedEpTitle := ""
	if seasonEps, _ := m.currentSeasonEpisodes(); m.seasonEpisodeIndex >= 0 && m.seasonEpisodeIndex < len(seasonEps) {
		ep := seasonEps[m.seasonEpisodeIndex]
		num := ep.Episode
		if num == 0 {
			num = m.seasonEpisodeIndex + 1
		}
		title := ep.Title
		if title == "" {
			title = fmt.Sprintf("Episode %d", num)
		}
		selectedEpTitle = fmt.Sprintf("%02d · %s", num, title)
	}

	return EpisodesData{
		SeriesTitle: seriesTitle,
		Episodes:    m.episodeResults,
		HistoryIndex: func() history.EpisodeIndex {
			if m.historyStore == nil {
				return nil
			}
			return history.BuildEpisodeIndex(m.historyStore.All(), seriesTitle)
		}(),
		SelectedIndex:        m.seasonEpisodeIndex,
		SeasonCount:          seasonCount,
		ActiveSeason:         m.activeSeason,
		SelectMode:           m.selectMode,
		SelectedIDs:          m.selectedEpisodes,
		FilterQuery:          m.episodeFilter,
		Filtering:            m.episodeFiltering,
		Loading:              m.loading,
		LoadingText:          m.loadingText,
		SpinnerFrame:         m.spinner.View(),
		Width:                width,
		Height:               height,
		Accent:               accent,
		PosterBlock:          posterBlock(m.searchPoster, m.searchPosterUnavailable, m.imgProtocol, kittySearchImageID, m.imagesEnabled),
		SeriesYear:           seriesYear,
		SeriesKind:           seriesKind,
		SeriesRating:         m.previewRating,
		SeriesGenres:         m.previewGenres,
		SeriesOverview:       m.previewOverview,
		TermWidth:            m.width,
		SelectedEpisodeTitle: selectedEpTitle,
	}
}
func (m *modelImpl) collectChaptersData(width, height int, accent lipgloss.AdaptiveColor) ChaptersData {
	title := "Chapters"
	overview := ""
	var genres []string
	providerName := ""
	if m.selectedSeries != nil {
		if m.selectedSeries.Title != "" {
			title = m.selectedSeries.Title
		}
		overview = m.selectedSeries.Overview
		genres = m.selectedSeries.Genres
		providerName = m.registry.DisplayName(selectedSeriesProvider(m.selectedSeries))
	}

	listH := max(3, height-7)
	m.chapterList.SetSize(max(20, width-4), listH)

	return ChaptersData{
		SeriesTitle:   title,
		Genres:        genres,
		ProviderName:  providerName,
		ChaptersCount: len(m.chapters),
		Overview:      overview,
		ListView:      m.chapterList.View(),
		Width:         width,
		Height:        height,
		Accent:        accent,
	}
}

func (m *modelImpl) collectPreviewData(width, height int, accent lipgloss.AdaptiveColor) PreviewData {
	seriesTitle := "Preview"
	if m.selectedSeries != nil && m.selectedSeries.Title != "" {
		seriesTitle = m.selectedSeries.Title
	} else if m.resolved != nil && m.resolved.SeriesTitle != "" {
		seriesTitle = m.resolved.SeriesTitle
	}

	epInfo := ""
	if m.selectedEpisode != nil && (m.selectedEpisode.Season > 0 || m.selectedEpisode.Episode > 0) {
		epTitle := m.selectedEpisode.Title
		if epTitle == "" {
			epTitle = fmt.Sprintf("Episode %d", m.selectedEpisode.Episode)
		}
		totalStr := ""
		if len(m.episodeResults) > 0 {
			totalStr = fmt.Sprintf("  (%d of %d)", m.selectedEpisodeIndex()+1, len(m.episodeResults))
		}
		if m.selectedEpisode.Season > 0 {
			epInfo = fmt.Sprintf("S%02d E%02d · %s%s", m.selectedEpisode.Season, m.selectedEpisode.Episode, epTitle, totalStr)
		} else {
			epInfo = fmt.Sprintf("EP %02d · %s%s", m.selectedEpisode.Episode, epTitle, totalStr)
		}
	}

	audioText := ""
	audioTrack := ""
	if m.appMode == provider.ModeAnime {
		mode := strings.ToLower(strings.TrimSpace(m.audioMode))
		if mode == "" {
			mode = "sub"
		}
		audioTrack = mode
		if mode == "dub" {
			audioText = "dub · English"
		} else {
			audioText = "sub · Japanese"
		}
	}
	// Non-anime modes show per-source audio in the sources table below,
	// so no header audio row is needed here.

	subtitlesText := ""
	subtitleType := ""
	subLang := ""
	if m.resolved != nil && m.resolved.SelectedSubtitle != nil {
		subLang = lang.Normalize(m.resolved.SelectedSubtitle.Language)
	}
	if subLang == "" {
		subLang = m.subtitleLangUsed
	}
	if subLang == "" && m.resolved != nil && len(m.rankedSources) > 0 && m.subtitleLanguage != "off" {
		subLang = m.subtitleLanguage
		if subLang == "" {
			subLang = "en"
		}
	}
	if subLang != "" && subLang != "off" {
		subtitlesText = subLang
		if src, ok := m.selectedPlaybackSource(); ok {
			subtitleType = detectSubtitleType(src, m.resolved, m.rawSubtitles, m.appMode, m.audioMode)
			if m.resolved != nil && m.resolved.SubtitlePath() != "" {
				subtitlesText += " ✓"
			}
		}
	}
	resumePos := m.currentResumePosition()

	poster := posterBlock(m.previewPoster, m.previewPosterUnavailable, m.imgProtocol, kittyPreviewImageID, m.imagesEnabled)

	var sources []provider.MediaSource
	if m.resolved != nil {
		sources = m.resolved.Playback
	}

	// Tripwire: ranked must never be empty while resolved sources exist
	// (the tier filter falls back to the full list). If this ever fires,
	// the qualities below identify the unhandled label shape.
	if len(m.rankedSources) == 0 && len(sources) > 0 {
		qualities := make([]string, 0, len(sources))
		for _, s := range sources {
			qualities = append(qualities, s.Quality)
		}
		tuiLog.Warn("preview desync: ranked empty with sources present",
			"qualityMode", m.qualityMode, "sources", len(sources), "qualities", strings.Join(qualities, "|"))
	}

	// Fall back to search-stamped metadata while the async details
	// fetch is in flight (or when it failed): providers like WeebCentral
	// ship overview/genres at search time.
	overview := m.previewOverview
	genres := m.previewGenres
	if overview == "" && m.selectedSeries != nil {
		overview = m.selectedSeries.Overview
	}
	if len(genres) == 0 && m.selectedSeries != nil {
		genres = m.selectedSeries.Genres
	}

	backends := make(map[string]string, len(m.rankedSources))
	providers := make(map[string]string, len(m.rankedSources))
	for _, scored := range m.rankedSources {
		src := scored.Source
		backends[src.Resolver+"\x00"+src.Quality] = sourceBackendName(src, m.registry.DisplayName)
		if _, ok := providers[src.Resolver]; !ok {
			providers[src.Resolver] = m.registry.DisplayName(src.Resolver)
		}
	}
	return PreviewData{
		SeriesTitle:      seriesTitle,
		EpisodeInfo:      epInfo,
		Genres:           genres,
		Rating:           m.previewRating,
		Overview:         overview,
		AudioText:        audioText,
		AudioTrack:       audioTrack,
		SubtitlesText:    subtitlesText,
		SubtitleType:     subtitleType,
		ResumePosition:   resumePos,
		PosterBlock:      poster,
		Sources:          sources,
		RankedSources:    m.rankedSources,
		BackendName:      backends,
		ProviderName:     providers,
		SelectedIndex:    m.previewSelectedIndex,
		PlayerName:       m.selectedPlayerName(),
		Autoplay:         m.autoplay,
		LoadingProviders: m.loadingProviders,
		TotalProviders:   m.totalProviders,
		FailedProvider:   m.failedProviderName,
		SpinnerFrame:     m.spinner.View(),
		Loading:          m.loading,
		Width:            width,
		Height:           height,
		Accent:           accent,
		Mode:             m.appMode,
	}
}

// detectSubtitleType determines the subtitle kind ("hard subs" or "soft subs")
// for the selected playback source and media context; returns empty string if unknown.
func detectSubtitleType(src provider.MediaSource, resolved *model.ResolvedMedia, rawSubtitles []model.SubtitleTrack, mode provider.ContentType, _ string) string {
	if mode != provider.ModeAnime {
		return ""
	}
	// A declared subtitle kind wins over heuristics: a hard-subtitled
	// stream stays "hard subs" even when the pool also lists
	// downloadable sidecar tracks.
	switch strings.ToLower(strings.TrimSpace(src.SubType)) {
	case provider.SubTypeHard:
		return "hard subs"
	case provider.SubTypeSoft:
		return "soft subs"
	}
	if len(src.Subtitles) > 0 {
		return "soft subs"
	}
	if (resolved != nil && len(resolved.Subtitles) > 0) || len(rawSubtitles) > 0 {
		return "soft subs"
	}
	lowerQuality := strings.ToLower(src.Quality)
	lowerURL := strings.ToLower(src.URL)
	if strings.Contains(lowerQuality, "soft sub") || strings.Contains(lowerQuality, "softsub") || strings.Contains(lowerQuality, "[soft]") || strings.Contains(lowerURL, "softsub") {
		return "soft subs"
	}
	return ""
}

func (m *modelImpl) collectHistoryData(width, height int, accent lipgloss.AdaptiveColor) HistoryData {
	continued, finished := splitHistoryGroups(m.historyGroups)

	activeTab := HistoryTabContinue
	activeList := continued
	if m.historyTab == historyTabFinished {
		activeTab = HistoryTabFinished
		activeList = finished
	}

	poster := posterBlock(m.historyPoster, m.historyPosterUnavailable, m.imgProtocol, kittyHistoryImageID, m.imagesEnabled)

	var selectedGroup *history.Group
	if idx := m.historySelectedIndex(); idx >= 0 && idx < len(activeList) {
		selectedGroup = &activeList[idx]
	}
	return HistoryData{
		ActiveTab:        activeTab,
		ContinueCount:    len(continued),
		FinishedCount:    len(finished),
		Groups:           activeList,
		SelectedIndex:    m.historySelectedIndex(),
		PosterBlock:      poster,
		SelectedGroup:    selectedGroup,
		ConfirmClearAll:  m.confirmClearHistory,
		ConfirmDeleteOne: m.confirmDelete,
		Width:            width,
		Height:           height,
		Accent:           accent,
	}
}

func (m *modelImpl) collectSettingsData(width, height int) SettingsData {
	accent := m.currentAccent()

	// Build audio languages summary
	var enabledSummary []string
	var allLangNames []string
	enabledMap := make(map[string]bool)
	allLangs := m.availableLanguages()
	for _, l := range allLangs {
		allLangNames = append(allLangNames, l.Display)
		// Absent from map or true = enabled; false = explicitly disabled
		isDisabled := false
		if val, ok := m.languageFilter[l.Code]; ok && !val {
			isDisabled = true
		} else if val, ok := m.languageFilter[l.Display]; ok && !val {
			isDisabled = true
		}
		if !isDisabled {
			enabledSummary = append(enabledSummary, l.Display)
			enabledMap[l.Display] = true
		}
	}

	audioSummary := "All"
	if len(enabledSummary) == 0 {
		audioSummary = "None"
	} else if len(enabledSummary) < len(allLangs) {
		if len(enabledSummary) <= 3 {
			audioSummary = strings.Join(enabledSummary, " · ")
		} else {
			audioSummary = fmt.Sprintf("%s · %s · %s +%d", enabledSummary[0], enabledSummary[1], enabledSummary[2], len(enabledSummary)-3)
		}
	}

	animeAudio := "Sub · Japanese"
	if strings.EqualFold(m.audioMode, "dub") {
		animeAudio = "Dub · English"
	}

	qualityName := "All"
	switch m.qualityMode {
	case qualityHighest:
		qualityName = "Highest"
	case qualityDataSaver:
		qualityName = "Data Saver"
	case qualityLowest:
		qualityName = "Lowest"
	}

	downloadQualityName := "Auto (same as stream)"
	switch m.downloadQuality {
	case downloadQuality4K:
		downloadQualityName = "4K (2160p)"
	case downloadQualityFHD:
		downloadQualityName = "FHD (1080p)"
	case downloadQualityHD:
		downloadQualityName = "HD (720p)"
	case downloadQualitySD:
		downloadQualityName = "SD (480p)"
	default:
		downloadQualityName = "Auto (same as stream)"
	}
	accentName := "Auto (per mode)"
	if m.accentIndex > 0 && m.accentIndex < len(accentPresets) {
		accentName = accentPresets[m.accentIndex].name
	} else if m.customAccentHex != "" {
		accentName = "Custom (" + m.customAccentHex + ")"
	}
	defaultModeName := "Last active"
	curMode := strings.ToLower(strings.TrimSpace(m.defaultMode))
	for _, opt := range m.availableStartupModes() {
		if opt.Key == curMode {
			defaultModeName = opt.Label
			break
		}
	}

	// Build modes rows for Settings > Modes
	var modeRows []ModeRow
	for _, modeKey := range m.configuredModes {
		kind := model.FromKey(modeKey)
		modeThm := ThemeFor(kind, "Auto")
		label := modeKey
		if s := kind.Spec(); s != nil && s.Label != "" {
			label = s.Label
		}
		avail := m.isModeAvailable(modeKey)
		var unavailNote string
		if !avail {
			if modeKey == "jellyfin" {
				unavailNote = "set JELLYFIN_URL"
			} else {
				unavailNote = "no provider"
			}
		}
		modeRows = append(modeRows, ModeRow{
			Key:         modeKey,
			Label:       label,
			Enabled:     !m.disabledModes[modeKey],
			Available:   avail,
			UnavailNote: unavailNote,
			Accent:      modeThm.Accent,
		})
	}

	return SettingsData{
		ActiveCategory:      m.settingsCategory,
		FocusedRowIndex:     m.settingsIndex,
		Width:               width,
		Height:              height,
		Accent:              accent,
		PlayerName:          m.selectedPlayerName(),
		QualityName:         qualityName,
		DownloadQualityName: downloadQualityName,
		Autoplay:            m.autoplay,
		AudioSummary:        audioSummary,
		SubtitleLanguage:    lang.Name(m.subtitleLanguage),
		AudioPickerOpen:     m.audioPickerOpen,
		AllLanguages:        allLangNames,
		EnabledLanguages:    enabledMap,
		PickerIndex:         m.audioPickerIndex,
		AnimeAudioTrack:     animeAudio,
		AnimeSubtitles:      !m.disableAnimeSubtitles,
		SkipSource:          m.skipProvider,
		AutoSkipIntro:       m.autoSkipIntro,
		AutoSkipEnding:      m.autoSkipEnding,
		AutoSkipRecap:       m.skipRecap,
		AutoSkipPreview:     m.skipPreview,
		AniListConnected:    m.anilistClient != nil && m.anilistClient.IsAuthenticated(),
		AniListAuthActive:   m.anilistAuthURL != "",
		AniListAuthURL:      m.anilistAuthURL,
		AuthInputView:       m.authInput.View(),
		TraktConnected:      m.traktClient != nil && m.traktClient.IsAuthenticated(),
		TraktAuthActive:     m.traktAuthActive,
		TraktUserCode:       m.traktUserCode,
		TraktVerifyURL:      m.traktVerifyURL,
		StartupSync:         m.startupSync,
		LoadingText:         m.loadingText,
		SpinnerFrame:        m.spinner.View(),
		PosterArtwork:       m.imagesEnabled,
		AccentName:          accentName,
		Transitions:         m.transitions,
		DefaultModeName:     defaultModeName,
		ModesRows:           modeRows,
	}
}

func (m *modelImpl) collectDownloadsData(width, height int, accent lipgloss.AdaptiveColor) DownloadsData {
	var active []ActiveDownload

	qualityName := "All"
	switch m.qualityMode {
	case qualityHighest:
		qualityName = "Highest"
	case qualityDataSaver:
		qualityName = "Data saver"
	case qualityLowest:
		qualityName = "Lowest"
	}

	if m.batchInProgress && m.batchTotal > 0 {
		seriesTitle := "Batch Download"
		if m.selectedSeries != nil && m.selectedSeries.Title != "" {
			seriesTitle = m.selectedSeries.Title
		}

		currentTitle := fmt.Sprintf("%s (Ep %d/%d)", seriesTitle, m.batchCurrent, m.batchTotal)
		if m.downloadTitle != "" {
			currentTitle = fmt.Sprintf("%s — %s (%d/%d)", seriesTitle, m.downloadTitle, m.batchCurrent, m.batchTotal)
		}

		active = append(active, ActiveDownload{
			Title:    currentTitle,
			Quality:  qualityName,
			Progress: m.batchEpisodeProgress,
			Speed:    m.downloadSpeed,
			ETA:      m.downloadETA,
			Queued:   false,
			Paused:   m.downloadPaused,
		})

		for i := m.batchCurrent + 1; i <= m.batchTotal; i++ {
			active = append(active, ActiveDownload{
				Title:   fmt.Sprintf("%s (Ep %d/%d)", seriesTitle, i, m.batchTotal),
				Quality: qualityName,
				Queued:  true,
			})
		}
	} else if (m.cancelDownload != nil || m.downloadOpID != 0 || (m.downloadPaused && m.pendingDownload != nil)) && !m.batchInProgress {
		title := "Download"
		if m.downloadTitle != "" {
			title = m.downloadTitle
		} else if m.pendingDownload != nil {
			title = m.pendingDownload.Episode.Title
			if title == "" {
				title = m.pendingDownload.Series.Title
			}
		} else if m.resolved != nil && m.resolved.SeriesTitle != "" {
			title = m.resolved.DisplayTitle()
		}

		active = append(active, ActiveDownload{
			Title:    title,
			Quality:  qualityName,
			Progress: m.downloadProgress / 100.0,
			Speed:    m.downloadSpeed,
			ETA:      m.downloadETA,
			Queued:   false,
			Paused:   m.downloadPaused,
		})
	}

	active = append(active, m.activeDownloads...)

	return DownloadsData{
		Active:        active,
		Done:          m.doneDownloads,
		SelectedIndex: m.downloadsIndex,
		Width:         width,
		Height:        height,
		Accent:        accent,
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
				{Key: "tab", Action: "cycle mode"},
				{Key: "space", Action: "new search"},
				{Key: "/", Action: "filter results"},
				{Key: "d", Action: "downloads"},
			}
		} else {
			screenName = "Search"
			contextKeys = []KeyBinding{
				{Key: "space/enter", Action: "focus search"},
				{Key: "tab", Action: "cycle mode"},
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
			{Key: "[ ]", Action: "switch season tabs"},
			{Key: "/", Action: "filter by title or number"},
			{Key: "g / G", Action: "first / last episode"},
			{Key: "pgup / pgdn", Action: "page up / down"},
			{Key: "ctrl+u / ctrl+d", Action: "half page (browse mode)"},
			{Key: "D", Action: "batch download selected"},
			{Key: "ctrl+a", Action: "select all episodes"},
			{Key: "ctrl+d", Action: "deselect all"},
			{Key: "d", Action: "downloads"},
			{Key: "a", Action: "toggle sub/dub (anime)"},
		}
	case viewPreview:
		screenName = "Preview"
		contextKeys = []KeyBinding{
			{Key: "enter", Action: "play selected source"},
			{Key: "↑↓ j k", Action: "choose playback source"},
			{Key: "enter / p", Action: "play selected source"},
			{Key: "n / ]", Action: "next episode"},
			{Key: "[", Action: "previous episode"},
			{Key: "D", Action: "download selected source"},
			{Key: "d", Action: "open downloads screen"},
			{Key: "A", Action: "toggle autoplay"},
			{Key: "a", Action: "toggle sub/dub (anime)"},
			{Key: "r", Action: "restart from the beginning"},
			{Key: "ctrl+p", Action: "switch external player"},
		}
	case viewHistory:
		screenName = "History"
		contextKeys = []KeyBinding{
			{Key: "enter", Action: "open preview with resume"},
			{Key: "tab", Action: "switch Continue / Finished tab"},
			{Key: "d", Action: "delete title from history"},
			{Key: "D", Action: "clear all history"},
			{Key: "/", Action: "filter history list"},
		}
	case viewSettings:
		screenName = "Settings"
		contextKeys = []KeyBinding{
			{Key: "↑↓ j k", Action: "navigate setting rows"},
			{Key: "←→ h l", Action: "change option value"},
			{Key: "tab", Action: "switch settings category"},
			{Key: "enter", Action: "edit value / open language picker"},
			{Key: "c / r / i", Action: "connect / revoke / import watched"},
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
			{Key: "p", Action: "pause / resume download"},
			{Key: "x", Action: "cancel selected item (press twice; deletes partials)"},
			{Key: "X", Action: "cancel all downloads (press twice)"},
			{Key: "enter", Action: "open download folder"},
			{Key: "↑↓ j k", Action: "navigate items"},
		}
	}

	globalKeys := []KeyBinding{
		{Key: "esc", Action: "back / close"},
		{Key: "?", Action: "help overlay"},
		{Key: "h", Action: "history"},
		{Key: "s", Action: "settings"},
		{Key: "d", Action: "downloads"},
		{Key: "ctrl+c", Action: "force quit"},
		{Key: "q", Action: "quit (2× while downloading: pauses downloads)"},
	}
	return RenderHelpOverlay(HelpData{
		ScreenName:  screenName,
		ContextKeys: contextKeys,
		GlobalKeys:  globalKeys,
		Width:       width,
		Accent:      accent,
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
