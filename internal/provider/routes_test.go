package provider

import "testing"

// TestBoostRankPreferredBackends pins the user's priority order: Anikoto
// Vidstream-2 tops anime whenever present, Movy.sx tops movies/tv/cartoon.
func TestBoostRankPreferredBackends(t *testing.T) {
	if got := BoostRank(ModeAnime, "anikoto", "1080p (Vidstream-2)"); got != 0 {
		t.Errorf("anime anikoto vidstream-2 rank = %d, want 0", got)
	}
	if got := BoostRank(ModeAnime, "anikoto", "1080p (HD-1)"); got != -1 {
		t.Errorf("anime anikoto HD-1 rank = %d, want -1 (no boost)", got)
	}
	if got := BoostRank(ModeAnime, "anicine", "1080p (Megaplay)"); got != -1 {
		t.Errorf("anime anicine rank = %d, want -1 (quality order decides)", got)
	}
	for _, mode := range []ContentType{ModeMovies, ModeTV, ModeCartoon} {
		if got := BoostRank(mode, "movysx", "1080p"); got != 0 {
			t.Errorf("%s movysx rank = %d, want 0", mode, got)
		}
		if got := BoostRank(mode, "pengu", "1080p [MovieBox]"); got != -1 {
			t.Errorf("%s pengu rank = %d, want -1", mode, got)
		}
	}
}

// TestRoutesCoverKnownProviders ensures every provider serving a mode has
// a routes-table entry (timeouts/strategies), so new registrations can't
// silently fall back to defaults.
func TestRoutesCoverKnownProviders(t *testing.T) {
	known := map[ContentType][]string{
		ModeAnime:    {"anicine", "anikoto", "miruro", "anilight"},
		ModeMovies:   {"anicine", "movysx", "pengu", "moovie"},
		ModeTV:       {"anicine", "movysx", "pengu", "moovie"},
		ModeCartoon:  {"anicine", "movysx", "pengu", "moovie"},
		ModeLive:     {"pengu"},
		ModeManga:    {"weebcentral"},
		ModeJellyfin: {"jellyfin"},
	}
	for mode, want := range known {
		have := make(map[string]bool)
		for _, r := range RoutesFor(mode) {
			have[r.Provider] = true
		}
		for _, p := range want {
			if !have[p] {
				t.Errorf("mode %q missing route for %q", mode, p)
			}
		}
	}
}
