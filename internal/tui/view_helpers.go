package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/lang"
	"kari/internal/model"
	"kari/internal/service"
	"kari/internal/termimg"
)

func (m *modelImpl) View() string {
	var output string
	if m.showHelp {
		output = m.renderHelpOverlay()
	} else {
		output = m.renderMainView()
	}

	// Kitty images are persistent overlays, not part of the normal text
	// flow — anything that stops showing one (leaving its screen, but also
	// the help overlay or a confirm dialog covering it in place) doesn't
	// remove it on its own, since whatever renders instead never mentions
	// that image again. So this always runs last, against whatever the
	// actual final output is, and deletes a slot's placement whenever this
	// frame doesn't show it (a harmless no-op once nothing is left to
	// delete) — computing it from the header/body building above and
	// hoping every branch remembers to include it is exactly how the help
	// overlay and the preview confirm dialog ended up skipping it before.
	if m.imgProtocol == termimg.ProtocolKitty {
		var cleanup strings.Builder
		if !m.searchPosterVisible() {
			cleanup.WriteString(termimg.DeleteKitty(kittySearchImageID))
		}
		if !m.previewPosterVisible() {
			cleanup.WriteString(termimg.DeleteKitty(kittyPreviewImageID))
		}
		output = cleanup.String() + output
	}

	return output
}

// searchPosterVisible/previewPosterVisible report whether this frame will
// actually show that slot's poster — every state that hides it (switching
// screens, the help overlay, a confirm dialog covering the screen) needs to
// be listed here so the Kitty cleanup above knows to clear it.
func (m *modelImpl) searchPosterVisible() bool {
	return m.activeView == viewSearch && !m.showHelp
}

func (m *modelImpl) previewPosterVisible() bool {
	return m.activeView == viewPreview && !m.showHelp && !m.confirmCompletion
}

func (m *modelImpl) renderMainView() string {
	dims := m.computeLayoutDims()

	header := m.renderHeader(dims)
	rule := m.renderRule(dims.contentW)
	body := m.renderBody(dims)
	footer := m.renderFooter(dims)

	rows := []string{
		header,
		rule,
		"",
		body,
		"",
	}
	if m.loading {
		rows = append(rows, m.renderLoadingLine(dims.contentW))
	}
	if statusLine := m.renderStatusLine(dims.contentW); statusLine != "" {
		rows = append(rows, statusLine)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	contentHeight := lipgloss.Height(content)

	// Calculate how many empty lines are needed to push the footer to the bottom
	gap := m.height - contentHeight - lipgloss.Height(footer)
	if gap < 0 {
		gap = 0
	}

	// Add empty lines as a gap
	spacer := strings.Repeat("\n", gap)
	finalContent := content + spacer + "\n" + footer

	if m.width > dims.contentW+4 {
		finalContent = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Top, finalContent)
	}

	return finalContent
}

func (m *modelImpl) renderRule(width int) string {
	return lipgloss.NewStyle().Foreground(colorBorder).Render(strings.Repeat("─", width))
}

func (m *modelImpl) renderHeader(dims layoutDims) string {
	breadcrumb := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("Kari") +
		mutedStyle.Render(" › ") +
		mutedStyle.Render(m.activeViewLabel())

	// player := ""
	// if name := m.selectedPlayerName(); name != "" {
	// 	player = mutedStyle.Render(strings.ToUpper(name))
	// }

	return sideBySide(breadcrumb, "", dims.contentW)
}

func (m *modelImpl) renderFooter(dims layoutDims) string {
	bindings := m.shortHelpBindings()
	var bindParts []string
	for _, b := range bindings {
		pair := keyStyle.Render(b.Help().Key) + " " + mutedStyle.Render(b.Help().Desc)
		bindParts = append(bindParts, pair)
	}

	// footerContent := mutedStyle.Render(string(m.appMode)) + "  ·  " + strings.Join(bindParts, "  ")
	footerContent := strings.Join(bindParts, "  ")
	return lipgloss.NewStyle().Width(dims.contentW).Align(lipgloss.Center).Render(footerContent)
}

func (m *modelImpl) renderLoadingLine(width int) string {
	spinner := infoStyle.Render(m.spinner.View())
	text := sectionTitleStyle.Render(m.loadingText)
	line := spinner + " " + text
	return lipgloss.Place(width, 1, lipgloss.Center, lipgloss.Center, line)
}

func (m *modelImpl) renderStatusLine(width int) string {
	if m.statusText == "" {
		return ""
	}

	var style lipgloss.Style
	switch m.statusType {
	case statusSuccess:
		style = lipgloss.NewStyle().Foreground(colorSuccess)
	case statusWarn:
		style = lipgloss.NewStyle().Foreground(colorWarn)
	case statusError:
		style = lipgloss.NewStyle().Foreground(colorError)
	default:
		style = lipgloss.NewStyle().Foreground(colorInfo)
	}

	// Truncate to avoid breaking layout
	text := m.statusText
	if lipgloss.Width(text) > width-4 {
		text = shorten(text, width-4)
	}

	return lipgloss.Place(width, 1, lipgloss.Center, lipgloss.Center, style.Render(text))
}

