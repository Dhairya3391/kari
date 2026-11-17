package kit

import (
	"testing"
	"time"

	"kari/internal/provider"
)

// TestNormalizeQualityCanonical proves raw upstream variants fold to the
// canonical height vocabulary.
func TestNormalizeQualityCanonical(t *testing.T) {
	cases := []struct {
		raw    string
		height int
		label  string
	}{
		{"1080p", 1080, "1080p"},
		{"FHD", 1080, "1080p"},
		{"1920x1080", 1080, "1080p"},
		{"1080p [4KHDHub] (Hindi)", 1080, "1080p"},
		{"4K [4KHDHub]", 2160, "2160p"},
		{"2160p", 2160, "2160p"},
		{"720p", 720, "720p"},
		{"Auto", 1080, "1080p"},
		{"Auto (HD1)", 1080, "1080p"},
		{"480p", 480, "480p"},
		{"", 0, "unknown"},
		{"gibberish", 0, "unknown"},
	}
	for _, tc := range cases {
		h, l := NormalizeQuality(tc.raw)
		if h != tc.height || l != tc.label {
			t.Errorf("NormalizeQuality(%q) = (%d,%q), want (%d,%q)", tc.raw, h, l, tc.height, tc.label)
		}
	}
}

// TestQualityHeightProvesCanonicalHeight proves QualityHeight is the height
// half of NormalizeQuality across the raw shapes providers emit.
func TestQualityHeightProvesCanonicalHeight(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"1080p", Quality1080},
		{"1920x1080", Quality1080},
		{"4K [4KHDHub]", Quality2160},
		{"576p PAL", Quality576},
		{"Auto", Quality1080},
		{"Auto 2160p", Quality2160},
		{"Master 4K", Quality2160},
		{"Auto 540p", 540},
		{"", QualityUnknown},
		{"gibberish", QualityUnknown},
	}
	for _, tc := range cases {
		if got := QualityHeight(tc.raw); got != tc.want {
			t.Errorf("QualityHeight(%q) = %d, want %d", tc.raw, got, tc.want)
		}
		if h, _ := NormalizeQuality(tc.raw); h != tc.want {
			t.Errorf("NormalizeQuality(%q) height = %d, want %d", tc.raw, h, tc.want)
		}
	}
}

