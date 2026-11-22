package calendar

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
)

var nonAlphanumericRegex = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// NormalizeTitle prepares a title for fuzzy/canonical matching against history entries.
func NormalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonAlphanumericRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// CrossReference evaluates releases against the user's history store to populate watch progress.
func CrossReference(releases []Release, store *history.Store, now time.Time) []ReleaseItem {
	var entries []history.Entry
	if store != nil {
		entries = store.All()
	}

	byAniList := make(map[int][]history.Entry)
	byTMDB := make(map[int][]history.Entry)
	byTitle := make(map[string][]history.Entry)

	for _, e := range entries {
		if e.AniListID > 0 {
			byAniList[e.AniListID] = append(byAniList[e.AniListID], e)
		}
		if e.TMDBID > 0 {
			byTMDB[e.TMDBID] = append(byTMDB[e.TMDBID], e)
		}
		norm := NormalizeTitle(e.Title)
		if norm != "" {
			byTitle[norm] = append(byTitle[norm], e)
		}
	}

	items := make([]ReleaseItem, 0, len(releases))
	for _, rel := range releases {
		var matched []history.Entry
		if rel.AniListID > 0 && len(byAniList[rel.AniListID]) > 0 {
			matched = byAniList[rel.AniListID]
		} else if rel.TMDBID > 0 && len(byTMDB[rel.TMDBID]) > 0 {
			matched = byTMDB[rel.TMDBID]
		} else {
			norm := NormalizeTitle(rel.Title)
			if len(byTitle[norm]) > 0 {
				matched = byTitle[norm]
			} else if rel.RomajiTitle != "" {
				normRomaji := NormalizeTitle(rel.RomajiTitle)
				if len(byTitle[normRomaji]) > 0 {
					matched = byTitle[normRomaji]
				}
			}
		}

		item := buildReleaseItem(rel, matched, now)
		items = append(items, item)
	}

	return items
}

func buildReleaseItem(rel Release, matched []history.Entry, now time.Time) ReleaseItem {
	if len(matched) == 0 {
		return ReleaseItem{
			Release:            rel,
			LastWatchedSeason:  0,
			LastWatchedEpisode: 0,
			NextEpisode:        1,
			UnwatchedCount:     rel.Episode,
			Status:             StatusNotStarted,
		}
	}

	if rel.Mode == provider.ModeAnime {
		return buildAnimeReleaseItem(rel, matched, now)
	}
	return buildSeriesReleaseItem(rel, matched, now)
}

func buildAnimeReleaseItem(rel Release, matched []history.Entry, now time.Time) ReleaseItem {
	maxWatched := 0
	for _, e := range matched {
		if e.Episode <= maxWatched {
			continue
		}
		if e.Complete || e.PercentComplete >= model.CompletionThreshold {
			maxWatched = e.Episode
		} else if e.PositionSecs > 5 || e.PercentComplete > 0 {
			maxWatched = max(0, e.Episode-1)
		}
	}

	item := ReleaseItem{
		Release:            rel,
		InHistory:          true,
		LastWatchedSeason:  1,
		LastWatchedEpisode: maxWatched,
	}

	if maxWatched >= rel.Episode {
		item.Status = StatusUpToDate
		item.NextEpisode = rel.Episode
		return item
	}

	item.NextEpisode = maxWatched + 1
	item.UnwatchedCount = rel.Episode - maxWatched
	item.Status = behindOrNew(item.UnwatchedCount, rel, now)
	return item
}