func (m *modelImpl) activeViewLabel() string {
	switch m.activeView {
	case viewSearch:
		return "Search"
	case viewEpisodes:
		return "Episodes"
	case viewPreview:
		return "Preview"
	case viewHistory:
		return "History"
	case viewSettings:
		return "Settings"
	default:
		return "Kari"
	}
}

func (m *modelImpl) renderBody(dims layoutDims) string {
	switch m.activeView {
	case viewSearch:
		return m.renderSearchScreen(dims)
	case viewEpisodes:
		return m.renderEpisodesScreen(dims)
	case viewPreview:
		return m.renderPreviewScreen(dims)
	case viewHistory:
		return m.renderHistoryScreen(dims)
	case viewSettings:
		return m.renderSettingsScreen(dims)
	default:
		return ""
	}
}

func (m *modelImpl) renderHistoryScreen(dims layoutDims) string {
	rows := []string{
		sectionTitleStyle.Render("Watch History"),
		mutedStyle.Render(fmt.Sprintf("%d titles", len(m.historyList.Items()))),
		"",
	}

	if len(m.historyList.Items()) == 0 {
		rows = append(rows, mutedStyle.Render("No watch history yet."))
	} else {
		rows = append(rows, mutedStyle.Render("/ to filter  ·  d delete  ·  D clear all"), "")
		rows = append(rows, m.historyList.View())
	}

	if m.confirmDelete {
		return m.renderConfirmDialog("Delete this title from history?", dims)
	}
	if m.confirmClearHistory {
		return m.renderConfirmDialog("Clear ALL history?", dims)
	}

	return strings.Join(rows, "\n")
}

func (m *modelImpl) renderConfirmDialog(title string, dims layoutDims) string {
	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(1, 2).
		Width(40).
		Align(lipgloss.Center).
		Render(fmt.Sprintf("%s\n\n[Y] Yes    [N] No", title))

	return lipgloss.Place(dims.contentW, 10, lipgloss.Center, lipgloss.Center, dialog)
}

// searchLeftWidth is the width of the search screen's left (results) column
// — shared with resizeLists so the list's own delegate truncates titles to
// the width it's actually rendered at, instead of the full content width.
// Without this, a long title fits the list's assumed width, skips
// truncation, and gets hard-wrapped instead by the Width() applied when
// this column is boxed in later, breaking the list's row alignment.
func searchLeftWidth(contentW int) int {
	if contentW <= 90 {
		return contentW
	}
	return contentW * 65 / 100
}

