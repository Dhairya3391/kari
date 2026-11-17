package timeouts

import (
	"testing"
	"time"
)

func TestBudgetAppliesFactorAndClamps(t *testing.T) {
	tests := []struct {
		name     string
		measured time.Duration
		ceiling  time.Duration
		want     time.Duration
	}{
		// A 7s measured P95 padded by 1.75 is 12.25s.
		{"sevenSecondBaseline", 7 * time.Second, 20 * time.Second, 12250 * time.Millisecond},
		{"ceilingWins", 30 * time.Second, 25 * time.Second, 25 * time.Second},
		{"floorWins", 100 * time.Millisecond, 10 * time.Second, minBudget},
		{"noCeiling", 2 * time.Second, 0, 3500 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Budget(tc.measured, tc.ceiling); got != tc.want {
				t.Fatalf("Budget(%v, %v) = %v, want %v", tc.measured, tc.ceiling, got, tc.want)
			}
		})
	}
}

// TestBudgetsRespectCeilings is the no-freeze guarantee: every operation
// budget derived from a measurement stays below its absolute ceiling, so no
// UI action can hang indefinitely even if a provider ignores its context.
func TestBudgetsRespectCeilings(t *testing.T) {
	tests := []struct {
		name    string
		budget  time.Duration
		ceiling time.Duration
	}{
		{"search", SearchOverall, SearchCeiling},
		{"searchPerProvider", SearchPerProvider, SearchCeiling},
		{"episodes", EpisodesOverall, EpisodeCeiling},
		{"resolve", SourceOverall, ResolveCeiling},
		{"resolvePerProvider", SourcePerProvider, ResolveCeiling},
		{"manifest", ManifestBatch, ManifestCeiling},
		{"subtitles", SubtitleFetch, SubtitleCeiling},
		{"skip", SkipBackground, SkipCeiling},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.budget > tc.ceiling {
				t.Fatalf("%s budget %v exceeds ceiling %v", tc.name, tc.budget, tc.ceiling)
			}
		})
	}
}