// TestIsAdaptiveQualityProvesBareAdaptiveLabels proves adaptive words with no
// named resolution count as adaptive while resolution-carrying labels do not.
func TestIsAdaptiveQualityProvesBareAdaptiveLabels(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"Auto", true},
		{"auto", true},
		{"Master", true},
		{"Auto (Hindi)", true},
		{"Auto [Provider]", true},
		{"1080p", false},
		{"FHD", false},
		{"Auto 1080p", false},
		{"Master 4K", false},
		{"Direct", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsAdaptiveQuality(tc.raw); got != tc.want {
			t.Errorf("IsAdaptiveQuality(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// TestNormalizeQualityAbsorbedCases pins the ranking-derived cases the
// canonical parser now owns: bracket tags, 576p, and explicit auto/master
// resolutions.
func TestNormalizeQualityAbsorbedCases(t *testing.T) {
	cases := []struct {
		raw    string
		height int
		label  string
	}{
		{"2160p", 2160, "2160p"},
		{"UHD 2160p", 2160, "2160p"},
		{"QHD 1440p", 1440, "1440p"},
		{"FHD [VegaMovies]", 1080, "1080p"},
		{"576p PAL", 576, "576p"},
		{"480p SD", 480, "480p"},
		{"360p", 360, "360p"},
		{"Auto 2160p", 2160, "2160p"},
		{"Master 4K", 2160, "2160p"},
		{"Auto 540p", 540, "480p"},
		{"—", 0, "unknown"},
	}
	for _, tc := range cases {
		h, l := NormalizeQuality(tc.raw)
		if h != tc.height || l != tc.label {
			t.Errorf("NormalizeQuality(%q) = (%d,%q), want (%d,%q)", tc.raw, h, l, tc.height, tc.label)
		}
	}
}

// TestNormalizeQualityLabelConsistency proves the label always comes from the
// canonical height mapping. QualityLabelFromHeight agrees for every positive
// sub-4K height; its 4K spelling is the one display-only exception.
func TestNormalizeQualityLabelConsistency(t *testing.T) {
	raws := []string{"2160p", "1440p", "1080p", "720p", "576p", "480p", "360p", "240p", "Auto", "gibberish", ""}
	for _, raw := range raws {
		h, label := NormalizeQuality(raw)
		if want := qualityLabel(h); label != want {
			t.Errorf("NormalizeQuality(%q) label = %q, want %q", raw, label, want)
		}
		if h > 0 && h < Quality2160 {
			if want := QualityLabelFromHeight(h); label != want {
				t.Errorf("NormalizeQuality(%q) label = %q, want QualityLabelFromHeight = %q", raw, label, want)
			}
		}
	}
}

// TestNormalizeLanguagesISO proves audio/subtitle tags fold to ISO 639-1.
func TestNormalizeLanguagesISO(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"Hindi", "hi"},
		{"hi", "hi"},
		{"English", "en"},
		{"Japanese", "ja"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeAudio(tc.raw); got != tc.want {
			t.Errorf("NormalizeAudio(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		if tc.raw != "" {
			if got := NormalizeSubtitle(tc.raw); got != tc.want {
				t.Errorf("NormalizeSubtitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		}
	}
}

// TestNormalizeSourceKind proves container detection.
func TestNormalizeSourceKind(t *testing.T) {
	if got := NormalizeSourceKind("https://cdn.example.com/x.m3u8"); got != provider.SourceTypeHLS {
		t.Errorf("m3u8 = %q", got)
	}
	if got := NormalizeSourceKind("https://cdn.example.com/x.mp4"); got != provider.SourceTypeMP4 {
		t.Errorf("mp4 = %q", got)
	}
	if got := NormalizeSourceKind("hls"); got != provider.SourceTypeHLS {
		t.Errorf("hls = %q", got)
	}
}

// TestNormalizeAudioMode proves dub/sub folding.
func TestNormalizeAudioMode(t *testing.T) {
	if got := NormalizeAudioMode("Dubbed"); got != provider.AudioDub {
		t.Errorf("Dubbed = %q", got)
	}
	if got := NormalizeAudioMode("Sub"); got != provider.AudioSub {
		t.Errorf("Sub = %q", got)
	}
	if got := NormalizeAudioMode(""); got != "" {
		t.Errorf("empty = %q", got)
	}
}

// BenchmarkNormalizeQuality measures the hot quality-folding path.
func BenchmarkNormalizeQuality(b *testing.B) {
	qualities := []string{"1080p [4KHDHub] (Hindi)", "4K [VegaMovies]", "Auto (HD1)", "FHD", "720p", "unknown"}

	for i := 0; b.Loop(); i++ {
		NormalizeQuality(qualities[i%len(qualities)])
	}
}

// BenchmarkDedupeSources measures merge-time dedupe over 200 rows.
func BenchmarkDedupeSources(b *testing.B) {
	in := make([]provider.MediaSource, 200)
	for i := range in {
		in[i] = provider.MediaSource{URL: "https://cdn.example.com/x.m3u8", Quality: "1080p"}
	}

	for b.Loop() {
		DedupeSources(in)
	}
}

// TestSortByQuality proves descending canonical height with stability.
func TestSortByQuality(t *testing.T) {
	in := []provider.MediaSource{
		{URL: "https://a/1", Quality: "480p"},
		{URL: "https://a/2", Quality: "1080p"},
		{URL: "https://a/3", Quality: "Auto"},
		{URL: "https://a/4", Quality: "unknown"},
	}
	SortByQuality(in)
	if in[0].Quality != "1080p" && in[0].Quality != "Auto" {
		t.Errorf("first = %q, want 1080p-class", in[0].Quality)
	}
	if last := in[len(in)-1].Quality; last != "unknown" {
		t.Errorf("last = %q, want unknown", last)
	}
}

// TestLocalStartsAt proves zero passes through and values localize.
func TestLocalStartsAt(t *testing.T) {
	if got := LocalStartsAt(time.Time{}); !got.IsZero() {
		t.Errorf("zero must pass through, got %v", got)
	}
	utc := time.Date(2026, 9, 21, 15, 30, 0, 0, time.UTC)
	if got := LocalStartsAt(utc); got.Location() != time.Local {
		t.Errorf("location = %v, want local", got.Location())
	}
}

// TestDedupeSources proves identical URLs collapse and blanks drop.
func TestDedupeSources(t *testing.T) {
	in := []provider.MediaSource{
		{URL: "https://a/x.m3u8", Quality: "1080p"},
		{URL: "https://a/x.m3u8 ", Quality: "1080p"},
		{URL: "https://a/x.m3u8/", Quality: "720p"},
		{URL: "", Quality: "junk"},
		{URL: "https://a/x.m3u8?token=1", Quality: "1080p"},
		{URL: "https://a/x.m3u8?token=2", Quality: "1080p"},
	}
	out := DedupeSources(in)
	if len(out) != 3 {
		t.Fatalf("dedupe = %d sources, want 3 (query strings significant): %+v", len(out), out)
	}
}
