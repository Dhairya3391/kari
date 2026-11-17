package service

import (
	"fmt"
	"time"

	"kari/internal/provider"
)

// LiveSectionKind names the high-level sections of the live guide.
type LiveSectionKind int

const (
	// LiveSectionNow holds streams that are airing at the reference time.
	LiveSectionNow LiveSectionKind = iota
	// LiveSectionUpcoming holds streams with a known future start time.
	LiveSectionUpcoming
	// LiveSectionChannels holds always-on channel entries.
	LiveSectionChannels
)

// LiveRowKind distinguishes a selectable result from a dim category heading.
type LiveRowKind int

const (
	// LiveRowResult is a selectable stream whose Index addresses the result slice.
	LiveRowResult LiveRowKind = iota
	// LiveRowCategory is a heading that groups consecutive results.
	LiveRowCategory
)

// LiveCounts is the per-section total shown in the live guide status strip.
type LiveCounts struct {
	Now      int
	Upcoming int
	Channels int
}

// LiveSection is one prepared section of the live guide. Start is the index of
// its first selectable result in the source slice, used by section-jump keys.
type LiveSection struct {
	Kind  LiveSectionKind
	Title string
	Count int
	Start int
}

// LiveRow is one row of the guide's flat display order. Category rows carry a
// Label and Count; result rows carry an Index into the source result slice.
type LiveRow struct {
	Kind    LiveRowKind
	Index   int
	Label   string
	Count   int
	Section LiveSectionKind
}

// LiveGuide is the prepared, immutable layout consumed by the live renderer.
type LiveGuide struct {
	Rows        []LiveRow
	Sections    []LiveSection
	Counts      LiveCounts
	Total       int
	SelectedRow int // flat row position of the selected result, or -1
}

// BuildLiveGuide partitions results into temporal sections and interleaves
// category headings in the same order the cursor traverses results, so a
// heading never reorders the list beneath the selection. A single now value
// classifies every result, keeping one frame internally consistent.
func BuildLiveGuide(results []provider.SearchResult, selected int, now time.Time) LiveGuide {
	guide := LiveGuide{Total: len(results), SelectedRow: -1}
	bySection := make([][]int, 3)

	for i := range results {
		kind := liveSectionFor(results[i], now)
		bySection[kind] = append(bySection[kind], i)
		switch kind {
		case LiveSectionNow:
			guide.Counts.Now++
		case LiveSectionUpcoming:
			guide.Counts.Upcoming++
		case LiveSectionChannels:
			guide.Counts.Channels++
		}
	}

	titles := [...]string{"Live now", "Upcoming", "Channels"}
	for kind := LiveSectionNow; kind <= LiveSectionChannels; kind++ {
		indices := bySection[kind]
		if len(indices) == 0 {
			continue
		}
		guide.Sections = append(guide.Sections, LiveSection{
			Kind:  kind,
			Title: titles[kind],
			Count: len(indices),
			Start: indices[0],
		})

		category := ""
		for pos, idx := range indices {
			name := LiveCategory(results[idx])
			if name != category {
				category = name
				run := 0
				for j := pos; j < len(indices) && LiveCategory(results[indices[j]]) == name; j++ {
					run++
				}
				// A heading above a single row costs a line without adding
				// grouping value; only multi-row runs earn a subheading.
				if run > 1 {
					guide.Rows = append(guide.Rows, LiveRow{
						Kind:    LiveRowCategory,
						Label:   name,
						Count:   run,
						Section: kind,
					})
				}
			}
			guide.Rows = append(guide.Rows, LiveRow{
				Kind:    LiveRowResult,
				Index:   idx,
				Section: kind,
			})
			if idx == selected {
				guide.SelectedRow = len(guide.Rows) - 1
			}
		}
	}
	return guide
}

// LiveCategory returns the dim grouping label for a live result.
func LiveCategory(result provider.SearchResult) string {
	if result.Group != "" && result.Group != "Channel" {
		return result.Group
	}
	if name := firstGenre(result.Genres); name != "" {
		return name
	}
	return "Other channels"
}

// LiveTimeLabel returns the compact status cell for a live result.
func LiveTimeLabel(result provider.SearchResult, now time.Time) string {
	if result.Group == "Channel" {
		return "24/7"
	}
	if result.Live {
		return "LIVE"
	}
	if !result.StartsAt.IsZero() {
		return result.StartsAt.Format("15:04")
	}
	return "—"
}

// LiveElapsedLabel returns the on-air duration for an event that already
// started. It is empty for channels and for events that have yet to begin.
func LiveElapsedLabel(result provider.SearchResult, now time.Time) string {
	if result.StartsAt.IsZero() {
		return ""
	}
	elapsed := now.Sub(result.StartsAt)
	if elapsed < 0 {
		return ""
	}
	if elapsed >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(elapsed.Hours()), int(elapsed.Minutes())%60)
	}
	return fmt.Sprintf("%02d:%02d", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
}

// LiveUpNext returns the time and title of the earliest scheduled result that
// has not started yet, skipping the selected row. Channels sit between events
// in the catalog order, so the search is not limited to adjacent rows.
func LiveUpNext(results []provider.SearchResult, selected int, now time.Time) (string, string) {
	best := -1
	for i := range results {
		if i == selected || results[i].StartsAt.IsZero() || !results[i].StartsAt.After(now) {
			continue
		}
		if best < 0 || results[i].StartsAt.Before(results[best].StartsAt) {
			best = i
		}
	}
	if best < 0 {
		return "", ""
	}
	layout := "15:04"
	if !sameDate(results[best].StartsAt, now) {
		layout = "Mon 15:04"
	}
	return results[best].StartsAt.Format(layout), results[best].Title
}

func liveSectionFor(result provider.SearchResult, now time.Time) LiveSectionKind {
	switch liveResultGroup(result, now) {
	case 0:
		return LiveSectionNow
	case 4:
		return LiveSectionChannels
	default:
		return LiveSectionUpcoming
	}
}
