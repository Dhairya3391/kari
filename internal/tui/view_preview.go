package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/model"
)

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
	previewBlock := posterBlock(m.previewPoster, m.previewPosterUnavailable, m.imgProtocol, kittyPreviewImageID, m.imagesEnabled)

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
	case len(downloadedSubs) > 0 && m.subtitleOpID == 0:
		rows = append(rows, "", mutedStyle.Render("Subtitles"))
		for _, sub := range downloadedSubs {
			rows = append(rows, lipgloss.NewStyle().Foreground(colorSuccess).Render("✓ "+sub.Label))
		}
	case m.resolveOpID != 0 || m.subtitleOpID != 0:
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
