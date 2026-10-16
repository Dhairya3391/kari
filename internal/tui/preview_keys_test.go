package tui

// Behavior pins for the Preview screen deltas: automatic resolve retry and
// the sub/dub audio toggle.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/ranking"
	"kari/internal/service"
)

func previewRetryModel() *modelImpl {
	m := rankingTestModel()
	m.appMode = provider.ModeAnime
	m.selectedSeries = &provider.SearchResult{Title: "Frieren", ID: "151807", Type: provider.ModeAnime}
	m.selectedEpisode = &provider.Episode{Title: "EP 1", ID: "watch/anikoto/151807/sub/1", Episode: 1, Season: 1}
	return m
}

// A resolve that comes back failed with no sources retries automatically,
// bounded by maxResolveAttempts, instead of surfacing an error immediately.
func TestResolveFailedAutoRetries(t *testing.T) {
	m := previewRetryModel()

	// First failure: retry scheduled, attempt counter advanced.
	mdl, _ := m.onResolveDone(resolveDoneMsg{opID: 0, err: provider.ErrNoSources})
	m = mdl.(*modelImpl)
	if m.resolveAttempts != 1 {
		t.Fatalf("resolveAttempts = %d, want 1", m.resolveAttempts)
	}
	if m.resolveOpID == 0 {
		t.Fatal("expected a retry resolve to be scheduled")
	}
	if !m.loading {
		t.Error("retry must keep the loading indicator up")
	}

	// Second failure still under the bound: retry again.
	mdl, _ = m.onResolveDone(resolveDoneMsg{opID: m.resolveOpID, err: provider.ErrNoSources})
	m = mdl.(*modelImpl)
	if m.resolveAttempts != 2 {
		t.Fatalf("resolveAttempts = %d, want 2", m.resolveAttempts)
	}

	// Hitting the bound stops retrying and surfaces the error.
	mdl, _ = m.onResolveDone(resolveDoneMsg{opID: m.resolveOpID, err: provider.ErrNoSources})
	m = mdl.(*modelImpl)
	if m.resolveOpID != 0 {
		t.Errorf("resolveOpID = %d, want 0 (no more retries)", m.resolveOpID)
	}
	if m.loading {
		t.Error("loading must drop once retries are exhausted")
	}
	if m.statusType != statusError {
		t.Errorf("status = %q, want error", m.statusType)
	}
}

// A resolve success resets the retry counter so the next episode starts
// with a full retry budget.
func TestResolveSuccessResetsAttempts(t *testing.T) {
	m := previewRetryModel()
	m.resolveAttempts = 2

	mdl, _ := m.onResolveDone(resolveDoneMsg{opID: 0})
	m = mdl.(*modelImpl)
	if m.resolveAttempts != 0 {
		t.Errorf("resolveAttempts = %d, want 0 after success", m.resolveAttempts)
	}
}

// 'a' on Preview flips sub/dub for anime and re-runs the episode listing.
func TestPreviewAudioToggleReloadsEpisodes(t *testing.T) {
	m := previewRetryModel()
	m.audioMode = provider.AudioSub

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = mdl.(*modelImpl)
	if !stringsEqualFold(m.audioMode, provider.AudioDub) {
		t.Errorf("audioMode = %q, want dub", m.audioMode)
	}
	if !m.loading {
		t.Error("episode reload should be loading")
	}

	mdl, _ = m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = mdl.(*modelImpl)
	if !stringsEqualFold(m.audioMode, provider.AudioSub) {
		t.Errorf("audioMode = %q, want back to sub", m.audioMode)
	}
}

