package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/filter"
	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

const (
	// previewMetaLines is the fixed height of the metadata block. Every
	// asynchronous field owns a slot from the first frame, so late data only
	// replaces the text inside its slot.
	previewMetaLines = 14
	// previewOverviewLines is the reserved synopsis slot inside the metadata
	// block.
	previewOverviewLines = 4
	// previewSourceSlots reserves the ↑/↓ "more" rows of the source table.
	previewSourceSlots = 2
)

// PreviewData holds all state necessary to render the Preview (Source Picker) screen.
type PreviewData struct {
	SeriesTitle      string
	EpisodeInfo      string // e.g. "S01 E01 · The Name of the Game  (1 of 40)"
	Genres           []string
	Rating           string
	Overview         string
	AudioText        string // e.g. "sub · Japanese" or "dub · English"
	AudioTrack       string // e.g. "sub" or "dub" for controls bar
	SubtitlesText    string // e.g. "English ✓"
	SubtitleType     string // e.g. "soft subs", "hard subs", or ""
	SubtitleProvider string // e.g. "Cinecat", "OpenSubtitles", or ""
	ResumePosition   string // e.g. "13:08"
	PosterBlock      string
	Sources          []provider.MediaSource
	RankedSources    []filter.RankedSource
	SelectedIndex    int
	PlayerName       string
	Autoplay         bool
	SkipInfo         string
	LoadingProviders int
	TotalProviders   int
	FailedProvider   string
	SpinnerFrame     string
	Loading          bool // resolve in flight; empty table means "still coming", not "none"
	// ProviderName maps a source Resolver to its user-facing provider
	// name (e.g. Miruro for miruro). Missing entries render the raw
	// resolver.
	ProviderName map[string]string
	Width        int
	TermWidth    int
	Height       int
	Accent       lipgloss.AdaptiveColor
	// Mode drives the audio column: anime audio is a sub/dub track choice
	// shown in the header, so the per-source column renders for other
	// modes only.
	Mode provider.ContentType
}

// RenderPreviewScreen renders the Preview source picker per KARI_TUI_SPEC §4.4.
func RenderPreviewScreen(data PreviewData) string {
	st := NewStyles(data.Accent)

	var rows []string
	// 1. Header (Poster on left, metadata on right if >= 80 cols or >= 70 content width and poster available)
	header := renderPreviewHeader(data, st)
	rows = append(rows, header, "")
	// 2. Sources Header Line (count or progressive loading state)
	sourcesTitle := renderSourcesTitle(data, st)
	rows = append(rows, sourcesTitle, "")

	headerLines := strings.Count(header, "\n") + 1
	windowSize := previewSourceWindow(data.Height)
	if data.Height > 0 {
		windowSize = max(1, max(4, data.Height-headerLines-5)-3)
	}
	sourcesTable := renderSourcesTable(data, st, windowSize)
	rows = append(rows, sourcesTable)

	// 5. Failure summary closes the section. Its two rows are reserved even
	// when there is no failure, so a late failure never pushes the table down.
	rows = append(rows, "", renderSourcesError(data, st))
	return strings.Join(rows, "\n")
}

// showPreviewPoster reserves the poster column from the first frame whenever
// the terminal is wide enough. It deliberately ignores whether artwork has
// loaded: the slot is always drawn, so its arrival cannot move the metadata or
// the source table.
func showPreviewPoster(data PreviewData) bool {
	return data.TermWidth >= 80 || data.Width >= 70
}

func renderPreviewHeader(data PreviewData, st Styles) string {
	posterW := previewPosterWidth(data)
	metaW := max(20, data.Width-posterW)
	metaBlock := renderPreviewMeta(data, st, metaW)

	if !showPreviewPoster(data) {
		return metaBlock
	}

	// Layout side-by-side with poster on left
	return lipgloss.JoinHorizontal(lipgloss.Top,
		data.PosterBlock,
		strings.Repeat(" ", posterGutter),
		metaBlock,
	)
}

