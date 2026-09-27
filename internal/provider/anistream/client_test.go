package anistream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
)

func newFixture(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()

	// Catalogue GraphQL at /.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "anilistId") {
			fmt.Fprint(w, `{"data":{"anime":{"id":"solo-leveling-cgjkx","anilistId":151807}}}`)
			return
		}
		http.Error(w, "unknown query", http.StatusBadRequest)
	})

	// Servers with hard/soft tips; no dub backends.
	mux.HandleFunc("/rest/api/servers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"subProviders":[
			{"id":"beep","default":true,"tip":"Soft sub, Fast"},
			{"id":"loli","default":false,"tip":"Hard sub, Fast"}
		],"dubProviders":[]}`)
	})

	// Sources per provider; loli serves a poisoned token URL.
	mux.HandleFunc("/rest/api/sources", func(w http.ResponseWriter, r *http.Request) {
		pid := r.URL.Query().Get("providerId")
		w.Header().Set("Content-Type", "application/json")
		switch pid {
		case "beep":
			fmt.Fprint(w, `{"sources":[
				{"url":"https://cdn.example.com/beep.m3u8","quality":"auto","type":"video/mpegurl"}
			],"tracks":[{"file":"https://cdn.example.com/en.vtt","label":"English"}],
			"headers":{"Referer":"https://playeng.example.com/"}}`)
		case "loli":
			fmt.Fprint(w, `{"sources":[
				{"url":"https://cdn.example.com/loli.m3u8?token=abc","quality":"auto","type":"video/mpegurl"}
			],"tracks":[],"headers":{"Referer":"https://play2.example.com/"}}`)
		default:
			fmt.Fprint(w, `{"sources":[]}`)
		}
	})

	// Master playlist for quality detection.
	mux.HandleFunc("/beep.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:RESOLUTION=1920x1080\nv.m3u8\n")
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := NewClientWithBases(nil, srv.URL, []string{srv.URL})
	if err != nil {
		t.Fatalf("NewClientWithBases: %v", err)
	}
	return c
}

func TestAnistreamCapabilities(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Alias() == "" || c.Name() != "anistream" {
		t.Errorf("identity wrong: %q %q", c.Alias(), c.Name())
	}
	if len(c.Modes()) != 1 || c.Modes()[0].Name != provider.ModeAnime {
		t.Errorf("modes = %+v, want anime only", c.Modes())
	}
	if !c.RequiresEpisodeListForMovies() {
		t.Error("movies must go through the episode flow")
	}
	if !c.Features(provider.ModeAnime).AudioSelection {
		t.Error("anime must declare audio selection")
	}
}

func TestSubTypeOf(t *testing.T) {
	if got := subTypeOf("Hard sub, Fast"); got != provider.SubTypeHard {
		t.Errorf("hard tip = %q", got)
	}
	if got := subTypeOf("Soft sub, Good"); got != provider.SubTypeSoft {
		t.Errorf("soft tip = %q", got)
	}
	if got := subTypeOf("Fast"); got != "" {
		t.Errorf("plain tip = %q, want blank", got)
	}
}

func TestSubTypeForServer(t *testing.T) {
	if got := subTypeForServer("loli", "Hard sub, Fast"); got != provider.SubTypeHard {
		t.Errorf("verified hard server = %q", got)
	}
	if got := subTypeForServer("uwu", "Hard sub, Fast"); got != "" {
		t.Errorf("unverified hard server = %q, want blank", got)
	}
	if got := subTypeForServer("beep", "Soft sub, Fast"); got != provider.SubTypeSoft {
		t.Errorf("soft server = %q", got)
	}
}

func TestAnistreamResolve(t *testing.T) {
	c := newFixture(t)

	ep := provider.Episode{ID: "watch/anistream/151807/sub/1", Episode: 1, Audio: "sub"}
	sources, err := c.ResolveSource(context.Background(), "151807", ep)
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	// beep resolves (quality detected from the playlist); loli's token
	// URL is dropped as poisoned.
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d: %+v", len(sources), sources)
	}
	s1 := sources[0]
	if !strings.Contains(s1.Quality, "auto") || !strings.Contains(s1.Quality, "beep") {
		t.Errorf("unexpected quality: %q", s1.Quality)
	}
	if s1.SubType != provider.SubTypeSoft {
		t.Errorf("subtype = %q, want soft", s1.SubType)
	}
	if s1.Referer != "https://playeng.example.com/" {
		t.Errorf("referer = %q", s1.Referer)
	}
	if len(s1.Subtitles) != 1 || s1.Subtitles[0].Language != "en" {
		t.Errorf("subtitles = %+v", s1.Subtitles)
	}
}

func TestAnistreamResolveDubMissing(t *testing.T) {
	c := newFixture(t)

	// No dub backends in the fixture: must fail loudly with the
	// audio-unavailable sentinel, never play sub audio.
	ep := provider.Episode{ID: "watch/anistream/151807/dub/1", Episode: 1, Audio: "dub"}
	_, err := c.ResolveSource(context.Background(), "151807", ep)
	if !errors.Is(err, provider.ErrAudioUnavailable) {
		t.Errorf("dub resolve err = %v, want ErrAudioUnavailable", err)
	}
}

func TestUnsupportedAnistreamServers(t *testing.T) {
	for _, id := range []string{"sora", "uwu", "SORA"} {
		if !unsupportedAnistreamServer(id) {
			t.Errorf("server %q was not filtered", id)
		}
	}
	for _, id := range []string{"beep", "zuna", "loli", "yuki"} {
		if unsupportedAnistreamServer(id) {
			t.Errorf("server %q was unexpectedly filtered", id)
		}
	}
}

func TestDetectQualityFromPlaylist(t *testing.T) {
	c := newFixture(t)
	got := c.detectQuality(context.Background(), c.bases[0]+"/beep.m3u8", "", "")
	if got != "1080p" {
		t.Errorf("detectQuality = %q, want 1080p", got)
	}
	if got := c.detectQuality(context.Background(), c.bases[0]+"/missing.m3u8", "", ""); got != "auto" {
		t.Errorf("detectQuality missing = %q, want auto", got)
	}
}
