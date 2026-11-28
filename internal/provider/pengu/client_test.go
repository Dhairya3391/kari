package pengu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

// movieStreams is a trimmed live-shaped payload: one promotional row without
// a URL, one 4K HLS lane with a subtitle, and one 1080p MP4 lane.
const movieStreams = `{"streams":[
 {"name":"✨ | support the project!","description":"pengu.uk/donate","externalUrl":"https://pengu.uk/donate"},
 {"url":"https://media.example/inception-4k","name":"🐧 PenguPlay ❄️ 4K • 2Peckle",
  "description":"🍿 Inception (2010)\n🎞️ 4K • HLS • BluRay • HEVC • ~8.2 Mbps\n🛰️ Source: 2Peckle\n💾 8.43 GB",
  "behaviorHints":{"filename":"Inception.2160p.mkv|^|4KHLSHEVC~8.2Mbps","videoSize":843},
  "subtitles":[{"url":"https://subs.example/en.srt","lang":"eng","label":"English"}]},
 {"url":"https://media.example/inception-1080","name":"🐧 PenguPlay ❄️ 1080p • VidLink",
  "description":"🍿 Inception (2010)\n🎞️ 1080p • MP4 • BluRay • x264 • ~3.0 Mbps\n🛰️ Source: VidLink\n🙊 No included subtitles",
  "behaviorHints":{"filename":"Inception.1080p.mp4"}}
]}`

// seriesStreams is the episode-shaped payload for a series resolve.
const seriesStreams = `{"streams":[
 {"url":"https://media.example/breaking-bad-s1e2","name":"🐧 PenguPlay ❄️ 1080p • 2Peckle",
  "description":"📡 Breaking Bad S01E02 (2008) • S01E02\n🎞️ 1080p • MKV • HEVC • ~5.0 Mbps\n🛰️ Source: 2Peckle"}
]}`

func TestClientIdentityAndModes(t *testing.T) {
	client, err := NewClientWithBase(tmdb.NewKeyPool(nil), "https://pengu.uk/", "test-token")
	if err != nil {
		t.Fatalf("NewClientWithBase: %v", err)
	}
	if client.Name() != "pengu" || client.Alias() != "PenguPlay" {
		t.Errorf("identity = %q/%q, want pengu/PenguPlay", client.Name(), client.Alias())
	}
	for _, want := range []provider.ContentType{provider.ModeMovies, provider.ModeTV, provider.ModeCartoon, provider.ModeLive} {
		found := false
		for _, mode := range client.Modes() {
			if mode.Name == want && mode.Priority == 2 {
				found = true
			}
		}
		if !found {
			t.Errorf("mode %q missing at priority 2: %+v", want, client.Modes())
		}
	}
	// The token segment must be the percent-encoded JSON object share links
	// use; a raw quote would break the request path.
	if !strings.Contains(client.apiBase, "%22test-token%22") || !strings.HasPrefix(client.apiBase, "https://pengu.uk/%7B") {
		t.Errorf("apiBase = %q, want an encoded auth_token path segment", client.apiBase)
	}
	if features := client.Features(provider.ModeLive); !features.AllowEmptyQuery || !features.NoCachedSearches {
		t.Errorf("live features = %+v, want browsable uncached search", features)
	}
}

// TestAPIConfigEnablesLiveAndFilters proves the path segment is valid JSON
// carrying the live flag plus the source/quality/audio allow-lists.
func TestAPIConfigEnablesLiveAndFilters(t *testing.T) {
	client, err := NewClientWithBase(tmdb.NewKeyPool(nil), "https://pengu.uk", "test-token")
	if err != nil {
		t.Fatalf("NewClientWithBase: %v", err)
	}
	segment := strings.TrimPrefix(client.apiBase, "https://pengu.uk/")
	raw, err := url.PathUnescape(segment)
	if err != nil {
		t.Fatalf("PathUnescape: %v", err)
	}
	var cfg map[string]string
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v (%s)", err, raw)
	}
	if cfg["auth_token"] != "test-token" || cfg["source_live-sports"] != "checked" {
		t.Errorf("config token/live = %q/%q", cfg["auth_token"], cfg["source_live-sports"])
	}
	for _, key := range []string{"source_2peckle", "res_1080", "audio_english", "subtitles_disabled"} {
		if _, ok := cfg[key]; !ok {
			t.Errorf("config missing %q", key)
		}
	}
}

func TestNewClientRejectsEmptyBaseOrToken(t *testing.T) {
	if _, err := NewClientWithBase(tmdb.NewKeyPool(nil), "", "token"); err == nil {
		t.Fatal("empty base must be rejected")
	}
	if _, err := NewClientWithBase(tmdb.NewKeyPool(nil), "https://pengu.uk", ""); err == nil {
		t.Fatal("empty token must be rejected")
	}
}

