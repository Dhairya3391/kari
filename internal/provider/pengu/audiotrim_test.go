package pengu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
)

// TestStreamAudioLanguageMarkers proves MovieBox-style filename markers
// feed language detection, including codes plain text misses.
func TestStreamAudioLanguageMarkers(t *testing.T) {
	mk := func(filename, desc string) penguStreamItem {
		return penguStreamItem{Name: "x", Description: desc, BehaviorHints: penguBehaviorHints{Filename: filename}}
	}
	cases := []struct {
		file string
		desc string
		want string
	}{
		{"1080pH.265DASHFrenchdubaudio~1.3Mbps.pad-MovieBox", "", "fr"},
		{"480pH.264MP4Russiansubaudio~0.6Mbps.pad-MovieBox", "", "ru"},
		{"1080pH.264MP4~2.2Mbps.pad-MovieBox", "", ""},
		{"720pH.264MP4esladubaudio~1.1Mbps.pad-MovieBox", "Audio: Spanish, English", "es"},
		{"", "Audio: Ukrainian", "uk"},
	}
	for _, tc := range cases {
		if got := streamAudioLanguage(mk(tc.file, tc.desc)); got != tc.want {
			t.Errorf("streamAudioLanguage(%q) = %q, want %q", tc.file, got, tc.want)
		}
	}
}

func TestParseStreamItemDubDisplay(t *testing.T) {
	item := penguStreamItem{
		Name: "MovieBox 1080p",
		URL:  "https://example.com/a.mp4",
		BehaviorHints: penguBehaviorHints{
			Filename: "1080pH.265DASHFrenchdubaudio~1.3Mbps.pad-MovieBox",
		},
	}
	src := parseStreamItem(item)
	if src.Language != "fr" {
		t.Errorf("Language = %q, want fr", src.Language)
	}
	if got := src.Audio.Languages; len(got) != 1 || got[0] != "fr" {
		t.Errorf("Audio.Languages = %v, want [fr]", got)
	}
	if src.Audio.Track != provider.TrackDub {
		t.Errorf("Audio.Track = %q, want dub", src.Audio.Track)
	}
	if src.Bitrate != 1300000 {
		t.Errorf("Bitrate = %d, want 1300000 from the Mbps marker", src.Bitrate)
	}
	if src.Height != 1080 {
		t.Errorf("Height = %d, want 1080", src.Height)
	}
	if src.Quality != "1080p" || src.Backend != "MovieBox" {
		t.Errorf("quality/backend = %q/%q, want 1080p/MovieBox", src.Quality, src.Backend)
	}
}

// TestResolveKeepsAllLanguagesWithTypedAudio serves MovieBox-style rows: a
// French dub, an English original, and a same-file mirror pair. Every lane is
// emitted with typed audio metadata; the central pipeline decides what to keep.
func TestResolveKeepsAllLanguagesWithTypedAudio(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	payload := `{"streams":[
		{"name":"MovieBox 1080p","url":"https://cdn.example.com/orig.mp4","behaviorHints":{"filename":"1080pH.264MP4~2.2Mbps.pad-MovieBox","videoSize":2000}},
		{"name":"MovieBox 1080p","url":"https://cdn.example.com/fr.mp4","behaviorHints":{"filename":"1080pH.264MP4Frenchdubaudio~1.3Mbps.pad-MovieBox","videoSize":1500}},
		{"name":"MovieBox 720p","url":"https://cdn.example.com/m1.mp4","behaviorHints":{"filename":"720pH.264MP4~1.0Mbps.pad-MovieBox","videoSize":1000}},
		{"name":"MovieBox 720p","url":"https://cdn.example.com/m2.mp4","behaviorHints":{"filename":"720pH.264MP4~1.0Mbps.pad-MovieBox","videoSize":1000}}
	]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{httpClient: server.Client(), configSegment: "ztest"}

	sources, err := c.ResolveSource(context.Background(), "603", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	// orig + one 720p mirror (same file/size collapses) + French dub.
	if len(sources) != 3 {
		t.Fatalf("sources = %d, want 3: %+v", len(sources), sources)
	}
	foundFrench := false
	for _, s := range sources {
		if len(s.Audio.Languages) > 0 && s.Audio.Languages[0] == "fr" {
			foundFrench = true
			if s.Audio.Track != provider.TrackDub {
				t.Errorf("French row Track = %q, want dub", s.Audio.Track)
			}
		}
		if s.Bitrate <= 0 {
			t.Errorf("source has no parsed bitrate: %+v", s)
		}
	}
	if !foundFrench {
		t.Error("provider-level filtering must no longer drop French audio")
	}
}

// TestResolveKeepsForeignAudioWithoutFilter proves the default (nil filter)
// changes nothing: everything playable is kept.
func TestResolveKeepsForeignAudioWithoutFilter(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	payload := `{"streams":[
		{"name":"MovieBox 1080p","url":"https://cdn.example.com/orig.mp4","behaviorHints":{"filename":"1080pH.264MP4~2.2Mbps.pad-MovieBox","videoSize":2000}},
		{"name":"MovieBox 1080p","url":"https://cdn.example.com/fr.mp4","behaviorHints":{"filename":"1080pH.264MP4Frenchdubaudio~1.3Mbps.pad-MovieBox","videoSize":1500}}
	]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{httpClient: server.Client(), configSegment: "ztest"}

	sources, err := c.ResolveSource(context.Background(), "603", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2: %+v", len(sources), sources)
	}
}

// TestResolveKeepsOnlyDeclaredLanguage proves a lone foreign-dub row is kept
// with its typed identity instead of being silently defaulted to English.
func TestResolveKeepsOnlyDeclaredLanguage(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	payload := `{"streams":[
		{"name":"MovieBox 1080p","url":"https://cdn.example.com/fr.mp4","behaviorHints":{"filename":"1080pH.264MP4Frenchdubaudio~1.3Mbps.pad-MovieBox","videoSize":1500}}
	]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{httpClient: server.Client(), configSegment: "ztest"}

	sources, err := c.ResolveSource(context.Background(), "603", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 || sources[0].Language != "fr" {
		t.Fatalf("expected declared French audio, got %+v", sources)
	}
	if got := sources[0].Audio.Languages; len(got) != 1 || got[0] != "fr" {
		t.Errorf("Audio.Languages = %v, want [fr]", got)
	}
	if sources[0].Audio.Track != provider.TrackDub {
		t.Errorf("Audio.Track = %q, want dub", sources[0].Audio.Track)
	}
}

func TestQualityHeight(t *testing.T) {
	cases := map[string]int{
		"1080p": 1080, "4K": 2160, "Auto": 0, "720p": 720,
	}
	for in, want := range cases {
		if got := qualityHeight(in); got != want {
			t.Errorf("qualityHeight(%q) = %d, want %d", in, got, want)
		}
	}
}
