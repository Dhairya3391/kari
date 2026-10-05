package tui

// Preview must re-rank on every resolve snapshot. Providers stream in
// progressively, and the old ensurePlaybackSelection no-op meant the first
// snapshot's order stuck: later arrivals (e.g. 4K) never appeared and the
// top pick stayed stale.

import (
	"testing"

	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/ranking"
)

func rankingTestModel() *modelImpl {
	return &modelImpl{
		keys:        defaultKeyMap(),
		appMode:     provider.ModeTV,
		qualityMode: qualityHighest,
		registry:    &provider.Registry{},
	}
}

func TestMergeResolvedReranksProgressiveArrivals(t *testing.T) {
	m := rankingTestModel()
	sd := provider.MediaSource{URL: "https://cdn.example.com/sd.m3u8", Quality: "480p", Resolver: "A"}
	uhd := provider.MediaSource{URL: "https://cdn.example.com/4k.m3u8", Quality: "4K", Resolver: "B"}

	// First provider arrives with only SD.
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{sd}})
	if len(m.rankedSources) != 1 {
		t.Fatalf("ranked = %d rows, want 1", len(m.rankedSources))
	}

	// Second provider adds 4K: under Highest the table narrows to the
	// FHD-or-better rows and best moves to top.
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{sd, uhd}})
	if len(m.rankedSources) != 1 {
		t.Fatalf("ranked = %d rows, want 1 (4K only under Highest)", len(m.rankedSources))
	}
	if got := m.rankedSources[0].Source.URL; got != uhd.URL {
		t.Errorf("best = %s, want %s under Highest", got, uhd.URL)
	}
	if m.previewSelectedIndex != 0 {
		t.Errorf("cursor should track the new best, got %d", m.previewSelectedIndex)
	}
}

func TestMergeResolvedKeepsUserCursor(t *testing.T) {
	m := rankingTestModel()
	fhd := provider.MediaSource{URL: "https://cdn.example.com/fhd.m3u8", Quality: "1080p", Resolver: "A"}
	uhd := provider.MediaSource{URL: "https://cdn.example.com/4k.m3u8", Quality: "4K", Resolver: "B"}
	snap := model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{fhd, uhd}}

	m.mergeResolved(snap)
	// User moves off best onto the FHD row.
	m.previewSelectedIndex = 1
	m.mergeResolved(snap)
	if m.previewSelectedIndex != 1 {
		t.Errorf("user cursor should stay on FHD row, got %d", m.previewSelectedIndex)
	}
	if got := m.rankedSources[m.previewSelectedIndex].Source.URL; got != fhd.URL {
		t.Errorf("cursor row = %s, want %s", got, fhd.URL)
	}
}

func TestMergeResolvedDropsPreviousEpisodeRanking(t *testing.T) {
	m := rankingTestModel()
	stale := provider.MediaSource{URL: "https://old.example.com/ep1.m3u8", Quality: "1080p", Resolver: "Z"}
	m.rankedSources = []ranking.ScoredSource{{Source: stale}}
	m.previewSelectedIndex = 0

	fresh := provider.MediaSource{URL: "https://cdn.example.com/ep2.m3u8", Quality: "720p", Resolver: "A"}
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{fresh}})
	if len(m.rankedSources) != 1 || m.rankedSources[0].Source.URL != fresh.URL {
		t.Errorf("fresh session must re-rank from scratch, got %+v", m.rankedSources)
	}
	if m.previewSelectedIndex != 0 {
		t.Errorf("cursor should reset to best, got %d", m.previewSelectedIndex)
	}
}

