package ranking

// Regression test for the audit's known bug: empty or unknown resolution
// strings used to evaluate to 1080p (the "auto" branch matched them),
// which broke lowest-quality ranking by sorting unknowns above real
// low resolutions. Empty/unknown must resolve to 0 and rank last under
// QualityLowest.

import (
	"testing"

	"kari/internal/provider"
)

func TestParseResolution_EmptyResolutionRegression(t *testing.T) {
	for _, raw := range []string{"", "unknown", "Unknown", "—", "   "} {
		if got := ParseResolution(raw); got != 0 {
			t.Errorf("ParseResolution(%q) = %d, want 0", raw, got)
		}
	}
}

func TestRankSources_LowestPutsUnknownLast(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "unknown", Resolver: "P1", Quality: ""},
		{URL: "low", Resolver: "P2", Quality: "480p"},
	}
	ranked := RankSources(sources, Criteria{Mode: provider.ModeTV, QualityMode: QualityLowest})
	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked sources, got %d", len(ranked))
	}
	if ranked[0].Source.URL != "low" {
		t.Errorf("expected 480p first under Lowest, got %q", ranked[0].Source.URL)
	}
	if ranked[1].Source.URL != "unknown" {
		t.Errorf("expected unknown resolution last under Lowest, got %q", ranked[1].Source.URL)
	}
}
