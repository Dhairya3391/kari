package cinecat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, srv.URL, srv.URL, srv.URL, srv.URL)
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

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, srv.URL, srv.URL, srv.URL, srv.URL)
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

// TestCinecatResolveAphrodite proves the self-hosted Aphrodite content edge
// resolves independently of ZCDN for both movies and TV episodes.
func TestCinecatResolveAphrodite(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cdn", func(w http.ResponseWriter, r *http.Request) {
		// ZCDN unavailable; Aphrodite alone must resolve playback.
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("/content/movie/603", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(aphroditeResponse{
			Found: true,
			Type:  "hls",
			URL:   fmt.Sprintf("http://%s/aphrodite/movie.m3u8", r.Host),
		})
	})

	mux.HandleFunc("/content/tv/1396/2/5", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(aphroditeResponse{
			Found: true,
			Type:  "hls",
			URL:   fmt.Sprintf("http://%s/aphrodite/tv.m3u8", r.Host),
		})
	})

	ladder := `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
https://cdn.example.com/aphrodite/v1/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720
https://cdn.example.com/aphrodite/v2/index.m3u8
`
	for _, path := range []string{"/aphrodite/movie.m3u8", "/aphrodite/tv.m3u8"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(ladder))
		})
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, srv.URL, srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	movie, err := client.ResolveSource(context.Background(), "603", provider.Episode{TMDBID: 603})
	if err != nil {
		t.Fatalf("ResolveSource movie failed: %v", err)
	}
	episode, err := client.ResolveSource(context.Background(), "1396", provider.Episode{
		TMDBID:  1396,
		Season:  2,
		Episode: 5,
	})
	if err != nil {
		t.Fatalf("ResolveSource TV failed: %v", err)
	}

	for name, sources := range map[string][]provider.MediaSource{"movie": movie, "episode": episode} {
		if len(sources) != 2 {
			t.Fatalf("%s: expected 2 Aphrodite tiers, got %d: %+v", name, len(sources), sources)
		}
		for i, want := range []string{"1080p", "720p"} {
			if sources[i].Quality != want {
				t.Errorf("%s: sources[%d].Quality = %q, want %q", name, i, sources[i].Quality, want)
			}
			if sources[i].Backend != aphroditeBackend {
				t.Errorf("%s: sources[%d].Backend = %q, want %q", name, i, sources[i].Backend, aphroditeBackend)
			}
		}
	}
}

func TestCinecatResolveLanes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cdn", func(w http.ResponseWriter, r *http.Request) {
		// ZCDN unavailable; lanes alone must still resolve playback.
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("/sources", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sourcesListResponse{
			Sources: []struct {
				Name string `json:"name"`
			}{
				{Name: "abyssinia"},
				{Name: "calico"},
			},
		})
	})

	mux.HandleFunc("/sources/abyssinia", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("tmdb") != "27205" || q.Get("type") != "movie" {
			http.Error(w, "bad lane query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(laneStreamsResponse{
			Streams: []laneStreamResponse{
				{Kind: "hls", URL: fmt.Sprintf("http://%s/lane-a/master.m3u8", r.Host)},
			},
		})
	})

	mux.HandleFunc("/sources/calico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "no_streams"}})
	})

	mux.HandleFunc("/lane-a/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
https://cdn.example.com/lane-a/v1/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720
https://cdn.example.com/lane-a/v2/index.m3u8
`))
	})

	mux.HandleFunc("/v1/movie/27205", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, srv.URL, srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}

	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d: %+v", len(sources), sources)
	}
	for i, want := range []string{"1080p", "720p"} {
		if sources[i].Quality != want {
			t.Errorf("sources[%d].Quality = %q, want %q", i, sources[i].Quality, want)
		}
		if sources[i].Backend != "Abyssinia" {
			t.Errorf("sources[%d].Backend = %q, want Abyssinia", i, sources[i].Backend)
		}
	}
}

// TestGatedStatusCooldowns proves throttling closes the gate only briefly.
func TestGatedStatusCooldowns(t *testing.T) {
	c := &Client{}
	_ = c.gatedStatusError("http://example.com", http.StatusUnauthorized)
	capUntil := c.capBlockedTil
	_ = c.gatedStatusError("http://example.com", http.StatusTooManyRequests)
	rateUntil := c.capBlockedTil
	if got := time.Until(capUntil); got < 25*time.Minute {
		t.Errorf("401 cooldown = %v, want about %v", got, capCooldown)
	}
	if got := time.Until(rateUntil); got > 3*time.Minute {
		t.Errorf("429 cooldown = %v, want about %v", got, rateLimitCooldown)
	}
}

// TestGatedLaneStatuses proves 401 and 429 responses on every gated lane
// start the cooldown, while the ungated Aphrodite edge keeps serving sources
// during it. A fully gated upstream still names the cause in its error.
func TestGatedLaneStatuses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		gateLane    bool
		aphrodite   bool
		wantMessage string
	}{
		{name: "401 on gated lanes closes the gate", status: http.StatusUnauthorized, aphrodite: true},
		{name: "429 on gated lanes closes the gate", status: http.StatusTooManyRequests, aphrodite: true},
		{name: "429 on a lane endpoint closes the gate", status: http.StatusTooManyRequests, gateLane: true, aphrodite: true},
		{name: "all gated lanes throttled", status: http.StatusTooManyRequests, wantMessage: "rate limited"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cdnCalls, sourceCalls, laneCalls atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/cdn", func(w http.ResponseWriter, r *http.Request) {
				cdnCalls.Add(1)
				if tt.gateLane {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				w.WriteHeader(tt.status)
			})
			mux.HandleFunc("/sources", func(w http.ResponseWriter, r *http.Request) {
				sourceCalls.Add(1)
				if !tt.gateLane {
					w.WriteHeader(tt.status)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(sourcesListResponse{
					Sources: []struct {
						Name string `json:"name"`
					}{
						{Name: "alpha"},
					},
				})
			})
			mux.HandleFunc("/sources/alpha", func(w http.ResponseWriter, r *http.Request) {
				laneCalls.Add(1)
				w.WriteHeader(tt.status)
			})
			if tt.aphrodite {
				mux.HandleFunc("/content/movie/550", func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(aphroditeResponse{
						Found: true,
						Type:  "hls",
						URL:   fmt.Sprintf("http://%s/aphrodite/movie.m3u8", r.Host),
					})
				})
				mux.HandleFunc("/aphrodite/movie.m3u8", func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
					_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-VERSION:6
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
https://cdn.example.com/aphrodite/v1.m3u8
`))
				})
			}
			mux.HandleFunc("/v1/movie/550", func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			})

			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, srv.URL, srv.URL, srv.URL, srv.URL)
			if err != nil {
				t.Fatalf("NewClientWithURLs: %v", err)
			}
			if !client.capGateOpen() {
				t.Fatal("cap gate should start open")
			}

			sources, err := client.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
			if tt.gateLane {
				if laneCalls.Load() == 0 {
					t.Fatal("lane endpoint was not tried")
				}
			} else if cdnCalls.Load() == 0 || sourceCalls.Load() == 0 {
				t.Fatalf("gated lanes were not tried: cdn=%d sources=%d", cdnCalls.Load(), sourceCalls.Load())
			}
			if client.capGateOpen() {
				t.Fatal("gated lanes should be closed after the gate response")
			}
			if !tt.aphrodite {
				if err == nil {
					t.Fatal("expected an error when no lane can serve the title")
				}
				if !errors.Is(err, provider.ErrNoSources) {
					t.Fatalf("expected benign ErrNoSources, got %v", err)
				}
				if !strings.Contains(err.Error(), tt.wantMessage) {
					t.Fatalf("error %q does not name %q", err, tt.wantMessage)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveSource: %v", err)
			}
			assertAphroditeOnly(t, sources)

			cdnCalls.Store(0)
			sourceCalls.Store(0)
			laneCalls.Store(0)
			sources, err = client.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
			if err != nil {
				t.Fatalf("ResolveSource during cooldown: %v", err)
			}
			assertAphroditeOnly(t, sources)
			if cdnCalls.Load() != 0 || sourceCalls.Load() != 0 || laneCalls.Load() != 0 {
				t.Fatalf("gated lanes ran during cooldown: cdn=%d sources=%d lane=%d",
					cdnCalls.Load(), sourceCalls.Load(), laneCalls.Load())
			}
		})
	}
}

