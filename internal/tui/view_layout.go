package tui

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/termimg"
)

func (m *modelImpl) currentKind() model.Kind {
	switch m.activeView {
	case viewSearch:
		return model.FromKey(string(m.appMode))
	case viewMangaSources:
		return model.KindManga
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

// crossfadeSteps is the number of interpolation steps in a theme transition.
const crossfadeSteps = 16

// buildCrossfadePalette precomputes every intermediate theme once, at the
// start of a transition. View() runs at up to 71fps during a crossfade, so
// parsing hex colors and lerping them on every frame is pure overhead.
func buildCrossfadePalette(from, to ModeTheme) [crossfadeSteps + 1]ModeTheme {
	var palette [crossfadeSteps + 1]ModeTheme
	fromDark := HexToRGBA(from.Accent.Dark)
	toDark := HexToRGBA(to.Accent.Dark)
	fromLight := HexToRGBA(from.Accent.Light)
	toLight := HexToRGBA(to.Accent.Light)
	fromDimDark := HexToRGBA(from.AccentDim.Dark)
	toDimDark := HexToRGBA(to.AccentDim.Dark)
	fromDimLight := HexToRGBA(from.AccentDim.Light)
	toDimLight := HexToRGBA(to.AccentDim.Light)
	for step := 0; step <= crossfadeSteps; step++ {
		frac := float64(step) / float64(crossfadeSteps)
		palette[step] = ModeTheme{
			Accent: lipgloss.AdaptiveColor{
				Dark:  RGBToHex(SmoothMixRGB(fromDark, toDark, frac)),
				Light: RGBToHex(SmoothMixRGB(fromLight, toLight, frac)),
			},
			AccentDim: lipgloss.AdaptiveColor{
				Dark:  RGBToHex(SmoothMixRGB(fromDimDark, toDimDark, frac)),
				Light: RGBToHex(SmoothMixRGB(fromDimLight, toDimLight, frac)),
			},
		}
	}
	return palette
}

func (m *modelImpl) currentModeTheme() ModeTheme {
	if !m.crossfadeActive {
		return ThemeFor(m.currentKind(), m.accentOverride())
	}
	if m.crossfadePalette[0].Accent.Dark == "" {
		m.crossfadePalette = buildCrossfadePalette(m.crossfadeFrom, m.crossfadeTo)
	}
	step := m.crossfadeStep
	if step < 0 {
		step = 0
	}
	if step > crossfadeSteps {
		step = crossfadeSteps
	}
	return m.crossfadePalette[step]
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
		if !m.livePosterVisible() {
			cleanup.WriteString(m.imgProtocol.Cleanup(kittyLiveImageID))
		}
		if !m.historyPosterVisible() {
			cleanup.WriteString(m.imgProtocol.Cleanup(kittyHistoryImageID))
		}
		if !m.previewPosterVisible() {
			cleanup.WriteString(m.imgProtocol.Cleanup(kittyPreviewImageID))
		}
		if !m.calendarPosterVisible() {
			cleanup.WriteString(m.imgProtocol.Cleanup(kittyCalendarImageID))
		}
		if !m.readerPageVisible() {
			cleanup.WriteString(m.imgProtocol.Cleanup(kittyReaderImageID))
		}
		if !m.searchPosterVisible() && !m.livePosterVisible() && !m.previewPosterVisible() &&
			!m.calendarPosterVisible() && !m.readerPageVisible() {
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

// contentWidth is the centered body width at the current terminal size.
func (m *modelImpl) contentWidth() int {
	return ComputeDims(m.width, m.height).ContentWidth
}

// searchPosterVisible reports whether the search-results or episodes
// inspector is actually drawing the series poster. The width gates mirror
// RenderResultsScreen and RenderEpisodesScreen: counting a poster the
// layout hides as an on-screen placement would make overlayLayoutKey clear
// the screen for an image that was never drawn.
func (m *modelImpl) searchPosterVisible() bool {
	if !m.imagesEnabled || m.showHelp || m.animeStatusPickerOpen || m.appMode == provider.ModeLive {
		return false
	}
	switch m.activeView {
	case viewSearch:
		// The results screen uses the filtered list for its details pane.
		visible, _ := m.visibleSeriesResults()
		return len(visible) > 0 && (m.width >= 90 || m.contentWidth() >= 70)
	case viewEpisodes:
		return m.width >= 80 && m.contentWidth() >= 65
	default:
		return false
	}
}

// livePosterVisible reports whether the live guide is drawing its "On now"
// artwork. The width gate mirrors RenderLiveResultsScreen's inspector split;
// counting a hidden poster would make overlay cleanup clear the wrong slot.
func (m *modelImpl) livePosterVisible() bool {
	if m.activeView != viewSearch || m.appMode != provider.ModeLive ||
		m.showHelp || m.animeStatusPickerOpen || !m.imagesEnabled {
		return false
	}
	if len(m.seriesResults) == 0 {
		return false
	}
	return m.contentWidth() >= liveInspectorMinWidth &&
		m.effectiveBodyHeight() >= liveMinInspectorHeight
}

func (m *modelImpl) previewPosterVisible() bool {
	if m.activeView != viewPreview || m.showHelp || m.animeStatusPickerOpen || !m.imagesEnabled {
		return false
	}
	// Mirrors showPreviewPoster in preview.go.
	return m.width >= 80 || m.contentWidth() >= 70
}

// previewHeaderSignature returns the plain text of the preview header's
// metadata block: the lines a sixel/iterm2 poster shares rows with. Styling
// is stripped because adaptive/crossfade colors can change without any text
// being rewritten, and only text writes erase an overlay image.
//
// Rendering the block costs a styled layout pass on every message, while the
// overlay key is consulted on each spinner tick and crossfade frame. The
// cached signature is keyed by the metadata that feeds the block, so ticks
// that cannot change it reuse the previous text.
func (m *modelImpl) previewHeaderSignature() string {
	accent := m.currentAccent()
	data := m.collectPreviewData(m.contentWidth(), m.height, accent)
	metaW := max(20, data.Width-previewPosterWidth(data))
	fingerprint := previewMetaFingerprint(data, metaW)
	if m.previewHeader.valid && m.previewHeader.fingerprint == fingerprint {
		return m.previewHeader.signature
	}
	signature := ansi.Strip(renderPreviewMeta(data, NewStyles(accent), metaW))
	m.previewHeader = previewHeaderCache{fingerprint: fingerprint, signature: signature, valid: true}
	return signature
}

// previewMetaFingerprint hashes every input of renderPreviewMeta so a cached
// signature survives until the metadata or available width actually changes.
// Styles are excluded: their escape codes are stripped from the signature.
func previewMetaFingerprint(data PreviewData, metaW int) uint64 {
	h := fnv.New64a()
	var width [8]byte
	binary.LittleEndian.PutUint64(width[:], uint64(metaW))
	h.Write(width[:])
	for _, field := range []string{
		data.SeriesTitle, data.EpisodeInfo, data.Rating, data.Overview,
		data.AudioText, data.SubtitlesText, data.SubtitleType,
		data.SubtitleProvider, data.SkipInfo, data.ResumePosition,
	} {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	for _, genre := range data.Genres {
		h.Write([]byte(genre))
		h.Write([]byte{0})
	}
	if data.Autoplay {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	if data.Completed {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	return h.Sum64()
}

func (m *modelImpl) historyPosterVisible() bool {
	if m.activeView != viewHistory || m.showHelp || m.animeStatusPickerOpen || !m.imagesEnabled {
		return false
	}
	// Mirrors showPosterPane in history.go, which gates on content width.
	return m.contentWidth() >= 100
}

// calendarPosterVisible reports whether the calendar detail pane is actually
// drawing the cover. The width gate mirrors RenderCalendarScreen's split, and
// the height gate mirrors the slot reservation in collectCalendarData;
// counting a hidden poster would make overlay cleanup clear the wrong slot.
func (m *modelImpl) calendarPosterVisible() bool {
	if m.activeView != viewCalendar || m.showHelp || m.animeStatusPickerOpen || !m.imagesEnabled {
		return false
	}
	if m.contentWidth() < calendarDetailPaneWidth {
		return false
	}
	detailHeight := calendarDetailBodyHeight(m.effectiveBodyHeight())
	return calendarPosterFits(detailHeight)
}

// readerPageVisible reports whether the fullscreen reader is drawing a
// rendered page. Help replaces the page (see renderReaderFullscreen), so a
// page hidden behind help must not count as visible or Kitty cleanup would
// leave its placement on screen.
func (m *modelImpl) readerPageVisible() bool {
	if m.activeView != viewReader || m.showHelp {
		return false
	}
	_, ok := m.readerRender[m.readerPage]
	return ok
}

// overlayLayoutKey snapshots every out-of-band image placement a frame
// would draw: which image, where, and at what terminal size. Non-image
// state (status text, spinner) is deliberately excluded so typing and
// progress ticks never force a full-screen clear. An empty key means no
// such graphics are on screen.
//
// Sixel and iTerm2 have no delete-by-id, so a moved or hidden placement can
// only be removed by clearing the screen. Comparing this key before and
// after an update tells Update when that clear is actually needed.
func (m *modelImpl) overlayLayoutKey() string {
	if !m.imgProtocol.NeedsScreenClear() || m.width <= 0 || m.height <= 0 {
		return ""
	}
	// The tiny-terminal notice replaces the whole UI, images included.
	if m.width < 36 || m.height < 10 {
		return ""
	}

	// Slot op IDs identify the artwork; rendered lengths catch a slot
	// whose image changed without a new fetch (notably poster size). Every
	// current render path bumps the op ID (posters) or the page index
	// (reader) when the bytes change, so equal keys imply equal placements.
	// Hashing the base64 payload here would run on every message, including
	// spinner ticks, for no added benefit today.
	var placements strings.Builder
	if m.searchPosterVisible() {
		fmt.Fprintf(&placements, "|search:%d:%d", m.searchPosterOpID, len(m.searchPoster))
	}
	if m.livePosterVisible() {
		fmt.Fprintf(&placements, "|live:%d:%d", m.livePosterOpID, len(m.livePoster))
	}
	if m.previewPosterVisible() {
		fmt.Fprintf(&placements, "|preview:%d:%d", m.previewPosterOpID, len(m.previewPoster))
		placements.WriteByte(':')
		placements.WriteString(m.previewHeaderSignature())
	}
	if m.historyPosterVisible() {
		fmt.Fprintf(&placements, "|history:%d:%d", m.historyPosterOpID, len(m.historyPoster))
	}
	if m.calendarPosterVisible() {
		fmt.Fprintf(&placements, "|calendar:%d:%d", m.calendarPosterOpID, len(m.calendarPoster))
	}
	if m.activeView == viewReader && !m.showHelp {
		if rendered, ok := m.readerRender[m.readerPage]; ok {
			fmt.Fprintf(&placements, "|reader:%d:%d:%d:%d:%d",
				m.readerPage, m.readerOpID, m.readerCols, m.readerRows, len(rendered))
		}
	}
	if placements.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("%s|%s|%dx%d", m.activeView, m.appMode, m.width, m.height) + placements.String()
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

// statusRowCount mirrors the status block built in renderMainView: a loading
// line plus at most one of toast, timed status, or "Now Playing". Overlay
// visibility gates use it so they reserve the same body height the renderer
// actually uses when a status row is on screen.
func (m *modelImpl) statusRowCount() int {
	n := 0
	if m.loading {
		n++
	}
	now := time.Now()
	if m.activeToast != nil && !m.activeToast.IsExpired(now) {
		return n + 1
	}
	if text := strings.TrimSpace(m.statusText); text != "" && (m.statusExpiresAt.IsZero() || now.Before(m.statusExpiresAt)) {
		return n + 1
	}
	if m.playOpID != 0 && !m.loading {
		return n + 1
	}
	return n
}

// effectiveBodyHeight is the body height after status rows are removed, the
// same value renderMainView passes to renderScreenBody.
func (m *modelImpl) effectiveBodyHeight() int {
	return max(5, ComputeDims(m.width, m.height).BodyHeight-m.statusRowCount())
}

func (m *modelImpl) renderMainView() string {
	accent := m.currentAccent()
	dims := ComputeDims(m.width, m.height)
	st := NewStyles(accent)

	// Breadcrumb header with active download badge in top-right corner
	crumbs := m.activeCrumbs()
	badge := m.activeDownloadBadge(st)
	header := RenderHeaderWithStatus(crumbs, accent, dims.ContentWidth, badge)

	// Status block above the footer: the loading indicator lives here
	// in a fixed slot so its appearance never shifts body content up
	// or down a line. It is computed before the body so every screen can
	// size its window to the height that actually remains after the status
	// rows are removed — otherwise RenderFrame clips the bottom hints.
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

	bodyDims := dims
	bodyDims.BodyHeight = max(5, dims.BodyHeight-len(statusRows))
	// The downloads projection is O(total completed downloads); collect it
	// once per frame and share it with the body and footer renderers.
	var downloads *DownloadsData
	if m.activeView == viewDownloads && !m.showHelp && !m.animeStatusPickerOpen {
		data := m.collectDownloadsData(bodyDims.ContentWidth, bodyDims.BodyHeight, accent)
		downloads = &data
	}
	var body string
	if m.showHelp {
		body = m.renderHelpOverlayContent(dims.ContentWidth, accent)
	} else if m.animeStatusPickerOpen {
		body = m.renderAnimeStatusOverlayContent(dims.ContentWidth, accent)
	} else {
		body = m.renderScreenBodyWithDownloads(bodyDims, accent, downloads)
	}

	// Footer
	bindings := m.activeKeyBindingsWithDownloads(downloads)
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
		return []string{"kari", "reading", strings.ToLower(title)}
	case viewMangaSources:
		title := "sources"
		if m.selectedSeries != nil && m.selectedSeries.Title != "" {
			title = m.selectedSeries.Title
		}
		return []string{"kari", "reading", strings.ToLower(title), "sources"}
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
	case viewCalendar:
		return []string{"kari", "calendar"}
	default:
		return []string{"kari"}
	}
}

// activeKeyBindings is the standalone form used by tests and callers without
// a prepared frame projection.
func (m *modelImpl) activeKeyBindings() []KeyBinding {
	return m.activeKeyBindingsWithDownloads(nil)
}

func (m *modelImpl) activeKeyBindingsWithDownloads(prepared *DownloadsData) []KeyBinding {
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
			if m.appMode == provider.ModeLive {
				return []KeyBinding{
					{Key: "enter", Action: "watch"},
					{Key: "space", Action: "search"},
					{Key: "/", Action: "filter"},
					{Key: "1/2/3", Action: "jump"},
					{Key: "r", Action: "refresh"},
					{Key: "tab/⇧tab", Action: "mode"},
					{Key: "?", Action: "help"},
					{Key: "esc", Action: "back"},
				}
			}
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
			{Key: "c", Action: "calendar"},
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
	case viewMangaSources:
		return []KeyBinding{
			{Key: "↑↓ j k", Action: "choose provider"},
			{Key: "enter", Action: "open title"},
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
		if m.subtitlePickerOpen {
			return []KeyBinding{
				{Key: "↑/↓", Action: "move"},
				{Key: "enter/space", Action: "select"},
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
		data := prepared
		if data == nil {
			collected := m.collectDownloadsData(m.width, m.height, m.currentAccent())
			data = &collected
		}
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
	case viewCalendar:
		return calendarFooterHints()
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

// renderScreenBodyWithDownloads renders the active screen. A prepared
// downloads projection is reused from the footer when available.
func (m *modelImpl) renderScreenBodyWithDownloads(dims Dims, accent lipgloss.AdaptiveColor, prepared *DownloadsData) string {
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
	case viewMangaSources:
		return m.renderMangaSources(dims.ContentWidth)
	case viewPreview:
		return RenderPreviewScreen(m.collectPreviewData(dims.ContentWidth, dims.BodyHeight, accent))

	case viewHistory:
		return RenderHistoryScreen(m.collectHistoryData(dims.ContentWidth, dims.BodyHeight, accent))

	case viewSettings:
		return RenderSettingsScreen(m.collectSettingsData(dims.ContentWidth, dims.BodyHeight))

	case viewDownloads:
		data := prepared
		if data == nil {
			collected := m.collectDownloadsData(dims.ContentWidth, dims.BodyHeight, accent)
			data = &collected
		}
		return RenderDownloadsScreen(*data)
	case viewCalendar:
		return RenderCalendarScreen(m.collectCalendarData(dims.ContentWidth, dims.BodyHeight, accent))

	default:
		return ""
	}
}
func (m *modelImpl) renderHelpOverlayContent(width int, accent lipgloss.AdaptiveColor) string {
	return m.renderHelpOverlayContentAtScroll(width, accent, max(0, m.helpScroll))
}

// renderHelpOverlayContentAtScroll renders the help box at an explicit scroll
// offset so callers can measure the unscrolled content independently of the
// current scroll position.
func (m *modelImpl) renderHelpOverlayContentAtScroll(width int, accent lipgloss.AdaptiveColor, scroll int) string {
	screenName := "Global"
	var contextKeys []KeyBinding

	switch m.activeView {
	case viewSearch:
		if len(m.seriesResults) > 0 {
			if m.appMode == provider.ModeLive {
				screenName = "Live guide"
				contextKeys = []KeyBinding{
					{Key: "enter", Action: "watch channel"},
					{Key: "↑↓ j k", Action: "move through guide"},
					{Key: "1/2/3", Action: "jump live/upcoming/channels"},
					{Key: "r", Action: "refresh live guide"},
					{Key: "/", Action: "filter channels"},
					{Key: "space", Action: "new search"},
					{Key: "tab/⇧tab", Action: "cycle mode"},
				}
				break
			}
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
				{Key: "c", Action: "calendar"},
			}
		}
	case viewMangaSources:
		screenName = "Manga sources"
		contextKeys = []KeyBinding{
			{Key: "↑↓ j k", Action: "choose provider"},
			{Key: "enter", Action: "open title"},
			{Key: "esc", Action: "back to results"},
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
			{Key: "u", Action: "sync completed history to connected accounts"},
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
	case viewCalendar:
		screenName = "Calendar"
		contextKeys = calendarKeyHints()
	}

	globalKeys := []KeyBinding{
		{Key: "esc", Action: "back / close"},
		{Key: "?", Action: "help overlay"},
		{Key: "h", Action: "history"},
		{Key: "s", Action: "settings"},
		{Key: "d", Action: "downloads"},
		{Key: "c", Action: "calendar"},
		{Key: "ctrl+c", Action: "force quit"},
		{Key: "q", Action: "quit (2× if downloading)"},
	}
	return RenderHelpOverlay(HelpData{
		ScreenName:  screenName,
		ContextKeys: contextKeys,
		GlobalKeys:  globalKeys,
		Width:       width,
		Scroll:      scroll,
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
