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
)

func TestSubtitleServiceDisabled(t *testing.T) {
	svc := NewSubtitleService(&config.Config{})
	media := model.ResolvedMedia{
		SeriesTitle:   "One Piece",
		EpisodeNumber: 1,
		Subtitles: []model.SubtitleTrack{
			{URL: "https://example.com/en.vtt", Language: "en", Resolver: "anilight"},
		},
	}

	track, err := svc.Fetch(context.Background(), media, "off", "anilight")
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
			Resolver: "anilight",
		}},
	}

	track, err := svc.Fetch(context.Background(), media, "en", "anilight")
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
		if r.URL.Path == "/en.vtt" {
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
			{URL: srv.URL + "/en.vtt", Language: "en", Resolver: "movysx"},
			{URL: srv.URL + "/id.vtt", Language: "id", Resolver: "movysx"},
		},
	}, "en", "movysx")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if track.Path == "" || track.Language != "id" {
		t.Fatalf("fallback track = %+v", track)
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

	track, err := svc.Fetch(context.Background(), media, "en", "anilight")
	if err == nil {
		t.Fatalf("expected an error when no subtitle source is available, got track: %+v", track)
	}
	if track.Path != "" {
		t.Fatalf("expected no track, got: %+v", track)
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
			{URL: "https://example.com/en.vtt", Language: "en", Resolver: "anilight"},
		},
	}

	track, err := svc.Fetch(context.Background(), animeMedia, "en", "anilight")
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
