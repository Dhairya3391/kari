package calendar

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"kari/internal/history"
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
		badge := fmt.Sprintf("Ep %d", rel.Episode)
		if rel.Mode == provider.ModeTV && rel.Season > 0 {
			badge = fmt.Sprintf("S%02dE%02d", rel.Season, rel.Episode)
		}
		return ReleaseItem{
			Release:            rel,
			InHistory:          false,
			LastWatchedSeason:  0,
			LastWatchedEpisode: 0,
			NextEpisode:        1,
			UnwatchedCount:     rel.Episode,
			Status:             StatusNotStarted,
			Badge:              badge,
		}
	}

	isAnime := rel.Mode == provider.ModeAnime

	if isAnime {
		return buildAnimeReleaseItem(rel, matched, now)
	}
	return buildSeriesReleaseItem(rel, matched, now)
}

func buildAnimeReleaseItem(rel Release, matched []history.Entry, now time.Time) ReleaseItem {
	maxWatched := 0
	hasIncomplete := false

	for _, e := range matched {
		if e.Episode > maxWatched {
			if e.Complete || e.PercentComplete >= 0.85 {
				maxWatched = e.Episode
			} else if e.PositionSecs > 5 || e.PercentComplete > 0 {
				hasIncomplete = true
				if e.Episode > maxWatched {
					maxWatched = e.Episode - 1
					if maxWatched < 0 {
						maxWatched = 0
					}
				}
			}
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
		item.UnwatchedCount = 0
		item.Badge = fmt.Sprintf("✓ Ep %d watched", rel.Episode)
		return item
	}

	item.NextEpisode = maxWatched + 1
	item.UnwatchedCount = rel.Episode - maxWatched

	if item.UnwatchedCount == 1 {
		if isSameDay(rel.AiringAt, now) {
			item.Status = StatusNewToday
			item.Badge = fmt.Sprintf("▶ Ep %d (new today)", rel.Episode)
		} else if rel.AiringAt.After(now) {
			item.Status = StatusNewToday
			item.Badge = fmt.Sprintf("▶ Ep %d (airs %s)", rel.Episode, rel.AiringAt.Local().Format("15:04"))
		} else {
			item.Status = StatusBehind
			item.Badge = fmt.Sprintf("▶ Ep %d (1 to watch)", rel.Episode)
		}
	} else {
		item.Status = StatusBehind
		item.Badge = fmt.Sprintf("Left: Ep %d-%d (%d left)", item.NextEpisode, rel.Episode, item.UnwatchedCount)
	}

	_ = hasIncomplete
	return item
}

func buildSeriesReleaseItem(rel Release, matched []history.Entry, now time.Time) ReleaseItem {
	maxWatchedSeason := 0
	maxWatchedEpInSeason := 0
	hasSeasonMatch := false

	for _, e := range matched {
		if e.Season == rel.Season {
			hasSeasonMatch = true
			if e.Episode > maxWatchedEpInSeason && (e.Complete || e.PercentComplete >= 0.85) {
				maxWatchedEpInSeason = e.Episode
			}
		}
		if e.Season > maxWatchedSeason && (e.Complete || e.PercentComplete >= 0.85) {
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
		// Viewer watched earlier seasons, but hasn't started this season
		item.LastWatchedSeason = maxWatchedSeason
		item.LastWatchedEpisode = 0
		item.NextEpisode = 1
		item.UnwatchedCount = rel.Episode
		item.Status = StatusBehind
		item.Badge = fmt.Sprintf("Left: S%02dE01-E%02d (%d left)", rel.Season, rel.Episode, rel.Episode)
		return item
	}

	if maxWatchedEpInSeason >= rel.Episode {
		item.Status = StatusUpToDate
		item.NextEpisode = rel.Episode
		item.UnwatchedCount = 0
		item.Badge = fmt.Sprintf("✓ S%02dE%02d watched", rel.Season, rel.Episode)
		return item
	}

	item.NextEpisode = maxWatchedEpInSeason + 1
	item.UnwatchedCount = rel.Episode - maxWatchedEpInSeason

	if item.UnwatchedCount == 1 {
		if isSameDay(rel.AiringAt, now) {
			item.Status = StatusNewToday
			item.Badge = fmt.Sprintf("▶ S%02dE%02d (new today)", rel.Season, rel.Episode)
		} else if rel.AiringAt.After(now) {
			item.Status = StatusNewToday
			item.Badge = fmt.Sprintf("▶ S%02dE%02d (airs %s)", rel.Season, rel.Episode, rel.AiringAt.Local().Format("15:04"))
		} else {
			item.Status = StatusBehind
			item.Badge = fmt.Sprintf("▶ S%02dE%02d (1 to watch)", rel.Season, rel.Episode)
		}
	} else {
		item.Status = StatusBehind
		item.Badge = fmt.Sprintf("Left: S%02dE%02d-E%02d (%d left)", rel.Season, item.NextEpisode, rel.Episode, item.UnwatchedCount)
	}

	return item
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