// 'a' is a no-op outside anime.
func TestPreviewRetrySupersedesSubtitleFetch(t *testing.T) {
	m := previewRetryModel()
	m.subtitleOpID = 7
	m.resolved = &model.ResolvedMedia{Playback: []provider.MediaSource{{URL: "https://cdn.example.com/old.m3u8", Resolver: "movysx"}}}
	m.previewPoster = "existing poster"
	m.previewOverview = "existing overview"
	m.activeView = viewPreview
	m.backStack = []viewState{viewEpisodes}

	mdl, cmd := m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	m2 := mdl.(*modelImpl)
	if cmd == nil || m2.resolveOpID == 0 {
		t.Fatal("R did not start a retry")
	}
	if m2.subtitleOpID != 0 || m2.resolved != nil {
		t.Fatalf("retry did not supersede subtitle state: opID=%d resolved=%+v", m2.subtitleOpID, m2.resolved)
	}
	if m2.previewPoster != "existing poster" || m2.previewOverview != "existing overview" {
		t.Fatal("stream retry must preserve the loaded preview artwork and details")
	}
}

func TestPreviewXDoesNotNavigate(t *testing.T) {
	m := previewRetryModel()
	m.activeView = viewPreview
	m.backStack = []viewState{viewEpisodes}
	m.playOpID = 7

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m2 := mdl.(*modelImpl)
	if m2.activeView != viewPreview {
		t.Fatalf("x changed preview view to %v", m2.activeView)
	}
}

func TestPreviewAudioToggleAnimeOnly(t *testing.T) {
	m := previewRetryModel()
	m.appMode = provider.ModeTV

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = mdl.(*modelImpl)
	if m.loading {
		t.Error("non-anime toggle must not reload episodes")
	}
	if m.statusType != statusWarn {
		t.Errorf("status = %q, want warn", m.statusType)
	}
}

// 'r' restarts the current episode from the beginning when a resume
// position exists and playback is not already running.
func TestPreviewRestartFromBeginning(t *testing.T) {
	m := previewRetryModel()
	m.resolved = &model.ResolvedMedia{SeriesTitle: "Frieren", Playback: []provider.MediaSource{
		{URL: "https://cdn.example.com/master.m3u8", Quality: "1080p", Resolver: "anikoto"},
	}}
	m.rankedSources = ranking.RankSources(m.resolved.Playback, ranking.Criteria{Mode: provider.ModeAnime})
	m.playOpID = 0

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = mdl.(*modelImpl)
	if m.playOpID == 0 {
		t.Error("restart should schedule playback")
	}
	if m.resolved.StartTime != 0 {
		t.Errorf("StartTime = %v, want 0 (from beginning)", m.resolved.StartTime)
	}
}

// 'r' does nothing while playback is already active.
func TestPreviewRestartIgnoredWhilePlaying(t *testing.T) {
	m := previewRetryModel()
	m.resolved = &model.ResolvedMedia{SeriesTitle: "Frieren", Playback: []provider.MediaSource{
		{URL: "https://cdn.example.com/master.m3u8", Quality: "1080p", Resolver: "anikoto"},
	}}
	m.rankedSources = ranking.RankSources(m.resolved.Playback, ranking.Criteria{Mode: provider.ModeAnime})
	m.playOpID = 7

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = mdl.(*modelImpl)
	if m.playOpID != 7 {
		t.Errorf("playOpID changed to %d, want unchanged", m.playOpID)
	}
	if m.loading {
		t.Error("no-op restart must not set loading")
	}
}

func TestPreviewDForDownloadAndDForDownloadsTab(t *testing.T) {
	m := previewRetryModel()
	m.resolved = &model.ResolvedMedia{SeriesTitle: "Frieren", Playback: []provider.MediaSource{
		{URL: "https://cdn.example.com/master.m3u8", Quality: "1080p", Resolver: "anikoto"},
	}}
	m.rankedSources = ranking.RankSources(m.resolved.Playback, ranking.Criteria{Mode: provider.ModeAnime})

	// Lowercase 'd' navigates to the downloads screen
	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = mdl.(*modelImpl)
	if m.activeView != viewDownloads {
		t.Errorf("activeView = %v, want viewDownloads on 'd'", m.activeView)
	}

	// Pop back to preview
	m.activeView = viewPreview

	// Uppercase 'D' initiates the download
	mdl, _ = m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	m = mdl.(*modelImpl)
	if m.downloadOpID == 0 {
		t.Errorf("downloadOpID should be set on 'D'")
	}
	if m.loading {
		t.Errorf("loading must be false for background downloads, got true")
	}
}