func (m *modelImpl) renderSearchScreen(dims layoutDims) string {
	if dims.contentW <= 90 {
		return m.renderSearchLeft(dims.contentW)
	}

	leftW := searchLeftWidth(dims.contentW)
	rightW := dims.contentW - leftW - 2
	leftCol := lipgloss.NewStyle().Width(leftW).Render(m.renderSearchLeft(leftW))
	rightCol := lipgloss.NewStyle().Width(rightW).Render(m.renderSearchRight())

	// The poster is joined on below the (already width-fixed) text column
	// rather than included inside renderSearchRight before wrapping: lipgloss
	// word-wraps content that doesn't fit a Style's Width, and since each
	// image row is one long space-free escape sequence, a wrap mid-row would
	// slice straight through it and corrupt the rest of the frame.
	// JoinVertical/JoinHorizontal only pad blocks to align them — they never
	// wrap — so this is safe regardless of terminal width.
	if block := posterBlock(m.searchPoster, m.searchPosterUnavailable, m.imgProtocol, kittySearchImageID); block != "" {
		rightCol = lipgloss.JoinVertical(lipgloss.Left, rightCol, "", block)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  ", rightCol)
}

// posterBlock returns rendered (the image) if present, a muted "no image
// available" line once a fetch has definitively failed or found no artwork,
// or "" while still loading — callers should omit the block entirely in
// that last case rather than reserving space for it, to avoid a layout
// jump when the fetch resolves.
func posterBlock(rendered string, unavailable bool, protocol termimg.Protocol, imageID uint32) string {
	if rendered != "" {
		return rendered
	}

	// Whenever this slot has no image to show — cleared results, a mode
	// switch, a fetch that hasn't finished yet, or one that failed — any
	// previous Kitty placement in it needs to be explicitly deleted too.
	// Unlike text, it doesn't just get overwritten by whatever renders in
	// its place next; nothing here even mentions that image again unless we
	// say so, so without this it keeps showing the last thing it had.
	var cleanup string
	if protocol == termimg.ProtocolKitty {
		cleanup = termimg.DeleteKitty(imageID)
	}

	if unavailable {
		return cleanup + mutedStyle.Render("No image available")
	}
	return cleanup
}

func (m *modelImpl) renderSearchLeft(width int) string {
	rows := []string{
		sectionTitleStyle.Render("Search"),
		"",
		m.queryInput.View(),
		"",
	}

	if len(m.seriesResults) == 0 {
		if m.historyStore != nil {
			all := m.historyStore.All()
			if len(all) > 0 {
				last := all[0]
				lastPlayed := fmt.Sprintf("Last: %s · %s", last.Title, relativeTime(last.WatchedAt))
				rows = append(rows, mutedStyle.Render(lastPlayed)+"  "+mutedStyle.Render("[H] history"), "")
			}
		}
		rows = append(rows, mutedStyle.Render("No results yet — type a query and press Enter"))
	} else {
		count := fmt.Sprintf("%d results", len(m.seriesResults))
		rows = append(rows, mutedStyle.Render(count)+"  "+mutedStyle.Render("/ to filter"), "")
		rows = append(rows, m.seriesList.View())
	}

	return strings.Join(rows, "\n")
}

func (m *modelImpl) renderSearchRight() string {
	var rows []string

	rows = append(rows, sectionTitleStyle.Render("Modes"), "")
	for _, mode := range m.modes {
		if mode == m.appMode {
			rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("● "+strings.ToUpper(string(mode))))
		} else {
			rows = append(rows, mutedStyle.Render("○ "+strings.ToUpper(string(mode))))
		}
	}

	rows = append(rows, "", sectionTitleStyle.Render("Players"), "")
	for _, p := range m.availablePlayers {
		if p == m.selectedPlayerName() {
			rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("● "+strings.ToUpper(p)))
		} else {
			rows = append(rows, mutedStyle.Render("○ "+strings.ToUpper(p)))
		}
	}

	return lipgloss.NewStyle().PaddingLeft(2).Render(strings.Join(rows, "\n"))
}

func (m *modelImpl) renderEpisodesScreen(dims layoutDims) string {
	seriesTitle := "Episodes"
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
	}

	selCount := len(m.selectedEpisodes)
	selInfo := ""
	if selCount > 0 {
		selInfo = lipgloss.NewStyle().Foreground(colorPrimary).Render(fmt.Sprintf(" · %d selected — [D]ownload", selCount))
	}

	rows := []string{
		mutedStyle.Render("← ") + sectionTitleStyle.Render(shorten(seriesTitle, dims.contentW-12)) + selInfo,
		mutedStyle.Render(fmt.Sprintf("%d episodes", len(m.episodeResults))),
		"",
	}

	if len(m.episodeResults) == 0 {
		rows = append(rows, mutedStyle.Render("No episodes available."))
	} else {
		rows = append(rows, mutedStyle.Render("space toggle · ctrl+a all · ctrl+d none  ·  / to filter  ·  g/G first/last"), "")
		rows = append(rows, m.episodeList.View())
	}

	return strings.Join(rows, "\n")
}

func (m *modelImpl) renderPreviewScreen(dims layoutDims) string {
	if m.resolved == nil {
		if m.selectedEpisode != nil {
			// Show metadata even if not yet resolved
			width := dims.contentW
			if width > 90 {
				width = 90
			}

			badges := []string{preparingBadge}
			if m.selectedEpisode.Filler {
				badges = append(badges, fillerBadge())
			}

			rows := []string{
				lipgloss.JoinHorizontal(lipgloss.Top, badges...),
				"",
			}

			title := ""
			if m.selectedSeries != nil {
				title = m.selectedSeries.Title
			}
			rows = append(rows, lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(shorten(title, width-6)))

			if m.selectedEpisode.Season > 0 || m.selectedEpisode.Number > 0 {
				rows = append(rows, mutedStyle.Render(fmt.Sprintf("Season %d  ·  Episode %d", m.selectedEpisode.Season, m.selectedEpisode.Number)))
			}

			cleanedTitle := cleanEpisodeTitle(m.selectedEpisode.Title, title)
			if cleanedTitle != "" {
				rows = append(rows, mutedStyle.Render(shorten(cleanedTitle, width-6)))
			}

			if m.selectedSeries != nil && m.selectedSeries.Provider != "" {
				rows = append(rows, "", mutedStyle.Render("via ")+lipgloss.NewStyle().Foreground(colorInfo).Render(m.selectedSeries.Provider))
			}

			return lipgloss.Place(dims.contentW, m.height/2, lipgloss.Center, lipgloss.Center, cardStyle.Width(width).Render(strings.Join(rows, "\n")))
		}
		return mutedStyle.Render("No media selected")
	}

	if m.confirmCompletion {
		return m.renderConfirmDialog("Did you finish this episode?", dims)
	}

	// The poster is joined in as its own block (never passed through a
	// lipgloss Style with Width() set) because lipgloss word-wraps content
	// that doesn't fit a Style's Width, and since each image row is one long
	// space-free escape sequence, a wrap mid-row would slice straight
	// through it and corrupt the rest of the frame. JoinVertical/
	// JoinHorizontal only pad blocks to align them — they never wrap — so
	// composing everything with those is what keeps this safe.
	previewBlock := posterBlock(m.previewPoster, m.previewPosterUnavailable, m.imgProtocol, kittyPreviewImageID)

	header := m.renderPreviewHeader(dims.contentW, previewBlock)
	controls := m.renderPreviewControlsRow(dims.contentW)
	return lipgloss.JoinVertical(lipgloss.Left, header, "", m.renderRule(dims.contentW), "", controls)
}

