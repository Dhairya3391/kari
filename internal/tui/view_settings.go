package tui

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/lang"
	"kari/internal/provider"
)

// settingsLastIndex bounds the vertically navigable setting slots
// (0=Trakt 1=AniList 2=Quality 3=Languages 4=SubtitleLang 5=Images 6=Appearance).
const settingsLastIndex = 6

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

	rows = append(rows, sectionTitleStyle.Render("Trakt.tv"))
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

	rows = append(rows, sectionTitleStyle.Render("AniList"))
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

	rows = append(rows, sectionTitleStyle.Render("Quality"))
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
	for _, l := range languages {
		if m.languageEnabled(l.Code) {
			enabledCount++
		}
	}
	rows = append(rows, sectionTitleStyle.Render(fmt.Sprintf("Languages · %d/%d enabled", enabledCount, len(languages))))
	if len(languages) == 0 {
		rows = append(rows, langStyle.Render(mutedStyle.Render("No audio-language filters for this mode")))
	} else {
		entries := make([]string, len(languages))
		for i, l := range languages {
			marker := "○"
			if m.languageEnabled(l.Code) {
				marker = "●"
			}
			text := marker + " " + l.Display
			switch {
			case m.settingsIndex == 3 && i == m.languageIndex:
				text = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(text)
			case m.languageEnabled(l.Code):
				text = textStyle.Render(text)
			default:
				text = mutedStyle.Render(text)
			}
			entries[i] = text
		}
		// Flowed into a wrapping grid rather than one language per line —
		// with a dozen toggles, a single column wastes vertical space.
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

	rows = append(rows, sectionTitleStyle.Render("Subtitle Language"))
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

	rows = append(rows, sectionTitleStyle.Render("Image Rendering"))
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

	rows = append(rows, sectionTitleStyle.Render("Appearance"))
	for _, line := range wrapEntries(accentParts, dims.contentW-4, "    ") {
		rows = append(rows, accentStyle.Render(line))
	}
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

// hasEnabledLanguage guards the loaded filter against a degenerate
// "everything disabled" state. It deliberately checks the full movies/TV
// language pool — not the active mode's slice — because at startup the
// active mode may be one with no audio languages at all (anime), where
// checking locally would wrongly conclude every language was disabled and
// wipe the user's saved filter.
func (m *modelImpl) hasEnabledLanguage() bool {
	for _, l := range m.registry.AudioLanguages(provider.ModeMovies, provider.ModeTV) {
		if m.languageEnabled(l.Code) {
			return true
		}
	}
	return false
}

func cleanEpisodeTitle(epTitle, seriesTitle string) string {
	if epTitle == "" {
		return ""
	}

	if seriesTitle != "" {
		if n := caseInsensitivePrefixLen(epTitle, seriesTitle); n > 0 {
			epTitle = epTitle[n:]
		}
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

func caseInsensitivePrefixLen(s, prefix string) int {
	if prefix == "" || s == "" {
		return 0
	}
	var bytes int
	si, pi := 0, 0
	for si < len(s) && pi < len(prefix) {
		sRune, sSize := utf8.DecodeRuneInString(s[si:])
		pRune, pSize := utf8.DecodeRuneInString(prefix[pi:])
		if !strings.EqualFold(string(sRune), string(pRune)) {
			return 0
		}
		bytes += sSize
		si += sSize
		pi += pSize
	}
	if pi < len(prefix) {
		return 0
	}
	return bytes
}

func (m *modelImpl) languageEnabled(lang string) bool {
	if m.languageFilter == nil || lang == "" {
		return true
	}
	enabled, configured := m.languageFilter[lang]
	return !configured || enabled
}
