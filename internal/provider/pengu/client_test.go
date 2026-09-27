package pengu

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
)

func TestBuildConfigSegment(t *testing.T) {
	token := "test_auth_token_12345"
	seg, err := buildConfigSegment(token)
	if err != nil {
		t.Fatalf("buildConfigSegment: %v", err)
	}

	if !strings.HasPrefix(seg, "z") {
		t.Fatalf("segment must start with 'z', got %q", seg)
	}

	rawB64 := strings.TrimPrefix(seg, "z")
	data, err := base64.RawURLEncoding.DecodeString(rawB64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}

	fr := flate.NewReader(bytes.NewReader(data))
	defer fr.Close()
	decompressed, err := io.ReadAll(fr)
	if err != nil {
		t.Fatalf("flate decompress: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(decompressed, &parsed); err != nil {
		t.Fatalf("unmarshal decompressed json: %v", err)
	}

	if parsed["auth_token"] != token {
		t.Errorf("auth_token = %v, want %v", parsed["auth_token"], token)
	}
	if parsed["source_4khdhub"] != "on" {
		t.Errorf("source_4khdhub = %v, want on", parsed["source_4khdhub"])
	}
	// Cinejoy and live-sports are backends enabled in Kari.
	if parsed["source_cinejoy"] != "on" {
		t.Errorf("source_cinejoy = %v, want on", parsed["source_cinejoy"])
	}
	if parsed["source_live-sports"] != "on" {
		t.Errorf("source_live-sports = %v, want on", parsed["source_live-sports"])
	}
	// 111477 and experimental are ignored/omitted.
	if parsed["source_111477"] == "on" {
		t.Errorf("source_111477 should not be enabled")
	}
	if parsed["source_experimental"] == "on" {
		t.Errorf("source_experimental should not be enabled")
	}
	// These backends play via Kari's header handling (verified with live
	// mpv probes), so they stay enabled.
	for _, key := range []string{"source_moviebox", "source_miruro", "source_anikoto", "source_2peckle", "source_vidlink", "source_moviesdrives", "source_4khdhub", "source_vegamovies", "source_vaplayer", "source_vidfast", "source_cinefreak", "source_cinejoy", "source_kisskh", "source_hdhub4u"} {
		if parsed[key] != "on" {
			t.Errorf("%s = %v, want on", key, parsed[key])
		}
	}
	if parsed["source_arctic"] != "off" {
		t.Errorf("source_arctic = %v, want off", parsed["source_arctic"])
	}
	for _, key := range []string{"source_hdghartv", "source_atlantic"} {
		if _, ok := parsed[key]; ok {
			t.Errorf("%s should not be present in Pengu config", key)
		}
	}
	if parsed["res_1080"] != "on" {
		t.Errorf("res_1080 = %v, want on", parsed["res_1080"])
	}
	// Movy.sx must NOT be requested from Pengu (Kari handles it independently).
	if parsed["source_movysx"] == "on" {
		t.Errorf("source_movysx should not be enabled in Pengu config")
	}
}

func TestParseStreamItem(t *testing.T) {
	item := penguStreamItem{
		Name: "🐧 PenguPlay 4K • 4KHDHub",
		Description: `🍿 Inception
🎞️ 4K • HLS
🛰️ Source: 4KHDHub
💾 12.4 GB
🎧 Audio: Hindi, English
📝 Subtitles: English`,
		URL: "https://example.com/stream.m3u8",
		BehaviorHints: penguBehaviorHints{
			Headers: map[string]string{
				"Referer":    "https://example.com/ref",
				"User-Agent": "CustomUA/1.0",
				"Cookie":     "sess=xyz",
			},
		},
		Subtitles: []penguSubtitle{
			{ID: "1", URL: "https://example.com/en.srt", Lang: "eng"},
		},
	}

	src := parseStreamItem(item)

	if src.Quality != "4K [4KHDHub] (Hindi)" {
		t.Errorf("Quality = %q, want %q", src.Quality, "4K [4KHDHub] (Hindi)")
	}
	if src.Type != provider.SourceTypeHLS {
		t.Errorf("Type = %q, want %q", src.Type, provider.SourceTypeHLS)
	}
	if src.Referer != "https://example.com/ref" {
		t.Errorf("Referer = %q, want https://example.com/ref", src.Referer)
	}
	if src.UserAgent != "CustomUA/1.0" {
		t.Errorf("UserAgent = %q, want CustomUA/1.0", src.UserAgent)
	}
	if src.CookieHeader != "sess=xyz" {
		t.Errorf("CookieHeader = %q, want sess=xyz", src.CookieHeader)
	}
	if src.Language != "hi" {
		t.Errorf("Language = %q, want hi", src.Language)
	}
	if len(src.Subtitles) != 1 || src.Subtitles[0].Language != "en" {
		t.Errorf("Subtitles = %+v, want 1 english track", src.Subtitles)
	}
}

func TestParseStreamItemNewBackends(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend string
	}{
		{"🐧 PenguPlay ❄️ 4K • Cinejoy · Lisbon", "Cinejoy"},
		{"🐧 PenguPlay 🧊 1080p • MovieBox", "MovieBox"},
		{"🐧 PenguPlay 🧊 1080p • Anikoto", "Anikoto"},
		{"🐧 PenguPlay 🧊 1080p • 2Peckle", "2Peckle"},
		{"🐧 PenguPlay 🧊 1080p • Miruro · Animegg", "Miruro"},
		{"🐧 PenguPlay 🧊 1080p • Arctic", "Arctic"},
		{"🐧 PenguPlay 🧊 1080p • Atlantic", "Atlantic"},
		{"🐧 PenguPlay 🧊 1080p • VixSrc", "VixSrc"},
	} {
		src := parseStreamItem(penguStreamItem{Name: tc.name, URL: "https://example.com/stream.m3u8"})
		want := "1080p [" + tc.backend + "]"
		if tc.backend == "Cinejoy" && strings.HasPrefix(tc.name, "🐧 PenguPlay ❄️ 4K") {
			want = "4K [" + tc.backend + "]"
		}
		if src.Quality != want {
			t.Errorf("Quality for %q = %q, want %q", tc.name, src.Quality, want)
		}
	}
}