// Movy leads whenever present, even when it arrives in a later progressive
// snapshot: every merge re-ranks, so the list updates dynamically and a
// cursor sitting on the top row tracks the new movy pick.
func TestMergeResolvedMovyFirstAndLateArrival(t *testing.T) {
	m := rankingTestModel()
	penguFHD := provider.MediaSource{URL: "https://cdn.example.com/pengu.m3u8", Quality: "1080p", Resolver: "pengu"}
	movyFHD := provider.MediaSource{URL: "https://cdn.example.com/movy.m3u8", Quality: "1080p", Resolver: "movysx"}

	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{penguFHD}})
	if len(m.rankedSources) != 1 || m.rankedSources[0].Source.Resolver != "pengu" {
		t.Fatalf("single pengu row should lead alone, got %+v", m.rankedSources)
	}

	// Movy arrives late: it jumps to top and the top-tracking cursor follows.
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{penguFHD, movyFHD}})
	if len(m.rankedSources) != 2 {
		t.Fatalf("ranked = %d rows, want 2", len(m.rankedSources))
	}
	if got := m.rankedSources[0].Source.URL; got != movyFHD.URL {
		t.Errorf("top = %s, want movy first", got)
	}
	if m.previewSelectedIndex != 0 {
		t.Errorf("cursor should track the new movy top pick, got %d", m.previewSelectedIndex)
	}

	// A cursor the user moved off the top stays on its source URL.
	m.previewSelectedIndex = 1
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{penguFHD, movyFHD}})
	if m.previewSelectedIndex != 1 {
		t.Errorf("user cursor should stay on pengu row, got %d", m.previewSelectedIndex)
	}
	if got := m.rankedSources[m.previewSelectedIndex].Source.URL; got != penguFHD.URL {
		t.Errorf("cursor row = %s, want %s", got, penguFHD.URL)
	}
}

// TestMergeResolvedAppendsOnRetry proves that when an excluded provider is retried
// and delivers sources, those sources are merged into the existing playback sources
// instead of overwriting the sources from previously successful providers.
func TestMergeResolvedAppendsOnRetry(t *testing.T) {
	m := rankingTestModel()
	m.qualityMode = qualityAll
	anikotoSrc := provider.MediaSource{URL: "https://cdn.example.com/anikoto.m3u8", Quality: "1080p", Resolver: "anikoto"}
	anilightSrc := provider.MediaSource{URL: "https://cdn.example.com/anilight.m3u8", Quality: "1080p", Resolver: "anilight"}

	// Initial resolve delivered from anikoto and anilight.
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "One Piece", Playback: []provider.MediaSource{anikotoSrc, anilightSrc}})
	if len(m.resolved.Playback) != 2 {
		t.Fatalf("initial playback = %d, want 2", len(m.resolved.Playback))
	}

	// Retry on failed provider 'l' succeeds and delivers its sources only.
	lSrc := provider.MediaSource{URL: "https://cdn.example.com/l.m3u8", Quality: "1080p", Resolver: "l"}
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "One Piece", Playback: []provider.MediaSource{lSrc}})

	// All 3 providers' sources must be present.
	if len(m.resolved.Playback) != 3 {
		t.Fatalf("after retry playback = %d sources, want 3; got: %+v", len(m.resolved.Playback), m.resolved.Playback)
	}
	if len(m.rankedSources) != 3 {
		t.Fatalf("after retry ranked = %d sources, want 3; got: %+v", len(m.rankedSources), m.rankedSources)
	}
}

