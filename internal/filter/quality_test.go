package filter

import (
	"slices"
	"testing"

	"kari/internal/provider"
)

func TestQualityModeFromLegacy(t *testing.T) {
	cases := []struct {
		value int
		want  QualityMode
	}{
		{0, QualityAll},
		{1, QualityHighest},
		{2, QualityDataSaver},
		{3, QualityLowest},
		{10, QualityUHD},
		{11, QualityFHD},
		{12, QualityHD},
		{13, QualitySD},
		{4, QualityAll},
		{99, QualityAll},
		{-1, QualityAll},
	}
	for _, tc := range cases {
		if got := QualityModeFromLegacy(tc.value); got != tc.want {
			t.Errorf("QualityModeFromLegacy(%d) = %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestSelectTierHighest(t *testing.T) {
	cases := []struct {
		name      string
		qualities []string
		want      []string
	}{
		{
			name:      "full HD and better win over lower tiers",
			qualities: []string{"4K", "1080p", "720p", "480p"},
			want:      []string{"4K", "1080p"},
		},
		{
			name:      "best lower tier kept when nothing reaches full HD",
			qualities: []string{"720p", "480p", "360p"},
			want:      []string{"720p"},
		},
		{
			name:      "adaptive survives alongside a known lower tier",
			qualities: []string{"Auto", "720p", "360p"},
			want:      []string{"Auto", "720p"},
		},
		{
			name:      "adaptive-only set stays visible",
			qualities: []string{"Auto", "Master"},
			want:      []string{"Auto", "Master"},
		},
		{
			name:      "all unknown labels stay visible",
			qualities: []string{"Direct", "CDN"},
			want:      []string{"Direct", "CDN"},
		},
		{
			name:      "explicit resolution in adaptive label is a real tier",
			qualities: []string{"Auto 1080p", "720p"},
			want:      []string{"Auto 1080p"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := keptQualities(SelectTier(qualitySources(tc.qualities...), QualityHighest))
			if !slices.Equal(got, tc.want) {
				t.Errorf("kept %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelectTierDataSaver(t *testing.T) {
	cases := []struct {
		name      string
		qualities []string
		want      []string
	}{
		{
			name:      "drops higher tiers when a lower tier exists",
			qualities: []string{"2160p", "1080p", "720p", "480p"},
			want:      []string{"1080p", "720p", "480p"},
		},
		{
			name:      "keeps only tier when no lower tier exists",
			qualities: []string{"2160p", "1440p"},
			want:      []string{"2160p", "1440p"},
		},
		{
			name:      "adaptive survives next to a low tier",
			qualities: []string{"Auto", "2160p", "720p"},
			want:      []string{"Auto", "720p"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := keptQualities(SelectTier(qualitySources(tc.qualities...), QualityDataSaver))
			if !slices.Equal(got, tc.want) {
				t.Errorf("kept %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelectTierLowest(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "a-hd", Resolver: "a", Quality: "1080p"},
		{URL: "a-sd", Resolver: "a", Quality: "720p"},
		{URL: "b-hd", Resolver: "b", Quality: "480p"},
		{URL: "b-sd", Resolver: "b", Quality: "360p"},
	}
	got := SelectTier(sources, QualityLowest)
	if len(got) != 2 || got[0].URL != "a-sd" || got[1].URL != "b-sd" {
		t.Fatalf("lowest tier per resolver = %v, want a-sd and b-sd", sourceURLs(got))
	}
}

func TestSelectTierExplicitLadders(t *testing.T) {
	cases := []struct {
		name      string
		mode      QualityMode
		qualities []string
		want      []string
	}{
		{"uhd falls back to fhd", QualityUHD, []string{"1080p", "720p"}, []string{"1080p"}},
		{"uhd falls back to hd", QualityUHD, []string{"720p", "480p"}, []string{"720p"}},
		{"fhd prefers hd over 4k", QualityFHD, []string{"2160p", "720p"}, []string{"720p"}},
		{"fhd absorbs qhd", QualityFHD, []string{"1080p", "1440p"}, []string{"1080p", "1440p"}},
		{"hd falls back to sd before 4k", QualityHD, []string{"480p", "2160p"}, []string{"480p"}},
		{"sd selects the lowest tier", QualitySD, []string{"2160p", "1080p", "720p", "480p"}, []string{"480p"}},
		{"no matching tier keeps unknown labels", QualityUHD, []string{"Direct", "CDN"}, []string{"Direct", "CDN"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := keptQualities(SelectTier(qualitySources(tc.qualities...), tc.mode))
			if !slices.Equal(got, tc.want) {
				t.Errorf("mode %d kept %v, want %v", tc.mode, got, tc.want)
			}
		})
	}
}

func TestSelectTierAdaptiveVisibleInAllModes(t *testing.T) {
	for _, mode := range []QualityMode{
		QualityAll, QualityHighest, QualityDataSaver, QualityLowest,
		QualityUHD, QualityFHD, QualityHD, QualitySD,
	} {
		sources := []provider.MediaSource{
			{URL: "adaptive", Resolver: "a", Quality: "Auto"},
			{URL: "known", Resolver: "b", Quality: "720p"},
		}
		got := sourceURLs(SelectTier(sources, mode))
		if !slices.Equal(got, []string{"adaptive", "known"}) {
			t.Errorf("mode %d kept %v, want both adaptive and known rows", mode, got)
		}
	}
}

func TestSelectTierPerResolverGuarantee(t *testing.T) {
	cases := []struct {
		name string
		mode QualityMode
	}{
		{"highest", QualityHighest},
		{"data saver", QualityDataSaver},
		{"lowest", QualityLowest},
		{"uhd", QualityUHD},
		{"fhd", QualityFHD},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sources := []provider.MediaSource{
				{URL: "a-high", Resolver: "a", Quality: "1080p"},
				{URL: "b-low", Resolver: "b", Quality: "480p"},
			}
			got := sourceURLs(SelectTier(sources, tc.mode))
			if len(got) != 2 {
				t.Fatalf("mode %d kept %v, want one row per resolver", tc.mode, got)
			}
			if got[0] != "a-high" || got[1] != "b-low" {
				t.Errorf("mode %d kept %v, want a-high and b-low", tc.mode, got)
			}
		})
	}
}

func TestSourceHeightPrefersDeclaredField(t *testing.T) {
	cases := []struct {
		name string
		src  provider.MediaSource
		want int
	}{
		{"declared field wins", provider.MediaSource{Height: 720, Quality: "4K"}, 720},
		{"label fallback", provider.MediaSource{Quality: "1080p"}, 1080},
		{"unknown label", provider.MediaSource{Quality: "Direct"}, 0},
		{"adaptive label", provider.MediaSource{Quality: "Auto"}, 1080},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceHeight(tc.src); got != tc.want {
				t.Errorf("sourceHeight = %d, want %d", got, tc.want)
			}
		})
	}
}

func qualitySources(qualities ...string) []provider.MediaSource {
	out := make([]provider.MediaSource, len(qualities))
	for i, quality := range qualities {
		out[i] = provider.MediaSource{URL: string(rune('a' + i)), Quality: quality}
	}
	return out
}

func keptQualities(sources []provider.MediaSource) []string {
	out := make([]string, 0, len(sources))
	for _, src := range sources {
		out = append(out, src.Quality)
	}
	return out
}

func sourceURLs(sources []provider.MediaSource) []string {
	out := make([]string, 0, len(sources))
	for _, src := range sources {
		out = append(out, src.URL)
	}
	return out
}
