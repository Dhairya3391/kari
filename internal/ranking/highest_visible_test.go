package ranking

import (
	"testing"

	"kari/internal/provider"
)

// KeepHighestVisible applies the Highest-mode display rule independently to
// each provider, preserving lower-tier fallbacks from other providers.
func TestKeepHighestVisible(t *testing.T) {
	cases := []struct {
		name      string
		qualities []string
		want      []string
	}{
		{
			name:      "fhd and 4k stay, hd and below hidden",
			qualities: []string{"4K", "1080p", "720p", "480p", "360p"},
			want:      []string{"4K", "1080p"},
		},
		{
			name:      "qhd counts as fhd or better",
			qualities: []string{"1440p", "720p"},
			want:      []string{"1440p"},
		},
		{
			name:      "auto master parses as fhd and stays",
			qualities: []string{"Auto", "720p", "360p"},
			want:      []string{"Auto"},
		},
		{
			name:      "no fhd keeps highest available tier",
			qualities: []string{"720p", "480p", "360p"},
			want:      []string{"720p"},
		},
		{
			name:      "single low source kept",
			qualities: []string{"360p"},
			want:      []string{"360p"},
		},
		{
			name:      "unparseable hidden when fhd present",
			qualities: []string{"1080p", "Direct"},
			want:      []string{"1080p"},
		},
		{
			name:      "all unparseable keeps everything",
			qualities: []string{"Direct", "CDN"},
			want:      []string{"Direct", "CDN"},
		},
		{
			name:      "empty stays empty",
			qualities: nil,
			want:      nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sources []provider.MediaSource
			for i, q := range tc.qualities {
				sources = append(sources, provider.MediaSource{
					URL:     string(rune('a' + i)),
					Quality: q,
				})
			}
			got := KeepHighestVisible(sources)
			if len(got) != len(tc.want) {
				t.Fatalf("kept %d rows %v, want %d %v", len(got), qualities(got), len(tc.want), tc.want)
			}
			for i, q := range tc.want {
				if got[i].Quality != q {
					t.Errorf("row %d = %q, want %q (all kept: %v)", i, got[i].Quality, q, qualities(got))
				}
			}
		})
	}
}

func TestKeepHighestVisibleRetainsEachProvidersBestTier(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "https://bingr.example/hd.m3u8", Resolver: "bingr", Quality: "720p"},
		{URL: "https://solis.example/fhd.m3u8", Resolver: "solis", Quality: "1080p"},
		{URL: "https://solis.example/hd.m3u8", Resolver: "solis", Quality: "720p"},
		{URL: "https://bingr.example/sd.m3u8", Resolver: "bingr", Quality: "480p"},
	}
	got := KeepHighestVisible(sources)
	if len(got) != 2 {
		t.Fatalf("kept %d rows, want each provider's best tier: %+v", len(got), got)
	}
	if got[0].Resolver != "bingr" || got[0].Quality != "720p" || got[1].Resolver != "solis" || got[1].Quality != "1080p" {
		t.Errorf("kept rows = %+v, want Bingr 720p and Solis 1080p", got)
	}
}

func qualities(sources []provider.MediaSource) []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.Quality)
	}
	return out
}
