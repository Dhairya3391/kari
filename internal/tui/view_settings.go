package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/lang"
)

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

	// Image rendering section
	imagesStyle := lipgloss.NewStyle().PaddingLeft(2)
	if m.settingsIndex == 5 {
		imagesStyle = imagesStyle.BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(colorPrimary)
	}

	enabledMarker, disabledMarker := "○", "○"
	if m.imagesEnabled {
		enabledMarker = "●"
	} else {
		disabledMarker = "●"
	}
	imagesLine := fmt.Sprintf("%s Enabled    %s Disabled", modeColor(enabledMarker), modeColor(disabledMarker))

	rows = append(rows, "Image Rendering")
	rows = append(rows, imagesStyle.Render(imagesLine))
	rows = append(rows, imagesStyle.Render(mutedStyle.Render("[←] [→] to change")))
	rows = append(rows, imagesStyle.Render(mutedStyle.Render("Posters are shown as \"Image rendering disabled\" in place when off")))
	rows = append(rows, "")

	// Appearance (accent color) section
	accentStyle := lipgloss.NewStyle().PaddingLeft(2)
	if m.settingsIndex == 6 {
		accentStyle = accentStyle.BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(colorPrimary)
	}

	// The ● marker follows the actually-applied color (colorPrimary), not
	// m.accentIndex — accentIndex is just where [←][→]/[c] point right now,
	// which can sit on Custom without a color having been entered yet
	// (nothing applied), or on a preset after Custom was already active.
	currentHex := string(colorPrimary)
	accentParts := make([]string, len(accentPresets)+1)
	activeIsPreset := false
	for i, preset := range accentPresets {
		marker := "○"
		if preset.hex == currentHex {
			marker = "●"
			activeIsPreset = true
		}
		accentParts[i] = modeColor(marker) + " " + preset.name
	}
	customMarker := "○"
	customLabel := "Custom"
	if !activeIsPreset {
		customMarker = "●"
	}
	if m.customAccentHex != "" {
		customLabel = "Custom (" + m.customAccentHex + ")"
	}
	accentParts[len(accentPresets)] = modeColor(customMarker) + " " + customLabel

	rows = append(rows, "Appearance")
	rows = append(rows, accentStyle.Render(shorten(strings.Join(accentParts, "    "), dims.contentW-4)))
	if m.editingAccentHex {
		rows = append(rows, accentStyle.Render(mutedStyle.Render("Enter a 6-digit hex color and press Enter (Esc to cancel):")))
		rows = append(rows, accentStyle.Render(m.hexInput.View()))
	} else if m.accentIndex == len(accentPresets) {
		rows = append(rows, accentStyle.Render(mutedStyle.Render("[←] [→] to change    [c] enter custom hex")))
	} else {
		rows = append(rows, accentStyle.Render(mutedStyle.Render("[←] [→] to change accent color")))
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