// renderPreviewHeader lays the poster and the title/metadata info out side
// by side (poster on the left) when there's room, so the space to the
// poster's right is used instead of sitting empty above a separate info
// card. It falls back to stacking them when the terminal's too narrow for
// that to be legible.
func (m *modelImpl) renderPreviewHeader(width int, imageBlock string) string {
	info := m.renderPreviewInfo(width)

	if m.previewPoster == "" {
		if imageBlock == "" {
			return info
		}
		// imageBlock still carries invisible Kitty cleanup, or a visible
		// "no image available" line — either way it belongs above info.
		return lipgloss.JoinVertical(lipgloss.Left, imageBlock, info)
	}

	if width < 70 {
		return lipgloss.JoinVertical(lipgloss.Left, imageBlock, "", info)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, imageBlock, "  ", info)
}

// renderPreviewInfo renders the title/badges/metadata text block. It's
// plain text with no bordered card around it (unlike the old design):
// boxing it made sense when it was the only thing in the left column, but
// sitting next to a poster a heavy border reads as a disconnected floating
// panel rather than part of one cohesive header.
func (m *modelImpl) renderPreviewInfo(totalWidth int) string {
	r := m.resolved
	mediaType := r.MediaType
	if mediaType == "" {
		mediaType = "unknown"
	}

	width := totalWidth
	if m.previewPoster != "" {
		width -= previewPosterMaxCols + 4
	}
	if width < 24 {
		width = 24
	}

	badges := []string{modeBadge(mediaType)}
	if m.selectedEpisode != nil && m.selectedEpisode.Filler {
		badges = append(badges, fillerBadge())
	}

	rows := []string{
		lipgloss.JoinHorizontal(lipgloss.Top, badges...),
		"",
	}

	primaryTitle := r.SeriesTitle
	if primaryTitle == "" {
		primaryTitle = r.EpisodeTitle
	}
	rows = append(rows, lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(shorten(primaryTitle, width)))

	if r.StartTime > 5 {
		resumeTime := formatDuration(r.StartTime)
		rows = append(rows, infoStyle.Render(fmt.Sprintf("󰐊 Resume at %s", resumeTime)))
	}

	if r.MediaType != "movie" {
		if r.SeasonNumber > 0 || r.EpisodeNumber > 0 {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("Season %d  ·  Episode %d", r.SeasonNumber, r.EpisodeNumber)))
		}

		cleanedTitle := cleanEpisodeTitle(r.EpisodeTitle, primaryTitle)
		if cleanedTitle != "" {
			rows = append(rows, mutedStyle.Render(shorten(cleanedTitle, width)))
		}
	}

	// r.Subtitles holds the raw multi-language candidate pool while a fetch
	// is still in flight (Path is empty until one is actually downloaded and
	// selected) — only ever render tracks that have finished downloading, so
	// the user never sees every available language flash by as if all were
	// selected.
	var downloadedSubs []model.SubtitleTrack
	for _, sub := range r.Subtitles {
		if sub.Path != "" {
			downloadedSubs = append(downloadedSubs, sub)
		}
	}
	switch {
	case len(downloadedSubs) > 0:
		rows = append(rows, "", mutedStyle.Render("Subtitles"))
		for _, sub := range downloadedSubs {
			rows = append(rows, lipgloss.NewStyle().Foreground(colorSuccess).Render("✓ "+sub.Label))
		}
	case m.subtitleOpID != 0:
		rows = append(rows, "", mutedStyle.Render("Fetching subtitles..."))
	default:
		rows = append(rows, "", mutedStyle.Render("No subtitles"))
	}

	var meta []string
	if len(m.previewGenres) > 0 {
		meta = append(meta, strings.Join(m.previewGenres, " · "))
	}
	if m.previewRating != "" {
		meta = append(meta, m.previewRating)
	}
	if len(meta) > 0 {
		rows = append(rows, "", mutedStyle.Render(shorten(strings.Join(meta, "   "), width)))
	}

	if m.previewOverview != "" {
		overviewWidth := width
		if overviewWidth > 100 {
			overviewWidth = 100
		}
		rows = append(rows, "", mutedStyle.Render(wrapWithEllipsis(m.previewOverview, overviewWidth, previewOverviewMaxLines)))
	}

	return strings.Join(rows, "\n")
}

