package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// SettingsCategory represents one of the settings tabs.
type SettingsCategory int

const (
	CategoryPlayback SettingsCategory = iota
	CategoryLanguages
	CategoryAnime
	CategoryModes
	CategoryAccounts
	CategoryInterface
)

var SettingsCategoryNames = []string{
	"Playback",
	"Languages",
	"Anime",
	"Modes",
	"Accounts",
	"Interface",
}

// ModeRow represents one content mode row in Settings > Modes.
type ModeRow struct {
	Key         string
	Label       string
	Enabled     bool
	Available   bool
	UnavailNote string
	Accent      lipgloss.AdaptiveColor
}

// SettingsData carries all state needed to render the Settings screen.
type SettingsData struct {
	ActiveCategory  SettingsCategory
	FocusedRowIndex int
	Width           int
	Height          int
	Accent          lipgloss.AdaptiveColor

	// Playback values
	PlayerName          string // "mpv", "iina"
	QualityName         string // "Highest", "All", "Data Saver", "Lowest"
	DownloadQualityName string // "Same as stream", "Highest", "Data Saver", "Lowest"
	Autoplay            bool   // on / off
	// Languages values
	AudioSummary     string // e.g. "English · Hindi · Japanese"
	SubtitleLanguage string // e.g. "English"
	AudioPickerOpen  bool
	AllLanguages     []string
	EnabledLanguages map[string]bool
	PickerIndex      int

	// Anime values
	AnimeAudioTrack string // "Sub · Japanese", "Dub · English"
	AnimeSubtitles  bool   // on / off
	SkipSource      string // "hybrid", "skipdb", "introdb", "anime-skip", "aniskip", "off"
	AutoSkipIntro   bool   // auto / ask
	AutoSkipEnding  bool   // auto / ask
	AutoSkipRecap   bool   // auto / ask
	AutoSkipPreview bool   // auto / ask

	// Modes values
	DefaultModeName string
	ModesRows       []ModeRow
	// Accounts values
	AniListConnected  bool
	AniListAuthActive bool
	AniListAuthURL    string
	AuthInputView     string
	TraktConnected    bool
	TraktAuthActive   bool
	TraktUserCode     string
	TraktVerifyURL    string
	StartupSync       bool
	TraktWaitingCode  string
	LoadingText       string
	SpinnerFrame      string
	// Interface values
	PosterArtwork bool   // on / off
	AccentName    string // "Auto (per mode)", "Purple", etc.
	Transitions   bool   // on / off
}

// RenderSettingsScreen renders the 2-pane Settings screen per KARI_TUI_SPEC §4.7.
func RenderSettingsScreen(data SettingsData) string {
	st := NewStyles(data.Accent)

	// If Audio Picker modal overlay is active, render it directly
	if data.AudioPickerOpen {
		return renderAudioLanguagePicker(data, st)
	}

	leftW := 22
	rightW := max(46, data.Width-leftW-8)
	var leftLines []string
	for i, catName := range SettingsCategoryNames {
		if SettingsCategory(i) == data.ActiveCategory {
			leftLines = append(leftLines, st.ActiveTab.Render(strings.ToUpper(catName)))
		} else {
			leftLines = append(leftLines, st.Dim.Render(strings.ToLower(catName)))
		}
		if data.Height >= 20 && i < len(SettingsCategoryNames)-1 {
			leftLines = append(leftLines, "")
		}
	}
	leftCol := strings.Join(leftLines, "\n")

	// Build right Settings content
	rightCol := renderCategoryContent(data, rightW, st)

	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftW).Render(leftCol),
		"        ",
		lipgloss.NewStyle().Width(rightW).Render(rightCol),
	)
}