func TestIsHousekeepingEntry(t *testing.T) {
	promo := penguStreamItem{
		Name:        "✨ | support the project!",
		Description: "pengu.uk/donate (donating hides this message)",
		ExternalURL: "https://pengu.uk/donate",
	}
	if !isHousekeepingEntry(promo) {
		t.Errorf("isHousekeepingEntry want true for donation banner")
	}

	real := penguStreamItem{
		Name: "🐧 PenguPlay 🧊 1080p • 4KHDHub",
		URL:  "https://pengu.uk/direct/external/stream",
	}
	if isHousekeepingEntry(real) {
		t.Errorf("isHousekeepingEntry want false for real stream")
	}
}

func TestResolveSourceKeepsDistinctEncodes(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	payload := `{"streams":[
		{"name":"✨ | support the project!","description":"pengu.uk/donate","externalUrl":"https://pengu.uk/donate"},
		{"name":"🐧 PenguPlay 🧊 1080p • 4KHDHub · PixelDrain","url":"https://cdn.example.com/a","behaviorHints":{"filename":"encode-a.mkv","videoSize":6764573491}},
		{"name":"🐧 PenguPlay 🧊 1080p • 4KHDHub · PixelDrain","url":"https://cdn.example.com/b","behaviorHints":{"filename":"encode-b.mkv","videoSize":3972844749}},
		{"name":"🐧 PenguPlay 🧊 1080p • 4KHDHub · PixelDrain","url":"https://cdn.example.com/b","behaviorHints":{"filename":"encode-b.mkv","videoSize":3972844749}},
		{"name":"🐧 PenguPlay ❄️ 4K • Cinejoy · Lisbon","url":"https://cdn.example.com/c","behaviorHints":{"filename":"cinejoy-4k","videoSize":16320000000}},
		{"name":"🐧 PenguPlay 1080p • Movy.sx","description":"Source: Movy.sx","url":"https://cdn.example.com/msx"}
	]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{httpClient: server.Client(), configSegment: "ztest"}

	sources, err := c.ResolveSource(context.Background(), "603", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	// promo skipped, exact URL duplicate collapsed, Movy.sx excluded:
	// 2 distinct 4KHDHub encodes + 1 Cinejoy.
	if len(sources) != 3 {
		t.Fatalf("sources = %d, want 3: %+v", len(sources), sources)
	}
	if sources[0].Quality != "1080p [4KHDHub]" {
		t.Errorf("sources[0].Quality = %q, want %q", sources[0].Quality, "1080p [4KHDHub]")
	}
	if sources[2].Quality != "4K [Cinejoy]" {
		t.Errorf("sources[2].Quality = %q, want %q", sources[2].Quality, "4K [Cinejoy]")
	}
}

func TestMapAudioLanguageUsesFirstListedLanguage(t *testing.T) {
	if got := mapAudioLanguage("Hindi, English"); got != "hi" {
		t.Errorf("mapAudioLanguage(Hindi, English) = %q, want hi", got)
	}
}

func TestRedactRequestError(t *testing.T) {
	segment := "zcustom_config_segment"
	err := redactRequestError(errors.New("Get https://pengu.uk/"+segment+"/stream/movie/tmdb:1.json: timeout"), segment)
	if strings.Contains(err.Error(), segment) {
		t.Fatalf("redacted error contains configuration segment: %q", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("redacted error = %q, want redaction marker", err)
	}
}

func TestParseStreamItemFilenameLanguage(t *testing.T) {
	item := penguStreamItem{
		Name: "🐧 PenguPlay 1080p • HDHub4u",
		URL:  "https://example.com/stream.m3u8",
		BehaviorHints: penguBehaviorHints{
			Filename: "The.Boys.S01E01.1080p.Hindi.DD5.1-English.5.1.HEVC.x265-HDHub4u.mkv",
		},
	}
	src := parseStreamItem(item)
	if src.Language != "hi" {
		t.Errorf("Language = %q, want hi", src.Language)
	}
	if src.Quality != "1080p [HDHub4u] (Hindi)" {
		t.Errorf("Quality = %q, want %q", src.Quality, "1080p [HDHub4u] (Hindi)")
	}
}

// A direct matroska file must type as progressive (mp4), never HLS:
// players and downloaders branch on Type, and an HLS label on an MKV
// body breaks manifest handling.
func TestParseStreamItemMatroskaTypesProgressive(t *testing.T) {
	for _, u := range []string{
		"https://cdn.example.com/ep.mkv",
		"https://cdn.example.com/ep.mkv?token=abc",
		"https://cdn.example.com/EP.WEBM",
	} {
		src := parseStreamItem(penguStreamItem{Name: "x 1080p", URL: u})
		if src.Type != provider.SourceTypeMP4 {
			t.Errorf("Type(%q) = %q, want mp4", u, src.Type)
		}
	}
	for _, u := range []string{
		"https://cdn.example.com/master.m3u8",
		"https://cdn.example.com/manifest?token=abc",
	} {
		src := parseStreamItem(penguStreamItem{Name: "x 1080p", URL: u})
		if src.Type != provider.SourceTypeHLS {
			t.Errorf("Type(%q) = %q, want hls", u, src.Type)
		}
	}
}

func TestIsExcludedStream(t *testing.T) {
	movyStream := penguStreamItem{
		Name:        "🐧 PenguPlay 1080p • Movy.sx",
		Description: "Source: Movy.sx",
		URL:         "https://pengu.uk/hls/movysx/stream.m3u8",
	}
	if !isExcludedStream(movyStream) {
		t.Errorf("isExcludedStream want true for Movy.sx stream")
	}

	twoPeckleStream := penguStreamItem{
		Name:        "🐧 PenguPlay 1080p • 2Peckle",
		Description: "Source: 2Peckle",
		URL:         "https://pengu.uk/direct/external/stream.mp4",
	}
	if isExcludedStream(twoPeckleStream) {
		t.Errorf("isExcludedStream want false for 2Peckle stream")
	}

	allowedStream := penguStreamItem{
		Name:        "🐧 PenguPlay 1080p • 4KHDHub",
		Description: "Source: 4KHDHub",
		URL:         "https://pengu.uk/direct/external/stream.mp4",
	}
	if isExcludedStream(allowedStream) {
		t.Errorf("isExcludedStream want false for 4KHDHub stream")
	}
}

func TestIsAuthPrompt(t *testing.T) {
	authStreams := []penguStreamItem{
		{
			Name:        "PenguPlay",
			Title:       "You must sign in",
			Description: "Your PenguPlay authentication is missing, invalid, or revoked.",
			URL:         "https://pengu.uk/signin.mp4",
		},
	}

	if !isAuthPrompt(authStreams) {
		t.Errorf("isAuthPrompt want true for signin prompt")
	}

	realStreams := []penguStreamItem{
		{
			Name: "Stream 1",
			URL:  "https://cdn.example.com/video.mp4",
		},
	}
	if isAuthPrompt(realStreams) {
		t.Errorf("isAuthPrompt want false for real stream")
	}
}

func TestAudioLanguagesCoverage(t *testing.T) {
	c := &Client{}
	langs := c.AudioLanguages()
	if len(langs) == 0 {
		t.Fatal("expected non-empty audio languages")
	}

	for _, l := range langs {
		if l.Code == "" || l.Display == "" {
			t.Errorf("invalid audio language: %+v", l)
		}
	}
}

func TestFetchPenguStreamsRateLimit(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	t.Run("429 status code returns ErrRateLimited", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
		}))
		defer server.Close()

		penguAPIBase = server.URL
		c := &Client{
			httpClient:    server.Client(),
			configSegment: "ztest",
		}

		_, err := c.fetchPenguStreams(context.Background(), provider.MediaTypeMovie, "tmdb:550")
		if !errors.Is(err, provider.ErrRateLimited) {
			t.Fatalf("fetchPenguStreams want ErrRateLimited, got %v", err)
		}
	})

	t.Run("200 status code with rate_limited error returns ErrRateLimited", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"streams":[],"error":"rate_limited"}`))
		}))
		defer server.Close()

		penguAPIBase = server.URL
		c := &Client{
			httpClient:    server.Client(),
			configSegment: "ztest",
		}

		_, err := c.fetchPenguStreams(context.Background(), provider.MediaTypeMovie, "tmdb:550")
		if !errors.Is(err, provider.ErrRateLimited) {
			t.Fatalf("fetchPenguStreams want ErrRateLimited, got %v", err)
		}
	})
}

