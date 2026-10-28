package cno

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestCnoModesAndMetadata(t *testing.T) {
	client, err := NewClientWithURL(&tmdb.KeyPool{}, "https://cno.to")
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	if client.Name() != "cno" {
		t.Errorf("Name() = %q, want %q", client.Name(), "cno")
	}
	if client.Alias() != "Cno" {
		t.Errorf("Alias() = %q, want %q", client.Alias(), "Cno")
	}

	modes := client.Modes()
	if len(modes) != 3 {
		t.Fatalf("Modes() count = %d, want 3", len(modes))
	}

	expectedModes := map[provider.ContentType]bool{
		provider.ModeMovies:  true,
		provider.ModeTV:      true,
		provider.ModeCartoon: true,
	}

	for _, m := range modes {
		if !expectedModes[m.Name] {
			t.Errorf("unexpected mode: %s", m.Name)
		}
		if m.Priority != 2 {
			t.Errorf("mode %s priority = %d, want 2", m.Name, m.Priority)
		}
	}
}

func TestCnoResolveMovie(t *testing.T) {
	s20Manifest := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-STREAM-INF:BANDWIDTH=10222043,RESOLUTION=1920x1080
https://v.cno.to/hls/test/1080p/index.m3u8`

	s21Manifest := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=16258889,RESOLUTION=3840x1604
https://v.lumenbox.top/v/token/3840x1604/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6125483,RESOLUTION=1920x1080
https://v.lumenbox.top/v/token/1920x1080/index.m3u8`

	subListJSON := `{"files":[{"id":"os-123","lang":"en","label":"English"},{"id":"os-456","lang":"es","label":"Spanish"}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/subs/list/movie/6977"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(subListJSON))
		case strings.Contains(r.URL.Path, "/api/s/20/movie/6977"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(s20Manifest))
		case strings.Contains(r.URL.Path, "/api/s/21/movie/6977"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(s21Manifest))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "6977", provider.Episode{TMDBID: 6977})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}

	// Should have 1 from s20 + 2 from s21 = 3 sources
	if len(sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(sources))
	}

	// Verify s20 source
	s20 := sources[0]
	if !strings.Contains(s20.URL, "https://v.cno.to/hls/test/1080p/index.m3u8") {
		t.Errorf("s20 URL mismatch: %s", s20.URL)
	}
	if len(s20.Subtitles) != 2 {
		t.Errorf("expected 2 subtitles, got %d", len(s20.Subtitles))
	}
	if s20.Subtitles[0].Language != "en" {
		t.Errorf("sub 0 language = %s, want en", s20.Subtitles[0].Language)
	}

	// Verify s21 source
	s21_4k := sources[1]
	if s21_4k.Quality != "2160p" {
		t.Errorf("s21_4k quality = %s, want 2160p", s21_4k.Quality)
	}
	if len(s21_4k.ExtraArgs) == 0 || !strings.Contains(s21_4k.ExtraArgs[0], "extension_picky=0") {
		t.Errorf("missing expected extra args on s21: %v", s21_4k.ExtraArgs)
	}
}

func TestCnoResolveStream(t *testing.T) {
	s20Manifest := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=6015211,RESOLUTION=1920x1080
https://v.cno.to/hls/breakingbad/1080p/index.m3u8`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/subs/list/tv/1396/1/2"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"files":[{"id":"os-tv1","lang":"en","label":"English"}]}`))
		case strings.Contains(r.URL.Path, "/api/s/20/tv/1396/1/2"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(s20Manifest))
		case strings.Contains(r.URL.Path, "/api/s/21/tv/1396/1/2"):
			http.Error(w, "server 21 offline", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	updates := make(chan []provider.MediaSource, 2)
	err = client.ResolveStream(context.Background(), "1396", provider.Episode{
		TMDBID:  1396,
		Season:  1,
		Episode: 2,
	}, updates)
	close(updates)

	if err != nil {
		t.Fatalf("ResolveStream failed: %v", err)
	}

	var all []provider.MediaSource
	for batch := range updates {
		all = append(all, batch...)
	}

	if len(all) != 1 {
		t.Fatalf("expected 1 source from streaming updates, got %d", len(all))
	}
	if !strings.Contains(all[0].URL, "https://v.cno.to/hls/breakingbad/1080p/index.m3u8") {
		t.Errorf("TV URL mismatch: %s", all[0].URL)
	}
}

func TestCnoResolveTV(t *testing.T) {
	s20Manifest := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=6015211,RESOLUTION=1920x1080
https://v.cno.to/hls/breakingbad/1080p/index.m3u8`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/subs/list/tv/1396/1/2"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"files":[{"id":"os-tv1","lang":"en","label":"English"}]}`))
		case strings.Contains(r.URL.Path, "/api/s/20/tv/1396/1/2"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(s20Manifest))
		case strings.Contains(r.URL.Path, "/api/s/21/tv/1396/1/2"):
			http.Error(w, "server 21 offline", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "1396", provider.Episode{
		TMDBID:  1396,
		Season:  1,
		Episode: 2,
	})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}

	if len(sources) != 1 {
		t.Fatalf("expected 1 source from server 20, got %d", len(sources))
	}
	if !strings.Contains(sources[0].URL, "https://v.cno.to/hls/breakingbad/1080p/index.m3u8") {
		t.Errorf("TV URL mismatch: %s", sources[0].URL)
	}
	if len(sources[0].Subtitles) != 1 || sources[0].Subtitles[0].Language != "en" {
		t.Errorf("TV subtitle mismatch: %v", sources[0].Subtitles)
	}
}

func TestCnoResolveFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	_, err = client.ResolveSource(context.Background(), "999999", provider.Episode{TMDBID: 999999})
	if err == nil {
		t.Fatal("expected error on total upstream failure, got nil")
	}
}

func TestParseCoordinates(t *testing.T) {
	// Movie from mediaID
	tmdbID, mediaType, season, ep, err := parseCoordinates("550", provider.Episode{})
	if err != nil || tmdbID != 550 || mediaType != provider.MediaTypeMovie || season != 1 || ep != 1 {
		t.Errorf("movie from mediaID failed: tmdb=%d type=%s s=%d ep=%d err=%v", tmdbID, mediaType, season, ep, err)
	}

	// TV from episode struct
	tmdbID, mediaType, season, ep, err = parseCoordinates("", provider.Episode{
		TMDBID:  1396,
		Season:  2,
		Episode: 5,
	})
	if err != nil || tmdbID != 1396 || mediaType != provider.MediaTypeTV || season != 2 || ep != 5 {
		t.Errorf("tv from episode struct failed: tmdb=%d type=%s s=%d ep=%d err=%v", tmdbID, mediaType, season, ep, err)
	}

	// Invalid ID
	_, _, _, _, err = parseCoordinates("not-an-id", provider.Episode{})
	if err == nil {
		t.Error("expected error for invalid ID, got nil")
	}
}
