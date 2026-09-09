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

const (
	// Accounts & Scrobbling
	settingsTraktIndex   = 0
	settingsAniListIndex = 1

	// Anime Preferences
	settingsAnimeAudioIndex   = 2
	settingsAnimeSubsIndex    = 3
	settingsSkipProviderIndex = 4
	settingsAutoIntroIndex    = 5
	settingsAutoEndingIndex   = 6
	settingsAutoRecapIndex    = 7
	settingsAutoPreviewIndex  = 8

	// Movies, TV & Cartoons
	settingsAudioLangIndex = 9
	settingsSubLangIndex   = 10

	// General Playback & Player
	settingsPlayerIndex   = 11
	settingsQualityIndex  = 12
	settingsAutoplayIndex = 13

	// Interface & Theme
	settingsImagesIndex     = 14
	settingsAppearanceIndex = 15

	settingsLastIndex = settingsAppearanceIndex
)

func (m *modelImpl) renderSettingsScreen(dims layoutDims) string {
	rows := []string{
		sectionTitleStyle.Render("Settings"),
		"",
	}

	modeColor := lipgloss.NewStyle().Foreground(colorPrimary).Render

	// Helper to render category separator
	renderCategory := func(title string) {
		titleStyled := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(title)
		ruleLen := dims.contentW - utf8.RuneCountInString(title) - 2
		if ruleLen < 2 {
			ruleLen = 2
		}
		rule := lipgloss.NewStyle().Foreground(colorBorder).Render(strings.Repeat("─", ruleLen))
		rows = append(rows, fmt.Sprintf("%s %s", titleStyled, rule))
	}

	// Helper to render setting card
	renderCard := func(index int, title, valueLine, keyHint, helpText string) {
		isFocused := m.settingsIndex == index
		cardStyle := lipgloss.NewStyle().PaddingLeft(2)
		if isFocused {
			cardStyle = cardStyle.BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(colorPrimary)
		} else {
			cardStyle = cardStyle.BorderLeft(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("#282828"))
		}

		var lines []string
		if isFocused {
			lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(title))
		} else {
			lines = append(lines, mutedStyle.Render(title))
		}
		lines = append(lines, valueLine)
		if isFocused && keyHint != "" {
			lines = append(lines, mutedStyle.Render(keyHint))
		}
		if helpText != "" {
			lines = append(lines, mutedStyle.Render(helpText))
		}

		rows = append(rows, cardStyle.Render(strings.Join(lines, "\n")))
		rows = append(rows, "")
	}

	// -------------------------------------------------------------------------
	// 1. ACCOUNTS & SCROBBLING
	// -------------------------------------------------------------------------
	renderCategory("ACCOUNTS & SCROBBLING")

	// Trakt.tv
	traktStatus := mutedStyle.Render("○ Not connected")
	if m.traktClient != nil && m.traktClient.IsAuthenticated() {
		traktStatus = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("● Connected")
	}
	if m.traktAuthCode != "" {
		traktStatus = lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render("● Waiting for authorization...")
	}
	traktVal := fmt.Sprintf("Status: %s", traktStatus)
	if m.traktAuthCode != "" {
		traktVal += fmt.Sprintf("\nGo to: %s\nEnter code: %s", m.traktAuthURL, m.traktAuthCode)
	}
	renderCard(settingsTraktIndex, "Trakt.tv", traktVal, "[c] connect    [r] revoke", "Automatically sync movies and TV watch progress across devices")

	// AniList
	anilistStatus := mutedStyle.Render("○ Not connected")
	if m.anilistClient != nil && m.anilistClient.IsAuthenticated() {
		anilistStatus = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("● Connected")
	}
	anilistVal := fmt.Sprintf("Status: %s", anilistStatus)
	if m.anilistAuthURL != "" {
		anilistVal += fmt.Sprintf("\nGo to: %s\nPaste code below and press Enter:\n%s", m.anilistAuthURL, m.authInput.View())
	}
	renderCard(settingsAniListIndex, "AniList", anilistVal, "[c] connect    [r] revoke", "Automatically scrobble completed anime episodes and sync progress")

	// -------------------------------------------------------------------------
	// 2. ANIME PREFERENCES
	// -------------------------------------------------------------------------
	renderCategory("ANIME PREFERENCES")

	// Default Audio Track
	subMarker, dubMarker := "●", "○"
	if strings.EqualFold(m.audioMode, provider.AudioDub) {
		subMarker, dubMarker = "○", "●"
	}
	audioTrackLine := fmt.Sprintf("%s Sub (Japanese)    %s Dub (English)", modeColor(subMarker), modeColor(dubMarker))
	renderCard(settingsAnimeAudioIndex, "Default Audio Track", audioTrackLine, "[←] [→] [space] toggle default audio", "Preferred default audio track when browsing and playing anime series")

	// Anime Subtitles
	animeSubOn, animeSubOff := "○", "○"
	if !m.disableAnimeSubtitles {
		animeSubOn = "●"
	} else {
		animeSubOff = "●"
	}
	animeSubLine := fmt.Sprintf("%s Enabled    %s Disabled", modeColor(animeSubOn), modeColor(animeSubOff))
	renderCard(settingsAnimeSubsIndex, "Anime Subtitles", animeSubLine, "[←] [→] [space] toggle", "When disabled, subtitles are suppressed for anime to prevent double hardsubs")

	// Skip Engine
	providerDisplay := "● Hybrid (Anime-Skip + AniSkip)"
	switch m.skipProvider {
	case "anime-skip":
		providerDisplay = "● Anime-Skip"
	case "aniskip":
		providerDisplay = "● AniSkip"
	case "off":
		providerDisplay = "○ Off"
	}
	renderCard(settingsSkipProviderIndex, "Skip Engine", lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(providerDisplay), "[←] [→] [space] cycle skip provider", "Provider for detecting opening, ending, recap, and preview timestamps")

	// Auto-skip Preferences
	skipOptions := []struct {
		index   int
		label   string
		enabled bool
	}{
		{index: settingsAutoIntroIndex, label: "Auto-skip Opening (Intro)", enabled: m.autoSkipIntro},
		{index: settingsAutoEndingIndex, label: "Auto-skip Ending (Outro)", enabled: m.autoSkipEnding},
		{index: settingsAutoRecapIndex, label: "Auto-skip Recap", enabled: m.skipRecap},
		{index: settingsAutoPreviewIndex, label: "Auto-skip Preview", enabled: m.skipPreview},
	}
	for _, option := range skipOptions {
		onMarker, offMarker := "○", "○"
		if option.enabled {
			onMarker = "●"
		} else {
			offMarker = "●"
		}
		optLine := fmt.Sprintf("%s Enabled    %s Disabled", modeColor(onMarker), modeColor(offMarker))
		renderCard(option.index, option.label, optLine, "[←] [→] [space] toggle", "When disabled, shows an on-screen [Enter] prompt to skip instead of jumping automatically")
	}

	// -------------------------------------------------------------------------
	// 3. MOVIES, TV & CARTOONS
	// -------------------------------------------------------------------------
	renderCategory("MOVIES, TV & CARTOONS")

	// Audio Languages
	languages := m.availableLanguages()
	if len(languages) == 0 {
		renderCard(settingsAudioLangIndex, "Audio Languages (Movies & TV)", mutedStyle.Render("No audio filters active (Anime sub/dub audio is chosen in episode list)"), "", "")
	} else {
		enabledCount := 0
		for _, l := range languages {
			if m.languageEnabled(l.Code) {
				enabledCount++
			}
		}
		entries := make([]string, len(languages))
		for i, l := range languages {
			marker := "○"
			if m.languageEnabled(l.Code) {
				marker = "●"
			}
			text := marker + " " + l.Display
			switch {
			case m.settingsIndex == settingsAudioLangIndex && i == m.languageIndex:
				text = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(text)
			case m.languageEnabled(l.Code):
				text = textStyle.Render(text)
			default:
				text = mutedStyle.Render(text)
			}
			entries[i] = text
		}
		langGrid := strings.Join(wrapEntries(entries, dims.contentW-4, "   "), "\n")
		title := fmt.Sprintf("Audio Languages (Movies & TV) · %d/%d enabled", enabledCount, len(languages))
		renderCard(settingsAudioLangIndex, title, langGrid, "[←] [→] navigate languages    [space] toggle", "Filter available audio tracks for movies and TV shows")
	}

	// Subtitles (Global)
	subVal := ""
	if m.subtitleLanguage == "off" {
		subVal = lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render("○ Off (Disabled)")
	} else {
		subVal = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("● " + lang.Name(m.subtitleLanguage))
	}
	subHelp := "Preferred subtitle language across all providers and OpenSubtitles"
	if m.subtitleLanguage == "off" {
		subHelp = "Subtitles disabled globally. No external subtitles will be downloaded or attached."
	}
	renderCard(settingsSubLangIndex, "Subtitles (Preferred Language)", subVal, "[←] [→] select language    [space] toggle global on/off", subHelp)

	// -------------------------------------------------------------------------
	// 4. GENERAL PLAYBACK & PLAYER
	// -------------------------------------------------------------------------
	renderCategory("GENERAL PLAYBACK & PLAYER")

	// Preferred Player
	playerParts := make([]string, 0, len(m.availablePlayers))
	for _, p := range m.availablePlayers {
		marker := "○"
		if p == m.selectedPlayerName() {
			marker = "●"
		}
		playerParts = append(playerParts, fmt.Sprintf("%s %s", modeColor(marker), strings.ToUpper(p)))
	}
	playerLine := strings.Join(wrapEntries(playerParts, dims.contentW-4, "    "), "\n")
	if len(m.availablePlayers) == 0 {
		playerLine = mutedStyle.Render("No external media player detected on system")
	}
	renderCard(settingsPlayerIndex, "Preferred Media Player", playerLine, "[←] [→] [space] cycle video player", "Default external player launched for video playback (e.g. MPV, IINA, VLC)")

	// Stream Quality
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
	qualityLine := fmt.Sprintf(
		"%s All    %s Highest    %s Data Saver    %s Lowest",
		modeColor(allMarker), modeColor(highestMarker), modeColor(dataSaverMarker), modeColor(lowestMarker),
	)
	renderCard(settingsQualityIndex, "Stream Quality", shorten(qualityLine, dims.contentW-4), "[←] [→] cycle quality mode", "Filters available streams in player preview to match bandwidth preferences")

	// Autoplay
	autoplayOn, autoplayOff := "○", "○"
	if m.autoPlayAfterResolve {
		autoplayOn = "●"
	} else {
		autoplayOff = "●"
	}
	autoplayLine := fmt.Sprintf("%s Enabled    %s Disabled", modeColor(autoplayOn), modeColor(autoplayOff))
	renderCard(settingsAutoplayIndex, "Autoplay Next Episode", autoplayLine, "[←] [→] [space] toggle autoplay", "Automatically advances and plays the next episode upon completion")

	// -------------------------------------------------------------------------
	// 5. INTERFACE & THEME
	// -------------------------------------------------------------------------
	renderCategory("INTERFACE & THEME")

	// Image Rendering
	enabledMarker, disabledMarker := "○", "○"
	if m.imagesEnabled {
		enabledMarker = "●"
	} else {
		disabledMarker = "●"
	}
	imagesLine := fmt.Sprintf("%s Enabled    %s Disabled", modeColor(enabledMarker), modeColor(disabledMarker))
	renderCard(settingsImagesIndex, "Poster Artwork Rendering", imagesLine, "[←] [→] [space] toggle", "Display high-resolution artwork in terminal using Kitty/Sixel/iTerm graphics")

	// Appearance (Accent Color)
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
	accentLine := strings.Join(wrapEntries(accentParts, dims.contentW-4, "    "), "\n")
	if m.editingAccentHex {
		accentLine += fmt.Sprintf("\n%s\n%s", mutedStyle.Render("Enter 6-digit hex color and press Enter:"), m.hexInput.View())
	}
	accentHint := "[←] [→] cycle accent color"
	if m.accentIndex == len(accentPresets) {
		accentHint = "[←] [→] cycle    [c] enter custom hex"
	}
	renderCard(settingsAppearanceIndex, "Theme Accent Color", accentLine, accentHint, "Customizes the primary accent color across all UI badges, borders, and controls")

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
		if pLen := caseInsensitivePrefixLen(epTitle, seriesTitle); pLen > 0 {
			epTitle = epTitle[pLen:]
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
	sLower := strings.ToLower(s)
	pLower := strings.ToLower(prefix)
	if strings.HasPrefix(sLower, pLower) {
		return len(prefix)
	}
	return 0
}

func (m *modelImpl) languageEnabled(lang string) bool {
	if m.languageFilter == nil {
		return true
	}
	enabled, ok := m.languageFilter[lang]
	if !ok {
		return true
	}
	return enabled
}