func renderCategoryContent(data SettingsData, width int, st Styles) string {
	var rows []string
	var desc string

	switch data.ActiveCategory {
	case CategoryPlayback:
		// 0: Preferred player, 1: Stream quality, 2: Download quality, 3: Autoplay next episode
		rows = append(rows, renderSettingRow("Preferred player", formatVal(data.PlayerName, data.FocusedRowIndex == 0, st), data.FocusedRowIndex == 0, width, st))
		rows = append(rows, renderSettingRow("Stream quality", formatVal(data.QualityName, data.FocusedRowIndex == 1, st), data.FocusedRowIndex == 1, width, st))
		rows = append(rows, renderSettingRow("Download quality", formatVal(data.DownloadQualityName, data.FocusedRowIndex == 2, st), data.FocusedRowIndex == 2, width, st))
		rows = append(rows, renderSettingRow("Autoplay next episode", formatVal(boolOnOff(data.Autoplay), data.FocusedRowIndex == 3, st), data.FocusedRowIndex == 3, width, st))

		switch data.FocusedRowIndex {
		case 0:
			desc = "External player application used for video streaming."
		case 1:
			desc = "Target stream resolution: Highest, Data Saver (<=1080p), or Lowest."
		case 2:
			desc = "Target download resolution: 4K (2160p), FHD (1080p), HD (720p), SD (480p), or Auto (same as stream)."
		case 3:
			desc = "Autoplay picks the best source and skips the preview."
		}

	case CategoryLanguages:
		// 0: Audio (movies & tv), 1: Subtitles
		rows = append(rows, renderSettingRow("Audio (movies & tv)", formatVal(data.AudioSummary, data.FocusedRowIndex == 0, st), data.FocusedRowIndex == 0, width, st))
		rows = append(rows, renderSettingRow("Subtitles", formatVal(data.SubtitleLanguage, data.FocusedRowIndex == 1, st), data.FocusedRowIndex == 1, width, st))

		switch data.FocusedRowIndex {
		case 0:
			desc = "enter on Audio opens a picker: space toggles audio languages."
		case 1:
			desc = "Preferred subtitle language for streaming and downloads."
		}

	case CategoryAnime:
		// 0: Audio track, 1: Subtitles, 2: Skip source, 3: Intro, 4: Outro, 5: Recap, 6: Preview
		rows = append(rows, renderSettingRow("Audio track", formatVal(data.AnimeAudioTrack, data.FocusedRowIndex == 0, st), data.FocusedRowIndex == 0, width, st))
		rows = append(rows, renderSettingRow("Subtitles", formatVal(boolOnOff(data.AnimeSubtitles), data.FocusedRowIndex == 1, st), data.FocusedRowIndex == 1, width, st))
		rows = append(rows, renderSettingRow("Skip source", formatVal(data.SkipSource, data.FocusedRowIndex == 2, st), data.FocusedRowIndex == 2, width, st))
		rows = append(rows, "", st.Dim.Render("Skipping"), "")
		rows = append(rows, renderSettingRow("Intro", formatVal(boolAskAuto(data.AutoSkipIntro), data.FocusedRowIndex == 3, st), data.FocusedRowIndex == 3, width, st))
		rows = append(rows, renderSettingRow("Outro", formatVal(boolAskAuto(data.AutoSkipEnding), data.FocusedRowIndex == 4, st), data.FocusedRowIndex == 4, width, st))
		rows = append(rows, renderSettingRow("Recap", formatVal(boolAskAuto(data.AutoSkipRecap), data.FocusedRowIndex == 5, st), data.FocusedRowIndex == 5, width, st))
		rows = append(rows, renderSettingRow("Preview", formatVal(boolAskAuto(data.AutoSkipPreview), data.FocusedRowIndex == 6, st), data.FocusedRowIndex == 6, width, st))

		switch data.FocusedRowIndex {
		case 0:
			desc = "Choose the audio track used by default for anime."
		case 1:
			desc = "Enable or disable subtitle overlay tracks for anime."
		case 2:
			desc = "Service engine used to resolve intro/outro skip chapters."
		case 3:
			desc = "Skip intro sequence: ask (prompts [Enter]) or auto (jumps automatically)."
		case 4:
			desc = "Skip outro sequence: ask (prompts [Enter]) or auto (jumps automatically)."
		case 5:
			desc = "Skip recap sequence: ask (prompts [Enter]) or auto (jumps automatically)."
		case 6:
			desc = "Skip preview / post-credits: ask (prompts [Enter]) or auto (jumps automatically)."
		}
	case CategoryModes:
		rows = append(rows, renderSettingRow("Startup mode", formatVal(data.DefaultModeName, data.FocusedRowIndex == 0, st), data.FocusedRowIndex == 0, width, st))
		for i, mr := range data.ModesRows {
			isFocused := (i + 1) == data.FocusedRowIndex
			rows = append(rows, renderModeSettingRow(mr, isFocused, width, st))
		}
		if data.FocusedRowIndex == 0 {
			desc = "Default mode opened when Kari starts up."
		} else {
			desc = "The order here is the order tab / shift+tab cycles modes on Search. [←]/[→] reorders, [Space]/[Enter] toggles."
		}
	case CategoryAccounts:
		// 0: AniList, 1: Trakt, 2: Startup sync.
		rows = append(rows, renderAccountRow("AniList", data.AniListConnected, data.FocusedRowIndex == 0, width, st))
		if data.AniListAuthActive {
			rows = append(rows, "")
			rows = append(rows, "    "+st.Bold.Render("AniList Authorization Token / Code:"))
			rows = append(rows, "    "+data.AuthInputView)
			rows = append(rows, "    "+st.Dim.Render("Paste code and press Enter · Esc to cancel"))
			rows = append(rows, "")
		}
		rows = append(rows, renderAccountRow("Trakt", data.TraktConnected, data.FocusedRowIndex == 1, width, st))
		if data.TraktAuthActive {
			rows = append(rows, "")
			rows = append(rows, "    "+st.Bold.Render("Trakt Device Code: ")+st.Ok.Render(data.TraktUserCode))
			rows = append(rows, "    "+st.Dim.Render("Visit ")+st.Bold.Render(data.TraktVerifyURL)+st.Dim.Render(" and enter code"))
			rows = append(rows, "    "+st.Dim.Render("Waiting for authorization... · Esc to cancel"))
			rows = append(rows, "")
		}
		rows = append(rows, renderAccountSettingRow("Startup sync", boolOnOff(data.StartupSync), data.FocusedRowIndex == 2, width, st))
		switch data.FocusedRowIndex {
		case 0:
			desc = "Automatically sync and scrobble anime watch progress."
		case 1:
			desc = "Automatically sync movies and TV show progress across devices."
		case 2:
			desc = "Automatically pull and sync watched history from connected accounts on startup."
		}

	case CategoryInterface:
		// 0: Poster artwork, 1: Accent, 2: Transitions
		rows = append(rows, renderSettingRow("Poster artwork", formatVal(boolOnOff(data.PosterArtwork), data.FocusedRowIndex == 0, st), data.FocusedRowIndex == 0, width, st))
		rows = append(rows, renderSettingRow("Accent", formatVal(data.AccentName, data.FocusedRowIndex == 1, st), data.FocusedRowIndex == 1, width, st))
		rows = append(rows, renderSettingRow("Transitions", formatVal(boolOnOff(data.Transitions), data.FocusedRowIndex == 2, st), data.FocusedRowIndex == 2, width, st))

		switch data.FocusedRowIndex {
		case 0:
			desc = "Enable high-resolution terminal poster artwork rendering."
		case 1:
			desc = "Theme accent color: Auto adapts per content mode."
		case 2:
			desc = "Smooth color transitions when changing themes or content modes."
		}
	}

	if desc != "" {
		rows = append(rows, "", "", st.Dim.Render(desc))
	}

	return strings.Join(rows, "\n")
}
func renderModeSettingRow(row ModeRow, isFocused bool, width int, st Styles) string {
	cursor := "  "
	if isFocused {
		cursor = st.Cursor.Render("▌ ")
	}

	var dot string
	if row.Available {
		dot = lipgloss.NewStyle().Foreground(row.Accent).Render("●")
	} else {
		dot = st.Dim.Render("○")
	}

	nameStr := row.Label
	if isFocused {
		nameStr = st.Bold.Render(row.Label)
	}

	var statusStr string
	if !row.Available {
		note := row.UnavailNote
		if note == "" {
			note = "unavailable"
		}
		statusStr = st.Dim.Render("unavailable · " + note)
	} else if row.Enabled {
		statusStr = st.Current.Render("on")
	} else {
		statusStr = st.Dim.Render("off")
	}

	labelPart := fmt.Sprintf("%s %-24s", dot, nameStr)
	return fmt.Sprintf("%s%s  %s", cursor, labelPart, statusStr)
}

