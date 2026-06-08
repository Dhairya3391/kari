package tui

import (
	"fmt"
	"strings"
	"time"

	"kari/internal/history"
	"kari/internal/model"

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

func seriesToItems(items []model.SearchResult) []list.Item {
	out := make([]list.Item, 0, len(items))
	for idx, it := range items {
		desc := strings.TrimSpace(it.Year)
		title := it.Title
		if it.Provider != "wco" {
			badge := resultTypeLabel(it)
			title = "[" + badge + "]  " + it.Title
		}

		out = append(out, rowItem{
			title: title,
			desc:  desc,
			key:   it.URL,
			index: idx,
		})
	}
	return out
}

func episodesToItems(items []model.EpisodeResult, historyStore *history.Store, seriesTitle string) []list.Item {
	out := make([]list.Item, 0, len(items))
	for idx, it := range items {
		marker := "[    ] "
		if historyStore != nil {
			entry, ok := historyStore.Get(history.EntryKey{
				Provider: it.Provider,
				Title:    seriesTitle,
				Season:   it.Season,
				Episode:  it.Number,
			})
			if ok {
				pct := int(entry.PercentComplete * 100)
				if pct > 0 {
					if pct > 100 {
						pct = 100
					}
					marker = fmt.Sprintf("[%3d%%] ", pct)
				} else if entry.Complete {
					marker = "[ ✓  ] "
				} else if entry.PositionSecs > 0 {
					marker = "[ ~  ] "
				}
			}
		}

		tag := "     "
		if it.Season > 0 && it.Number > 0 {
			tag = fmt.Sprintf("S%d E%02d", it.Season, it.Number)
		} else if it.Number > 0 {
			tag = fmt.Sprintf("E%02d", it.Number)
		} else if it.Season > 0 {
			tag = fmt.Sprintf("S%d", it.Season)
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
		if it.Kind == "movie" {
			desc = "Movie"
		}

		out = append(out, rowItem{
			title: title,
			desc:  desc,
			key:   it.URL,
			index: idx,
		})
	}
	return out
}

func historyGroupsToItems(groups []history.Group) []list.Item {
	out := make([]list.Item, 0, len(groups))
	for idx, group := range groups {
		entry := group.ContinueEntry
		marker := "[    ] "
		pct := int(entry.PercentComplete * 100)
		if pct > 0 {
			if pct > 100 {
				pct = 100
			}
			marker = fmt.Sprintf("[%3d%%] ", pct)
		} else if entry.Complete {
			marker = "[ ✓  ] "
		} else if entry.PositionSecs > 0 {
			marker = "[ ~  ] "
		}

		action := historyGroupActionLabel(group)
		title := lipgloss.NewStyle().Foreground(colorMuted).Render(marker) +
			lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%s · ", group.ProviderName)) +
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
	if mediaType == "movie" {
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
		return fmt.Sprintf("S%02dE%02d", entry.Season, entry.Episode)
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
		return fmt.Sprintf("S%02dE%02d", entry.Season, entry.Episode+1)
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
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
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
