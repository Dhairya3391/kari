package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/service"
)

func (m *modelImpl) View() string {
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

	if m.showHelp {
		return m.renderHelpOverlay()
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

func (m *modelImpl) renderSearchScreen(dims layoutDims) string {
	if dims.contentW <= 90 {
		return m.renderSearchLeft(dims.contentW)
	}

	leftW := dims.contentW * 65 / 100
	rightW := dims.contentW - leftW - 2
	return twoColumns(m.renderSearchLeft(leftW), m.renderSearchRight(), leftW, rightW)
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

	if dims.contentW <= 90 {
		leftW := dims.contentW
		card := m.renderPreviewCard(leftW)
		controls := m.renderPreviewControls(dims.contentW)
		return lipgloss.JoinVertical(lipgloss.Left, card, "", controls)
	}

	leftW := dims.contentW * 60 / 100
	rightW := dims.contentW - leftW - 2
	return twoColumns(m.renderPreviewCard(leftW), m.renderPreviewControls(rightW), leftW, rightW)
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

	rows = append(rows, "Languages (MovieBox only)")
	if len(languages) == 0 {
		rows = append(rows, langStyle.Render(mutedStyle.Render("No languages configured")))
	} else {
		for i, lang := range languages {
			marker := "○"
			if m.languageEnabled(lang) {
				marker = "●"
			}
			entry := marker + " " + lang
			if m.settingsIndex == 3 && i == m.languageIndex {
				entry = lipgloss.NewStyle().Foreground(colorPrimary).Render(entry)
			}
			rows = append(rows, langStyle.Render(entry))
		}
		rows = append(rows, langStyle.Render(mutedStyle.Render("[←] [→] navigate    [space] toggle")))
	}
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

func (m *modelImpl) renderPreviewCard(width int) string {
	r := m.resolved
	mediaType := r.MediaType
	if mediaType == "" {
		mediaType = "unknown"
	}

	badges := []string{modeBadge(mediaType)}
	if m.selectedEpisode != nil && m.selectedEpisode.Filler {
		badges = append(badges, fillerBadge())
	}

	rows := []string{
		lipgloss.JoinHorizontal(lipgloss.Top, badges...),
		"",
	}

	var primaryTitle string
	if r.MediaType == "movie" {
		primaryTitle = r.SeriesTitle
		if primaryTitle == "" {
			primaryTitle = r.EpisodeTitle
		}
	} else {
		primaryTitle = r.SeriesTitle
		if primaryTitle == "" {
			primaryTitle = r.EpisodeTitle
		}
	}
	rows = append(rows, lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(shorten(primaryTitle, width-6)))

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
			rows = append(rows, mutedStyle.Render(shorten(cleanedTitle, width-6)))
		}
	}

	rows = append(rows, "", mutedStyle.Render("via ")+lipgloss.NewStyle().Foreground(colorInfo).Render(r.Resolver))

	if len(r.Subtitles) > 0 {
		rows = append(rows, mutedStyle.Render("Subtitles"))
		for _, sub := range r.Subtitles {
			rows = append(rows, lipgloss.NewStyle().Foreground(colorSuccess).Render("✓ "+sub.Label))
		}
	} else {
		rows = append(rows, mutedStyle.Render("No subtitles"))
	}

	return cardStyle.Width(width).Render(strings.Join(rows, "\n"))
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

func (m *modelImpl) renderPreviewControls(width int) string {
	r := m.resolved
	var rows []string

	filtered := m.filteredPlayback()
	if len(filtered) == 0 {
		return mutedStyle.Render("No sources available")
	}

	rows = append(rows, sectionTitleStyle.Render("Source"), "")
	for _, actualIdx := range filtered {
		src := r.Playback[actualIdx]
		label := src.Label
		if strings.TrimSpace(label) == "" {
			label = "Unknown"
		}
		if actualIdx == m.selectedPlayback {
			rows = append(rows, lipgloss.NewStyle().
				Foreground(colorPrimary).
				BorderLeft(true).
				BorderStyle(lipgloss.ThickBorder()).
				BorderForeground(colorPrimary).
				PaddingLeft(1).
				Render("● "+label))
		} else {
			rows = append(rows, mutedStyle.Render("  ○ "+label))
		}
	}
	rows = append(rows, "", mutedStyle.Render("tab / shift+tab to switch"))

	rows = append(rows, "", sectionTitleStyle.Render("Players"), "")
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
		rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("[A]    ")+"  "+textStyle.Render("Status: ")+status)
	}

	rows = append(rows, "", sectionTitleStyle.Render("Actions"), "")
	rows = append(rows,
		lipgloss.NewStyle().Foreground(colorPrimary).Render("[enter]")+"  "+textStyle.Render("Play"),
	)
	if r.StartTime > 5 {
		rows = append(rows, lipgloss.NewStyle().Foreground(colorWarn).Render("[r]    ")+"  "+textStyle.Render("Restart"))
	}

	if m.canPlayNextEpisode() {
		rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("[n]    ")+"  "+textStyle.Render("Play next"))
	}

	rows = append(rows,
		lipgloss.NewStyle().Foreground(colorMuted).Render("[d]    ")+"  "+mutedStyle.Render("Download"),
	)

	return lipgloss.NewStyle().PaddingLeft(2).Render(strings.Join(rows, "\n"))
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
