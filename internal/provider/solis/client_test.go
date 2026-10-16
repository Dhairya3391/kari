package solis

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestClientMetadata(t *testing.T) {
	pool := tmdb.NewKeyPool([]string{"dummy-key"})
	c, err := NewClientWithURL(pool, "http://localhost:8080")
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	if got := c.Name(); got != "solis" {
		t.Errorf("Name() = %q, want solis", got)
	}
	if got := c.Alias(); got != "Solis" {
		t.Errorf("Alias() = %q, want Solis", got)
	}

	modes := c.Modes()
	if len(modes) != 3 {
		t.Fatalf("len(Modes()) = %d, want 3", len(modes))
	}
	for _, m := range modes {
		if m.Priority != 2 {
			t.Errorf("mode %s priority = %d, want 2", m.Name, m.Priority)
		}
	}
}

func TestResolveSourceSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/resolve" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("id") != "550" {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}

		resp := apiResolveResponse{
			Sources: []apiSource{
				{
					URL:     "https://cdn.example.com/s1.m3u8",
					Quality: "1080p (Dexter)",
					Type:    "hls",
					Referer: "https://example.com/",
				},
				{
					URL:     "https://cdn.example.com/s2.m3u8",
					Quality: "1080p (Courage)",
					Type:    "hls",
				},
			},
			Subtitles: []apiSubtitle{
				{
					URL:      "https://sub.example.com/en.vtt",
					Language: "English",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	pool := tmdb.NewKeyPool([]string{"dummy-key"})
	c, err := NewClientWithURL(pool, ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	sources, err := c.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}

	if len(sources) != 2 {
		t.Fatalf("len(sources) = %d, want 2", len(sources))
	}
	if sources[0].Quality != "1080p (Dexter)" {
		t.Errorf("sources[0].Quality = %q", sources[0].Quality)
	}
	if len(sources[0].Subtitles) != 1 {
		t.Fatalf("len(subtitles) = %d, want 1", len(sources[0].Subtitles))
	}
	if sources[0].Subtitles[0].Language != "en" {
		t.Errorf("subtitle language = %q, want en", sources[0].Subtitles[0].Language)
	}
}

func TestResolveSourceNoSources(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := apiResolveResponse{
			Sources:   []apiSource{},
			Subtitles: []apiSubtitle{},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	pool := tmdb.NewKeyPool([]string{"dummy-key"})
	c, err := NewClientWithURL(pool, ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	_, err = c.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
	if err != provider.ErrNoSources {
		t.Errorf("err = %v, want ErrNoSources", err)
	}
}

func TestResolveStreamSSE(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		fmt.Fprintf(w, "event: source\ndata: {\"url\":\"https://cdn.example.com/s1.m3u8\",\"quality\":\"1080p (Dexter)\"}\n\n")
		flusher.Flush()

		fmt.Fprintf(w, "event: subtitles\ndata: [{\"url\":\"https://sub.example.com/en.vtt\",\"language\":\"en\"}]\n\n")
		flusher.Flush()

		fmt.Fprintf(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	}))
	defer ts.Close()

	pool := tmdb.NewKeyPool([]string{"dummy-key"})
	c, err := NewClientWithURL(pool, ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	updates := make(chan []provider.MediaSource, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = c.ResolveStream(ctx, "550", provider.Episode{TMDBID: 550}, updates)
	if err != nil {
		t.Fatalf("ResolveStream failed: %v", err)
	}

	select {
	case batch := <-updates:
		if len(batch) == 0 {
			t.Fatal("empty batch received")
		}
		if batch[0].Quality != "1080p (Dexter)" {
			t.Errorf("batch[0].Quality = %q", batch[0].Quality)
		}
	default:
		t.Fatal("expected update on channel")
	}
}