func TestResolveMovie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/stream/movie/tmdb:27205.json") {
			t.Errorf("unexpected movie path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, movieStreams)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	sources, err := client.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2 (promotional row skipped): %+v", len(sources), sources)
	}

	first := sources[0]
	if first.Quality != "2160p" || first.Height != 2160 {
		t.Errorf("4K lane quality = %q/%d, want 2160p/2160", first.Quality, first.Height)
	}
	if first.Type != provider.SourceTypeHLS {
		t.Errorf("4K lane type = %q, want hls", first.Type)
	}
	if first.Backend != "2Peckle" {
		t.Errorf("4K lane backend = %q, want 2Peckle", first.Backend)
	}
	if len(first.Subtitles) != 1 || first.Subtitles[0].Language != "en" {
		t.Errorf("4K lane subtitles = %+v, want one en track", first.Subtitles)
	}

	second := sources[1]
	if second.Quality != "1080p" || second.Type != provider.SourceTypeMP4 || second.Backend != "VidLink" {
		t.Errorf("1080p lane = %+v", second)
	}
	if len(second.Subtitles) != 0 {
		t.Errorf("1080p lane subtitles = %+v, want none", second.Subtitles)
	}
}

func TestResolveSeries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/stream/series/tmdb:1396:1:2.json") {
			t.Errorf("unexpected series path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, seriesStreams)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	sources, err := client.ResolveSource(context.Background(), "1396", provider.Episode{
		TMDBID: 1396, Season: 1, Episode: 2,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(sources))
	}
	if sources[0].Quality != "1080p" || sources[0].Type != provider.SourceTypeMP4 {
		t.Errorf("series lane = %+v", sources[0])
	}
}

func TestResolveNoPlayableLanes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"streams":[{"name":"✨ support","externalUrl":"https://pengu.uk/donate"}]}`)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, err := client.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("err = %v, want ErrNoSources", err)
	}
}

func TestResolveRejectedTokenIsAuthRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, err := client.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if !errors.Is(err, provider.ErrAuthRequired) {
		t.Fatalf("err = %v, want ErrAuthRequired", err)
	}
}

func TestResolveInvalidID(t *testing.T) {
	client := newTestClient(t, httptest.NewServer(http.NotFoundHandler()))
	if _, err := client.ResolveSource(context.Background(), "not-an-id", provider.Episode{}); !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("err = %v, want ErrNoSources", err)
	}
}

// newTestClient points a production client at server while keeping the real
// token-segment construction.
func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClientWithBase(tmdb.NewKeyPool(nil), server.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClientWithBase: %v", err)
	}
	return client
}

// liveManifestJSON advertises one live catalog plus a movie catalog that must
// be ignored.
const liveManifestJSON = `{"catalogs":[
 {"type":"movie","id":"pp-movies","name":"Movies - Movie"},
 {"type":"tv","id":"pp-live-now","name":"Live Now - TV"},
 {"type":"tv","id":"pp-live-soccer","name":"Soccer - TV (PenguTV)"},
 {"type":"tv","id":"pp-live-channels","name":"Channels - TV"}
]}`

// liveCatalogJSON carries one airing event and one upcoming event.
const liveCatalogJSON = `{"metas":[
 {"id":"pp-live:sktech~alpha","type":"tv","name":"🔴 LIVE: MotoGP",
  "poster":"https://pengu.uk/live/poster.jpg","description":"MotoGP\nMotorsport",
  "genres":["🏎️ Motorsport"],"released":"2026-11-28T09:10:00.000Z"},
 	 {"id":"pp-live:sportzx~beta","type":"tv","name":"Cricket Final",
  "description":"T20 World Cup","genres":["🏏 Cricket"],
  "released":"2099-01-01T15:00:00.000Z"}
]}`

func TestSearchLive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifest.json"):
			fmt.Fprint(w, liveManifestJSON)
		case strings.HasSuffix(r.URL.Path, "/catalog/tv/pp-live-now.json"):
			fmt.Fprint(w, liveCatalogJSON)
		case strings.HasSuffix(r.URL.Path, "/catalog/tv/pp-live-channels.json"):
			fmt.Fprint(w, `{"metas":[{"id":"pp-live:channel~one","name":"📺 Sky Sports Main Event"}]}`)
		case strings.HasSuffix(r.URL.Path, "/catalog/tv/pp-live-soccer.json"):
			// The airing event repeats here and must not duplicate; the
			// distinct entry proves the catalog name becomes the group.
			fmt.Fprint(w, `{"metas":[
			 {"id":"pp-live:sktech~alpha","name":"🔴 LIVE: MotoGP","released":"2026-11-28T09:10:00.000Z"},
			 {"id":"pp-live:daddylive~gamma","name":"Arsenal vs Chelsea","description":"Premier League","released":"2099-01-01T15:00:00.000Z"}
			]}`)
		default:
			t.Errorf("unexpected live search path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	// Preload the full snapshot so the search assertions do not race the
	// background fill that the first live search starts.
	entries, err := client.fetchLiveEntries(context.Background())
	if err != nil {
		t.Fatalf("fetchLiveEntries: %v", err)
	}
	client.live.store(entries)

	results, err := client.Search(context.Background(), "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search live: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("live results = %d, want 4 deduped items: %+v", len(results), results)
	}
	first := results[0]
	if first.ID != "pp-live:sktech~alpha" || first.Group != "🏎️ Motorsport" {
		t.Errorf("first live result = %+v", first)
	}
	if first.MediaType != provider.MediaTypeLive || first.CoverURL == "" || first.StartsAt.IsZero() {
		t.Errorf("live mapping = %+v", first)
	}
	upcoming := results[2]
	if upcoming.ID != "pp-live:daddylive~gamma" || upcoming.Live || upcoming.Group != "Soccer" {
		t.Errorf("upcoming result = %+v, want group Soccer and not live", upcoming)
	}
	channel := results[3]
	if channel.Group != "Channel" || !channel.Live || !channel.StartsAt.IsZero() {
		t.Errorf("channel result = %+v, want 24/7 Channel", channel)
	}

	filtered, err := client.Search(context.Background(), "arsenal", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != "pp-live:daddylive~gamma" {
		t.Errorf("filtered live results = %+v", filtered)
	}

	if _, err := client.Search(context.Background(), "no-such-event", provider.ModeLive); !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("empty live search err = %v, want ErrNoResults", err)
	}
}

func TestResolveLive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/stream/tv/pp-live:sktech~alpha.json") {
			t.Errorf("unexpected live stream path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"streams":[
		 {"url":"https://pengu.uk/hls/live/index.m3u8","name":"🐧 PenguPlay 🧊 1080p • SK Tech",
		  "description":"🍿 TNT SPORTS\n🎞️ 1080p • HLS • ClearKey\n🛰️ Source: SK Tech"}
		]}`)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	sources, err := client.ResolveSource(context.Background(), "pp-live:sktech~alpha", provider.Episode{ID: "pp-live:sktech~alpha"})
	if err != nil {
		t.Fatalf("ResolveSource live: %v", err)
	}
	if len(sources) != 1 || sources[0].Quality != "1080p" || sources[0].Type != provider.SourceTypeHLS {
		t.Fatalf("live sources = %+v", sources)
	}
	episodes, err := client.FetchEpisodes(context.Background(), provider.SearchResult{Title: "MotoGP", ID: "pp-live:sktech~alpha", Type: provider.ModeLive})
	if err != nil || len(episodes) != 1 || episodes[0].ID != "pp-live:sktech~alpha" {
		t.Fatalf("live episodes = %+v, %v", episodes, err)
	}
}