// previewPosterWidth is the fixed number of columns the poster slot reserves
// (image plus its gutter), or 0 when the poster is hidden. It never depends on
// the rendered image, whose width varies with aspect ratio and protocol.
func previewPosterWidth(data PreviewData) int {
	if !showPreviewPoster(data) {
		return 0
	}
	return previewPosterMaxCols + posterGutter
}

// renderPreviewMeta builds the text block that sits beside the poster, or
// fills the header when no poster is shown. It is split out of
// renderPreviewHeader so the overlay layout key can compare exactly the
// lines that share rows with a Sixel/iTerm2 placement (see overlayLayoutKey).
func renderPreviewMeta(data PreviewData, st Styles, metaW int) string {
	fit := func(s string) string { return truncate(s, metaW) }
	empty := ""
	metaRows := make([]string, 0, previewMetaLines)

	// Title, episode, genres/rating: three fixed rows.
	metaRows = append(metaRows, fit(st.Bold.Render(truncate(data.SeriesTitle, metaW))))
	if data.EpisodeInfo == "" {
		metaRows = append(metaRows, empty)
	} else {
		metaRows = append(metaRows, fit(data.EpisodeInfo))
	}

	var tagParts []string
	ratingW := 0
	if data.Rating != "" && data.Rating != "—" {
		// Reserve space for the rating badge before the separator.
		ratingW = lipgloss.Width("★ " + data.Rating + " · ")
	}
	if len(data.Genres) > 0 {
		availGenreW := max(10, metaW-ratingW)
		tagParts = append(tagParts, st.Dim.Render(truncate(strings.Join(data.Genres, " · "), availGenreW)))
	}
	if data.Rating != "" && data.Rating != "—" {
		tagParts = append(tagParts, st.Current.Render("★ ")+data.Rating)
	}
	if len(tagParts) == 0 {
		metaRows = append(metaRows, empty)
	} else {
		metaRows = append(metaRows, fit(strings.Join(tagParts, st.Dim.Render(" · "))))
	}

	// Synopsis: a blank separator plus a fixed four-line slot.
	metaRows = append(metaRows, "")
	for _, line := range strings.Split(wrapFixed(data.Overview, metaW, previewOverviewLines), "\n") {
		metaRows = append(metaRows, st.Dim.Render(line))
	}

	// Audio, subtitles, autoplay, skip data, resume: five fixed rows.
	metaRows = append(metaRows, "")
	if data.AudioText == "" {
		metaRows = append(metaRows, st.Dim.Render("audio      ")+empty)
	} else {
		audioFmt := data.AudioText
		if strings.HasPrefix(audioFmt, "sub") {
			audioFmt = st.Current.Render("sub") + st.Dim.Render(strings.TrimPrefix(audioFmt, "sub"))
		} else if strings.HasPrefix(audioFmt, "dub") {
			audioFmt = st.Current.Render("dub") + st.Dim.Render(strings.TrimPrefix(audioFmt, "dub"))
		}
		metaRows = append(metaRows, fit(st.Dim.Render("audio      ")+audioFmt))
	}
	if data.SubtitlesText == "" {
		metaRows = append(metaRows, st.Dim.Render("subtitles  ")+empty)
	} else {
		subFmt := data.SubtitlesText
		subType := data.SubtitleType
		if subType == "" && strings.Contains(subFmt, " · ") {
			parts := strings.SplitN(subFmt, " · ", 2)
			subFmt, subType = parts[0], parts[1]
		}
		if strings.HasSuffix(subFmt, "✓") {
			base := strings.TrimSpace(strings.TrimSuffix(subFmt, "✓"))
			subFmt = st.Current.Render(base) + " " + st.Ok.Render("✓")
		} else {
			subFmt = st.Current.Render(subFmt)
		}
		if data.SubtitleProvider != "" {
			subFmt += st.Dim.Render(" · " + data.SubtitleProvider)
		}
		if subType != "" {
			subFmt += st.Dim.Render(" · " + subType)
		}
		metaRows = append(metaRows, fit(st.Dim.Render("subtitles  ")+subFmt))
	}
	autoVal := "off"
	if data.Autoplay {
		autoVal = "on"
	}
	metaRows = append(metaRows, fit(st.Dim.Render("autoplay   ")+st.Current.Render(autoVal)))
	if data.SkipInfo == "" {
		metaRows = append(metaRows, st.Dim.Render("skip data  ")+empty)
	} else {
		metaRows = append(metaRows, fit(st.Dim.Render("skip data  ")+st.Current.Render(data.SkipInfo)))
	}
	if data.ResumePosition == "" {
		metaRows = append(metaRows, st.Dim.Render("resume     ")+st.Dim.Render("—"))
	} else {
		metaRows = append(metaRows, fit(st.Dim.Render("resume     ")+st.Current.Render(data.ResumePosition)))
	}
	return strings.Join(padLines(metaRows, previewMetaLines), "\n")
}

