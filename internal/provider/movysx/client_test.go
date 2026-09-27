package movysx

// End-to-end tests for direct Movy.sx resolution. Fixture servers stand in
// for TMDB metadata and the source service (seed + city shards serving
// stream-cipher-encrypted payloads); no intermediary API is involved.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/config"
	"kari/internal/provider"
	"kari/internal/tmdb"
)

type movyFixture struct {
	tmdb      *httptest.Server
	stream    *httptest.Server
	seed      string
	tmdbPaths []string
}

func newMovyFixture(t *testing.T, shardPayload func() string) *movyFixture {
	t.Helper()
	fx := &movyFixture{}
	fx.seed = "fixture-seed"

	fx.tmdb = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.tmdbPaths = append(fx.tmdbPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/movie/27205"):
			fmt.Fprint(w, `{"title":"Inception","release_date":"2010-07-16","imdb_id":"tt1375666","number_of_seasons":0,"external_ids":{"imdb_id":"tt1375666"}}`)
		case strings.HasPrefix(r.URL.Path, "/tv/1396"):
			fmt.Fprint(w, `{"name":"Breaking Bad","first_air_date":"2008-01-20","imdb_id":"tt0903747","number_of_seasons":5,"external_ids":{"imdb_id":"tt0903747"}}`)
		default:
			http.Error(w, `{"status_message":"not found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(fx.tmdb.Close)

	fx.stream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/seed":
			if r.URL.Query().Get("mediaId") == "" {
				http.Error(w, "mediaId required", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"seed":%q,"ttlMs":30000}`, fx.seed)
		case strings.HasSuffix(r.URL.Path, "/sources"):
			p := shardPayload()
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, encryptForTest(t, p, fx.seed, tmdbIDOf(r)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fx.stream.Close)
	return fx
}

// tmdbIDOf extracts tmdbId from shard query params for fixture encryption.
func tmdbIDOf(r *http.Request) uint32 {
	var id int
	fmt.Sscanf(r.URL.Query().Get("tmdbId"), "%d", &id)
	return uint32(id)
}

func (fx *movyFixture) client() *Client {
	return &Client{
		httpClient: fx.stream.Client(),
		keyPool:    tmdb.NewKeyPool([]string{"test-key"}),
		streamBase: fx.stream.URL,
		tmdbBase:   fx.tmdb.URL,
	}
}

func TestModesAndName(t *testing.T) {
	c := &Client{}
	if c.Name() != "movysx" {
		t.Errorf("Name() = %q, want movysx", c.Name())
	}
	if c.Alias() != "Movy.sx" {
		t.Errorf("Alias() = %q, want Movy.sx", c.Alias())
	}
	if got := c.Modes(); len(got) != 3 {
		t.Errorf("Modes() count = %d, want 3", len(got))
	}
}

func TestResolveSourceMovie(t *testing.T) {
	fx := newMovyFixture(t, func() string {
		return `{"sources":[
			{"url":"https://cdn.example.com/master.m3u8","quality":"2160p"},
			{"url":"https://cdn.example.com/movie.mp4","quality":"1080p"},
			{"url":"https://cdn.example.com/movie.mp4","quality":"1080p"}
		],"subtitles":[
			{"url":"https://cdn.example.com/en.vtt","lang":"en"},
			{"url":"https://cdn.example.com/hi.vtt","lang":"hin"}
		]}`
	})
	c := fx.client()

	sources, err := c.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}
	if len(fx.tmdbPaths) == 0 || fx.tmdbPaths[0] != "/movie/27205" {
		t.Errorf("expected TMDB movie lookup, got %v", fx.tmdbPaths)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 deduped sources, got %d", len(sources))
	}
	if sources[0].URL != "https://cdn.example.com/master.m3u8" {
		t.Errorf("unexpected sources[0].URL: %s", sources[0].URL)
	}
	if sources[0].Type != provider.SourceTypeHLS {
		t.Errorf("m3u8 source Type = %q, want hls", sources[0].Type)
	}
	if sources[1].Type != provider.SourceTypeMP4 {
		t.Errorf("mp4 source Type = %q, want mp4", sources[1].Type)
	}
	for _, s := range sources {
		if s.Referer != config.MovyReferer {
			t.Errorf("Referer = %q, want %q", s.Referer, config.MovyReferer)
		}
		if strings.Contains(s.URL, "127.0.0.1") || strings.Contains(s.URL, "/proxy/") {
			t.Errorf("source must be direct, got %s", s.URL)
		}
	}
	if len(sources[0].Subtitles) != 2 {
		t.Fatalf("expected 2 subtitles, got %d", len(sources[0].Subtitles))
	}
	if sources[0].Subtitles[0].Language != "en" {
		t.Errorf("subtitle language = %q, want en", sources[0].Subtitles[0].Language)
	}
}