func assertAphroditeOnly(t *testing.T, sources []provider.MediaSource) {
	t.Helper()
	if len(sources) == 0 {
		t.Fatal("expected Aphrodite sources")
	}
	for _, source := range sources {
		if source.Backend != aphroditeBackend {
			t.Fatalf("unexpected backend %q in %+v", source.Backend, sources)
		}
	}
}

// TestCinecatLaneDeclaredQuality proves a single-rendition lane keeps its own
// quality label instead of collapsing into an identical "Auto (Lane)" row,
// while a master ladder still exposes its tiers plus an adaptive row.
func TestCinecatLaneDeclaredQuality(t *testing.T) {
	c := &Client{siteBase: "https://beta.cinecat.eu"}

	single := c.candidateSources(sourceCandidate{
		URL:     "https://edge.cinecat.eu/hls/abc/master.m3u8",
		Backend: "Siamese",
		Label:   "720p",
	}, nil, nil)
	if len(single) != 1 {
		t.Fatalf("single-rendition lane produced %d sources, want 1", len(single))
	}
	if single[0].Quality != "720p" {
		t.Errorf("single-rendition quality = %q, want 720p", single[0].Quality)
	}
	if single[0].Backend != "Siamese" {
		t.Errorf("backend = %q, want Siamese", single[0].Backend)
	}

	uhd := c.candidateSources(sourceCandidate{
		URL:     "https://edge.cinecat.eu/hls/uhd/master.m3u8",
		Backend: "Siamese",
		Label:   "2160p",
	}, nil, nil)
	if len(uhd) != 1 || uhd[0].Quality != "4K (2160p)" {
		t.Fatalf("4K lane quality = %+v, want 4K (2160p)", uhd)
	}

	undeclared := c.candidateSources(sourceCandidate{
		URL:     "https://edge.cinecat.eu/hls/def/master.m3u8",
		Backend: "Siamese",
	}, nil, nil)
	if len(undeclared) != 1 || undeclared[0].Quality != "Auto (Siamese)" {
		t.Fatalf("undeclared rendering = %+v, want one Auto (Siamese) row", undeclared)
	}

	master := c.candidateSources(sourceCandidate{
		URL:     "https://edge.cinecat.eu/hls/ghi/master.m3u8",
		Backend: "Siamese",
		Label:   "720p",
	}, []variantStream{{URL: "https://edge.cinecat.eu/hls/ghi/p0.m3u8", Quality: "1080p", Height: 1080}}, nil)
	if len(master) != 2 {
		t.Fatalf("master ladder produced %d sources, want tier + adaptive", len(master))
	}
	if master[0].Quality != "1080p" || master[1].Quality != "Auto (Siamese)" {
		t.Errorf("master qualities = %q, %q; want 1080p, Auto (Siamese)", master[0].Quality, master[1].Quality)
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

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, srv.URL, srv.URL, srv.URL, srv.URL)
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