// previewOverviewMaxLines caps how many lines the plot summary can take —
// otherwise a long TMDB/AniList description can push everything below it
// (the divider, Source/Players/Actions) far enough down to scroll the
// screen and hide the header above.
const previewOverviewMaxLines = 4

// wrapWithEllipsis word-wraps text to width, truncating to at most maxLines
// with a trailing "..." if it would otherwise run longer.
func wrapWithEllipsis(text string, width, maxLines int) string {
	lines := wrapText(text, width)
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}

	lines = lines[:maxLines]
	last := lines[maxLines-1]
	for lipgloss.Width(last)+3 > width && last != "" {
		last = strings.TrimRight(last[:len(last)-1], " ")
	}
	lines[maxLines-1] = last + "..."
	return strings.Join(lines, "\n")
}

func wrapText(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	lines := make([]string, 0, len(words)/8+1)
	cur := words[0]
	for _, word := range words[1:] {
		candidate := cur + " " + word
		if lipgloss.Width(candidate) > width {
			lines = append(lines, cur)
			cur = word
			continue
		}
		cur = candidate
	}
	return append(lines, cur)
}

func (m *modelImpl) renderSettingsScreen(dims layoutDims) string {
	rows := []string{
		sectionTitleStyle.Render("Settings"),
		"",
	}

	// Trakt.tv section
	traktStatus := mutedStyle.Render("○ Not connected")
	if m.traktClient != nil && m.traktClient.IsAuthenticated() {
		traktStatus = lipgloss.NewStyle().Foreground(colorSuccess).Render("● Connected")
	}
	if m.traktAuthCode != "" {
		traktStatus = lipgloss.NewStyle().Foreground(colorWarn).Render("● Waiting for auth...")
	}

	traktStyle := lipgloss.NewStyle().PaddingLeft(2)
	if m.settingsIndex == 0 {
		traktStyle = traktStyle.BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(colorPrimary)
	}

	rows = append(rows, "Trakt.tv")
	rows = append(rows, traktStyle.Render(fmt.Sprintf("Status: %s\n[C] Connect    [R] Revoke", traktStatus)))
	if m.traktAuthCode != "" {
		rows = append(rows, traktStyle.Render(fmt.Sprintf("\nGo to: %s\nEnter code: %s", m.traktAuthURL, m.traktAuthCode)))
	}
	rows = append(rows, "")

	// AniList section
	anilistStatus := mutedStyle.Render("○ Not connected")
	if m.anilistClient != nil && m.anilistClient.IsAuthenticated() {
		anilistStatus = lipgloss.NewStyle().Foreground(colorSuccess).Render("● Connected")
	}

	anilistStyle := lipgloss.NewStyle().PaddingLeft(2)
	if m.settingsIndex == 1 {
		anilistStyle = anilistStyle.BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(colorPrimary)
	}

	rows = append(rows, "AniList")
	rows = append(rows, anilistStyle.Render(fmt.Sprintf("Status: %s\n[C] Connect    [R] Revoke", anilistStatus)))

	if m.anilistAuthURL != "" {
		rows = append(rows, anilistStyle.Render("\nA browser window should have opened."))
		rows = append(rows, anilistStyle.Render("If not, go to: "+m.anilistAuthURL))
		rows = append(rows, anilistStyle.Render("\nPaste the code here and press Enter:"))
		rows = append(rows, anilistStyle.Render(m.authInput.View()))
	}

	rows = append(rows, "")

	// Quality section
	qualityStyle := lipgloss.NewStyle().PaddingLeft(2)
	if m.settingsIndex == 2 {
		qualityStyle = qualityStyle.BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(colorPrimary)
	}

	allMarker, highestMarker, dataSaverMarker, lowestMarker := "○", "○", "○", "○"
	switch m.qualityMode {
	case qualityAll:
		allMarker = "●"
	case qualityHighest:
		highestMarker = "●"
	case qualityDataSaver:
		dataSaverMarker = "●"
	case qualityLowest:
		lowestMarker = "●"
	}

	modeColor := lipgloss.NewStyle().Foreground(colorPrimary).Render

	rows = append(rows, "Quality")
	qualityLine := fmt.Sprintf(
		"%s All    %s Highest    %s Data Saver    %s Lowest",
		modeColor(allMarker), modeColor(highestMarker), modeColor(dataSaverMarker), modeColor(lowestMarker),
	)
	rows = append(rows, qualityStyle.Render(shorten(qualityLine, dims.contentW-4)))
	rows = append(rows, qualityStyle.Render(mutedStyle.Render("[←] [→] to change")))
	rows = append(rows, "")

	// Language section
	languages := m.availableLanguages()
	langStyle := lipgloss.NewStyle().PaddingLeft(2)
	if m.settingsIndex == 3 {
		langStyle = langStyle.BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(colorPrimary)
	}

	enabledCount := 0
	for _, code := range languages {
		if m.languageEnabled(code) {
			enabledCount++
		}
	}
	rows = append(rows, fmt.Sprintf("Languages (MovieBox only) · %d/%d enabled", enabledCount, len(languages)))
	if len(languages) == 0 {
		rows = append(rows, langStyle.Render(mutedStyle.Render("No languages configured")))
	} else {
		entries := make([]string, len(languages))
		for i, code := range languages {
			marker := "○"
			if m.languageEnabled(code) {
				marker = "●"
			}
			text := marker + " " + movieboxLanguageLabel(code)
			switch {
			case m.settingsIndex == 3 && i == m.languageIndex:
				text = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(text)
			case m.languageEnabled(code):
				text = textStyle.Render(text)
			default:
				text = mutedStyle.Render(text)
			}
			entries[i] = text
		}
		// Flowed into a wrapping grid rather than one language per line —
		// with 11 MovieBox languages, a single column was a lot of vertical
		// space for what's really just a set of toggles.
		for _, line := range wrapEntries(entries, dims.contentW-4, "   ") {
			rows = append(rows, langStyle.Render(line))
		}
		rows = append(rows, langStyle.Render(mutedStyle.Render("[←] [→] navigate    [space] toggle")))
	}
	rows = append(rows, "")

	// Subtitle language section
	subLangStyle := lipgloss.NewStyle().PaddingLeft(2)
	if m.settingsIndex == 4 {
		subLangStyle = subLangStyle.BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(colorPrimary)
	}

	rows = append(rows, "Subtitle Language")
	rows = append(rows, subLangStyle.Render(lipgloss.NewStyle().Foreground(colorPrimary).Render(lang.Name(m.subtitleLanguage))))
	rows = append(rows, subLangStyle.Render(mutedStyle.Render("[←] [→] to change")))
	rows = append(rows, subLangStyle.Render(mutedStyle.Render("Preferred when a provider or OpenSubtitles offers more than one language")))
	rows = append(rows, "")

	return strings.Join(rows, "\n")
}