// The Stream quality setting preselects the best match. Under Highest only
// FHD (1080p) and above stay visible, falling back to the highest available
// tier when nothing reaches FHD; other modes order every row without hiding.
func TestMergeResolvedQualitySettingOrdersNotHides(t *testing.T) {
	m := rankingTestModel()
	mixed := []provider.MediaSource{
		{URL: "https://cdn.example.com/sd.m3u8", Quality: "480p", Resolver: "A"},
		{URL: "https://cdn.example.com/hd.m3u8", Quality: "720p", Resolver: "A"},
		{URL: "https://cdn.example.com/fhd.m3u8", Quality: "1080p", Resolver: "B"},
		{URL: "https://cdn.example.com/4k.m3u8", Quality: "4K", Resolver: "B"},
	}

	// Highest: 4K preselected, FHD next, HD and below hidden.
	m.qualityMode = qualityHighest
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: mixed})
	if len(m.rankedSources) != 2 {
		t.Fatalf("highest must keep FHD and above only, got %+v", m.rankedSources)
	}
	if got := m.rankedSources[0].Source.URL; got != "https://cdn.example.com/4k.m3u8" {
		t.Errorf("top pick = %s, want 4K under Highest", got)
	}
	if got := m.rankedSources[1].Source.URL; got != "https://cdn.example.com/fhd.m3u8" {
		t.Errorf("second pick = %s, want FHD directly under 4K", got)
	}

	// Lowest: same rows, smallest tier preselected.
	m.qualityMode = qualityLowest
	m.rankAndSelectSources()
	if len(m.rankedSources) != 4 {
		t.Fatalf("lowest must keep every row, got %+v", m.rankedSources)
	}
	if got := m.rankedSources[0].Source.URL; got != "https://cdn.example.com/sd.m3u8" {
		t.Errorf("top pick = %s, want 480p under Lowest", got)
	}

	// Data Saver: caps preference at 1080p, so FHD leads.
	m.qualityMode = qualityDataSaver
	m.rankAndSelectSources()
	if got := m.rankedSources[0].Source.URL; got != "https://cdn.example.com/fhd.m3u8" {
		t.Errorf("top pick = %s, want FHD under Data Saver", got)
	}
	if len(m.rankedSources) != 4 {
		t.Errorf("data saver must keep every row, got %d", len(m.rankedSources))
	}

	// A mid-only list under Highest keeps its best tier only.
	mid := []provider.MediaSource{
		{URL: "https://cdn.example.com/hd.m3u8", Quality: "720p", Resolver: "A"},
		{URL: "https://cdn.example.com/sd.m3u8", Quality: "480p", Resolver: "A"},
	}
	m.resolved = nil
	m.qualityMode = qualityHighest
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: mid})
	if len(m.rankedSources) != 1 || m.rankedSources[0].Source.Quality != "720p" {
		t.Errorf("highest-available 720p must be the only row, got %+v", m.rankedSources)
	}
}

// Hard-subtitled anime rows lead the table even below higher-quality
// softsubs: burned-in subs need no plumbing and cannot desync. Challenged
// hosts never join the lead, and size-first modes keep their semantics.
func TestHardsubFirstLeadsAnimeTable(t *testing.T) {
	mk := func(url, quality, subType string) ranking.ScoredSource {
		return ranking.ScoredSource{Source: provider.MediaSource{URL: url, Quality: quality, SubType: subType, Resolver: "miruro"}}
	}
	soft1080 := mk("https://cdn.example.com/soft.m3u8", "1080p", provider.SubTypeSoft)
	hard720 := mk("https://cdn.example.com/hard.m3u8", "720p", provider.SubTypeHard)
	challengedHard := mk("https://vault-01.uwucdn.top/stream/01/uwu.m3u8", "1080p", provider.SubTypeHard)

	got := hardsubFirst(provider.ModeAnime, qualityAll, true, []ranking.ScoredSource{soft1080, hard720, challengedHard})
	if len(got) != 3 || got[0].Source.URL != hard720.Source.URL {
		t.Fatalf("hardsub must lead, got %v", urls(got))
	}
	if got[2].Source.URL != challengedHard.Source.URL {
		t.Fatalf("challenged hardsub must stay last, got %v", urls(got))
	}

	// Subtitles off: no hardsub lift.
	if got := hardsubFirst(provider.ModeAnime, qualityAll, false, []ranking.ScoredSource{soft1080, hard720}); got[0].Source.URL != soft1080.Source.URL {
		t.Fatalf("subs-off must keep ranked order, got %v", urls(got))
	}
	// Lowest mode: size-first semantics win.
	if got := hardsubFirst(provider.ModeAnime, qualityLowest, true, []ranking.ScoredSource{soft1080, hard720}); got[0].Source.URL != soft1080.Source.URL {
		t.Fatalf("lowest mode must keep ranked order, got %v", urls(got))
	}
	// Non-anime: untouched.
	if got := hardsubFirst(provider.ModeMovies, qualityAll, true, []ranking.ScoredSource{soft1080, hard720}); got[0].Source.URL != soft1080.Source.URL {
		t.Fatalf("movies must keep ranked order, got %v", urls(got))
	}
}

func urls(s []ranking.ScoredSource) []string {
	out := make([]string, 0, len(s))
	for _, v := range s {
		out = append(out, v.Source.URL)
	}
	return out
}