func TestPreviewRetryPreservesDeliveredSources(t *testing.T) {
	m := previewRetryModel()
	reg := &provider.Registry{}
	reg.Register(&repairFake{name: "anikoto", mode: provider.ModeAnime,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/anikoto.m3u8", Quality: "1080p"}}})
	reg.Register(&repairFake{name: "pengu", mode: provider.ModeAnime, err: provider.ErrTimeout})
	m.registry = reg
	m.mediaService = service.NewMediaService(reg)
	series := provider.SearchResult{Title: "Frieren", ID: "151807", Provider: "anikoto", Type: provider.ModeAnime}
	episode := provider.Episode{ID: "watch/anikoto/151807/sub/1", Episode: 1, Season: 1}
	m.selectedSeries = &series
	m.selectedEpisode = &episode

	// Seed resolve: anikoto succeeds, pengu fails.
	_, _ = m.mediaService.Resolve(context.Background(), provider.ModeAnime, series, episode, nil, service.ResolveOptions{})
	m.resolved = &model.ResolvedMedia{
		SeriesTitle: "Frieren",
		Playback: []provider.MediaSource{
			{URL: "https://cdn.example.com/anikoto.m3u8", Quality: "1080p", Resolver: "anikoto"},
		},
	}
	m.refreshRanking()
	m.activeView = viewPreview

	mdl, cmd := m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	m2 := mdl.(*modelImpl)
	if cmd == nil || m2.resolveOpID == 0 {
		t.Fatal("R did not start a retry")
	}
	if len(m2.rankedSources) == 0 {
		t.Fatal("existing delivered sources must be preserved during retry")
	}
	if m2.loading {
		t.Fatal("loading must be false when existing sources are present so table stays visible")
	}
	if m2.resolved == nil || len(m2.resolved.Playback) == 0 {
		t.Fatal("resolved.Playback must retain the kept sources")
	}
}

func stringsEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func TestPreviewMarkCompleteToggle(t *testing.T) {
	store, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(store.Close)

	m := previewRetryModel()
	m.selectedSeries.MediaType = provider.MediaTypeAnime
	m.historyStore = store
	m.activeView = viewPreview
	m.resolved = &model.ResolvedMedia{
		SeriesTitle:   "Frieren",
		EpisodeTitle:  "EP 1",
		MediaType:     provider.MediaTypeAnime,
		SeasonNumber:  1,
		EpisodeNumber: 1,
		StartTime:     120,
	}

	// Press 'm' to mark complete
	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	m = mdl.(*modelImpl)

	key := history.EntryKey{
		Title:     "Frieren",
		Mode:      string(provider.ModeAnime),
		MediaType: provider.MediaTypeAnime,
		Season:    1,
		Episode:   1,
	}
	entry, ok := store.Get(key)
	if !ok || !entry.Complete {
		t.Errorf("expected episode to be marked complete in store")
	}
	if m.hasResumePosition() {
		t.Errorf("hasResumePosition() should be false after marking complete")
	}
	if m.resolved.StartTime != 0 {
		t.Errorf("resolved.StartTime should be 0 after marking complete, got %f", m.resolved.StartTime)
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "marked complete") {
		t.Errorf("expected toast 'marked complete', got %+v", m.activeToast)
	}

	// Press 'm' again to unmark
	mdl, _ = m.updatePreview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	m = mdl.(*modelImpl)

	if _, ok := store.Get(key); ok {
		t.Errorf("expected entry to be deleted after unmarking")
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "unmarked") {
		t.Errorf("expected toast 'unmarked', got %+v", m.activeToast)
	}
}
