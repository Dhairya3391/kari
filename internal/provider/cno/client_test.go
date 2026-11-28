package cno

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	const movieDetail = `{"sources":[` +
		`{"kind":"embed","url":"https://example.test/embed"},` +
		`{"kind":"hls","url":"/api/s/21/movie/6977?v=9","label":"Server 21"}]}`
	const hlsManifest = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=16258889,RESOLUTION=3840x2160
https://v.lumenbox.top/v/token/3840x2160/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6125483,RESOLUTION=1920x1080
https://v.lumenbox.top/v/token/1920x1080/index.m3u8`
	const subListJSON = `{"files":[{"id":"os-123","lang":"en","label":"English"},{"id":"os-456","lang":"es","label":"Spanish"}]}`

	var sawVersionedHLS, sawDeadServer, sawSameOrigin bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/movie/6977":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(movieDetail))
		case strings.Contains(r.URL.Path, "/api/s/20/"):
			sawDeadServer = true
			http.NotFound(w, r)
		case r.URL.Path == "/api/s/21/movie/6977":
			sawVersionedHLS = r.URL.Query().Get("v") == "9"
			sawSameOrigin = r.Header.Get("Sec-Fetch-Site") == "same-origin"
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(hlsManifest))
		case strings.Contains(r.URL.Path, "/api/subs/list/movie/6977"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(subListJSON))
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

	if sawDeadServer {
		t.Error("the retired server 20 endpoint must not be requested")
	}
	if !sawVersionedHLS {
		t.Error("HLS path from the detail listing was not used verbatim")
	}
	if !sawSameOrigin {
		t.Error("HLS request must carry Sec-Fetch-Site: same-origin; the nodes reject cross-site-looking requests")
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 HLS sources, got %d", len(sources))
	}

	first := sources[0]
	if first.Quality != "2160p" || first.Height != 2160 || first.Bitrate != 16258889 {
		t.Errorf("4K metadata mismatch: %+v", first)
	}
	if first.Backend != "Server 21" {
		t.Errorf("backend = %q, want %q", first.Backend, "Server 21")
	}
	if len(first.ExtraArgs) == 0 || !strings.Contains(first.ExtraArgs[0], "extension_picky=0") {
		t.Errorf("missing expected extra args: %v", first.ExtraArgs)
	}
	for _, s := range sources {
		if len(s.Subtitles) != 2 || s.Subtitles[0].Language != "en" || s.Subtitles[1].Language != "es" {
			t.Errorf("subtitle mismatch on %s: %v", s.URL, s.Subtitles)
		}
	}
}

