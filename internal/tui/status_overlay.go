package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/scrobble"
)

// AnimeStatusAction identifies the user action selected in the anime status overlay.
type AnimeStatusAction int

const (
	// ActionAnimeWatching sets the anime's tracking status to CURRENT on AniList.
	ActionAnimeWatching AnimeStatusAction = iota
	// ActionAnimeRewatching sets the tracking status to REPEATING on AniList, resets progress to 0,
	// increments repeat count, and clears stale local playback history for clean rewatch resumption.
	ActionAnimeRewatching
	// ActionAnimeCompleted sets tracking status to COMPLETED and advances progress to total episodes.
	ActionAnimeCompleted
	// ActionAnimePaused sets tracking status to PAUSED (on hold).
	ActionAnimePaused
	// ActionAnimeDropped sets tracking status to DROPPED.
	ActionAnimeDropped
	// ActionAnimePlanning sets tracking status to PLANNING.
	ActionAnimePlanning
	// ActionAnimeRemoveFromAniList removes the anime list entry from the user's AniList library.
	ActionAnimeRemoveFromAniList
	// ActionAnimeResetLocalHistory deletes local playback history for this anime without modifying AniList.
	ActionAnimeResetLocalHistory
)

// AnimeStatusChoice binds an action to its display label and description.
type AnimeStatusChoice struct {
	Action AnimeStatusAction
	Label  string
	Desc   string
}

// DefaultAnimeStatusChoices enumerates the selectable actions in the anime status picker modal.
var DefaultAnimeStatusChoices = []AnimeStatusChoice{
	{Action: ActionAnimeWatching, Label: "Watching", Desc: "Set status to Watching"},
	{Action: ActionAnimeRewatching, Label: "Rewatching", Desc: "Reset progress & clear old watch history"},
	{Action: ActionAnimeCompleted, Label: "Completed", Desc: "Set status to Completed"},
	{Action: ActionAnimePaused, Label: "Paused", Desc: "Put series on hold"},
	{Action: ActionAnimeDropped, Label: "Dropped", Desc: "Drop series"},
	{Action: ActionAnimePlanning, Label: "Planning", Desc: "Plan to watch"},
	{Action: ActionAnimeRemoveFromAniList, Label: "Remove from AniList", Desc: "Delete entry from AniList library"},
	{Action: ActionAnimeResetLocalHistory, Label: "Reset Local History", Desc: "Delete local playback history"},
}

// AnimeStatusOverlayData packages the parameters needed to render the anime status overlay dialog.
type AnimeStatusOverlayData struct {
	Title         string
	AniListID     int
	Connected     bool
	Loading       bool
	CurrentEntry  *scrobble.MediaListEntry
	TotalEpisodes int
	SelectedIndex int
	Accent        lipgloss.AdaptiveColor
	Width         int
}

// RenderAnimeStatusOverlay formats the anime status picker as a centered overlay box.
func RenderAnimeStatusOverlay(data AnimeStatusOverlayData) string {
	st := NewStyles(data.Accent)
	boxW := min(62, max(44, data.Width-6))
	contentW := boxW - 6

	var rows []string

	titleStr := truncate(data.Title, contentW)
	rows = append(rows, st.Bold.Render(titleStr))

	if !data.Connected {
		rows = append(rows, st.Dim.Render("AniList: not connected (settings > accounts)"))
	} else if data.Loading {
		rows = append(rows, st.Dim.Render("AniList: loading status..."))
	} else if data.CurrentEntry == nil {
		rows = append(rows, st.Dim.Render("AniList: not in list"))
	} else {
		epPart := ""
		if data.TotalEpisodes > 0 {
			epPart = fmt.Sprintf(" (%d/%d)", data.CurrentEntry.Progress, data.TotalEpisodes)
		} else if data.CurrentEntry.Progress > 0 {
			epPart = fmt.Sprintf(" (%d)", data.CurrentEntry.Progress)
		}
		repeatPart := ""
		if data.CurrentEntry.Repeat > 0 {
			repeatPart = fmt.Sprintf(" · rewatched %d×", data.CurrentEntry.Repeat)
		}
		statusText := fmt.Sprintf("%s%s%s", data.CurrentEntry.Status, epPart, repeatPart)
		rows = append(rows, fmt.Sprintf("%s %s", st.Dim.Render("AniList:"), st.Current.Render(statusText)))
	}

	rows = append(rows, "")

	labelW := 22
	for i, choice := range DefaultAnimeStatusChoices {
		cursor := "  "
		if i == data.SelectedIndex {
			cursor = st.Cursor.Render("▌ ")
		}

		currentTag := "  "
		if data.CurrentEntry != nil {
			switch choice.Action {
			case ActionAnimeWatching:
				if data.CurrentEntry.Status == scrobble.StatusCurrent {
					currentTag = st.Ok.Render("● ")
				}
			case ActionAnimeRewatching:
				if data.CurrentEntry.Status == scrobble.StatusRepeating {
					currentTag = st.Ok.Render("● ")
				}
			case ActionAnimeCompleted:
				if data.CurrentEntry.Status == scrobble.StatusCompleted {
					currentTag = st.Ok.Render("● ")
				}
			case ActionAnimePaused:
				if data.CurrentEntry.Status == scrobble.StatusPaused {
					currentTag = st.Ok.Render("● ")
				}
			case ActionAnimeDropped:
				if data.CurrentEntry.Status == scrobble.StatusDropped {
					currentTag = st.Ok.Render("● ")
				}
			case ActionAnimePlanning:
				if data.CurrentEntry.Status == scrobble.StatusPlanning {
					currentTag = st.Ok.Render("● ")
				}
			}
		}

		labelTrunc := truncate(choice.Label, labelW)
		labelPad := strings.Repeat(" ", max(0, labelW-lipgloss.Width(labelTrunc)))
		var labelPart string
		if i == data.SelectedIndex {
			labelPart = st.Current.Render(labelTrunc) + labelPad
		} else {
			labelPart = labelTrunc + labelPad
		}

		descStr := st.Dim.Render(choice.Desc)
		rows = append(rows, fmt.Sprintf("%s%s%s%s", cursor, labelPart, currentTag, descStr))
	}

	content := strings.Join(rows, "\n")
	box := RenderOverlay(content, boxW, data.Accent)
	return box + "\n\n  " + st.Dim.Render("enter select · esc cancel")
}
