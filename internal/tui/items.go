package tui

import (
	"fmt"
	"kari/internal/history"
	"kari/internal/provider"
	"strings"
	"time"

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
	idxs := make([]int, len(items))
	for i := range idxs {
		idxs[i] = i
	}
	return seriesToItemsIndexed(items, idxs)
}

// seriesToItemsIndexed builds rows for a (possibly filtered) subset of
// results while preserving each row's original index, so selection always
// resolves back to the full result list.
func seriesToItemsIndexed(items []provider.SearchResult, idxs []int) []list.Item {
	out := make([]list.Item, 0, len(items))
	for pos, it := range items {
		idx := pos
		if pos < len(idxs) {
			idx = idxs[pos]
		}
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

// progressMarker renders the left-hand marker for a history entry,
// consistent across history rows and episode watermarks: a checkmark
// once effectively finished (past watchedDoneThreshold, or flagged
// Complete), the percent while partway through, and an explicit 0% at
// zero — visually distinct from the done arrow. Entries with no record
// at all keep the caller's blank default instead.
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
	default:
		return "[  0%] "
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
			titleStyle = lipgloss.NewStyle().Foreground(ColorErr)
		}

		title := lipgloss.NewStyle().Foreground(colorMuted).Render(marker) + titleStyle.Render(fmt.Sprintf("%-7s", tag)) + (func() string {
			if it.Filler {
				return lipgloss.NewStyle().Foreground(ColorErr).Render(it.Title)
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

// chaptersToItems renders manga/comic chapters as selectable rows using
// the provider-stamped display label (fractional numbers included).
func chaptersToItems(items []provider.MangaChapter) []list.Item {
	out := make([]list.Item, 0, len(items))
	for idx, it := range items {
		out = append(out, rowItem{
			title: it.DisplayLabel(),
			desc:  "",
			key:   it.ID,
			index: idx,
		})
	}
	return out
}

// splitHistoryGroups divides groups into in-progress (including
// untouched 0% rows) and finished, backing the Continue/Finished tabs.
func splitHistoryGroups(groups []history.Group) (continued, finished []history.Group) {
	for _, group := range groups {
		// Finished means everything watched and nothing in progress;
		// anything else continues.
		if group.HasComplete && !group.HasIncomplete {
			finished = append(finished, group)
		} else {
			continued = append(continued, group)
		}
	}
	return continued, finished
}

// historyGroupRow builds one flat history row: marker + kind + title,
// with status-only description (resume point, completion, time).
func historyGroupRow(group history.Group, done bool, index int) rowItem {
	entry := group.ContinueEntry
	if done {
		entry = group.FarthestComplete
	}
	marker := progressMarker(entry)
	title := lipgloss.NewStyle().Foreground(colorMuted).Render(marker) +
		lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%s · ", historyKindLabel(group.Mode, group.MediaType))) +
		group.Title
	return rowItem{
		title: title,
		desc:  historyRowStatus(group, done),
		key:   group.Key.String(),
		index: index,
	}
}

// historyTabItems builds the active tab's flat rows.
func historyTabItems(groups []history.Group, finishedTab bool) []list.Item {
	out := make([]list.Item, 0, len(groups))
	for i, group := range groups {
		out = append(out, historyGroupRow(group, finishedTab, i))
	}
	return out
}

// historyRowStatus renders the one-line status for a history row:
// resume position plus relative time for in-progress titles,
// completion plus time for finished ones. Only status and resume
// point — no percentages, counts, or repeated labels.
func historyRowStatus(group history.Group, finished bool) string {
	timePart := relativeTime(group.LastPlayed.WatchedAt)
	if finished {
		return fmt.Sprintf("Completed · %s", timePart)
	}
	entry := group.ContinueEntry
	var resume string
	switch {
	case entry.Mode == string(provider.ModeManga) && entry.PositionSecs > 0 && entry.DurationSecs > 0:
		resume = fmt.Sprintf("p%d/%d", int(entry.PositionSecs), int(entry.DurationSecs))
	case entry.PositionSecs > 0 && entry.DurationSecs > 0:
		resume = fmt.Sprintf("%s/%s", formatDuration(entry.PositionSecs), formatDuration(entry.DurationSecs))
	default:
		resume = "0%"
	}
	parts := []string{}
	if tag := historyEntryTag(entry); tag != "" {
		parts = append(parts, tag)
	}
	parts = append(parts, resume, timePart)
	return strings.Join(parts, " · ")
}

func historyEntryTag(entry history.Entry) string {
	// Manga progress keys chapters by catalog number in EpisodeTitle
	// (fractional numbers don't fit the int Episode field).
	if entry.Mode == string(provider.ModeManga) && strings.TrimSpace(entry.EpisodeTitle) != "" {
		return "Ch " + strings.TrimSpace(entry.EpisodeTitle)
	}
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