func TestVerifyLiveStreamIgnoresNonLiveModes(t *testing.T) {
	client := newTestClient(t, httptest.NewServer(http.NotFoundHandler()))
	if err := client.VerifyLiveStream(context.Background(), provider.ModeMovies, provider.MediaSource{URL: "https://example.invalid/x"}); err != nil {
		t.Fatalf("non-live verify = %v, want nil", err)
	}
}

func TestLiveResultMapsSchedule(t *testing.T) {
	now := time.Now()
	meta := func(released time.Time) liveMeta {
		return liveMeta{
			ID:       "pp-live:test",
			Name:     "Match",
			Released: released.UTC().Format(time.RFC3339),
			Genres:   []string{"⚽ Soccer"},
		}
	}

	airing := liveResult(meta(now.Add(-time.Hour)), "Football", false)
	if !airing.Live || airing.StartsAt.IsZero() {
		t.Errorf("airing event = %+v, want live with start time", airing)
	}
	finished := liveResult(meta(now.Add(-6*time.Hour)), "Football", false)
	if finished.Live {
		t.Errorf("old event = %+v, want not live", finished)
	}
	future := liveResult(meta(now.Add(2*time.Hour)), "Football", false)
	if future.Live || future.StartsAt.IsZero() {
		t.Errorf("future event = %+v, want scheduled", future)
	}
	channel := liveResult(meta(now.Add(-time.Hour)), "Channel", true)
	if !channel.Live || !channel.StartsAt.IsZero() {
		t.Errorf("channel = %+v, want 24/7 with no schedule", channel)
	}
}

func TestLaneMetadataUsesEveryCandidate(t *testing.T) {
	item := streamItem{
		Description: "🎞️ HLS • ~5 Mbps",
		Behavior:    streamBehavior{Filename: "Movie.1080p.mkv"},
		Name:        "Movie 1080p",
	}
	quality, height, sourceType := laneMetadata(item)
	if quality != "1080p" || height != 1080 || sourceType != provider.SourceTypeHLS {
		t.Fatalf("laneMetadata = %q/%d/%q, want 1080p/1080/HLS", quality, height, sourceType)
	}
}
