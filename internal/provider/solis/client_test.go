package solis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

type solisTestRoundTripper struct {
	target string
}

func (rt solisTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "cdn.example.com" {
		cloned := req.Clone(req.Context())
		urlCopy := *req.URL
		urlCopy.Scheme = "http"
		urlCopy.Host = rt.target
		cloned.URL = &urlCopy
		return http.DefaultTransport.RoundTrip(cloned)
	}
	return http.DefaultTransport.RoundTrip(req)
}

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
	if sources[0].Backend != "Dexter" || sources[0].Quality != "1080p" {
		t.Errorf("sources[0] quality/backend = %q/%q, want 1080p/Dexter", sources[0].Quality, sources[0].Backend)
	}
	if len(sources[0].Subtitles) != 1 {
		t.Fatalf("len(subtitles) = %d, want 1", len(sources[0].Subtitles))
	}
	if sources[0].Subtitles[0].Language != "en" {
		t.Errorf("subtitle language = %q, want en", sources[0].Subtitles[0].Language)
	}
}

func TestResolveSourceDropsExplicitWarpPlaceholder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/resolve":
			resp := apiResolveResponse{Sources: []apiSource{
				{URL: "https://cdn.example.com/blocked", Quality: "4K (Mordecai)", Type: "hls"},
				{URL: "https://cdn.example.com/playlist", Quality: "1080p (Plankton)", Type: "hls"},
			}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case "/blocked":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, "Please disable Cloudflare WARP VPN to continue")
		case "/playlist":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-VERSION:3\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithURL(tmdb.NewKeyPool([]string{"dummy-key"}), server.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	client.httpClient.Transport = solisTestRoundTripper{target: strings.TrimPrefix(server.URL, "http://")}
	sources, err := client.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("len(sources) = %d, want 1: %+v", len(sources), sources)
	}
	if sources[0].Backend != "Plankton" || sources[0].Quality != "1080p" {
		t.Errorf("source = %q %q, want Plankton 1080p", sources[0].Backend, sources[0].Quality)
	}
}

func TestResolveStreamDropsExplicitWarpPlaceholder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/resolve":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: source\ndata: {\"url\":\"https://cdn.example.com/blocked\",\"quality\":\"4K (Mordecai)\",\"type\":\"hls\"}\n\n")
			_, _ = fmt.Fprint(w, "event: source\ndata: {\"url\":\"https://cdn.example.com/playlist\",\"quality\":\"1080p (Plankton)\",\"type\":\"hls\"}\n\n")
			_, _ = fmt.Fprint(w, "event: done\ndata: {}\n\n")
		case "/blocked":
			_, _ = fmt.Fprint(w, "Please disable Cloudflare WARP VPN to continue")
		case "/playlist":
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-VERSION:3\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithURL(tmdb.NewKeyPool([]string{"dummy-key"}), server.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	client.httpClient.Transport = solisTestRoundTripper{target: strings.TrimPrefix(server.URL, "http://")}
	updates := make(chan []provider.MediaSource, 2)
	if err := client.ResolveStream(context.Background(), "550", provider.Episode{TMDBID: 550}, updates); err != nil {
		t.Fatalf("ResolveStream failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1 playable lane", len(updates))
	}
	if sources := <-updates; len(sources) != 1 || sources[0].Backend != "Plankton" {
		t.Fatalf("sources = %+v, want Plankton lane only", sources)
	}
}