func (m *modelImpl) hasEnabledLanguage() bool {
	for _, lang := range m.availableLanguages() {
		if m.languageEnabled(lang) {
			return true
		}
	}
	return false
}

// movieboxLanguages is the hardcoded list of all languages the MovieBox API can return.
// Derived from testing across 17 movies and 12 TV series.
var movieboxLanguages = []string{
	"Original",
	"English",
	"English sub",
	"Bengali",
	"esla",
	"Hindi",
	"Kannada",
	"Malayalam",
	"ptbr",
	"Tamil",
	"Telugu",
}

// movieboxLanguageNames gives a couple of the cryptic MovieBox codes a
// readable label; anything not listed here is already readable as-is
// (e.g. "Hindi", "Tamil").
var movieboxLanguageNames = map[string]string{
	"esla": "Spanish (LatAm)",
	"ptbr": "Portuguese (BR)",
}

func movieboxLanguageLabel(code string) string {
	if name, ok := movieboxLanguageNames[code]; ok {
		return name
	}
	return code
}

// wrapEntries flows already-rendered entries (may contain ANSI styling)
// into lines no wider than width, joining same-line entries with gap —
// the same word-wrap shape as wrapText, but for a list of discrete chips
// instead of a paragraph of words.
func wrapEntries(entries []string, width int, gap string) []string {
	if len(entries) == 0 {
		return nil
	}
	gapWidth := lipgloss.Width(gap)

	var lines []string
	cur := entries[0]
	curWidth := lipgloss.Width(cur)
	for _, e := range entries[1:] {
		w := lipgloss.Width(e)
		if curWidth+gapWidth+w > width {
			lines = append(lines, cur)
			cur = e
			curWidth = w
			continue
		}
		cur += gap + e
		curWidth += gapWidth + w
	}
	return append(lines, cur)
}

func (m *modelImpl) availableLanguages() []string {
	return movieboxLanguages
}