func renderSettingRow(label, value string, isFocused bool, width int, st Styles) string {
	cursor := "  "
	if isFocused {
		cursor = st.Cursor.Render("▌ ")
	}

	labelW := 28
	labelTrunc := truncate(label, labelW)
	labelPadded := labelTrunc + strings.Repeat(" ", max(0, labelW-lipgloss.Width(labelTrunc)))
	if isFocused {
		labelPadded = st.Bold.Render(labelTrunc) + strings.Repeat(" ", max(0, labelW-lipgloss.Width(labelTrunc)))
	}

	return fmt.Sprintf("%s%s  %s", cursor, labelPadded, value)
}

func renderAccountRow(name string, connected bool, isFocused bool, width int, st Styles) string {
	cursor := "  "
	if isFocused {
		cursor = st.Cursor.Render("▌ ")
	}
	nameStr := fmt.Sprintf("%-20s", name)
	if isFocused {
		nameStr = st.Bold.Render(fmt.Sprintf("%-20s", name))
	}

	statusDot := st.Dim.Render("○ not connected")
	if connected {
		statusDot = st.Ok.Render("● connected")
	}
	statusPadded := statusDot + strings.Repeat(" ", max(0, 18-lipgloss.Width(statusDot)))

	actionHint := ""
	if isFocused {
		if connected {
			actionHint = st.Dim.Render("i  import   r  revoke")
		} else {
			actionHint = st.Dim.Render("c  connect")
		}
	}

	return fmt.Sprintf("%s%s  %s  %s", cursor, nameStr, statusPadded, actionHint)
}

