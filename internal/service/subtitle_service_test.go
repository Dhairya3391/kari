package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"kari/internal/config"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/util"
)

type fakeOpenSubtitleSource struct {
	track model.SubtitleTrack
	calls int
}

func (f *fakeOpenSubtitleSource) Configured() bool { return true }

func (f *fakeOpenSubtitleSource) FetchBestSubtitle(
	_ context.Context,
	_, _ string,
	_, _, _ int,
) (model.SubtitleTrack, bool, error) {
	f.calls++
	return f.track, true, nil
}

func TestSubtitleServiceDisabled(t *testing.T) {
	svc := NewSubtitleService(&config.Config{})
	media := model.ResolvedMedia{
		SeriesTitle:   "One Piece",
		EpisodeNumber: 1,
		Subtitles: []model.SubtitleTrack{
			{URL: "https://example.com/en.vtt", Language: "en", Resolver: "miruro"},
		},
	}

	track, err := svc.Fetch(context.Background(), media, "off", "miruro")
	if err != nil {
		t.Fatalf("expected nil error when subtitles disabled, got: %v", err)
	}
	if track.Path != "" {
		t.Fatalf("expected no track when disabled, got: %+v", track)
	}
}

func TestSubtitleServiceDownloadsAnimeProviderTrack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n"))
	}))
	defer srv.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = srv.Client()
	media := model.ResolvedMedia{
		SeriesTitle:   "Subtitle Delivery Anime",
		EpisodeNumber: 1,
		MediaType:     provider.MediaTypeAnime,
		Subtitles: []model.SubtitleTrack{{
			URL:      srv.URL + "/subtitle.vtt",
			Language: "en",
			Resolver: "miruro",
		}},
	}

	track, err := svc.Fetch(context.Background(), media, "en", "miruro")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if track.Path == "" {
		t.Fatalf("downloaded track = %+v", track)
	}
	if _, err := os.Stat(track.Path); err != nil {
		t.Fatalf("subtitle path does not exist: %v", err)
	}
}

func TestSubtitleServiceKeepsTracksBoundToTheirPlaybackSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nsource subtitle\n"))
	}))
	defer srv.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = srv.Client()
	media := model.ResolvedMedia{
		SeriesTitle:   "Source-bound subtitles",
		EpisodeNumber: 1,
		MediaType:     provider.MediaTypeAnime,
		Subtitles: []model.SubtitleTrack{
			{URL: srv.URL + "/first.vtt", Language: "en", Resolver: "pengu", SourceID: "source-one"},
			{URL: srv.URL + "/second.vtt", Language: "en", Resolver: "pengu", SourceID: "source-two"},
		},
	}

	for _, sourceID := range []string{"source-one", "source-two"} {
		track, err := svc.FetchForSource(context.Background(), media, "en", "pengu", sourceID)
		if err != nil {
			t.Fatalf("FetchForSource(%q): %v", sourceID, err)
		}
		if track.SourceID != sourceID {
			t.Errorf("source %q received subtitle for %q", sourceID, track.SourceID)
		}
	}
}

func TestSubtitleServiceDoesNotBorrowOtherProviderSubtitles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nother provider subtitle\n"))
	}))
	defer srv.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = srv.Client()
	media := model.ResolvedMedia{
		SeriesTitle:        "Isolation test",
		EpisodeNumber:      1,
		TotalResolvers:     2,
		CompletedResolvers: []string{"pengu", "anikoto"},
		Subtitles: []model.SubtitleTrack{{
			URL: srv.URL + "/fallback.vtt", Language: "en", Resolver: "anikoto", SourceID: "other-source",
		}},
	}
	_, err := svc.FetchForSource(context.Background(), media, "en", "pengu", "pengu-source")
	if err == nil {
		t.Fatal("subtitles must not be borrowed from another provider")
	}
}