func cleanEpisodeTitle(epTitle, seriesTitle string) string {
	if epTitle == "" {
		return ""
	}

	// Remove series title from beginning (case insensitive)
	lowerEp := strings.ToLower(epTitle)
	lowerSeries := strings.ToLower(seriesTitle)
	if seriesTitle != "" && strings.HasPrefix(lowerEp, lowerSeries) {
		epTitle = epTitle[len(seriesTitle):]
	}
	epTitle = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(epTitle), "-"))

	// Remove standard season/episode prefixes
	re1 := regexp.MustCompile(`(?i)^(?:season\s*\d+)?\s*(?:episode|ep)\s*\d+\s*-?\s*`)
	re2 := regexp.MustCompile(`(?i)^s\d+e\d+\s*-?\s*`)
	epTitle = re1.ReplaceAllString(epTitle, "")
	epTitle = re2.ReplaceAllString(epTitle, "")
	epTitle = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(epTitle), "-"))

	return epTitle
}

func (m *modelImpl) languageEnabled(lang string) bool {
	if m.languageFilter == nil || lang == "" {
		return true
	}
	enabled, configured := m.languageFilter[lang]
	return !configured || enabled
}

func (m *modelImpl) filteredPlayback() []int {
	if m.resolved == nil {
		return nil
	}
	if len(m.resolved.Playback) == 0 {
		return nil
	}
	return service.FilterPlaybackIndices(m.resolved.Playback, m.qualityMode, m.languageFilter)
}

// renderPreviewControlsRow lays Source, Players (+Autoplay), and Actions out
// as three side-by-side columns spanning the full width instead of one tall
// column pinned to the right with the rest of the screen sitting empty
// beside it, falling back to a stacked single column on narrow terminals.
func (m *modelImpl) renderPreviewControlsRow(width int) string {
	filtered := m.filteredPlayback()
	if len(filtered) == 0 {
		return mutedStyle.Render("No sources available")
	}

	sourceW := width * 44 / 100
	playersW := width * 26 / 100
	actionsW := width - sourceW - playersW - 4
	if width < 90 {
		sourceW, playersW, actionsW = width, width, width
	}

	source := m.renderSourceColumn(filtered, sourceW)
	players := m.renderPlayersColumn()
	actions := m.renderActionsColumn()

	if width < 90 {
		return lipgloss.JoinVertical(lipgloss.Left, source, "", players, "", actions)
	}

	cols := []string{
		lipgloss.NewStyle().Width(sourceW).Render(source),
		lipgloss.NewStyle().Width(playersW).Render(players),
		lipgloss.NewStyle().Width(actionsW).Render(actions),
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols[0], "  ", cols[1], "  ", cols[2])
}

// sourceSplitThreshold is how many playback sources trigger splitting the
// list into two side-by-side columns instead of one tall one — otherwise a
// title with a dozen quality/language variants makes Source towers over
// Players and Actions next to it.
const sourceSplitThreshold = 8

func (m *modelImpl) renderSourceColumn(filtered []int, width int) string {
	r := m.resolved
	items := make([]string, 0, len(filtered))
	for _, actualIdx := range filtered {
		src := r.Playback[actualIdx]
		label := src.Label
		if strings.TrimSpace(label) == "" {
			label = "Unknown"
		}
		if actualIdx == m.selectedPlayback {
			items = append(items, lipgloss.NewStyle().
				Foreground(colorPrimary).
				BorderLeft(true).
				BorderStyle(lipgloss.ThickBorder()).
				BorderForeground(colorPrimary).
				PaddingLeft(1).
				Render("● "+label))
		} else {
			items = append(items, mutedStyle.Render("  ○ "+label))
		}
	}

	rows := []string{sectionTitleStyle.Render("Source"), "", layoutSourceItems(items, width), "", mutedStyle.Render("tab / shift+tab to switch")}
	return strings.Join(rows, "\n")
}

// layoutSourceItems splits items into two side-by-side columns once there
// are more than sourceSplitThreshold of them and width can actually fit two
// columns without wrapping the longer labels (e.g. "[MOVIEBOX] 1080p
// Hindi") — otherwise it's just one column, same as before.
func layoutSourceItems(items []string, width int) string {
	itemW := 0
	for _, it := range items {
		if w := lipgloss.Width(it); w > itemW {
			itemW = w
		}
	}

	if len(items) <= sourceSplitThreshold || itemW*2+2 > width {
		return strings.Join(items, "\n")
	}

	half := (len(items) + 1) / 2
	left := lipgloss.NewStyle().Width(itemW).Render(strings.Join(items[:half], "\n"))
	right := strings.Join(items[half:], "\n")
	return lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
}

