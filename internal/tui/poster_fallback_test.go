package tui

// Behavior pins for poster fallback: terminals with no graphics protocol
// (tmux, plain xterm) still get quadrant-block art instead of nothing.

import (
	"errors"
	"testing"

	"kari/internal/model"
	"kari/internal/poster"
	"kari/internal/provider"
	"kari/internal/termimg"
	"kari/internal/util"
)

// providerSearchResultFrieren is a minimal selected series for prefetch tests.
func providerSearchResultFrieren() provider.SearchResult {
	return provider.SearchResult{Title: "Frieren", ID: "151807", Type: provider.ModeAnime, MediaType: provider.MediaTypeAnime}
}

func TestPreviewPosterFetchFailureKeepsExistingPoster(t *testing.T) {
	m := &modelImpl{previewPoster: "POSTER_ART", previewPosterOpID: 7}
	m.Update(posterLoadedMsg{slot: posterSlotPreview, opID: 7, err: errors.New("temporary fetch failure")})
	if m.previewPoster != "POSTER_ART" {
		t.Fatalf("preview poster = %q, want existing poster preserved", m.previewPoster)
	}
}

func TestEffectiveImgProtocol(t *testing.T) {
	m := &modelImpl{imgProtocol: termimg.ProtocolNone}
	if got := m.effectiveImgProtocol(); got != termimg.ProtocolBlocks {
		t.Errorf("effectiveImgProtocol(none) = %v, want blocks", got)
	}
	for _, p := range []termimg.Protocol{termimg.ProtocolBlocks, termimg.ProtocolSixel, termimg.ProtocolIterm, termimg.ProtocolKitty} {
		m := &modelImpl{imgProtocol: p}
		if got := m.effectiveImgProtocol(); got != p {
			t.Errorf("effectiveImgProtocol(%v) = %v, want unchanged", p, got)
		}
	}
}

// With no graphics protocol, poster fetching must still start (blocks
// fallback) instead of bailing out silently.
func TestFetchPosterCmdFallsBackToBlocks(t *testing.T) {
	m := &modelImpl{
		imgProtocol:   termimg.ProtocolNone,
		imagesEnabled: true,
		posterClient:  poster.NewClient(nil),
		posterCache:   util.NewBoundedCache[string](30),
	}
	cmd := m.fetchPosterCmd(posterSlotSearch, 1, 0, "anime", "Frieren", "", "", searchPosterMaxCols, searchPosterMaxRows, kittySearchImageID)
	if cmd == nil {
		t.Fatal("fetchPosterCmd with ProtocolNone must return a command (blocks fallback), got nil")
	}
}

// Images off still disables fetching entirely.
func TestFetchPosterCmdRespectsImagesOff(t *testing.T) {
	m := &modelImpl{
		imgProtocol:   termimg.ProtocolNone,
		imagesEnabled: false,
		posterClient:  poster.NewClient(nil),
	}
	if cmd := m.fetchPosterCmd(posterSlotSearch, 1, 0, "anime", "Frieren", "", "", searchPosterMaxCols, searchPosterMaxRows, kittySearchImageID); cmd != nil {
		t.Fatal("fetchPosterCmd with images off must return nil")
	}
}

// No selection means no prefetch; a selection prefetches without
// touching slot lifecycle (no opID bump, no clearing).
func TestPrefetchPreviewPoster(t *testing.T) {
	m := &modelImpl{
		imgProtocol:   termimg.ProtocolNone,
		imagesEnabled: true,
		posterClient:  poster.NewClient(nil),
		posterCache:   util.NewBoundedCache[string](30),
	}
	if cmd := m.prefetchPreviewPoster(); cmd != nil {
		t.Fatal("prefetch without selection must return nil")
	}
	frieren := providerSearchResultFrieren()
	m.selectedSeries = &frieren
	opID := m.previewPosterOpID
	_ = m.prefetchPreviewPoster()
	if m.previewPosterOpID != opID {
		t.Error("prefetch must not bump the slot opID (trigger owns lifecycle)")
	}
	if m.previewPoster != "" || m.previewPosterUnavailable {
		t.Error("prefetch must not touch slot state")
	}
}

func TestTriggerPreviewPosterKeepsLoadedSeriesPoster(t *testing.T) {
	series := providerSearchResultFrieren()
	m := &modelImpl{
		resolved:       &model.ResolvedMedia{SeriesTitle: series.Title},
		selectedSeries: &series,
		previewPoster:  "POSTER_ART",
		imagesEnabled:  true,
		imgProtocol:    termimg.ProtocolBlocks,
		posterClient:   poster.NewClient(nil),
		posterCache:    util.NewBoundedCache[string](30),
	}
	if cmd := m.triggerPreviewPoster(); cmd != nil {
		t.Fatal("audio-only re-resolution should not refetch an already loaded series poster")
	}
}

// posterBlock never collapses the column while images are on: loading
// and absent states render explicit placeholders.
func TestPosterBlockPlaceholders(t *testing.T) {
	loading := posterBlock("", false, termimg.ProtocolNone, 0, true)
	if loading == "" {
		t.Fatal("loading poster must render a placeholder, got empty")
	}
	absent := posterBlock("", true, termimg.ProtocolNone, 0, true)
	if absent == "" {
		t.Fatal("absent poster must render a placeholder, got empty")
	}
	if loading == absent {
		t.Error("loading and absent placeholders must differ")
	}
	if got := posterBlock("ART", false, termimg.ProtocolNone, 0, true); got != "ART" {
		t.Errorf("rendered art must pass through, got %q", got)
	}
}

func TestSearchPosterVisibleOnEpisodesView(t *testing.T) {
	m := &modelImpl{
		imagesEnabled: true,
		searchPoster:  "POSTER_ART",
		activeView:    viewEpisodes,
	}
	if !m.searchPosterVisible() {
		t.Errorf("searchPosterVisible must be true on viewEpisodes so poster is not cleaned up")
	}

	m.activeView = viewSearch
	if !m.searchPosterVisible() {
		t.Errorf("searchPosterVisible must be true on viewSearch")
	}

	m.activeView = viewSettings
	if m.searchPosterVisible() {
		t.Errorf("searchPosterVisible must be false on viewSettings")
	}
}