func TestLiveFeaturesAndModes(t *testing.T) {
	c := &Client{}
	modes := c.Modes()
	var hasLive bool
	for _, m := range modes {
		if m.Name == provider.ModeLive {
			hasLive = true
			break
		}
	}
	if !hasLive {
		t.Fatal("expected ModeLive in Modes()")
	}

	f := c.Features(provider.ModeLive)
	if !f.AllowEmptyQuery {
		t.Errorf("expected AllowEmptyQuery=true for ModeLive")
	}
	if !f.NoCachedSearches {
		t.Errorf("expected NoCachedSearches=true for ModeLive")
	}
	if f.SearchPlaceholder == "" {
		t.Errorf("expected non-empty SearchPlaceholder for ModeLive")
	}
}

func TestSearchLiveCatalog(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "pp-live-now") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(penguCatalogResponse{
				Metas: []penguCatalogMeta{
					{
						ID:          "pp-live:daddylive~123",
						Name:        "🔴 LIVE: Match 1",
						ReleaseInfo: "2026-09-20 10:00",
						Poster:      "https://example.com/p1.jpg",
						Description: "Match 1 live description",
						Genres:      []string{"Cricket"},
					},
				},
			})
			return
		}
		if strings.Contains(r.URL.Path, "pp-live-channels") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(penguCatalogResponse{
				Metas: []penguCatalogMeta{
					{
						ID:          "pp-live:binged~espn",
						Name:        "📺 ESPN",
						ReleaseInfo: "24/7",
						Poster:      "https://example.com/espn.jpg",
						Description: "ESPN 24/7 Channel",
						Genres:      []string{"Sports"},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(penguCatalogResponse{Metas: []penguCatalogMeta{}})
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{
		httpClient:    server.Client(),
		configSegment: "ztest",
	}

	results, err := c.Search(context.Background(), "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search(ModeLive) error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].ID != "pp-live:daddylive~123" || results[0].Title != "LIVE: Match 1" || results[0].Type != provider.ModeLive || results[0].MediaType != provider.MediaTypeLive {
		t.Errorf("unexpected results[0]: %+v", results[0])
	}
	if results[1].ID != "pp-live:binged~espn" || results[1].Title != "ESPN" {
		t.Errorf("unexpected results[1]: %+v", results[1])
	}
}

// One stalled catalog (the real channel list is ~1.5 MB) must not sink the
// fast one: an empty live browse still surfaces live-now results.
func TestSearchLiveReturnsPartialWhenCatalogStalls(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "pp-live-now") {
			_ = json.NewEncoder(w).Encode(penguCatalogResponse{
				Metas: []penguCatalogMeta{{ID: "pp-live:daddylive~1", Name: "🔴 LIVE: Match", ReleaseInfo: "2026-09-20 10:00"}},
			})
			return
		}
		// Stall the channel catalog until the caller's context expires.
		<-r.Context().Done()
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{httpClient: server.Client(), configSegment: "ztest"}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	results, err := c.Search(ctx, "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search(ModeLive) error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "pp-live:daddylive~1" {
		t.Fatalf("got %+v, want the live-now result despite the stalled channel catalog", results)
	}
}

// The upstream releaseInfo carries a GMT offset suffix; it must parse
// into local time instead of dropping to zero (which hid every timed
// event from the schedule).
func TestParseLiveStart(t *testing.T) {
	got := parseLiveStart("2026-09-21 21:00 GMT+5:30")
	if got.IsZero() {
		t.Fatal("GMT-offset releaseInfo must parse")
	}
	// 21:00 at +05:30 is 15:30 UTC.
	want := time.Date(2026, 9, 21, 15, 30, 0, 0, time.UTC)
	if !got.UTC().Equal(want) {
		t.Errorf("parse = %v, want %v", got.UTC(), want)
	}
	if got := parseLiveStart("2026-09-20 10:00"); got.IsZero() {
		t.Error("bare datetime must parse")
	}
	if got := parseLiveStart("24/7"); !got.IsZero() {
		t.Errorf("channel marker must stay zero, got %v", got)
	}
	if got := parseLiveStart(""); !got.IsZero() {
		t.Errorf("blank must stay zero, got %v", got)
	}
}

func TestResolveLiveSource(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/stream/tv/pp-live:daddylive~123.json") {
			t.Errorf("unexpected request path: %q, want /stream/tv/...", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(penguResponse{
			Streams: []penguStreamItem{
				{
					Name:  "🐧 PenguPlay 🐧 Auto • DaddyLive · tv1 123",
					Title: "Match Stream",
					URL:   "https://pengu.uk/hls/live-sports/playlist/abc/index.m3u8",
					BehaviorHints: penguBehaviorHints{
						ProxyHeaders: &penguProxyHeader{
							Request: map[string]string{
								"Referer": "https://tiestep.top/",
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{
		httpClient:    server.Client(),
		configSegment: "ztest",
	}

	sources, err := c.ResolveSource(context.Background(), "pp-live:daddylive~123", provider.Episode{Title: "Match 1"})
	if err != nil {
		t.Fatalf("ResolveSource() error: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("got %d sources, want 1", len(sources))
	}
	if sources[0].Referer != "https://tiestep.top/" {
		t.Errorf("Referer = %q, want https://tiestep.top/", sources[0].Referer)
	}
	if sources[0].Type != provider.SourceTypeHLS {
		t.Errorf("Type = %q, want hls", sources[0].Type)
	}
	if !strings.Contains(sources[0].Quality, "DaddyLive") {
		t.Errorf("Quality = %q, want DaddyLive in source name", sources[0].Quality)
	}
}

func TestFetchEpisodesLive(t *testing.T) {
	c := &Client{}
	series := provider.SearchResult{
		Title:     "🔴 LIVE: Match",
		ID:        "pp-live:123",
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
	}
	eps, err := c.FetchEpisodes(context.Background(), series)
	if err != nil {
		t.Fatalf("FetchEpisodes() error: %v", err)
	}
	if len(eps) != 1 || eps[0].ID != "pp-live:123" || eps[0].Title != "🔴 LIVE: Match" {
		t.Errorf("unexpected episodes: %+v", eps)
	}
}