func TestResolveSourceTVEpisode(t *testing.T) {
	fx := newMovyFixture(t, func() string {
		return `{"sources":[{"url":"https://cdn.example.com/s01e01.m3u8","quality":"1080p"}]}`
	})
	c := fx.client()

	sources, err := c.ResolveSource(context.Background(), "1396", provider.Episode{TMDBID: 1396, Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}
	if len(sources) != 1 || sources[0].Quality != "1080p" {
		t.Errorf("unexpected sources: %+v", sources)
	}
	foundTV := false
	for _, p := range fx.tmdbPaths {
		if p == "/tv/1396" {
			foundTV = true
		}
	}
	if !foundTV {
		t.Errorf("expected TMDB tv lookup, got %v", fx.tmdbPaths)
	}
}

func TestResolveSourceNotFound(t *testing.T) {
	fx := newMovyFixture(t, func() string { return `{"sources":[]}` })
	c := fx.client()

	_, err := c.ResolveSource(context.Background(), "999", provider.Episode{TMDBID: 999})
	if err == nil {
		t.Fatal("expected error on unknown TMDB id, got nil")
	}
}

func TestResolveSourceNoSources(t *testing.T) {
	fx := newMovyFixture(t, func() string { return `{"sources":[],"subtitles":[]}` })
	c := fx.client()

	if _, err := c.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205}); err != provider.ErrNoSources {
		t.Errorf("err = %v, want ErrNoSources", err)
	}
}

// TestResolveSourceAudioRows proves dubbed audio streams arrive as
// language-tagged sources (no repeats).
func TestResolveSourceAudioRows(t *testing.T) {
	// Fixture carries dub-labeled rows inside sources.
	fx2 := newMovyFixture(t, func() string {
		return `{"sources":[
			{"url":"https://cdn.example.com/master.m3u8","quality":"1080p"},
			{"url":"https://cdn.example.com/dub-hi.m3u8","quality":"Hindi"},
			{"url":"https://cdn.example.com/dub-hi.m3u8","quality":"Hindi"},
			{"url":"https://cdn.example.com/master.m3u8","quality":"Hindi"},
			{"url":"","quality":"Telugu"}
		],"subtitles":[]}`
	})
	c := fx2.client()

	sources, err := c.ResolveSource(context.Background(), "1396", provider.Episode{TMDBID: 1396, Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("want main + 1 deduped Hindi row, got %+v", sources)
	}
	dub := sources[1]
	if dub.Language != "hi" {
		t.Errorf("dub Language = %q, want hi", dub.Language)
	}
	if dub.Type != provider.SourceTypeHLS {
		t.Errorf("dub Type = %q, want hls", dub.Type)
	}
	if dub.Referer != config.MovyReferer {
		t.Errorf("dub Referer = %q, want movy referer", dub.Referer)
	}
}

// TestStreamTypeDeclaredFirst proves declared types win, URL inference
// covers manifests, and unknown extensionless URLs keep "auto".
func TestStreamTypeDeclaredFirst(t *testing.T) {
	cases := []struct {
		url, declared, want string
	}{
		{"https://cdn.example.com/x", "hls", provider.SourceTypeHLS},
		{"https://cdn.example.com/x.m3u8", "mp4", provider.SourceTypeMP4},
		{"https://cdn.example.com/x.mpd", "", "dash"},
		{"https://cdn.example.com/x", "dash", "dash"},
		{"https://cdn.example.com/x.mkv", "", provider.SourceTypeMP4},
		{"https://cdn.example.com/signed?token=abc", "", provider.SourceTypeMP4},
		{"https://cdn.example.com/signed?token=abc", "auto", "auto"},
	}
	for _, tc := range cases {
		if got := streamType(tc.url, tc.declared); got != tc.want {
			t.Errorf("streamType(%q,%q) = %q, want %q", tc.url, tc.declared, got, tc.want)
		}
	}
}

func TestResolveSourceInvalidMediaID(t *testing.T) {
	c := &Client{httpClient: http.DefaultClient, streamBase: "http://unused", tmdbBase: "http://unused"}
	if _, err := c.ResolveSource(context.Background(), "not-a-number", provider.Episode{}); err == nil {
		t.Error("expected error for non-numeric media ID, got nil")
	}
}

func TestResolveSourceSeedCached(t *testing.T) {
	fx := newMovyFixture(t, func() string { return `{"sources":[]}` })
	// Second resolve must reuse the cached seed.
	c := fx.client()
	if _, err := c.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205}); !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("err = %v, want ErrNoSources", err)
	}
	if _, err := c.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205}); !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("err = %v, want ErrNoSources", err)
	}
	if v, ok := c.seedCache.Load(27205); !ok || v.(seedEntry).seed == "" {
		t.Error("seed must be cached after first resolve")
	}
}
