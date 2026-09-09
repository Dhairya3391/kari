package service

import (
	"context"
	"testing"

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

	tracks, err := svc.Fetch(context.Background(), media, "off", "anilight")
	if err != nil {
		t.Fatalf("expected nil error when subtitles disabled, got: %v", err)
	}
	if len(tracks) != 0 {
		t.Fatalf("expected 0 tracks when disabled, got: %+v", tracks)
	}
}

func TestSubtitleServiceAnimeNoSoftSubsSkipsOpenSubtitles(t *testing.T) {
	svc := NewSubtitleService(&config.Config{
		OpenSubtitlesKey:  "dummy",
		OpenSubtitlesUser: "dummy",
		OpenSubtitlesPass: "dummy",
	})
	media := model.ResolvedMedia{
		SeriesTitle:   "Hardsubbed Anime",
		EpisodeNumber: 1,
		MediaType:     provider.MediaTypeAnime,
		Subtitles:     nil, // No soft subtitles available
	}

	tracks, err := svc.Fetch(context.Background(), media, "en", "anilight")
	if err == nil {
		t.Fatalf("expected error indicating no soft subtitles, got tracks: %+v", tracks)
	}
	if len(tracks) != 0 {
		t.Fatalf("expected 0 tracks, got: %+v", tracks)
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

	// For anime: should return nil tracks immediately when anime subtitles are disabled
	tracks, err := svc.Fetch(context.Background(), animeMedia, "en", "anilight")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tracks) != 0 {
		t.Fatalf("expected 0 tracks for anime when disableAnimeSubtitles is true, got: %+v", tracks)
	}
}
