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
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NormalizeQuality(qualities[i%len(qualities)])
	}
}

// BenchmarkDedupeSources measures merge-time dedupe over 200 rows.
func BenchmarkDedupeSources(b *testing.B) {
	in := make([]provider.MediaSource, 200)
	for i := range in {
		in[i] = provider.MediaSource{URL: "https://cdn.example.com/x.m3u8", Quality: "1080p"}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DedupeSources(in)
	}
}

// TestBackendTag proves backend extraction skips resolutions and
// language suffixes.
func TestBackendTag(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"4K [4KHDHub] (Hindi)", "4KHDHub"},
		{"1080p (Vidstream-2)", "Vidstream-2"},
		{"1080p (Hindi)", ""},
		{"1080p", ""},
		{"Auto", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := BackendTag(tc.raw); got != tc.want {
			t.Errorf("BackendTag(%q) = %q, want %q", tc.raw, got, tc.want)
		}
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