func TestSubtitleServiceFallsBackToWorkingMirrorOfSameResolver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken.vtt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nworking subtitle\n"))
	}))
	defer srv.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = srv.Client()
	media := model.ResolvedMedia{
		SeriesTitle:   "Mirror Fallback",
		EpisodeNumber: 1,
		MediaType:     provider.MediaTypeAnime,
		Subtitles: []model.SubtitleTrack{
			{URL: srv.URL + "/broken.vtt", Language: "en", Resolver: "miruro", SourceID: "mirror-1"},
			{URL: srv.URL + "/working.vtt", Language: "en", Resolver: "miruro", SourceID: "mirror-2"},
		},
	}

	track, err := svc.FetchForSource(context.Background(), media, "en", "miruro", "mirror-1")
	if err != nil {
		t.Fatalf("FetchForSource failed: %v", err)
	}
	if track.Resolver != "miruro" {
		t.Errorf("resolver = %q, want miruro", track.Resolver)
	}
	if track.SourceID != "mirror-2" {
		t.Errorf("sourceID = %q, want mirror-2", track.SourceID)
	}
}

func TestSubtitleServiceBoundsFailedProviderCandidates(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = srv.Client()
	media := model.ResolvedMedia{
		SeriesTitle:   "Bounded Subtitle Search",
		EpisodeNumber: 1,
		Subtitles: []model.SubtitleTrack{
			{URL: srv.URL + "/one.vtt", Language: "en", Resolver: "movysx"},
			{URL: srv.URL + "/two.vtt", Language: "en", Resolver: "movysx"},
			{URL: srv.URL + "/three.vtt", Language: "en", Resolver: "movysx"},
			{URL: srv.URL + "/four.vtt", Language: "en", Resolver: "movysx"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := svc.Fetch(ctx, media, "en", "movysx")
	if err == nil {
		t.Fatal("expected subtitle lookup failure")
	}
	if got := requests.Load(); got > subtitleProviderCandidateLimit {
		t.Fatalf("provider requests = %d, want at most %d", got, subtitleProviderCandidateLimit)
	}
}

func TestSubtitleServiceFallsBackToWorkingProviderTrack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/es.vtt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n"))
	}))
	defer srv.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = srv.Client()
	track, err := svc.Fetch(context.Background(), model.ResolvedMedia{
		SeriesTitle: "Alternate provider subtitle",
		Subtitles: []model.SubtitleTrack{
			{URL: srv.URL + "/es.vtt", Language: "es", Resolver: "movysx"},
			{URL: srv.URL + "/en.vtt", Language: "en", Resolver: "movysx"},
		},
	}, "es", "movysx")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if track.Path == "" || track.Language != "en" {
		t.Fatalf("fallback track = %+v, want en", track)
	}
}

func TestSubtitleServiceRejectsUnrequestedForeignLanguageWhenEnglishRequested(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/en.vtt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nestonian text\n"))
	}))
	defer srv.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = srv.Client()
	track, err := svc.Fetch(context.Background(), model.ResolvedMedia{
		SeriesTitle: "No English Subtitle",
		Subtitles: []model.SubtitleTrack{
			{URL: srv.URL + "/en.vtt", Language: "en", Resolver: "movysx"},
			{URL: srv.URL + "/et.vtt", Language: "et", Resolver: "movysx"},
		},
	}, "en", "movysx")
	if err == nil && track.Path != "" {
		t.Fatalf("expected error or no track, got unrequested foreign track: %+v", track)
	}
}

func TestSubtitleServiceAnimeWithoutProviderTrackReturnsNoSubtitles(t *testing.T) {
	svc := NewSubtitleService(&config.Config{})
	media := model.ResolvedMedia{
		SeriesTitle:   "Hardsubbed Anime",
		EpisodeNumber: 1,
		MediaType:     provider.MediaTypeAnime,
		Subtitles:     nil,
	}

	track, err := svc.Fetch(context.Background(), media, "en", "miruro")
	if err == nil {
		t.Fatalf("expected an error when no subtitle source is available, got track: %+v", track)
	}
	if track.Path != "" {
		t.Fatalf("expected no track, got: %+v", track)
	}
}

