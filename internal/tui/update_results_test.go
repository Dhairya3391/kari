package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"

	"kari/internal/config"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/ranking"
	"kari/internal/service"
)

func unavailableTestModel() *modelImpl {
	m := rankingTestModel()
	m.appMode = provider.ModeAnime
	m.activeView = viewPreview
	m.backStack = []viewState{viewEpisodes}
	m.selectedSeries = &provider.SearchResult{Title: "ONE PIECE", ID: "21", Type: provider.ModeAnime}
	m.episodeResults = []provider.Episode{
		{Title: "Episode 1178", Episode: 1178, Season: 1, Audio: "dub", ID: "watch/x/21/dub/1178"},
		{Title: "Episode 1179", Episode: 1179, Season: 1, Audio: "dub", ID: "watch/x/21/dub/1179"},
	}
	m.selectedEpisode = &m.episodeResults[1]
	m.episodeIndex = 1
	m.selectedEpisodes = map[int]struct{}{1: {}}
	m.episodeList = list.New(nil, list.NewDefaultDelegate(), 80, 20)
	return m
}

func TestUnavailableAudioStaysOnPreview(t *testing.T) {
	m := unavailableTestModel()
	m.handleUnavailableAudio()

	if len(m.episodeResults) != 2 {
		t.Fatalf("episodes = %+v, want both rows retained", m.episodeResults)
	}
	if m.selectedEpisode == nil || m.selectedEpisode.Episode != 1179 {
		t.Fatalf("selectedEpisode = %+v, want episode 1179", m.selectedEpisode)
	}
	if m.activeView != viewPreview {
		t.Errorf("view = %v, want preview", m.activeView)
	}
	if !strings.Contains(m.statusText, "press a") {
		t.Errorf("status = %q, want a track-switch hint", m.statusText)
	}
}

// TestOnResolveDoneSkipsRetryOnAudioUnavailable proves a verified-missing
// track never triggers the pointless auto-retry loop.
func TestOnResolveDoneSkipsRetryOnAudioUnavailable(t *testing.T) {
	m := unavailableTestModel()
	m.resolveOpID = 7
	m.resolveAttempts = 0
	err := fmt.Errorf("all failed: %w", provider.ErrAudioUnavailable)

	_, cmd := m.onResolveDone(resolveDoneMsg{opID: 7, err: err})
	if cmd != nil {
		t.Error("audio-unavailable must not schedule a retry command")
	}
	if m.loading {
		t.Error("loading must be settled, not left spinning")
	}
	if len(m.episodeResults) != 2 {
		t.Errorf("episodes = %d, want both rows retained", len(m.episodeResults))
	}
	if m.activeView != viewPreview {
		t.Errorf("view = %v, want preview", m.activeView)
	}
	if !strings.Contains(m.statusText, "press a") {
		t.Errorf("status = %q, want a track-switch hint", m.statusText)
	}
}

func TestOnResolveDoneMaterializesProviderSubtitle(t *testing.T) {
	m := unavailableTestModel()
	m.appMode = provider.ModeMovies
	m.subtitleService = service.NewSubtitleService(&config.Config{})
	m.resolveOpID = 7
	m.loading = true
	m.loadingText = "Preparing playback..."
	m.selectedSeries = &provider.SearchResult{Title: "Avengers: Endgame", ID: "299534", MediaType: provider.MediaTypeMovie}
	m.selectedEpisode = &provider.Episode{Title: "Avengers: Endgame", ID: "299534"}

	mdl, _ := m.onResolveDone(resolveDoneMsg{opID: 7, resolved: model.ResolvedMedia{
		SeriesTitle: "Avengers: Endgame",
		MediaType:   provider.MediaTypeMovie,
		Playback: []provider.MediaSource{{
			URL:      "https://cdn.example.com/movie.m3u8",
			Resolver: "movysx",
			Referer:  "https://www.movy.sx/",
		}},
		Subtitles: []model.SubtitleTrack{{URL: "https://cdn.example.com/en.vtt", Language: "en", Resolver: "movysx"}},
	}})
	m = mdl.(*modelImpl)
	if m.subtitleOpID == 0 {
		t.Fatalf("provider subtitle state = opID %d", m.subtitleOpID)
	}
}

func TestStalePlayResultDoesNotMutateCurrentOperation(t *testing.T) {
	m := readySourceModel()
	m.playOpID = 9
	m.loading = true
	m.loadingText = "Opening player..."
	m.autoPlayAfterResolve = true

	mdl, _ := m.onPlayDone(playDoneMsg{opID: 8})
	m = mdl.(*modelImpl)
	if m.playOpID != 9 || !m.loading || m.loadingText != "Opening player..." || !m.autoPlayAfterResolve {
		t.Fatalf("stale result mutated active operation: %+v", m)
	}
}

// TestToggleAnimeAudioClearsResolved proves switching tracks on preview
// drops the old audio's rows instead of merging into them.
func TestToggleAnimeAudioClearsResolved(t *testing.T) {
	m := unavailableTestModel()
	m.audioMode = provider.AudioDub
	m.resolved = &model.ResolvedMedia{
		SeriesTitle: "ONE PIECE",
		Playback: []provider.MediaSource{
			{URL: "https://cdn.example.com/dub.m3u8", Quality: "1080p", Resolver: "x"},
		},
	}
	m.rankedSources = []ranking.ScoredSource{
		{Source: provider.MediaSource{URL: "https://cdn.example.com/dub.m3u8"}},
	}

	mdl, _ := m.toggleAnimeAudio()
	m2 := mdl.(*modelImpl)
	if m2.resolved != nil {
		t.Errorf("resolved = %+v, want cleared before re-resolve", m2.resolved.Playback)
	}
	if len(m2.rankedSources) != 0 {
		t.Errorf("ranked = %d, want cleared", len(m2.rankedSources))
	}
	if m2.audioMode != provider.AudioSub {
		t.Errorf("audioMode = %q, want sub", m2.audioMode)
	}
}

// TestToggleAnimeAudioDoesNotAppendOldSources proves that after toggling audio,
// arriving sources do not merge with the previous track's sources.
func TestToggleAnimeAudioDoesNotAppendOldSources(t *testing.T) {
	m := unavailableTestModel()
	m.audioMode = provider.AudioDub
	m.resolved = &model.ResolvedMedia{
		SeriesTitle: "ONE PIECE",
		Playback: []provider.MediaSource{
			{URL: "https://cdn.example.com/dub.m3u8", Quality: "1080p", Resolver: "x"},
		},
	}
	m.rankedSources = []ranking.ScoredSource{
		{Source: provider.MediaSource{URL: "https://cdn.example.com/dub.m3u8"}},
	}

	mdl, _ := m.toggleAnimeAudio()
	m2 := mdl.(*modelImpl)

	// New sources arrive for sub track
	subSource := provider.MediaSource{URL: "https://cdn.example.com/sub.m3u8", Quality: "1080p", Resolver: "y"}
	m2.mergeResolved(model.ResolvedMedia{
		SeriesTitle: "ONE PIECE",
		Playback:    []provider.MediaSource{subSource},
	})

	if len(m2.resolved.Playback) != 1 {
		t.Fatalf("resolved.Playback = %d, want 1; got %+v", len(m2.resolved.Playback), m2.resolved.Playback)
	}
	if m2.resolved.Playback[0].URL != subSource.URL {
		t.Errorf("expected only sub source, got %+v", m2.resolved.Playback[0])
	}
}