func renderSourcesTitle(data PreviewData, st Styles) string {
	if data.LoadingProviders > 0 && data.TotalProviders > 0 {
		spinner := data.SpinnerFrame
		if spinner == "" {
			spinner = "⠋"
		}
		spinner = st.Current.Render(spinner)
		done := data.TotalProviders - data.LoadingProviders
		if done < 0 {
			done = 0
		}
		return fmt.Sprintf("Sources  %s %d of %d providers · %d found", spinner, done, data.TotalProviders, len(data.RankedSources))
	}

	return fmt.Sprintf("Sources  %d", len(data.RankedSources))
}

// renderSourcesError renders the failure summary when no playable source is
// available. Partial provider failures stay available through the footer's
// retry binding instead of interrupting a usable source list.
func renderSourcesError(data PreviewData, st Styles) string {
	if data.FailedProvider == "" || len(data.RankedSources) > 0 || data.Loading || data.LoadingProviders > 0 {
		return ""
	}
	errText := st.Err.Render(fmt.Sprintf("✗ %s failed · %d found", data.FailedProvider, len(data.RankedSources)))
	retryText := st.Dim.Render("R retry")
	gap := max(1, data.Width-lipgloss.Width(errText)-lipgloss.Width(retryText))
	return errText + strings.Repeat(" ", gap) + retryText
}

// previewSourceWindow is the fixed source-row viewport for a terminal height.
func previewSourceWindow(height int) int {
	switch {
	case height >= 38:
		return 12
	case height >= 30:
		return 9
	case height >= 25:
		return 7
	default:
		return 5
	}
}

