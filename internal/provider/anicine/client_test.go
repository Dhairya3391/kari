package anicine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

// Canned Megaplay enc token; decrypts to a nexabloom master.m3u8.
const fixtureEnc = "wdeBruh3qqn_i5wUNnyaPcXqidp1UWP84FfPHzGyKXDMoY_RzXqbC0h49XmoI7d0vYZArA5rcKY-FQxnEk8NNEWezec9dd2jwxp1UbLN43p_7CMxXPDF5BUX86bUm0_Uuw0-dlv_yT9MKsnmlOgDFm3ReCPZPJloNlqBF7aftTk"

func newFixture(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	var base string

	// AniList GraphQL (search + media) at the server root.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "Media(") {
			fmt.Fprint(w, `{"data":{"Media":{"id":151807,"idMal":52299,"title":{"romaji":"Ore dake Level Up na Ken","english":"Solo Leveling","userPreferred":"Solo Leveling","native":"X"},"format":"TV","status":"FINISHED","seasonYear":2024,"episodes":2,"synonyms":[],"nextAiringEpisode":null}}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"Page":{"media":[
			{"id":151807,"title":{"romaji":"Ore dake Level Up na Ken","english":"Solo Leveling","userPreferred":"Solo Leveling","native":"X"},"seasonYear":2024,"format":"TV"}
		]}}}`)
	})

	// Worker token with a distant expiry.
	mux.HandleFunc("/v1/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":"fixture-token","exp":%d}`, time.Now().Add(2*time.Hour).UnixMilli())
	})

	// Worker movie + TV endpoints.
	mux.HandleFunc("/v1/movies/27205", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"sources":[
			{"url":"https://cdn.example.com/movie.m3u8","type":"hls","quality":"Auto","provider":{"id":"vaplayer","name":"VaPlayer"}},
			{"url":"https://cdn.example.com/movie.m3u8","type":"hls","quality":"Auto","provider":{"id":"vaplayer","name":"VaPlayer"}}
		],"subtitles":[{"url":"https://cdn.example.com/en.vtt","label":"English"}]}`)
	})
	mux.HandleFunc("/v1/tv/1396/seasons/1/episodes/1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"sources":[
			{"url":"https://cdn.example.com/s01e01.m3u8","type":"hls","quality":"1080p","provider":{"id":"vaplayer","name":"VaPlayer"}}
		],"subtitles":[]}`)
	})

	// Embed error page (unreleased track) vs shapeless page (drift).
	mux.HandleFunc("/stream/errpage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Error - MegaPlay</title></head><body>gone</body></html>`)
	})
	mux.HandleFunc("/stream/shapeless", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><div>redesigned</div></body></html>`)
	})

	// Megaplay MAL embed + getSources (served from the same mux since the
	// test client points embedBase at it).
	mux.HandleFunc("/stream/mal/52299/1/sub", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><div class="player" data-id="999">File 999 - sub</div></body></html>`)
	})
	mux.HandleFunc("/stream/getSources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "999" {
			http.Error(w, "unknown file", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"enc":%q,"tracks":[{"file":"https://cdn.example.com/sub.vtt","label":"English","kind":"captions"}]}`,
			fixtureEnc)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	_ = base

	c, err := NewClientWithBaseURL(tmdb.NewKeyPool([]string{"k"}), srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL: %v", err)
	}
	return c, srv
}

func TestAnicineCapabilities(t *testing.T) {
	c, err := NewClient(tmdb.NewKeyPool([]string{"k"}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Alias() == "" || c.Name() != "anicine" {
		t.Errorf("identity wrong: %q %q", c.Alias(), c.Name())
	}
	for _, m := range []provider.ContentType{provider.ModeAnime, provider.ModeMovies, provider.ModeTV, provider.ModeCartoon} {
		found := false
		for _, mode := range c.Modes() {
			if mode.Name == m && mode.Priority == 1 {
				found = true
			}
		}
		if !found {
			t.Errorf("mode %q missing at priority 1: %+v", m, c.Modes())
		}
	}
	if !c.RequiresEpisodeListForMovies() {
		t.Error("movies must go through the episode flow")
	}
	if !c.Features(provider.ModeAnime).AudioSelection {
		t.Error("anime must declare audio selection")
	}
}

func TestAnicineAnimeSearch(t *testing.T) {
	c, _ := newFixture(t)
	results, err := c.Search(context.Background(), "Solo Leveling", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != "151807" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestAnicineAnimeEpisodes(t *testing.T) {
	c, _ := newFixture(t)
	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "151807", Type: provider.ModeAnime})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	// 2 episodes x sub+dub = 4.
	if len(eps) != 4 {
		t.Fatalf("expected 4 episodes, got %d", len(eps))
	}
	if eps[0].Audio != "sub" || !strings.HasPrefix(eps[0].ID, "watch/anicine/151807/sub/1") {
		t.Errorf("unexpected eps[0]: %+v", eps[0])
	}
}

func TestAnicineAnimeResolve(t *testing.T) {
	c, _ := newFixture(t)
	ep := provider.Episode{ID: "watch/anicine/151807/sub/1", Episode: 1, Audio: "sub"}
	sources, err := c.ResolveSource(context.Background(), "151807", ep)
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	s1 := sources[0]
	if !strings.HasPrefix(s1.URL, "https://fetch.nexabloom.top/") || !strings.Contains(s1.URL, "?token=") {
		t.Errorf("expected signed direct CDN URL, got %s", s1.URL)
	}
	if s1.Type != provider.SourceTypeHLS || s1.Referer == "" || s1.UserAgent == "" {
		t.Errorf("mpv transport incomplete: %+v", s1)
	}
	if len(s1.Subtitles) != 1 {
		t.Errorf("expected 1 subtitle, got %+v", s1.Subtitles)
	}
}

func TestAnicineMovieResolve(t *testing.T) {
	c, _ := newFixture(t)
	sources, err := c.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	// Duplicate URL collapses to one.
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if sources[0].URL != "https://cdn.example.com/movie.m3u8" {
		t.Errorf("unexpected URL: %s", sources[0].URL)
	}
	if len(sources[0].Subtitles) != 1 || sources[0].Subtitles[0].Language != "en" {
		t.Errorf("unexpected subtitles: %+v", sources[0].Subtitles)
	}
}

func TestAnicineTVResolve(t *testing.T) {
	c, _ := newFixture(t)
	sources, err := c.ResolveSource(context.Background(), "1396", provider.Episode{TMDBID: 1396, Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 || !strings.Contains(sources[0].Quality, "VaPlayer") {
		t.Errorf("unexpected sources: %+v", sources)
	}
}

func TestAnicineTokenRefresh(t *testing.T) {
	c, _ := newFixture(t)
	token, err := c.bearerToken(context.Background())
	if err != nil {
		t.Fatalf("bearerToken: %v", err)
	}
	if token != "fixture-token" {
		t.Errorf("token = %q", token)
	}
	// Second call serves the cache without HTTP (would fail closed server).
	if token2, err := c.bearerToken(context.Background()); err != nil || token2 != token {
		t.Errorf("cached token = %q, %v", token2, err)
	}
}

// TestAnicineRespectsDeadline proves a hung worker fails fast on context
// cancellation instead of hanging past the provider deadline (the service
// bounds each provider at 12s; this is what surfaces as
// "Sources ✗ anicine timed out").
func TestAnicineRespectsDeadline(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(60 * time.Second):
		}
	}))
	defer hang.Close()

	c, err := NewClientWithBaseURL(tmdb.NewKeyPool([]string{"k"}), hang.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err = c.ResolveSource(ctx, "27205", provider.Episode{TMDBID: 27205})
	if err == nil {
		t.Fatal("hung worker must error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("resolve hung %v past deadline", elapsed)
	}
}

// TestMalIDCache proves repeat resolves survive AniList stalls: a cached
// MAL id is served even with a dead context.
func TestMalIDCache(t *testing.T) {
	c, _ := newFixture(t)
	malCache.Store("999999", malEntry{malID: 12345, cachedAt: time.Now()})
	defer malCache.Delete("999999")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // dead context: any network use fails
	got, err := c.malID(ctx, "999999")
	if err != nil {
		t.Fatalf("cached malID: %v", err)
	}
	if got != 12345 {
		t.Errorf("malID = %d, want 12345", got)
	}
}

// TestEmbedErrorPageIsAudioUnavailable proves a 200 embed page explicitly
// titled as an error (unreleased dub) maps to the audio-unavailable
// sentinel, while a shapeless page stays a generic error.
func TestEmbedErrorPageIsAudioUnavailable(t *testing.T) {
	c, srv := newFixture(t)
	_, err := c.megaplayFileID(context.Background(), srv.URL+"/stream/errpage")
	if !errors.Is(err, provider.ErrAudioUnavailable) {
		t.Errorf("error page err = %v, want ErrAudioUnavailable", err)
	}
	_, err = c.megaplayFileID(context.Background(), srv.URL+"/stream/shapeless")
	if err == nil || errors.Is(err, provider.ErrAudioUnavailable) {
		t.Errorf("shapeless page err = %v, want generic non-sentinel error", err)
	}
}
