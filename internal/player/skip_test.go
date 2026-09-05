package player

import (
	"testing"

	"kari/internal/model"
)

func TestGetSkipArgs_OffProvider(t *testing.T) {
	args, scriptPath := getSkipArgs(nil, nil, SkipSettings{Provider: "off"}, model.ResolvedMedia{
		SeriesTitle:   "Frieren",
		EpisodeNumber: 1,
	})
	if args != nil || scriptPath != "" {
		t.Fatalf("expected nil args when provider is off, got args=%v path=%s", args, scriptPath)
	}
}

func TestGetSkipArgs_InvalidEpisode(t *testing.T) {
	args, scriptPath := getSkipArgs(nil, nil, SkipSettings{Provider: "hybrid"}, model.ResolvedMedia{
		SeriesTitle:   "Frieren",
		EpisodeNumber: 0,
	})
	if args != nil || scriptPath != "" {
		t.Fatalf("expected nil args for episode 0, got args=%v path=%s", args, scriptPath)
	}
}

func TestGetSkipArgs_EmptyTitle(t *testing.T) {
	args, scriptPath := getSkipArgs(nil, nil, SkipSettings{Provider: "hybrid"}, model.ResolvedMedia{
		SeriesTitle:   "",
		EpisodeNumber: 1,
	})
	if args != nil || scriptPath != "" {
		t.Fatalf("expected nil args for empty title, got args=%v path=%s", args, scriptPath)
	}
}