func renderAccountSettingRow(label, value string, isFocused bool, width int, st Styles) string {
	cursor := "  "
	if isFocused {
		cursor = st.Cursor.Render("▌ ")
	}

	labelStr := fmt.Sprintf("%-20s", label)
	if isFocused {
		labelStr = st.Bold.Render(fmt.Sprintf("%-20s", label))
	}

	valStr := formatVal(value, isFocused, st)
	return fmt.Sprintf("%s%s  %s", cursor, labelStr, valStr)
}

func formatVal(val string, isFocused bool, st Styles) string {
	if isFocused {
		return st.EditableValue.Render("‹ " + val + " ›")
	}
	return st.Dim.Render(val)
}

func boolOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func boolAskAuto(b bool) string {
	if b {
		return "auto"
	}
	return "ask"
}

func renderAudioLanguagePicker(data SettingsData, st Styles) string {
	var rows []string

	rows = append(rows, st.Bold.Render("Select Audio Languages"), "")

	for i, l := range data.AllLanguages {
		cursor := "  "
		if i == data.PickerIndex {
			cursor = st.Cursor.Render("▌ ")
		}

		status := "[ ] "
		if data.EnabledLanguages[l] {
			status = st.Ok.Render("[✓] ")
		}

		label := l
		if i == data.PickerIndex {
			label = st.Bold.Render(l)
		}

		rows = append(rows, fmt.Sprintf("%s%s%s", cursor, status, label))
	}

	rows = append(rows, "", st.Dim.Render("space toggle   enter/esc save & close"))
	return strings.Join(rows, "\n")
}