func TestResolveSourceMergesPerSourceAndTopLevelSubtitles(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := apiResolveResponse{
			Sources: []apiSource{
				{
					URL:     "https://cdn.example.com/s1.m3u8",
					Quality: "1080p (Dexter)",
					Type:    "hls",
					Subtitles: []apiSubtitle{
						{URL: "https://sub.example.com/es.vtt", Language: "Spanish"},
					},
				},
			},
			Subtitles: []apiSubtitle{
				{URL: "https://sub.example.com/en.vtt", Language: "English"},
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
	if len(sources) != 1 {
		t.Fatalf("len(sources) = %d, want 1", len(sources))
	}
	if len(sources[0].Subtitles) != 2 {
		t.Fatalf("len(subtitles) = %d, want 2", len(sources[0].Subtitles))
	}
	if got := sources[0].Subtitles[0].Language; got != "es" {
		t.Errorf("first subtitle language = %q, want es", got)
	}
	if got := sources[0].Subtitles[1].Language; got != "en" {
		t.Errorf("second subtitle language = %q, want en", got)
	}
}

func TestResolveSourcePerSourceSubtitlesWithoutTopLevel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := apiResolveResponse{
			Sources: []apiSource{
				{
					URL:     "https://cdn.example.com/s1.m3u8",
					Quality: "1080p (Dexter)",
					Type:    "hls",
					Subtitles: []apiSubtitle{
						{URL: "https://sub.example.com/en.vtt", Language: "en"},
					},
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
	if len(sources) != 1 || len(sources[0].Subtitles) != 1 {
		t.Fatalf("sources/subtitles = %+v, want one source with one subtitle", sources)
	}
	if got := sources[0].Subtitles[0].Language; got != "en" {
		t.Errorf("subtitle language = %q, want en", got)
	}
}

func TestSplitSourceQuality(t *testing.T) {
	tests := []struct {
		input string
		wantQ string
		wantB string
	}{
		{"1080p (Dexter)", "1080p", "Dexter"},
		{"2160p", "2160p", ""},
		{"Auto (Adaptive)", "Auto (Adaptive)", ""},
	}
	for _, tc := range tests {
		quality, backend := splitSourceQuality(tc.input)
		if quality != tc.wantQ || backend != tc.wantB {
			t.Errorf("splitSourceQuality(%q) = %q/%q, want %q/%q", tc.input, quality, backend, tc.wantQ, tc.wantB)
		}
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
	if !errors.Is(err, provider.ErrNoSources) {
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
		if batch[0].Quality != "1080p" || batch[0].Backend != "Dexter" {
			t.Errorf("batch[0].Quality = %q", batch[0].Quality)
		}
	default:
		t.Fatal("expected update on channel")
	}
}

func TestResolveStreamMergesPerSourceAndStreamSubtitles(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		fmt.Fprint(w, "event: source\ndata: {\"url\":\"https://cdn.example.com/s1.m3u8\",\"quality\":\"1080p (Dexter)\",\"subtitles\":[{\"url\":\"https://sub.example.com/es.vtt\",\"language\":\"es\"}]}\n\n")
		flusher.Flush()

		fmt.Fprint(w, "event: subtitles\ndata: [{\"url\":\"https://sub.example.com/en.vtt\",\"language\":\"en\"}]\n\n")
		flusher.Flush()

		fmt.Fprint(w, "event: done\ndata: {\"ok\":true}\n\n")
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

	if err := c.ResolveStream(ctx, "550", provider.Episode{TMDBID: 550}, updates); err != nil {
		t.Fatalf("ResolveStream failed: %v", err)
	}

	first := recvSources(t, updates)
	if len(first) != 1 || len(first[0].Subtitles) != 1 {
		t.Fatalf("first update = %+v, want one source with one subtitle", first)
	}
	if got := first[0].Subtitles[0].Language; got != "es" {
		t.Errorf("first subtitle language = %q, want es", got)
	}

	second := recvSources(t, updates)
	if len(second) != 1 || len(second[0].Subtitles) != 2 {
		t.Fatalf("second update = %+v, want one source with two subtitles", second)
	}
	if got := second[0].Subtitles[1].Language; got != "en" {
		t.Errorf("second subtitle language = %q, want en", got)
	}
}

func TestResolveStreamKeepsStreamSubtitlesArrivingFirst(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		fmt.Fprint(w, "event: subtitles\ndata: [{\"url\":\"https://sub.example.com/en.vtt\",\"language\":\"en\"}]\n\n")
		flusher.Flush()

		fmt.Fprint(w, "event: source\ndata: {\"url\":\"https://cdn.example.com/s1.m3u8\",\"quality\":\"1080p (Dexter)\",\"subtitles\":[{\"url\":\"https://sub.example.com/es.vtt\",\"language\":\"es\"}]}\n\n")
		flusher.Flush()

		fmt.Fprint(w, "event: done\ndata: {\"ok\":true}\n\n")
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

	if err := c.ResolveStream(ctx, "550", provider.Episode{TMDBID: 550}, updates); err != nil {
		t.Fatalf("ResolveStream failed: %v", err)
	}

	batch := recvSources(t, updates)
	if len(batch) != 1 || len(batch[0].Subtitles) != 2 {
		t.Fatalf("update = %+v, want one source with two subtitles", batch)
	}
	if got := batch[0].Subtitles[0].Language; got != "es" {
		t.Errorf("first subtitle language = %q, want es", got)
	}
	if got := batch[0].Subtitles[1].Language; got != "en" {
		t.Errorf("second subtitle language = %q, want en", got)
	}
}

// recvSources reads one buffered update or fails the test instead of blocking
// forever when a regression stops the expected emission.
func recvSources(t *testing.T, updates <-chan []provider.MediaSource) []provider.MediaSource {
	t.Helper()
	select {
	case batch, ok := <-updates:
		if !ok {
			t.Fatal("updates channel closed unexpectedly")
		}
		return batch
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for update")
		return nil
	}
}
