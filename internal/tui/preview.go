package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/ranking"
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
	ResumePosition   string // e.g. "13:08"
	PosterBlock      string
	Sources          []provider.MediaSource
	RankedSources    []ranking.ScoredSource
	SelectedIndex    int
	PlayerName       string
	Autoplay         bool
	LoadingProviders int
	TotalProviders   int
	FailedProvider   string
	SpinnerFrame     string
	Loading          bool // resolve in flight; empty table means "still coming", not "none"
	// BackendName maps a source Resolver to its display backend (e.g.
	// the [4KHDHub] tag behind Pengu). Missing entries render the raw
	// resolver so the table never blanks.
	BackendName map[string]string
	Width       int
	Height      int
	Accent      lipgloss.AdaptiveColor
	// Mode drives the audio column: anime audio is a sub/dub track choice
	// shown in the header, so the per-source column renders for other
	// modes only.
	Mode provider.ContentType
}

// RenderPreviewScreen renders the Preview source picker per KARI_TUI_SPEC §4.4.
func RenderPreviewScreen(data PreviewData) string {
	st := NewStyles(data.Accent)

	var rows []string

	// 1. Header (Poster on left, metadata on right if >= 80 cols and poster available)
	header := renderPreviewHeader(data, st)
	rows = append(rows, header, "")
	// 2. Sources Header Line (count or progressive loading state)
	sourcesTitle := renderSourcesTitle(data, st)
	rows = append(rows, sourcesTitle, "")

	// 4. Tabular Sources List
	sourcesTable := renderSourcesTable(data, st)
	rows = append(rows, sourcesTable)

	// 5. Failure summary closes the section (below header, table, and
	// everything between) so the layout reads finished instead of
	// interrupting the show info mid-screen.
	if failureLine := renderSourcesError(data, st); failureLine != "" {
		rows = append(rows, "", failureLine)
	}
	// Body stays left-aligned: RenderFrame already centers the
	// content column, so centering here too double-indents narrow tables
	// to the right half on wide terminals.
	return strings.Join(rows, "\n")
}

func renderPreviewHeader(data PreviewData, st Styles) string {
	var metaRows []string

	// Series Title
	metaRows = append(metaRows, st.Bold.Render(data.SeriesTitle))

	// Episode code & title
	if data.EpisodeInfo != "" {
		metaRows = append(metaRows, data.EpisodeInfo)
	}

	// Genres & Rating
	var tagParts []string
	if len(data.Genres) > 0 {
		tagParts = append(tagParts, st.Dim.Render(strings.Join(data.Genres, " · ")))
	}
	if data.Rating != "" && data.Rating != "—" {
		tagParts = append(tagParts, st.Current.Render("★ ")+data.Rating)
	}
	if len(tagParts) > 0 {
		metaRows = append(metaRows, strings.Join(tagParts, st.Dim.Render(" · ")))
	}

	// Plot synopsis
	if data.Overview != "" {
		metaRows = append(metaRows, "")
		wrapped := wrapText(data.Overview, max(30, data.Width-34), 4)
		metaRows = append(metaRows, st.Dim.Render(wrapped))
	}

	// Audio line, Subtitles line, Autoplay & Resume lines
	autoVal := "off"
	if data.Autoplay {
		autoVal = "on"
	}

	metaRows = append(metaRows, "")
	if data.AudioText != "" {
		audioFmt := data.AudioText
		if strings.HasPrefix(audioFmt, "sub") {
			audioFmt = st.Current.Render("sub") + st.Dim.Render(strings.TrimPrefix(audioFmt, "sub"))
		} else if strings.HasPrefix(audioFmt, "dub") {
			audioFmt = st.Current.Render("dub") + st.Dim.Render(strings.TrimPrefix(audioFmt, "dub"))
		}
		metaRows = append(metaRows, st.Dim.Render("audio      ")+audioFmt)
	}
	if data.SubtitlesText != "" {
		subFmt := data.SubtitlesText
		subType := data.SubtitleType
		if subType == "" && strings.Contains(subFmt, " · ") {
			parts := strings.SplitN(subFmt, " · ", 2)
			subFmt = parts[0]
			subType = parts[1]
		}
		if strings.HasSuffix(subFmt, "✓") {
			base := strings.TrimSpace(strings.TrimSuffix(subFmt, "✓"))
			subFmt = st.Current.Render(base) + " " + st.Ok.Render("✓")
		} else {
			subFmt = st.Current.Render(subFmt)
		}
		if subType != "" {
			subFmt += st.Dim.Render(" · " + subType)
		}
		metaRows = append(metaRows, st.Dim.Render("subtitles  ")+subFmt)
	}
	metaRows = append(metaRows, st.Dim.Render("autoplay   ")+st.Current.Render(autoVal))
	if data.ResumePosition != "" {
		metaRows = append(metaRows, st.Dim.Render("resume     ")+st.Current.Render(data.ResumePosition))
	}
	metaBlock := strings.Join(metaRows, "\n")

	if data.PosterBlock == "" || data.Width < 80 {
		return metaBlock
	}

	// Layout side-by-side with poster on left
	return lipgloss.JoinHorizontal(lipgloss.Top,
		data.PosterBlock,
		"    ",
		metaBlock,
	)
}

