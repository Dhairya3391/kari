package player

import (
	"strings"
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

func TestBuildSkipArgsFromTimes(t *testing.T) {
	times := combinedSkipTimes{
		OpStart:      45.0,
		OpEnd:        135.0,
		EdStart:      1350.0,
		EdEnd:        1440.0,
		RecapStart:   0.0,
		RecapEnd:     45.0,
		PreviewStart: 1440.0,
		PreviewEnd:   1470.0,
	}
	settings := SkipSettings{
		Provider:       "hybrid",
		AutoSkipIntro:  true,
		AutoSkipEnding: true,
		SkipRecap:      true,
		SkipPreview:    true,
	}

	args, scriptPath := buildSkipArgsFromTimes(times, settings)
	defer cleanupAniskipScript(scriptPath)

	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
	if scriptPath == "" {
		t.Fatal("expected non-empty scriptPath")
	}

	// Verify script option arguments
	hasScript := false
	hasScriptOpts := false
	for _, arg := range args {
		if strings.HasPrefix(arg, "--script=") {
			hasScript = true
		}
		if strings.HasPrefix(arg, "--script-opts=") {
			hasScriptOpts = true
			if !strings.Contains(arg, "skip-op_start=45.000000") || !strings.Contains(arg, "skip-op_end=135.000000") {
				t.Errorf("script opts missing op boundaries: %s", arg)
			}
			if !strings.Contains(arg, "skip-auto_intro=1") || !strings.Contains(arg, "skip-auto_ending=1") {
				t.Errorf("script opts missing auto flags: %s", arg)
			}
		}
	}
	if !hasScript || !hasScriptOpts {
		t.Errorf("missing --script or --script-opts in args: %v", args)
	}
}