func (m *modelImpl) renderPlayersColumn() string {
	r := m.resolved
	rows := []string{sectionTitleStyle.Render("Players"), ""}
	for _, p := range m.availablePlayers {
		if p == m.selectedPlayerName() {
			rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("● "+strings.ToUpper(p)))
		} else {
			rows = append(rows, mutedStyle.Render("○ "+strings.ToUpper(p)))
		}
	}
	rows = append(rows, "", mutedStyle.Render("[ctrl+p] to switch player"))

	if r.MediaType == "anime" || r.MediaType == "tv" || r.MediaType == "cartoon" {
		status := mutedStyle.Render("OFF")
		if m.autoplay {
			status = lipgloss.NewStyle().Foreground(colorSuccess).Render("ON")
		}
		rows = append(rows, "", sectionTitleStyle.Render("Autoplay"), "")
		rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("[A]")+"  "+textStyle.Render("Status: ")+status)
	}
	return strings.Join(rows, "\n")
}

func (m *modelImpl) renderActionsColumn() string {
	r := m.resolved
	rows := []string{sectionTitleStyle.Render("Actions"), ""}
	rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("[enter]")+"  "+textStyle.Render("Play"))
	if r.StartTime > 5 {
		rows = append(rows, lipgloss.NewStyle().Foreground(colorWarn).Render("[r]")+"      "+textStyle.Render("Restart"))
	}
	if m.canPlayNextEpisode() {
		rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("[n]")+"      "+textStyle.Render("Play next"))
	}
	rows = append(rows, lipgloss.NewStyle().Foreground(colorMuted).Render("[d]")+"      "+mutedStyle.Render("Download"))
	return strings.Join(rows, "\n")
}

func fillerBadge() string {
	return fillerBadgeStr
}

func (m *modelImpl) renderHelpOverlay() string {
	var sections []string

	sections = append(sections, sectionTitleStyle.Render("Navigation"), "")
	sections = append(sections,
		"  "+keyStyle.Render("↑/↓ j/k")+"   "+mutedStyle.Render("move"),
		"  "+keyStyle.Render("g/G")+"      "+mutedStyle.Render("top/bottom"),
		"  "+keyStyle.Render("esc")+"      "+mutedStyle.Render("back / cancel"),
		"  "+keyStyle.Render("ctrl+h")+"   "+mutedStyle.Render("home"),
		"  "+keyStyle.Render("q")+"        "+mutedStyle.Render("quit"),
	)

	sections = append(sections, "", sectionTitleStyle.Render("Search"), "")
	sections = append(sections,
		"  "+keyStyle.Render("space")+"    "+mutedStyle.Render("focus search"),
		"  "+keyStyle.Render("enter")+"    "+mutedStyle.Render("search / select"),
		"  "+keyStyle.Render("tab")+"      "+mutedStyle.Render("switch mode"),
		"  "+keyStyle.Render("/")+"        "+mutedStyle.Render("filter results"),
	)

	sections = append(sections, "", sectionTitleStyle.Render("Episodes"), "")
	sections = append(sections,
		"  "+keyStyle.Render("space")+"    "+mutedStyle.Render("toggle select"),
		"  "+keyStyle.Render("ctrl+a")+"   "+mutedStyle.Render("select all"),
		"  "+keyStyle.Render("ctrl+d")+"   "+mutedStyle.Render("deselect all"),
		"  "+keyStyle.Render("D")+"        "+mutedStyle.Render("batch download"),
		"  "+keyStyle.Render("a")+"        "+mutedStyle.Render("sub/dub"),
	)

	sections = append(sections, "", sectionTitleStyle.Render("Playback"), "")
	sections = append(sections,
		"  "+keyStyle.Render("enter/p")+"  "+mutedStyle.Render("play"),
		"  "+keyStyle.Render("n")+"        "+mutedStyle.Render("play next"),
		"  "+keyStyle.Render("r")+"        "+mutedStyle.Render("restart"),
		"  "+keyStyle.Render("A")+"        "+mutedStyle.Render("autoplay toggle"),
		"  "+keyStyle.Render("d")+"        "+mutedStyle.Render("download"),
		"  "+keyStyle.Render("tab")+"      "+mutedStyle.Render("switch source"),
		"  "+keyStyle.Render("ctrl+p")+"   "+mutedStyle.Render("switch player"),
	)

	sections = append(sections, "", sectionTitleStyle.Render("General"), "")
	sections = append(sections,
		"  "+keyStyle.Render("h")+"        "+mutedStyle.Render("history"),
		"  "+keyStyle.Render("s")+"        "+mutedStyle.Render("settings"),
		"  "+keyStyle.Render("x")+"        "+mutedStyle.Render("stop download"),
		"  "+keyStyle.Render("?")+"        "+mutedStyle.Render("toggle this help"),
	)

	content := strings.Join(sections, "\n")
	boxW := 48
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(1, 2).
		Width(boxW).
		Render(content)

	overlay := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box, lipgloss.WithWhitespaceChars(" "), lipgloss.WithWhitespaceForeground(lipgloss.Color("0")))

	return overlay
}
