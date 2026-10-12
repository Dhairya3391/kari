package cinecat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestCinecatCapabilities(t *testing.T) {
	c, err := NewClient(tmdb.NewKeyPool([]string{"dummy-key"}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.Alias() != "Cinecat" {
		t.Errorf("Alias() = %q, want Cinecat", c.Alias())
	}
	if c.Name() != "cinecat" {
		t.Errorf("Name() = %q, want cinecat", c.Name())
	}

	for _, expectedMode := range []provider.ContentType{provider.ModeMovies, provider.ModeTV, provider.ModeCartoon} {
		found := false
		for _, m := range c.Modes() {
			if m.Name == expectedMode && m.Priority == 2 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("mode %q missing at priority 2: %+v", expectedMode, c.Modes())
		}
	}
}

func TestCinecatResolveMovie(t *testing.T) {
	var (
		cdnCalled  bool
		subsCalled bool
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/cdn", func(w http.ResponseWriter, r *http.Request) {
		cdnCalled = true
		if r.URL.Query().Get("tmdb") != "550" || r.URL.Query().Get("type") != "movie" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		// Verify expected browser headers
		if r.Header.Get("Referer") == "" || r.Header.Get("Origin") == "" {
			http.Error(w, "missing browser headers", http.StatusForbidden)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(zcdnResponse{
			Ready: true,
			URL:   fmt.Sprintf("http://%s/play/550/master.m3u8", r.Host),
		})
	})

	mux.HandleFunc("/play/550/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		// Absolute public hosts so provider.FilterDirectSources keeps variants in tests
		// (relative refs would resolve to the httptest 127.0.0.1 host and be filtered).
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-VERSION:6
#EXT-X-STREAM-INF:BANDWIDTH=16000000,RESOLUTION=3840x2160
https://cdn.example.com/play/550/v0/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
https://cdn.example.com/play/550/v1/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720
https://cdn.example.com/play/550/v2/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1200000,RESOLUTION=852x480
https://cdn.example.com/play/550/v3/index.m3u8
`))
	})

	mux.HandleFunc("/v1/movie/550", func(w http.ResponseWriter, r *http.Request) {
		subsCalled = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"label": "English Hi", "file": "http://example.com/en.vtt"},
			{"label": "Spanish", "file": "http://example.com/es.vtt"},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}

	if !cdnCalled {
		t.Error("expected CDN endpoint to be called")
	}
	if !subsCalled {
		t.Error("expected subtitle endpoint to be called")
	}

	// 4 variants (4K, 1080p, 720p, 480p); the Auto master is the httptest
	// 127.0.0.1 URL and is dropped by FilterDirectSources as non-direct.
	if len(sources) != 4 {
		t.Fatalf("expected 4 sources, got %d", len(sources))
	}

	if sources[0].Quality != "4K (2160p)" {
		t.Errorf("sources[0].Quality = %q, want 4K (2160p)", sources[0].Quality)
	}
	if !strings.HasSuffix(sources[0].URL, "/play/550/v0/index.m3u8") {
		t.Errorf("sources[0].URL = %q, want v0/index.m3u8 suffix", sources[0].URL)
	}

	if sources[1].Quality != "1080p" {
		t.Errorf("sources[1].Quality = %q, want 1080p", sources[1].Quality)
	}
	if sources[2].Quality != "720p" {
		t.Errorf("sources[2].Quality = %q, want 720p", sources[2].Quality)
	}
	if sources[3].Quality != "480p" {
		t.Errorf("sources[3].Quality = %q, want 480p", sources[3].Quality)
	}

	// Verify subtitles attached
	for i, s := range sources {
		if len(s.Subtitles) != 2 {
			t.Errorf("sources[%d] subtitles count = %d, want 2", i, len(s.Subtitles))
		}
	}
}

func TestCinecatResolveTVEpisode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cdn", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("tmdb") != "1396" || q.Get("type") != "tv" || q.Get("season") != "2" || q.Get("episode") != "5" {
			http.Error(w, "bad TV query params", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(zcdnResponse{
			Ready: true,
			URL:   fmt.Sprintf("http://%s/play/1396-s2e5/master.m3u8", r.Host),
		})
	})

	mux.HandleFunc("/play/1396-s2e5/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-VERSION:6
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
https://cdn.example.com/play/1396-s2e5/v1/index.m3u8
`))
	})

	mux.HandleFunc("/v1/tv/1396/2/5", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"label": "English", "file": "http://example.com/en.vtt"},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "1396", provider.Episode{
		TMDBID:  1396,
		Season:  2,
		Episode: 5,
	})
	if err != nil {
		t.Fatalf("ResolveSource TV failed: %v", err)
	}

	if len(sources) != 1 {
		t.Fatalf("expected 1 source (1080p; Auto master is local mock and filtered), got %d", len(sources))
	}
}

func TestCinecatBuildingOrNotReady(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cdn", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(zcdnResponse{
			Ready:  false,
			Status: "building",
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	_, err = client.ResolveSource(context.Background(), "999", provider.Episode{TMDBID: 999})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("expected ErrNoSources on 202 building status, got: %v", err)
	}
}

func TestResolutionLabel(t *testing.T) {
	tests := []struct {
		height int
		want   string
	}{
		{2160, "4K (2160p)"},
		{1440, "1440p"},
		{1080, "1080p"},
		{720, "720p"},
		{480, "480p"},
		{360, "360p"},
		{240, "240p"},
	}

	for _, tt := range tests {
		got := resolutionLabel(tt.height)
		if got != tt.want {
			t.Errorf("resolutionLabel(%d) = %q, want %q", tt.height, got, tt.want)
		}
	}
}
