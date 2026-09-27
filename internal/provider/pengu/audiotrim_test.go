package pengu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if !strings.Contains(src.Quality, "(French)") {
		t.Errorf("Quality = %q, want French marker", src.Quality)
	}
}

func TestLanguageEnabledSemantics(t *testing.T) {
	c := &Client{}
	if !c.languageEnabled("fr", "French") {
		t.Error("nil filter must enable everything")
	}
	c2 := &Client{langFilter: map[string]bool{"fr": false}}
	if c2.languageEnabled("fr", "French") {
		t.Error("explicit false must disable")
	}
	if !c2.languageEnabled("hi", "Hindi") {
		t.Error("missing key must stay enabled")
	}
}

// TestResolveFiltersForeignAudio serves MovieBox-style rows: a French dub,
// an English original, and a same-file mirror pair. With French disabled,
// only the original and one mirror survive.
func TestResolveFiltersForeignAudio(t *testing.T) {
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
	c := &Client{httpClient: server.Client(), configSegment: "ztest", langFilter: map[string]bool{"fr": false}}

	sources, err := c.ResolveSource(context.Background(), "603", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	// orig + one 720p mirror (same file/size collapses); French dropped.
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2: %+v", len(sources), sources)
	}
	for _, s := range sources {
		if strings.Contains(s.Quality, "French") {
			t.Errorf("disabled French audio leaked: %+v", s)
		}
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

// TestResolveFallsBackWhenAllFiltered ensures a title available only in a
// disabled language still plays instead of erroring.
func TestResolveFallsBackWhenAllFiltered(t *testing.T) {
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
	c := &Client{httpClient: server.Client(), configSegment: "ztest", langFilter: map[string]bool{"fr": false}}

	sources, err := c.ResolveSource(context.Background(), "603", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 || !strings.Contains(sources[0].Quality, "French") {
		t.Fatalf("expected fallback French audio, got %+v", sources)
	}
}

func TestQualityHeight(t *testing.T) {
	cases := map[string]int{
		"1080p [MovieBox]": 1080, "4K [Cinejoy]": 2160, "Auto": 0, "720p": 720,
	}
	for in, want := range cases {
		if got := qualityHeight(in); got != want {
			t.Errorf("qualityHeight(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestMirrorBackend(t *testing.T) {
	if got := mirrorBackend("1080p [MovieBox] (Hindi)"); got != "MovieBox" {
		t.Errorf("mirrorBackend = %q, want MovieBox", got)
	}
	if got := mirrorBackend("1080p"); got != "" {
		t.Errorf("mirrorBackend bare = %q, want empty", got)
	}
}