func TestSubtitleServiceDoesNotUseOpenSubtitlesForAnime(t *testing.T) {
	path := t.TempDir() + "/opensub.srt"
	if err := os.WriteFile(path, []byte("1\n00:00,000 --> 00:00,500\nanime subtitle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	openSubtitles := &fakeOpenSubtitleSource{
		track: model.SubtitleTrack{Path: path, Resolver: "opensubtitles", Language: "en"},
	}
	svc := NewSubtitleService(&config.Config{})
	svc.openSubtitles = openSubtitles
	_, err := svc.Fetch(context.Background(), model.ResolvedMedia{
		SeriesTitle: "Anime movie without provider subtitles",
		MediaType:   provider.MediaTypeMovie,
		ContentMode: provider.ModeAnime,
	}, "en", "pengu")
	if err == nil {
		t.Fatal("anime subtitle lookup must not use OpenSubtitles")
	}
	if openSubtitles.calls != 0 {
		t.Fatalf("OpenSubtitles calls = %d, want 0", openSubtitles.calls)
	}
}

func TestSubtitleServiceDisableAnimeSubtitlesOnly(t *testing.T) {
	svc := NewSubtitleService(&config.Config{})
	svc.SetDisableAnimeSubtitles(true)

	animeMedia := model.ResolvedMedia{
		SeriesTitle:   "One Piece",
		EpisodeNumber: 1,
		MediaType:     provider.MediaTypeAnime,
		Subtitles: []model.SubtitleTrack{
			{URL: "https://example.com/en.vtt", Language: "en", Resolver: "miruro"},
		},
	}

	track, err := svc.Fetch(context.Background(), animeMedia, "en", "miruro")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if track.Path != "" {
		t.Fatalf("expected no anime track when disabled, got: %+v", track)
	}
}

func TestSubtitleServiceUsesParentSourceHeaders(t *testing.T) {
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n"))
	}))
	defer server.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = server.Client()
	media := model.ResolvedMedia{
		Playback: []provider.MediaSource{{
			URL:          "https://cdn.example.com/video.m3u8",
			UserAgent:    "Custom-Agent",
			Referer:      "https://provider.example/watch/1",
			CookieHeader: "session=abc",
		}},
		Subtitles: []model.SubtitleTrack{{
			URL:       server.URL + "/subtitle.vtt",
			SourceURL: "https://cdn.example.com/video.m3u8",
			Language:  "en",
			Resolver:  "pengu",
		}},
	}

	track, err := svc.Fetch(context.Background(), media, "en", "pengu")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if track.Path == "" {
		t.Fatal("expected materialized subtitle")
	}
	got := <-headers
	if got.Get("User-Agent") != "Custom-Agent" {
		t.Errorf("User-Agent = %q", got.Get("User-Agent"))
	}
	if got.Get("Referer") != "https://provider.example/watch/1" {
		t.Errorf("Referer = %q", got.Get("Referer"))
	}
	if got.Get("Origin") != "https://provider.example" {
		t.Errorf("Origin = %q", got.Get("Origin"))
	}
	if got.Get("Cookie") != "session=abc" {
		t.Errorf("Cookie = %q", got.Get("Cookie"))
	}
}

func TestSubtitleServiceRejectsHTMLAndTriesNextCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/challenge" {
			_, _ = w.Write([]byte("<!doctype html><html><body>challenge</body></html>"))
			return
		}
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n"))
	}))
	defer server.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = server.Client()
	track, err := svc.Fetch(context.Background(), model.ResolvedMedia{
		Subtitles: []model.SubtitleTrack{
			{URL: server.URL + "/challenge", Language: "en", Resolver: "movysx"},
			{URL: server.URL + "/valid", Language: "en", Resolver: "movysx"},
		},
	}, "en", "movysx")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if track.Path == "" {
		t.Fatal("expected valid fallback subtitle")
	}
}

