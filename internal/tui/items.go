package tui

import (
	"fmt"
	"strings"
	"time"

	"kari/internal/history"
	"kari/internal/provider"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

type rowItem struct {
	title string
	desc  string
	key   string
	index int
}

func (i rowItem) Title() string       { return i.title }
func (i rowItem) Description() string { return i.desc }
func (i rowItem) FilterValue() string { return i.title + " " + i.desc }

func seriesToItems(items []provider.SearchResult) []list.Item {
	out := make([]list.Item, 0, len(items))
	for idx, it := range items {
		desc := strings.TrimSpace(it.Year)
		badge := resultTypeLabel(it)
		title := "[" + badge + "]  " + it.Title

		out = append(out, rowItem{
			title: title,
			desc:  desc,
			key:   it.ID,
			index: idx,
		})
	}
	return out
}

// watchedDoneThreshold is how far into an episode or movie counts as
// "done" for display purposes — past this point the progress marker shows
// a checkmark instead of a specific percentage, since a number like 99%
// (or 96%, 97%...) reads as unfinished when really it's just credits.
const watchedDoneThreshold = 95

// progressMarker renders the left-hand marker for a history entry: a
// checkmark once it's effectively finished (either past
// watchedDoneThreshold, or already flagged Complete by the history store's
// own — lower — completion threshold), a percentage while partway through,
// a squiggle for a bare resume position with no known percent, or blank.
func progressMarker(entry history.Entry) string {
	pct := int(entry.PercentComplete * 100)
	switch {
	case pct >= watchedDoneThreshold || entry.Complete:
		return "[  ✓ ] "
	case pct > 0:
		if pct > 100 {
			pct = 100
		}
		return fmt.Sprintf("[%3d%%] ", pct)
	case entry.PositionSecs > 0:
		return "[  ~ ] "
	default:
		return "[    ] "
	}
}

func episodesToItems(items []provider.Episode, historyStore *history.Store, seriesTitle string, mode provider.ContentType, mediaType string, selected map[int]struct{}) []list.Item {
	out := make([]list.Item, 0, len(items))
	for idx, it := range items {
		marker := "[    ] "
		if _, sel := selected[idx]; sel {
			marker = "[sel] "
		} else if historyStore != nil {
			entry, ok := historyStore.Get(history.EntryKey{
				Title:     seriesTitle,
				Mode:      string(mode),
				MediaType: mediaType,
				Season:    it.Season,
				Episode:   it.Episode,
			})
			if ok {
				marker = progressMarker(entry)
			}
		}

		tag := "       "
		if it.Season > 0 && it.Episode > 0 {
			tag = fmt.Sprintf("S%02d E%02d", it.Season, it.Episode)
		} else if it.Episode > 0 {
			tag = fmt.Sprintf("E%02d", it.Episode)
		} else if it.Season > 0 {
			tag = fmt.Sprintf("S%02d", it.Season)
		}

		// Apply filler color if episode is marked as filler
		titleColor := colorMuted
		titleStyle := lipgloss.NewStyle().Foreground(titleColor)
		if it.Filler {
			titleStyle = lipgloss.NewStyle().Foreground(colorWarn)
		}

		title := lipgloss.NewStyle().Foreground(colorMuted).Render(marker) + titleStyle.Render(fmt.Sprintf("%-7s", tag)) + (func() string {
			if it.Filler {
				return lipgloss.NewStyle().Foreground(colorWarn).Render(it.Title)
			}
			return it.Title
		}())
		desc := ""
		if mediaType == provider.MediaTypeMovie {
			desc = "Movie"
		}

		out = append(out, rowItem{
			title: title,
			desc:  desc,
			key:   it.ID,
			index: idx,
		})
	}
	return out
}

func historyGroupsToItems(groups []history.Group) []list.Item {
	out := make([]list.Item, 0, len(groups))
	for idx, group := range groups {
		entry := group.ContinueEntry
		marker := progressMarker(entry)

		action := historyGroupActionLabel(group)
		title := lipgloss.NewStyle().Foreground(colorMuted).Render(marker) +
			lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%s · ", historyKindLabel(group.Mode, group.MediaType))) +
			group.Title

		if action != "" {
			title += lipgloss.NewStyle().Foreground(colorMuted).Render(" · " + action)
		}

		lastTag := historyEntryTag(group.LastPlayed)
		desc := fmt.Sprintf("Last played %s", relativeTime(group.LastPlayed.WatchedAt))
		if lastTag != "" {
			desc += " · " + lastTag
		}
		if group.WatchedCount > 0 {
			desc += fmt.Sprintf(" · %d watched", group.WatchedCount)
		}

		if entry.PositionSecs > 0 && entry.DurationSecs > 0 {
			pos := formatDuration(entry.PositionSecs)
			dur := formatDuration(entry.DurationSecs)
			desc += fmt.Sprintf(" · resume %s / %s", pos, dur)
		} else if entry.PositionSecs > 0 {
			pos := formatDuration(entry.PositionSecs)
			desc += fmt.Sprintf(" · resume %s in", pos)
		}

		out = append(out, rowItem{
			title: title,
			desc:  desc,
			key:   group.Key.String(),
			index: idx,
		})
	}
	return out
}

func historyGroupActionLabel(group history.Group) string {
	mediaType := strings.ToLower(strings.TrimSpace(group.MediaType))
	if mediaType == provider.MediaTypeMovie {
		if group.HasIncomplete {
			return "Resume"
		}
		return "Replay"
	}
	if group.HasIncomplete {
		if tag := historyEntryTag(group.ContinueEntry); tag != "" {
			return "Resume " + tag
		}
		return "Resume"
	}
	if group.HasComplete {
		if tag := nextEpisodeTag(group.FarthestComplete); tag != "" {
			return "Continue " + tag
		}
		return "Continue"
	}
	return "Replay"
}

func historyEntryTag(entry history.Entry) string {
	if entry.Season > 0 && entry.Episode > 0 {
		return fmt.Sprintf("S%02d E%02d", entry.Season, entry.Episode)
	}
	if entry.Episode > 0 {
		return fmt.Sprintf("E%02d", entry.Episode)
	}
	if entry.Season > 0 {
		return fmt.Sprintf("S%02d", entry.Season)
	}
	return ""
}

func nextEpisodeTag(entry history.Entry) string {
	if entry.Episode <= 0 {
		return ""
	}
	if entry.Season > 0 {
		return fmt.Sprintf("S%02d E%02d", entry.Season, entry.Episode+1)
	}
	return fmt.Sprintf("E%02d", entry.Episode+1)
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		w := int(d.Hours() / (24 * 7))
		if w == 1 {
			return "1w ago"
		}
		return fmt.Sprintf("%dw ago", w)
	case d < 365*24*time.Hour:
		m := int(d.Hours() / (24 * 30))
		if m == 1 {
			return "1mo ago"
		}
		return fmt.Sprintf("%dmo ago", m)
	default:
		return t.Format("2006-01-02")
	}
}

func formatDuration(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
