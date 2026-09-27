package service

import (
	"testing"

	"kari/internal/provider"
	"kari/internal/ranking"
)

func TestSourceQuality(t *testing.T) {
	tests := []struct {
		label string
		want  int
	}{
		{"1080p", 1080},
		{"FHD", 1080},
		{"FHD [4KHDHub]", 1080},
		{"4K", 2160},
		{"4K [4KHDHub]", 2160},
		{"HD", 720},
		{"HD [VidFast]", 720},
		{"SD", 480},
		{"SD [VAPlayer]", 480},
		{"QHD", 1440},
		{"1080p [VegaMovies]", 1080},
		{"1080p [HDHub4u] (Hindi)", 1080},
		{"720p [VAPlayer]", 720},
		{"[X] 720p Hindi", 720},
		{"2160p", 2160},
		{"4k HDR", 2160},
		{"UHD blur", 2160},
		{"480", 480},   // bare integer form
		{"Auto", 1080}, // adaptive HLS master: every rendition up to FHD
		{"Auto (HD1)", 1080},
		{"Auto (l)", 1080},
		{"1080p (Vidstream-2)", 1080},
		{"720p (Server)", 720},
		{"Embed (x)", 0}, // embed frame, no resolution info
		{"Direct", 0},
		{"4K [Movy: Server]", 2160},
		{"1080p [Movy: Server] (Hindi)", 1080},
		{"4K [Src]", 2160},
		{"720p [Src] (Hindi)", 720},
		{"360p", 360},
		{"576p", 576},
		{"480p", 480},
		{"1440p", 1440},
		{"Live", 0},     // live event, no fixed resolution
		{"Jellyfin", 0}, // direct server stream, no label
		{"Cam", 0},      // ungraded upstream label
		{"4K 9.80 GB", 2160},
		{"—", 0},
		{"Unknown", 0},
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

// TestSourceQualityAgreesWithRanking pins the contract between downloads
// and Preview: every quality label observed across providers must parse
// identically in service.SourceQuality and ranking.ParseResolution.
func TestSourceQualityAgreesWithRanking(t *testing.T) {
	labels := []string{
		"1080p", "FHD", "FHD [4KHDHub]", "4K", "4K [4KHDHub]", "HD",
		"HD [VidFast]", "SD", "SD [VAPlayer]", "QHD", "1080p [VegaMovies]",
		"1080p [HDHub4u] (Hindi)", "720p [VAPlayer]", "[X] 720p Hindi",
		"2160p", "4k HDR", "UHD blur", "480", "Auto", "Auto (HD1)",
		"Auto (l)", "1080p (Vidstream-2)", "720p (Server)", "Embed (x)",
		"Direct", "4K [Movy: Server]", "1080p [Movy: Server] (Hindi)",
		"4K [Src]", "720p [Src] (Hindi)", "360p", "576p", "480p",
		"1440p", "240p", "Live", "Jellyfin", "Cam", "4K 9.80 GB",
		"—", "Unknown", "HLS", "dcloud", "",
	}
	for _, label := range labels {
		if got, want := SourceQuality(label), ranking.ParseResolution(label); got != want {
			t.Errorf("parsers disagree on %q: service=%d ranking=%d", label, got, want)
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

func TestFilterPlaybackIndicesMatchesPenguLanguageCodes(t *testing.T) {
	playback := []provider.MediaSource{
		{URL: "hindi", Quality: "1080p", Language: "hi"},
		{URL: "english", Quality: "1080p", Language: "en"},
	}

	got := FilterPlaybackIndices(playback, 0, map[string]bool{"hi": true, "en": false})
	if len(got) != 1 || playback[got[0]].URL != "hindi" {
		t.Fatalf("Pengu language filter wrong: %v", got)
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

func TestFilterPlaybackIndicesResolutionModes(t *testing.T) {
	playback := []provider.MediaSource{
		{URL: "src-4k", Quality: "4K (2160p)", Resolver: "A"},
		{URL: "src-fhd", Quality: "1080p FHD", Resolver: "A"},
		{URL: "src-hd", Quality: "720p HD", Resolver: "A"},
		{URL: "src-sd", Quality: "480p SD", Resolver: "A"},
	}

	// 4K (mode 10)
	got4K := FilterPlaybackSources(playback, 10, nil)
	if len(got4K) != 1 || got4K[0].URL != "src-4k" {
		t.Errorf("4K filter expected src-4k, got %+v", got4K)
	}

	// FHD (mode 11)
	gotFHD := FilterPlaybackSources(playback, 11, nil)
	if len(gotFHD) != 1 || gotFHD[0].URL != "src-fhd" {
		t.Errorf("FHD filter expected src-fhd, got %+v", gotFHD)
	}

	// HD (mode 12)
	gotHD := FilterPlaybackSources(playback, 12, nil)
	if len(gotHD) != 1 || gotHD[0].URL != "src-hd" {
		t.Errorf("HD filter expected src-hd, got %+v", gotHD)
	}

	// SD (mode 13) with 480p
	gotSD := FilterPlaybackSources(playback, 13, nil)
	if len(gotSD) != 1 || gotSD[0].URL != "src-sd" {
		t.Errorf("SD filter expected src-sd, got %+v", gotSD)
	}

	// SD (mode 13) with only 360p or 240p available
	lowPlayback := []provider.MediaSource{
		{URL: "src-1080", Quality: "1080p", Resolver: "A"},
		{URL: "src-360", Quality: "360p", Resolver: "A"},
	}
	got360 := FilterPlaybackSources(lowPlayback, 13, nil)
	if len(got360) != 1 || got360[0].URL != "src-360" {
		t.Errorf("SD filter expected 360p when 480p absent, got %+v", got360)
	}

	// Quality fallback when requested tier is absent on all providers:
	// Requesting 4K when only 1080p and 720p exist falls back to highest available (1080p)
	midPlayback := []provider.MediaSource{
		{URL: "src-720", Quality: "720p", Resolver: "A"},
		{URL: "src-1080", Quality: "1080p", Resolver: "B"},
	}
	gotFallback4K := FilterPlaybackSources(midPlayback, 10, nil)
	if len(gotFallback4K) != 1 || gotFallback4K[0].URL != "src-1080" {
		t.Errorf("4K fallback expected highest available 1080p, got %+v", gotFallback4K)
	}

	// Multiple providers: provider A has 720p, provider B has 1080p, user requests 1080p
	multiProvider := []provider.MediaSource{
		{URL: "prov-a-720", Quality: "720p", Resolver: "ProvA"},
		{URL: "prov-b-1080", Quality: "1080p", Resolver: "ProvB"},
	}
	got1080 := FilterPlaybackSources(multiProvider, 11, nil)
	if len(got1080) != 1 || got1080[0].URL != "prov-b-1080" {
		t.Errorf("expected 1080p from ProvB, got %+v", got1080)
	}
}

// whose quality labels carry no tier: they must survive every mode so a
// guaranteed candidate always exists for downloads.
func TestFilterPlaybackIndicesUnparseableLabels(t *testing.T) {
	direct := []provider.MediaSource{
		{URL: "a", Quality: "Direct", Resolver: "x"},
		{URL: "b", Quality: "Jellyfin", Resolver: "y"},
	}
	for mode := 1; mode <= 3; mode++ {
		if got := FilterPlaybackIndices(direct, mode, nil); len(got) != 2 {
			t.Errorf("mode %d: all-untiered list must survive: %v", mode, got)
		}
	}
}

func TestContainsSourceIgnoresCosmeticURLDifferences(t *testing.T) {
	existing := []provider.MediaSource{{URL: "https://cdn.example.com/hls/master.m3u8"}}
	dupes := []provider.MediaSource{
		{URL: "https://cdn.example.com/hls/master.m3u8/"},
		{URL: "  https://cdn.example.com/hls/master.m3u8  "},
	}
	for _, d := range dupes {
		if !containsSource(existing, d) {
			t.Errorf("should dedup %q", d.URL)
		}
	}
	other := provider.MediaSource{URL: "https://cdn.example.com/hls/master.m3u8?token=abc"}
	if containsSource(existing, other) {
		t.Errorf("query-string tokens are significant, must not dedup: %q", other.URL)
	}
}