func TestSubtitleServiceRedownloadsStalePath(t *testing.T) {
	requests := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n"))
	}))
	defer server.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = server.Client()
	track, err := svc.Fetch(context.Background(), model.ResolvedMedia{
		Subtitles: []model.SubtitleTrack{{
			Path:     "/tmp/stale-subtitle.srt",
			URL:      server.URL + "/subtitle.vtt",
			Language: "en",
			Resolver: "movysx",
		}},
	}, "en", "movysx")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if track.Path == "" || track.Path == "/tmp/stale-subtitle.srt" {
		t.Fatalf("stale path was not replaced: %+v", track)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestSubtitleServiceReusesCachedFileForSameURL(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nreused\n"))
	}))
	defer server.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = server.Client()

	media := model.ResolvedMedia{
		SeriesTitle: "Deduplication Test",
		Subtitles: []model.SubtitleTrack{
			{URL: server.URL + "/sub.vtt", Language: "en", Resolver: "movysx"},
		},
	}

	t1, err := svc.Fetch(context.Background(), media, "en", "movysx")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	// Reset in-memory cache to force downloadProviderSubtitle call
	svc.cache = util.NewBoundedCache[model.SubtitleTrack](10)

	t2, err := svc.Fetch(context.Background(), media, "en", "movysx")
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}

	if t1.Path != t2.Path {
		t.Errorf("paths differ: %q vs %q", t1.Path, t2.Path)
	}
	if requests.Load() != 1 {
		t.Errorf("expected 1 network request due to disk cache reuse, got %d", requests.Load())
	}
}

func TestSubtitleServiceHierarchyMatchingOverOther(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/matching.vtt":
			_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nmatching provider sub\n"))
		case "/other.vtt":
			_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nother provider sub\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = srv.Client()

	media := model.ResolvedMedia{
		SeriesTitle: "Hierarchy Test",
		MediaType:   provider.MediaTypeMovie,
		Subtitles: []model.SubtitleTrack{
			{URL: srv.URL + "/matching.vtt", Language: "en", Resolver: "athena"},
			{URL: srv.URL + "/other.vtt", Language: "en", Resolver: "movysx"},
		},
	}

	// Case 1: active resolver is athena; matching candidate must be selected
	track, err := svc.Fetch(context.Background(), media, "en", "athena")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if track.Resolver != "athena" {
		t.Errorf("expected athena track, got resolver %q", track.Resolver)
	}

	// Case 2: active resolver has no subs (e.g. cinecat); must NOT borrow other providers' subs
	svc.cache = util.NewBoundedCache[model.SubtitleTrack](10)
	_, err = svc.Fetch(context.Background(), media, "en", "cinecat")
	if err == nil {
		t.Fatal("expected no subtitles error when active resolver has no subs and no OpenSubtitles configured")
	}
}

// TestSubtitleServiceProviderSubsNotPoisonedByOpenSubtitles verifies that
// scrolling through sources does not assign an OpenSubtitles track to a
// resolver that carries its own provider subtitles. A previous resolver
// (no provider subs) may fall back to OpenSubtitles and cache that result;
// when the user switches to a resolver that has its own subs the provider
// track must win, not the cached OpenSub track from the other resolver.
func TestSubtitleServiceProviderSubsNotPoisonedByOpenSubtitles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nprovider sub\n"))
	}))
	defer srv.Close()

	svc := NewSubtitleService(&config.Config{})
	svc.httpClient = srv.Client()

	media := model.ResolvedMedia{
		SeriesTitle:   "Fire Force",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		MediaType:     provider.MediaTypeAnime,
		// Only anikoto has provider subs; miruro has none.
		Subtitles: []model.SubtitleTrack{
			{URL: srv.URL + "/provider.vtt", Language: "en", Resolver: "anikoto"},
		},
	}

	// Simulate: user first selected a miruro source (no provider subs).
	// Manually inject an OpenSub-style track under miruro's cacheKey to
	// represent a prior fetch that reached OpenSubtitles and cached its result.
	openSubPath := t.TempDir() + "/opensub.srt"
	if err := os.WriteFile(openSubPath, []byte("1\n00:00,000 --> 00:01,000\nopemsub track\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	miruroKey := "0:1:1:Fire Force:en:miruro"
	svc.cache.Set(miruroKey, model.SubtitleTrack{Path: openSubPath, Resolver: "opensubtitles"})

	// Now user scrolls to anikoto source — must get anikoto's provider sub.
	track, err := svc.Fetch(context.Background(), media, "en", "anikoto")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if track.Resolver != "anikoto" {
		t.Errorf("expected anikoto provider track, got resolver %q (path %q)", track.Resolver, track.Path)
	}
}
