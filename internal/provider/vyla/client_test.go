package vyla

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestVylaMetadata(t *testing.T) {
	keyPool := tmdb.NewKeyPool([]string{"dummy-key"})
	c, err := NewClientWithURL(keyPool, "http://localhost:7860")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if c.Name() != "vyla" {
		t.Errorf("expected name vyla, got %s", c.Name())
	}
	if c.Alias() != "Vyla" {
		t.Errorf("expected alias Vyla, got %s", c.Alias())
	}

	modes := c.Modes()
	if len(modes) != 2 {
		t.Fatalf("expected 2 modes, got %d", len(modes))
	}
	if modes[0].Name != provider.ModeMovies || modes[1].Name != provider.ModeTV {
		t.Errorf("unexpected modes: %+v", modes)
	}
}

func TestVylaResolveStreamMovieSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/movie" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("id") != "155" {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		// Event 1: meta with subtitles
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"meta\",\"meta\":{\"id\":155,\"title\":\"The Dark Knight\"},\"subtitles\":[{\"label\":\"English\",\"file\":\"https://sub.vdrk.site/v1/en.vtt\",\"type\":\"vtt\"}]}\n\n")
		flusher.Flush()

		// Event 2: source 1 (MP4)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"source\",\"source\":{\"source\":\"xdownloader\",\"url\":\"https://noob.broggl.farm/api?url=https://test.com/stream.mp4\",\"label\":\"X-Downloader 1080p\",\"quality\":\"1080p\",\"type\":\"mp4\",\"headers\":{\"Referer\":\"https://films365.org\"}}}\n\n")
		flusher.Flush()

		// Event 3: source 2 (HLS)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"source\",\"source\":{\"source\":\"111477\",\"url\":\"https://noob.broggl.farm/api?url=https://test.com/master.m3u8\",\"label\":\"111477 4K Remux\",\"quality\":\"2160p\",\"type\":\"hls\"}}\n\n")
		flusher.Flush()

		// Event 4: done
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"done\",\"total\":2}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	keyPool := tmdb.NewKeyPool([]string{"dummy-key"})
	client, err := NewClientWithHTTPClient(keyPool, server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTPClient failed: %v", err)
	}

	updates := make(chan []provider.MediaSource, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.ResolveStream(ctx, "155", provider.Episode{TMDBID: 155}, updates)
	if err != nil {
		t.Fatalf("ResolveStream returned error: %v", err)
	}
	close(updates)

	var received []provider.MediaSource
	for batch := range updates {
		received = append(received, batch...)
	}

	if len(received) < 2 {
		t.Fatalf("expected at least 2 sources, got %d", len(received))
	}

	// First source checks
	s1 := received[0]
	if s1.Type != provider.SourceTypeMP4 {
		t.Errorf("expected MP4 type, got %s", s1.Type)
	}
	if s1.Quality != "1080P" || s1.Backend != "XDownloader" {
		t.Errorf("quality/backend = %s/%s, want 1080P/XDownloader", s1.Quality, s1.Backend)
	}
	if s1.Referer != "https://films365.org" {
		t.Errorf("expected referer https://films365.org, got %s", s1.Referer)
	}
	if len(s1.Subtitles) == 0 || s1.Subtitles[0].Language != "en" {
		t.Errorf("expected English subtitles normalized to en, got %+v", s1.Subtitles)
	}

	// Second source checks
	s2 := received[1]
	if s2.Type != provider.SourceTypeHLS {
		t.Errorf("expected HLS type, got %s", s2.Type)
	}
	if s2.Quality != "2160P" || s2.Backend != "111477" {
		t.Errorf("quality/backend = %s/%s, want 2160P/111477", s2.Quality, s2.Backend)
	}
}
func TestVylaResolveSourceTV(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tv" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("id") != "1396" || r.URL.Query().Get("season") != "2" || r.URL.Query().Get("episode") != "5" {
			http.Error(w, "bad coords", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"source\",\"source\":{\"source\":\"thugtv\",\"url\":\"https://noob.broggl.farm/api?url=https://test.com/ep.m3u8\",\"label\":\"ThugTV (1080p)\",\"quality\":\"1080p\",\"type\":\"hls\"}}\n\n")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"done\",\"total\":1}\n\n")
	}))
	defer server.Close()

	keyPool := tmdb.NewKeyPool([]string{"dummy-key"})
	client, err := NewClientWithHTTPClient(keyPool, server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTPClient failed: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "1396", provider.Episode{
		TMDBID:  1396,
		Season:  2,
		Episode: 5,
	})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}

	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if sources[0].Quality != "1080P" || sources[0].Backend != "ThugTV" {
		t.Errorf("quality/backend = %s/%s, want 1080P/ThugTV", sources[0].Quality, sources[0].Backend)
	}
}

func TestVylaHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer server.Close()

	keyPool := tmdb.NewKeyPool([]string{"dummy-key"})
	client, err := NewClientWithHTTPClient(keyPool, server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTPClient failed: %v", err)
	}

	_, err = client.ResolveSource(context.Background(), "155", provider.Episode{TMDBID: 155})
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
}