func renderSourcesTable(data PreviewData, st Styles, windowSize int) string {
	if windowSize <= 0 {
		windowSize = previewSourceWindow(data.Height)
	}
	tableLines := 1 + windowSize + previewSourceSlots
	if len(data.RankedSources) == 0 {
		// While providers are still resolving, an empty table means
		// "sources are on the way", not "nothing found". The reserved rows
		// keep the table from growing when the first batch arrives.
		var msg string
		if data.Loading || data.LoadingProviders > 0 {
			sp := data.SpinnerFrame
			if sp == "" {
				sp = "⠋"
			}
			msg = st.Current.Render(sp) + " " + st.Dim.Render("Getting sources from providers…")
		} else {
			msg = st.Dim.Render("  No playback sources found.")
		}
		return strings.Join(padLines([]string{msg}, tableLines), "\n")
	}
	var rows []string

	showAudio := data.Mode != provider.ModeAnime
	audioWidth := 10
	if data.Mode == provider.ModeLive {
		// Live channels often list several broadcast languages; a narrow
		// column would truncate them into a misleading single name.
		audioWidth = 26
	}
	cursorH := "  "
	qualityH := fmt.Sprintf("%-12s", "quality")
	providerH := fmt.Sprintf("%-14s", "provider")
	sourceH := fmt.Sprintf("%-18s", "source")
	headerRow := fmt.Sprintf("%s%s  %s  %s", cursorH, st.Dim.Render(qualityH), st.Dim.Render(providerH), st.Dim.Render(sourceH))
	if showAudio {
		headerRow += "  " + st.Dim.Render(fmt.Sprintf("%-*s", audioWidth, "audio"))
	}
	rows = append(rows, headerRow)

	// Visible window of rows: responsive to terminal height so sources aren't cramped.
	total := len(data.RankedSources)
	startIdx := windowStart(total, data.SelectedIndex, windowSize)
	endIdx := min(total, startIdx+windowSize)

	if hint := windowHint(startIdx, true, st); hint != "" {
		rows = append(rows, hint)
	}

	for i := startIdx; i < endIdx; i++ {
		scored := data.RankedSources[i]
		src := scored.Source
		isFocused := i == data.SelectedIndex

		cursor := "  "
		if isFocused {
			cursor = st.Cursor.Render("▌ ")
		}

		quality := formatSourceQuality(src.Quality)
		qualityStr := fmt.Sprintf("%-12s", truncate(quality, 12))

		providerName := src.Resolver
		if data.ProviderName != nil {
			if name, ok := data.ProviderName[src.Resolver]; ok && name != "" {
				providerName = name
			}
		}
		if providerName == "" {
			providerName = "—"
		}
		providerStr := st.Dim.Render(fmt.Sprintf("%-14s", truncate(providerName, 14)))

		sourceName := strings.TrimSpace(src.Backend)
		if sourceName == "" {
			sourceName = providerName
		}
		if sourceName == "" {
			sourceName = "—"
		}
		sourceStr := st.Dim.Render(fmt.Sprintf("%-18s", truncate(sourceName, 18)))

		row := fmt.Sprintf("%s%s  %s  %s", cursor, qualityStr, providerStr, sourceStr)
		if showAudio {
			audioStr := fmt.Sprintf("%-*s", audioWidth, truncate(formatSourceAudio(src, audioWidth), audioWidth))
			if isFocused {
				row = fmt.Sprintf("%s%s  %s  %s  %s", cursor,
					st.Current.Render(qualityStr), providerStr, sourceStr, st.Current.Render(audioStr))
			} else {
				row += "  " + st.Dim.Render(audioStr)
			}
		} else if isFocused {
			row = fmt.Sprintf("%s%s  %s  %s", cursor, st.Current.Render(qualityStr), providerStr, sourceStr)
		}
		rows = append(rows, row)
	}

	if hint := windowHint(total-endIdx, false, st); hint != "" {
		rows = append(rows, hint)
	}

	return strings.Join(padLines(rows, tableLines), "\n")
}

// formatSourceAudio names the audio language for the sources table. A display
// label wins because live channels may broadcast in several languages; then a
// single detected track language; then a dash when the stream does not
// advertise language metadata.
func formatSourceAudio(src provider.MediaSource, width int) string {
	if label := strings.TrimSpace(src.AudioLabel); label != "" {
		languages := strings.Split(label, " · ")
		if len(languages) > 1 {
			return truncate(strings.TrimSpace(languages[0]), max(1, width-3)) + fmt.Sprintf(" +%d", len(languages)-1)
		}
		return label
	}
	if l := strings.TrimSpace(src.Language); l != "" {
		return lang.Name(l)
	}
	return "—"
}

func formatSourceQuality(raw string) string {
	if kit.IsAdaptiveQuality(raw) {
		return "Auto"
	}
	res := kit.QualityHeight(raw)
	switch {
	case res >= 2160:
		return "4K"
	case res >= 1440:
		return "QHD"
	case res >= 1080:
		return "FHD"
	case res >= 720:
		return "HD"
	case res >= 480:
		return "SD"
	case res > 0:
		return fmt.Sprintf("%dp", res)
	default:
		return "—"
	}
}