func buildSeriesReleaseItem(rel Release, matched []history.Entry, now time.Time) ReleaseItem {
	maxWatchedSeason := 0
	maxWatchedEpInSeason := 0
	hasSeasonMatch := false

	for _, e := range matched {
		if e.Season == rel.Season {
			hasSeasonMatch = true
			if e.Episode > maxWatchedEpInSeason && (e.Complete || e.PercentComplete >= model.CompletionThreshold) {
				maxWatchedEpInSeason = e.Episode
			}
		}
		if e.Season > maxWatchedSeason && (e.Complete || e.PercentComplete >= model.CompletionThreshold) {
			maxWatchedSeason = e.Season
		}
	}

	item := ReleaseItem{
		Release:            rel,
		InHistory:          true,
		LastWatchedSeason:  rel.Season,
		LastWatchedEpisode: maxWatchedEpInSeason,
	}

	if !hasSeasonMatch {
		item.LastWatchedSeason = maxWatchedSeason
		item.LastWatchedEpisode = 0
		item.NextEpisode = 1
		item.UnwatchedCount = rel.Episode
		item.Status = StatusBehind
		return item
	}

	if maxWatchedEpInSeason >= rel.Episode {
		item.Status = StatusUpToDate
		item.NextEpisode = rel.Episode
		return item
	}

	item.NextEpisode = maxWatchedEpInSeason + 1
	item.UnwatchedCount = rel.Episode - maxWatchedEpInSeason
	item.Status = behindOrNew(item.UnwatchedCount, rel, now)
	return item
}

// behindOrNew distinguishes the single-episode case that airs today or later
// from a backlog the viewer has to catch up on.
func behindOrNew(unwatched int, rel Release, now time.Time) WatchStatus {
	if unwatched != 1 {
		return StatusBehind
	}
	if isSameDay(rel.AiringAt, now) || rel.AiringAt.After(now) {
		return StatusNewToday
	}
	return StatusBehind
}

// BuildWeeklySchedule arranges releases into 7 consecutive day schedules starting at weekStart.
func BuildWeeklySchedule(releases []Release, store *history.Store, weekStart, now time.Time) WeeklySchedule {
	items := CrossReference(releases, store, now)

	// Build 7 calendar days
	days := make([]DaySchedule, 7)
	startMidnight := time.Date(weekStart.Year(), weekStart.Month(), weekStart.Day(), 0, 0, 0, 0, weekStart.Location())

	for i := range 7 {
		dayDate := startMidnight.AddDate(0, 0, i)
		days[i] = DaySchedule{
			Date:         dayDate,
			DayName:      dayDate.Format("Monday"),
			ShortDayName: dayDate.Format("Mon"),
			IsToday:      isSameDay(dayDate, now),
			Releases:     nil,
		}
	}

	for _, it := range items {
		relDay := it.Release.AiringAt.In(weekStart.Location())
		relMidnight := time.Date(relDay.Year(), relDay.Month(), relDay.Day(), 0, 0, 0, 0, weekStart.Location())

		diffDays := int(relMidnight.Sub(startMidnight).Hours() / 24)
		if diffDays >= 0 && diffDays < 7 {
			days[diffDays].Releases = append(days[diffDays].Releases, it)
		}
	}

	// Sort each day's releases: in-history items first, then by AiringAt, then Title
	for i := range days {
		sort.Slice(days[i].Releases, func(a, b int) bool {
			itemA := days[i].Releases[a]
			itemB := days[i].Releases[b]

			if itemA.InHistory != itemB.InHistory {
				return itemA.InHistory // Watching shows first!
			}
			if itemA.Release.AiringAt.Equal(itemB.Release.AiringAt) {
				return itemA.Release.Title < itemB.Release.Title
			}
			return itemA.Release.AiringAt.Before(itemB.Release.AiringAt)
		})
	}

	return WeeklySchedule{
		Days:      days,
		UpdatedAt: time.Now(),
	}
}

// StartOfWeek returns Monday 00:00:00 of the week containing t.
func StartOfWeek(t time.Time) time.Time {
	loc := t.Location()
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday as 7th day
	}
	daysToMonday := weekday - 1
	monday := t.AddDate(0, 0, -daysToMonday)
	return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, loc)
}

func isSameDay(t1, t2 time.Time) bool {
	y1, m1, d1 := t1.Date()
	y2, m2, d2 := t2.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}
