package service

import (
	"sort"
	"strings"
	"time"

	"kari/internal/provider"
)

// SortLiveResults orders live results to match the live screen's sections.
func SortLiveResults(results []provider.SearchResult, now time.Time) {
	sort.SliceStable(results, func(i, j int) bool {
		leftGroup := liveResultGroup(results[i], now)
		rightGroup := liveResultGroup(results[j], now)
		if leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		left, right := results[i], results[j]
		if leftGroup == 4 {
			leftCategory, rightCategory := firstGenre(left.Genres), firstGenre(right.Genres)
			if leftCategory != rightCategory {
				return strings.ToLower(leftCategory) < strings.ToLower(rightCategory)
			}
		} else {
			if left.StartsAt.IsZero() != right.StartsAt.IsZero() {
				return !left.StartsAt.IsZero()
			}
			if !left.StartsAt.IsZero() && !left.StartsAt.Equal(right.StartsAt) {
				return left.StartsAt.Before(right.StartsAt)
			}
		}
		return strings.ToLower(left.Title) < strings.ToLower(right.Title)
	})
}

func liveResultGroup(result provider.SearchResult, now time.Time) int {
	if result.Group == "Channel" {
		return 4
	}
	if !result.StartsAt.IsZero() {
		if !result.StartsAt.After(now) {
			if now.Sub(result.StartsAt) <= provider.LiveEventMaxDuration {
				return 0
			}
			return 3
		}
		if sameDate(result.StartsAt, now) {
			return 1
		}
		if sameDate(result.StartsAt, now.AddDate(0, 0, 1)) {
			return 2
		}
		return 3
	}
	if result.Live {
		return 0
	}
	return 3
}

func sameDate(left, right time.Time) bool {
	leftYear, leftMonth, leftDay := left.Date()
	rightYear, rightMonth, rightDay := right.Date()
	return leftYear == rightYear && leftMonth == rightMonth && leftDay == rightDay
}

func firstGenre(genres []string) string {
	if len(genres) == 0 {
		return ""
	}
	return genres[0]
}