func renderSourcesTitle(data PreviewData, st Styles) string {
	if data.LoadingProviders > 0 && data.TotalProviders > 0 {
		spinner := data.SpinnerFrame
		if spinner == "" {
			spinner = "⠋"
		}
		spinner = st.Current.Render(spinner)
		return fmt.Sprintf("Sources  %s %d of %d providers · %d found", spinner, data.TotalProviders-data.LoadingProviders, data.TotalProviders, len(data.RankedSources))
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
	// Capital R: lowercase r restarts playback, R retries the
	// failed provider. The old "r retry" hint sent users into
	// restart instead of retry.
	retryText := st.Dim.Render("R retry")
	gap := max(1, data.Width-lipgloss.Width(errText)-lipgloss.Width(retryText))
	return errText + strings.Repeat(" ", gap) + retryText
}

func renderSourcesTable(data PreviewData, st Styles) string {
	if len(data.RankedSources) == 0 {
		// While providers are still resolving, an empty table means
		// "sources are on the way", not "nothing found".
		if data.Loading || data.LoadingProviders > 0 {
			sp := data.SpinnerFrame
			if sp == "" {
				sp = "⠋"
			}
			return st.Current.Render(sp) + " " + st.Dim.Render("Getting sources from providers…")
		}
		return st.Dim.Render("  No playback sources found.")
	}
	var rows []string

	// Audio column renders for non-anime modes: there it names the dubbed
	// track language per source (defaulting to English). Anime audio is a
	// sub/dub track choice shown in the header instead. Size stays out —
	// sizes are unreliable across providers.
	showAudio := data.Mode != provider.ModeAnime
	cursorH := "  "
	qualityH := fmt.Sprintf("%-12s", "quality")
	providerH := fmt.Sprintf("%-20s", "provider")
	headerRow := fmt.Sprintf("%s%s  %s", cursorH, st.Dim.Render(qualityH), st.Dim.Render(providerH))
	if showAudio {
		headerRow += "  " + st.Dim.Render(fmt.Sprintf("%-10s", "audio"))
	}
	rows = append(rows, headerRow)

	// Visible window of 5 rows, cursor kept mid-window while it moves.
	total := len(data.RankedSources)
	windowSize := 5
	startIdx := windowStart(total, data.SelectedIndex, windowSize)
	endIdx := min(total, startIdx+windowSize)

	if startIdx > 0 {
		rows = append(rows, st.Dim.Render(fmt.Sprintf("  ↑ %d more", startIdx)))
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

		// The provider column names the backend actually serving the
		// stream (e.g. 4KHDHub behind Pengu), never the bare
		// aggregator — identical qualities from one resolver would
		// otherwise render as indistinguishable rows.
		providerName := src.Resolver
		if data.BackendName != nil {
			if name, ok := data.BackendName[src.Resolver+"\x00"+src.Quality]; ok && name != "" {
				providerName = name
			}
		}
		if providerName == "" {
			providerName = "—"
		}
		providerStr := st.Dim.Render(fmt.Sprintf("%-20s", truncate(providerName, 20)))

		row := fmt.Sprintf("%s%s  %s", cursor, qualityStr, providerStr)
		if showAudio {
			audioStr := fmt.Sprintf("%-10s", truncate(formatSourceAudio(src), 10))
			if isFocused {
				row = fmt.Sprintf("%s%s  %s  %s", cursor,
					st.Current.Render(qualityStr), providerStr, st.Current.Render(audioStr))
			} else {
				row += "  " + st.Dim.Render(audioStr)
			}
		} else if isFocused {
			row = fmt.Sprintf("%s%s  %s", cursor, st.Current.Render(qualityStr), providerStr)
		}
		rows = append(rows, row)
	}

	if endIdx < total {
		rows = append(rows, st.Dim.Render(fmt.Sprintf("  ↓ %d more", total-endIdx)))
	}

	return strings.Join(rows, "\n")
}

// formatSourceAudio names the audio language for the sources table: the
// detected track language, defaulting to English for the main track so
// every row carries a value and the column never shifts.
func formatSourceAudio(src provider.MediaSource) string {
	if l := strings.TrimSpace(src.Language); l != "" {
		return lang.Name(l)
	}
	return "English"
}

func formatSourceQuality(raw string) string {
	res := ranking.ParseResolution(raw)
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