func TestCnoResolveHLSFallback(t *testing.T) {
	const (
		okManifest = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=6125483,RESOLUTION=1920x1080
https://v.lumenbox.top/v/token/1080p/index.m3u8`
		notOnServer = `{"error":"Not on this server"}`
		twoMasters  = `{"sources":[` +
			`{"kind":"hls","url":"/api/s/21/movie/6977?v=9","label":"Server 21"},` +
			`{"kind":"hls","url":"/api/s/22/movie/6977?v=9","label":"Server 22"}]}`
		deadMasters = `{"sources":[` +
			`{"kind":"hls","url":"/api/s/21/movie/6977?v=9","label":"Server 21"},` +
			`{"kind":"hls","url":"/api/s/23/movie/6977?v=8","label":"Server 23"}]}`
		bodyOnlyMaster = `{"sources":[` +
			`{"kind":"hls","url":"/api/s/24/movie/6977?v=9","label":"Server 24"},` +
			`{"kind":"hls","url":"/api/s/22/movie/6977?v=9","label":"Server 22"}]}`
		emptyThenHealthy = `{"sources":[` +
			`{"kind":"hls","url":"/api/s/25/movie/6977?v=9","label":"Server 25"},` +
			`{"kind":"hls","url":"/api/s/22/movie/6977?v=9","label":"Server 22"}]}`
		embedAndMaster = `{"sources":[` +
			`{"kind":"embed","url":"/api/s/21/movie/6977?v=9","label":"Embed"},` +
			`{"kind":"hls","url":"/api/s/22/movie/6977?v=9","label":"Server 22"}]}`
	)

	tests := []struct {
		name       string
		detail     string
		wantErr    error
		wantCount  int
		wantHit    []string
		wantNotHit []string
	}{
		{
			name:      "404 master then healthy master",
			detail:    twoMasters,
			wantCount: 1,
			wantHit:   []string{"/api/s/21/movie/6977", "/api/s/22/movie/6977"},
		},
		{
			name:      "not-on-server body then healthy master",
			detail:    bodyOnlyMaster,
			wantCount: 1,
			wantHit:   []string{"/api/s/24/movie/6977", "/api/s/22/movie/6977"},
		},
		{
			name:      "empty playlist then healthy master",
			detail:    emptyThenHealthy,
			wantCount: 1,
			wantHit:   []string{"/api/s/25/movie/6977", "/api/s/22/movie/6977"},
		},
		{
			name:    "all masters 404",
			detail:  deadMasters,
			wantErr: provider.ErrNoSources,
			wantHit: []string{"/api/s/21/movie/6977", "/api/s/23/movie/6977"},
		},
		{
			name:       "non-HLS sources ignored",
			detail:     embedAndMaster,
			wantCount:  1,
			wantHit:    []string{"/api/s/22/movie/6977"},
			wantNotHit: []string{"/api/s/21/movie/6977"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				hits = make(map[string]int)
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				hits[r.URL.Path]++
				mu.Unlock()
				switch {
				case r.URL.Path == "/api/movie/6977":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.detail))
				case strings.Contains(r.URL.Path, "/api/s/21/"),
					strings.Contains(r.URL.Path, "/api/s/23/"):
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(notOnServer))
				case strings.Contains(r.URL.Path, "/api/s/24/"):
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(notOnServer))
				case strings.Contains(r.URL.Path, "/api/s/25/"):
					w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
					_, _ = w.Write([]byte("#EXTM3U\n"))
				case strings.Contains(r.URL.Path, "/api/s/22/"):
					w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
					_, _ = w.Write([]byte(okManifest))
				case strings.Contains(r.URL.Path, "/api/subs/list/movie/6977"):
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"files":[]}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
			if err != nil {
				t.Fatalf("NewClientWithURL: %v", err)
			}

			sources, err := client.ResolveSource(context.Background(), "6977", provider.Episode{TMDBID: 6977})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ResolveSource err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("ResolveSource: %v", err)
			}
			if len(sources) != tc.wantCount {
				t.Fatalf("sources = %d, want %d", len(sources), tc.wantCount)
			}

			mu.Lock()
			defer mu.Unlock()
			for _, path := range tc.wantHit {
				if hits[path] == 0 {
					t.Errorf("expected %s to be requested; hits = %v", path, hits)
				}
			}
			for _, path := range tc.wantNotHit {
				if hits[path] != 0 {
					t.Errorf("expected %s not to be requested; hits = %v", path, hits)
				}
			}
		})
	}
}

func TestCnoResolveStream(t *testing.T) {
	const tvDetail = `{"sources":[{"kind":"hls","url":"/api/s/21/tv/1396/1/2?v=3","label":"Server 21"}]}`
	const hlsManifest = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=6015211,RESOLUTION=1920x1080
https://v.lumenbox.top/v/token/1080p/index.m3u8`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/tv/1396/episode/1/2":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tvDetail))
		case r.URL.Path == "/api/s/21/tv/1396/1/2":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(hlsManifest))
		case strings.Contains(r.URL.Path, "/api/subs/list/tv/1396/1/2"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"files":[{"id":"os-tv1","lang":"en","label":"English"}]}`))
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
	if !strings.Contains(all[0].URL, "https://v.lumenbox.top/v/token/1080p/index.m3u8") {
		t.Errorf("TV URL mismatch: %s", all[0].URL)
	}
	if len(all[0].Subtitles) != 1 || all[0].Subtitles[0].Language != "en" {
		t.Errorf("TV subtitle mismatch: %v", all[0].Subtitles)
	}
}

func TestCnoResolveTV(t *testing.T) {
	const tvDetail = `{"sources":[{"kind":"hls","url":"/api/s/21/tv/1396/1/2?v=3","label":"Server 21"}]}`
	const hlsManifest = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=6015211,RESOLUTION=1920x1080
https://v.lumenbox.top/v/token/1080p/index.m3u8`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/tv/1396/episode/1/2":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tvDetail))
		case r.URL.Path == "/api/s/21/tv/1396/1/2":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(hlsManifest))
		case strings.Contains(r.URL.Path, "/api/subs/list/tv/1396/1/2"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"files":[{"id":"os-tv1","lang":"en","label":"English"}]}`))
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
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if !strings.Contains(sources[0].URL, "https://v.lumenbox.top/v/token/1080p/index.m3u8") {
		t.Errorf("TV URL mismatch: %s", sources[0].URL)
	}
	if len(sources[0].Subtitles) != 1 || sources[0].Subtitles[0].Language != "en" {
		t.Errorf("TV subtitle mismatch: %v", sources[0].Subtitles)
	}
}

func TestCnoResolveNoHLSSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/movie/6977" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sources":[{"kind":"embed","url":"https://example.test/embed"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	_, err = client.ResolveSource(context.Background(), "6977", provider.Episode{TMDBID: 6977})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("expected ErrNoSources for embed-only listing, got %v", err)
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
