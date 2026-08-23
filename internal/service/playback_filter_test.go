package service

import (
	"testing"

	"kari/internal/provider"
)

func TestSourceQuality(t *testing.T) {
	tests := []struct {
		label string
		want  int
	}{
		{"1080p", 1080},
		{"[X] 720p Hindi", 720},
		{"2160p", 2160},
		{"4k HDR", 2160},
		{"UHD blur", 2160},
		{"480", 480},  // bare integer form
		{"Auto", 0},   // unparseable
		{"HLS", 0},    // unparseable
		{"dcloud", 0}, // CDN label, no resolution
		{"", 0},
	}
	for _, tt := range tests {
		if got := SourceQuality(tt.label); got != tt.want {
			t.Errorf("SourceQuality(%q)=%d want %d", tt.label, got, tt.want)
		}
	}
}

func TestFilterPlaybackIndicesLanguageFilter(t *testing.T) {
	playback := []provider.MediaSource{
		{URL: "a", Quality: "1080p English", Language: "English"},
		{URL: "b", Quality: "1080p Hindi", Language: "Hindi"},
	}
	// The filter map holds every known language; only explicitly disabled
	// codes drop sources.
	got := FilterPlaybackIndices(playback, 0, map[string]bool{"English": false, "Hindi": true})
	if len(got) != 1 || playback[got[0]].URL != "b" {
		t.Fatalf("language filter wrong: %v", got)
	}
	// Empty/nil filter keeps everything.
	if got = FilterPlaybackIndices(playback, 0, nil); len(got) != 2 {
		t.Fatalf("nil filter should keep all, got %v", got)
	}
	// Case-insensitive match against the filter keys.
	got = FilterPlaybackIndices(playback, 0, map[string]bool{"english": false})
	if len(got) != 1 || playback[got[0]].URL != "b" {
		t.Fatalf("case-insensitive language match failed: %v", got)
	}
}

func TestFilterPlaybackIndicesQualityModes(t *testing.T) {
	playback := []provider.MediaSource{
		{URL: "hi", Quality: "1080p"},
		{URL: "mid", Quality: "720p"},
		{URL: "lo", Quality: "480p"},
	}

	urls := func(idx []int) []string {
		out := make([]string, 0, len(idx))
		for _, i := range idx {
			out = append(out, playback[i].URL)
		}
		return out
	}

	// Highest mode keeps only the max tier.
	if got := urls(FilterPlaybackIndices(playback, 1, nil)); len(got) != 1 || got[0] != "hi" {
		t.Fatalf("highest mode wrong: %v", got)
	}
	// Lowest mode keeps only the min tier.
	if got := urls(FilterPlaybackIndices(playback, 3, nil)); len(got) != 1 || got[0] != "lo" {
		t.Fatalf("lowest mode wrong: %v", got)
	}
	// Data-saver (2) keeps everything below max.
	got := urls(FilterPlaybackIndices(playback, 2, nil))
	if len(got) != 2 || got[0] != "mid" || got[1] != "lo" {
		t.Fatalf("data-saver mode wrong: %v", got)
	}
}

func TestFilterPlaybackIndicesGuaranteesPerResolver(t *testing.T) {
	// A resolver whose only source is far below the requested tier must
	// still survive the filter.
	playback := []provider.MediaSource{
		{URL: "big-1080", Quality: "1080p", Resolver: "strong"},
		{URL: "tiny-240", Quality: "240p", Resolver: "weak"},
	}
	got := FilterPlaybackIndices(playback, 1, nil)
	if len(got) != 2 {
		t.Fatalf("weak resolver dropped entirely: %v", got)
	}
}

func TestFilterPlaybackSourcesMatchesIndices(t *testing.T) {
	playback := []provider.MediaSource{
		{URL: "keep", Quality: "1080p"},
		{URL: "drop", Quality: "240p"},
	}
	got := FilterPlaybackSources(playback, 1, nil)
	if len(got) != 1 || got[0].URL != "keep" {
		t.Fatalf("FilterPlaybackSources wrong: %+v", got)
	}
}
