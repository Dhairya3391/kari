package tui

import (
	"testing"
	"time"

	"kari/internal/filter"
	"kari/internal/model"
	"kari/internal/provider"
)

// prepareForMode seeds preview state through the domain ranking pipeline with
// neutral preferences, so tests exercise the same rows the TUI renders.
func prepareForMode(sources []provider.MediaSource, mode provider.ContentType, quality int) []filter.RankedSource {
	return filter.Prepare(sources,
		filter.FromLegacy(quality, 0, nil, "", "", false, "").For(mode),
		mode, nil, time.Time{})
}

func TestMiruroAutoRemainsVisibleAcrossQualityFilters(t *testing.T) {
	for _, qualityMode := range []int{qualityAll, qualityHighest, qualityLowest, qualityDataSaver} {
		m := rankingTestModel()
		m.appMode = provider.ModeAnime
		setRankingQuality(m, qualityMode)
		miruro := provider.MediaSource{
			URL: "https://cdn.example.com/miruro.m3u8", Quality: "Auto", Resolver: "miruro",
		}
		bingr := provider.MediaSource{
			URL: "https://cdn.example.com/bingr.m3u8", Quality: "1080p", Resolver: "bingr",
		}
		m.mergeResolved(model.ResolvedMedia{Playback: []provider.MediaSource{miruro}})
		m.mergeResolved(model.ResolvedMedia{Playback: []provider.MediaSource{miruro, bingr}})
		if len(m.rankedSources) != 2 || m.rankedSources[0].Source.URL != miruro.URL {
			t.Fatalf("quality mode %d hid or reordered Miruro Auto: %v", qualityMode, urls(m.rankedSources))
		}
	}
}

func TestPreviewPreferredProviderFirstWithoutQualitySorting(t *testing.T) {
	m := rankingTestModel()
	m.appMode = provider.ModeAnime
	setRankingQuality(m, qualityAll)
	m.setPreferredProvider(provider.ModeAnime, "miruro")
	bingr := provider.MediaSource{URL: "https://cdn.example.com/bingr.m3u8", Quality: "1080p", Resolver: "bingr"}
	auto := provider.MediaSource{URL: "https://cdn.example.com/auto.m3u8", Quality: "Auto", Resolver: "miruro"}
	hd := provider.MediaSource{URL: "https://cdn.example.com/hd.m3u8", Quality: "720p", Resolver: "miruro"}
	m.mergeResolved(model.ResolvedMedia{Playback: []provider.MediaSource{bingr}})
	m.previewSelectedIndex = 0
	m.manualPlaybackSelected = true
	m.mergeResolved(model.ResolvedMedia{Playback: []provider.MediaSource{bingr, auto, hd}})
	want := []string{auto.URL, hd.URL, bingr.URL}
	for i, source := range m.rankedSources {
		if source.Source.URL != want[i] {
			t.Fatalf("preferred-first source order = %v, want %v", urls(m.rankedSources), want)
		}
	}
	if selected, ok := m.selectedPlaybackSource(); !ok || selected.URL != bingr.URL {
		t.Fatal("later preferred-provider arrivals moved the user's selection")
	}
}

func TestPlaybackSelectionDoesNotBypassAudioFilter(t *testing.T) {
	m := rankingTestModel()
	m.setAudioLanguages(filter.NewLanguagePreference(nil, []string{"hi"}))
	m.mergeResolved(model.ResolvedMedia{Playback: []provider.MediaSource{{
		URL: "https://cdn.example.com/hindi.m3u8", Quality: "Auto", Language: "hi",
		Audio: provider.AudioInfo{Languages: []string{"hi"}},
	}}})
	if _, ok := m.selectedPlaybackSource(); ok || len(m.orderedPlaybackSources()) != 0 {
		t.Fatal("filtered sources must not become playback fallbacks")
	}
}

func rankingTestModel() *modelImpl {
	return &modelImpl{
		keys:        defaultKeyMap(),
		appMode:     provider.ModeTV,
		filterScope: filter.FromLegacy(qualityHighest, 0, nil, "", "", false, ""),
		qualityMode: qualityHighest,
		registry:    &provider.Registry{},
	}
}

// setRankingQuality seeds the active mode's typed quality policy. Ranking now
// reads the scope, so tests must drive it through the same mutator as Settings.
func setRankingQuality(m *modelImpl, legacy int) {
	m.setQuality(m.appMode, filter.QualityModeFromLegacy(legacy))
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

	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{sd, uhd}})
	if len(m.rankedSources) != 2 {
		t.Fatalf("visible = %d rows, want 2 (one per provider)", len(m.rankedSources))
	}
	if got := m.rankedSources[0].Source.URL; got != uhd.URL {
		t.Errorf("source = %s, want %s", got, uhd.URL)
	}
	if got := m.rankedSources[1].Source.URL; got != sd.URL {
		t.Errorf("second = %s, want %s", got, sd.URL)
	}
	if m.previewSelectedIndex != 0 {
		t.Errorf("cursor should reset to the available source, got index %d", m.previewSelectedIndex)
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
		t.Errorf("user cursor should stay on row 1, got %d", m.previewSelectedIndex)
	}
	if got := m.rankedSources[m.previewSelectedIndex].Source.URL; got != fhd.URL {
		t.Errorf("cursor row = %s, want %s", got, fhd.URL)
	}
}

func TestMergeResolvedDropsPreviousEpisodeRanking(t *testing.T) {
	m := rankingTestModel()
	stale := provider.MediaSource{URL: "https://old.example.com/ep1.m3u8", Quality: "1080p", Resolver: "Z"}
	m.rankedSources = []filter.RankedSource{{Source: stale}}
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

// Progressive arrivals preserve provider arrival order.
func TestMergeResolvedArrivalOrderAndLateArrival(t *testing.T) {
	m := rankingTestModel()
	fakeaggFHD := provider.MediaSource{URL: "https://cdn.example.com/fakeagg.m3u8", Quality: "1080p", Resolver: "fakeagg"}
	movyFHD := provider.MediaSource{URL: "https://cdn.example.com/movy.m3u8", Quality: "1080p", Resolver: "movysx"}

	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{fakeaggFHD}})
	if len(m.rankedSources) != 1 || m.rankedSources[0].Source.Resolver != "fakeagg" {
		t.Fatalf("single fakeagg row should lead alone, got %+v", m.rankedSources)
	}

	// Movy arrives later: it appends after fakeagg in arrival order.
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{fakeaggFHD, movyFHD}})
	if len(m.rankedSources) != 2 {
		t.Fatalf("ranked = %d rows, want 2", len(m.rankedSources))
	}
	if got := m.rankedSources[0].Source.URL; got != fakeaggFHD.URL {
		t.Errorf("top = %s, want fakeagg first in arrival order", got)
	}
	if got := m.rankedSources[1].Source.URL; got != movyFHD.URL {
		t.Errorf("second = %s, want movy second in arrival order", got)
	}

	// A cursor the user moved off the top stays on its source URL.
	m.previewSelectedIndex = 1
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: []provider.MediaSource{fakeaggFHD, movyFHD}})
	if m.previewSelectedIndex != 1 {
		t.Errorf("user cursor should stay on movy row, got %d", m.previewSelectedIndex)
	}
	if got := m.rankedSources[m.previewSelectedIndex].Source.URL; got != movyFHD.URL {
		t.Errorf("cursor row = %s, want %s", got, movyFHD.URL)
	}
}

// TestMergeResolvedAppendsOnRetry proves that when an excluded provider is retried
// and delivers sources, those sources are merged into the existing playback sources
// instead of overwriting the sources from previously successful providers.
func TestMergeResolvedAppendsOnRetry(t *testing.T) {
	m := rankingTestModel()
	setRankingQuality(m, qualityAll)
	anikotoSrc := provider.MediaSource{URL: "https://cdn.example.com/anikoto.m3u8", Quality: "1080p", Resolver: "anikoto"}
	miruroSrc := provider.MediaSource{URL: "https://cdn.example.com/miruro.m3u8", Quality: "1080p", Resolver: "miruro"}

	// Initial resolve delivered from anikoto and miruro.
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "One Piece", Playback: []provider.MediaSource{anikotoSrc, miruroSrc}})
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

// Quality filters remain active without changing source ordering.
func TestMergeResolvedQualitySettingFiltersSources(t *testing.T) {
	m := rankingTestModel()
	mixed := []provider.MediaSource{
		{URL: "https://cdn.example.com/sd.m3u8", Quality: "480p", Resolver: "A"},
		{URL: "https://cdn.example.com/hd.m3u8", Quality: "720p", Resolver: "A"},
		{URL: "https://cdn.example.com/fhd.m3u8", Quality: "1080p", Resolver: "B"},
		{URL: "https://cdn.example.com/4k.m3u8", Quality: "4K", Resolver: "B"},
	}

	setRankingQuality(m, qualityHighest)
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: mixed})
	if len(m.rankedSources) != 3 {
		t.Fatalf("highest must filter fixed qualities, got %+v", m.rankedSources)
	}

	// Lowest keeps the lowest fixed tier per provider, in arrival order.
	setRankingQuality(m, qualityLowest)
	m.rankAndSelectSources()
	if len(m.rankedSources) != 2 {
		t.Fatalf("lowest must filter each provider's qualities, got %+v", m.rankedSources)
	}

	// Data Saver drops higher fixed tiers while keeping a provider visible.
	setRankingQuality(m, qualityDataSaver)
	m.rankAndSelectSources()
	if len(m.rankedSources) != 3 {
		t.Errorf("data saver must filter higher tiers, got %d", len(m.rankedSources))
	}
	// If no FHD+ source exists anywhere, Highest falls back to the best lower tier.
	mid := []provider.MediaSource{
		{URL: "https://cdn.example.com/hd.m3u8", Quality: "720p", Resolver: "A"},
		{URL: "https://cdn.example.com/sd.m3u8", Quality: "480p", Resolver: "A"},
	}
	m.resolved = nil
	setRankingQuality(m, qualityHighest)
	m.mergeResolved(model.ResolvedMedia{SeriesTitle: "The Boys", Playback: mid})
	if len(m.rankedSources) != 1 || m.rankedSources[0].Source.Quality != "720p" {
		t.Errorf("720p should be the best available fallback, got %+v", m.rankedSources)
	}
}

// Sub-kind ranking now lives inside filter.Prepare: an anime hardsub earns
// the top Key.SubKind plus the hard-sub title boost, while challenged hosts
// still sort last. The signal itself is mode-dependent, not quality-dependent.
func TestHardsubFirstLeadsAnimeTable(t *testing.T) {
	mk := func(url, quality, subType string) provider.MediaSource {
		return provider.MediaSource{URL: url, Quality: quality, SubType: subType, Resolver: "miruro"}
	}
	soft1080 := mk("https://cdn.example.com/soft.m3u8", "1080p", provider.SubTypeSoft)
	hard720 := mk("https://cdn.example.com/hard.m3u8", "720p", provider.SubTypeHard)
	challengedHard := mk("https://vault-01.uwucdn.top/stream/01/uwu.m3u8", "1080p", provider.SubTypeHard)

	got := prepareForMode([]provider.MediaSource{soft1080, hard720, challengedHard}, provider.ModeAnime, qualityAll)
	if len(got) != 3 || got[0].Source.URL != hard720.URL {
		t.Fatalf("hardsub must lead, got %v", urls(got))
	}
	if got[2].Source.URL != challengedHard.URL {
		t.Fatalf("challenged hardsub must stay last, got %v", urls(got))
	}
	rowByURL := func(rows []filter.RankedSource, url string) filter.RankedSource {
		for _, row := range rows {
			if row.Source.URL == url {
				return row
			}
		}
		t.Fatalf("row %s missing from %v", url, urls(rows))
		return filter.RankedSource{}
	}
	if got[0].Key.SubKind != 2 || got[1].Key.SubKind != 0 {
		t.Fatalf("anime hardsub must outrank softsub on SubKind: %+v", got)
	}

	off := filter.Prepare([]provider.MediaSource{soft1080, hard720},
		filter.FromLegacy(qualityAll, 0, nil, "", "", true, "").For(provider.ModeAnime),
		provider.ModeAnime, nil, time.Time{})
	if len(off) != 2 || off[0].Source.URL != hard720.URL {
		t.Fatalf("subs-off must keep the hardsub lead, got %v", urls(off))
	}
	if on, offRow := rowByURL(got, hard720.URL), rowByURL(off, hard720.URL); on.Key.Subtitle == 0 || offRow.Key.Subtitle != 0 {
		t.Fatalf("subs-off must remove the hardsub title boost: on=%+v off=%+v", on.Key, offRow.Key)
	}
	if offRow := rowByURL(off, hard720.URL); offRow.Key.SubKind != 2 || rowByURL(off, soft1080.URL).Key.SubKind != 0 {
		t.Fatalf("subs-off must not alter sub-kind ranking: %+v", off)
	}

	// Non-anime: sub-kind carries no boost and quality decides the order.
	movies := prepareForMode([]provider.MediaSource{soft1080, hard720}, provider.ModeMovies, qualityAll)
	if len(movies) != 2 || movies[0].Source.URL != soft1080.URL {
		t.Fatalf("movies must keep ranked order, got %v", urls(movies))
	}
	for _, row := range movies {
		if row.Key.SubKind != 0 {
			t.Fatalf("movies must not apply anime sub-kind ranking: %+v", row)
		}
	}
}

func TestRankAndSelectSourcesFiltersDisabledAudioLanguages(t *testing.T) {
	m := rankingTestModel()
	m.appMode = provider.ModeMovies
	setRankingQuality(m, qualityAll)
	m.setAudioLanguages(filter.NewLanguagePreference(
		[]string{"ja", "hi"}, []string{"ta", "te"},
	))

	en := provider.MediaSource{URL: "https://cdn.example.com/en.m3u8", Quality: "FHD", Resolver: "movysx", Language: ""}
	hi := provider.MediaSource{URL: "https://cdn.example.com/hi.m3u8", Quality: "FHD", Resolver: "movysx", Language: "hi"}
	ta := provider.MediaSource{URL: "https://cdn.example.com/ta.m3u8", Quality: "FHD", Resolver: "movysx", Language: "ta"}
	te := provider.MediaSource{URL: "https://cdn.example.com/te.m3u8", Quality: "FHD", Resolver: "movysx", Language: "te"}

	m.resolved = &model.ResolvedMedia{
		SeriesTitle: "The Housemaid",
		Playback:    []provider.MediaSource{en, hi, ta, te},
	}
	m.rankAndSelectSources()

	if len(m.rankedSources) != 2 {
		t.Fatalf("expected 2 ranked sources (en, hi), got %d: %+v", len(m.rankedSources), m.rankedSources)
	}
	for _, s := range m.rankedSources {
		if s.Source.Language == "ta" || s.Source.Language == "te" {
			t.Errorf("disabled language %q must not be in rankedSources", s.Source.Language)
		}
	}
}
func TestGroupSourcesByProvider(t *testing.T) {
	m := rankingTestModel()
	m.appMode = provider.ModeMovies
	setRankingQuality(m, qualityAll)

	sampleFHD := provider.MediaSource{URL: "sample-fhd", Quality: "1080p [Sample]", Resolver: "sample"}
	sample4K := provider.MediaSource{URL: "sample-4k", Quality: "2160p [Sample]", Resolver: "sample"}
	movyFHD := provider.MediaSource{URL: "movy-fhd", Quality: "1080p [Movy]", Resolver: "movysx"}
	movy4K := provider.MediaSource{URL: "movy-4k", Quality: "2160p [Movy]", Resolver: "movysx"}
	movyHindi := provider.MediaSource{URL: "movy-hi", Quality: "1080p [Movy]", Resolver: "movysx", Language: "hi"}
	vylaFHD := provider.MediaSource{URL: "vyla-fhd", Quality: "1080P [XDownloader]", Resolver: "vyla"}
	vyla4K := provider.MediaSource{URL: "vyla-4k", Quality: "2160P [111477]", Resolver: "vyla"}

	m.resolved = &model.ResolvedMedia{
		Playback: []provider.MediaSource{sampleFHD, vylaFHD, movyFHD, sample4K, movy4K, vyla4K, movyHindi},
	}
	m.rankAndSelectSources()

	if len(m.rankedSources) != 7 {
		t.Fatalf("expected 7 sources, got %d", len(m.rankedSources))
	}

	want := []string{
		"sample-4k", "sample-fhd",
		"movy-4k", "movy-fhd", "movy-hi",
		"vyla-4k", "vyla-fhd",
	}
	for i, url := range want {
		if m.rankedSources[i].Source.URL != url {
			t.Fatalf("row %d = %s, want %s (got %v)", i, m.rankedSources[i].Source.URL, url, urls(m.rankedSources))
		}
	}
}

func TestGroupSourcesByProviderQualityLowest(t *testing.T) {
	m := rankingTestModel()
	m.appMode = provider.ModeMovies
	setRankingQuality(m, qualityLowest)

	fhd := provider.MediaSource{URL: "movy-fhd", Quality: "1080p", Resolver: "movysx"}
	sd := provider.MediaSource{URL: "movy-480p", Quality: "480p", Resolver: "movysx"}
	fourK := provider.MediaSource{URL: "movy-4k", Quality: "2160p", Resolver: "movysx"}

	m.resolved = &model.ResolvedMedia{Playback: []provider.MediaSource{fhd, fourK, sd}}
	m.rankAndSelectSources()

	if len(m.rankedSources) != 1 || m.rankedSources[0].Source.URL != "movy-480p" {
		t.Errorf("lowest filter must keep SD, got %v", urls(m.rankedSources))
	}
}

func TestGroupSourcesByProviderAnimeAudio(t *testing.T) {
	m := rankingTestModel()
	setRankingQuality(m, qualityAll)
	m.appMode = provider.ModeAnime

	// In anime mode, English audio must not be prioritized above Japanese.
	enSrc := provider.MediaSource{URL: "src-en", Quality: "1080p", Language: "en", Resolver: "anikoto"}
	jaSrc := provider.MediaSource{URL: "src-ja", Quality: "1080p", Language: "ja", Resolver: "anikoto"}

	m.resolved = &model.ResolvedMedia{Playback: []provider.MediaSource{jaSrc, enSrc}}
	m.rankAndSelectSources()

	if len(m.rankedSources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(m.rankedSources))
	}
	if m.rankedSources[0].Source.URL != "src-ja" {
		t.Errorf("anime mode should preserve Japanese track before English, got %s", m.rankedSources[0].Source.URL)
	}
}

func TestGroupSourcesByProviderKeepsChallengedLast(t *testing.T) {
	healthy := provider.MediaSource{
		URL:      "https://cdn.example.com/1080.m3u8",
		Quality:  "1080p",
		Resolver: "miruro",
	}
	challenged := provider.MediaSource{
		URL:      "https://vault-01.uwucdn.top/stream/01/uwu.m3u8",
		Quality:  "2160p",
		Resolver: "miruro",
	}

	got := prepareForMode([]provider.MediaSource{healthy, challenged}, provider.ModeAnime, qualityAll)

	if len(got) != 2 || got[0].Source.URL != healthy.URL || got[1].Source.URL != challenged.URL {
		t.Fatalf("challenged host must stay last, got %v", urls(got))
	}
	if got[1].Key.Playable >= got[0].Key.Playable {
		t.Fatalf("challenged host must be demoted by transport, got %+v", got)
	}
}

func TestGroupSourcesByProviderDataSaver(t *testing.T) {
	fourK := provider.MediaSource{URL: "4k", Quality: "2160p", Resolver: "fourk"}
	fhd := provider.MediaSource{URL: "fhd", Quality: "1080p", Resolver: "movysx"}
	hd := provider.MediaSource{URL: "hd", Quality: "720p", Resolver: "movysx"}
	unknown := provider.MediaSource{URL: "unknown", Quality: "Unknown", Resolver: "movysx"}

	got := filter.GroupByProvider(
		prepareForMode([]provider.MediaSource{fourK, hd, unknown, fhd}, provider.ModeMovies, qualityDataSaver), "")

	want := []string{"fhd", "hd", "unknown", "4k"}
	gotURLs := urls(got)
	if len(gotURLs) != len(want) {
		t.Fatalf("data saver order = %v, want %v", gotURLs, want)
	}
	for i := range want {
		if gotURLs[i] != want[i] {
			t.Fatalf("data saver order = %v, want %v", gotURLs, want)
		}
	}
}

func TestGroupSourcesByProviderLowestKeepsUnknownLast(t *testing.T) {
	unknown := provider.MediaSource{URL: "unknown", Quality: "Unknown", Resolver: "movysx"}
	hd := provider.MediaSource{URL: "hd", Quality: "720p", Resolver: "movysx"}
	sd := provider.MediaSource{URL: "sd", Quality: "480p", Resolver: "movysx"}

	// Neutral ranking still places the unknown-quality row last.
	got := filter.GroupByProvider(
		prepareForMode([]provider.MediaSource{unknown, hd, sd}, provider.ModeMovies, qualityAll), "")
	want := []string{"hd", "sd", "unknown"}
	gotURLs := urls(got)
	if len(gotURLs) != len(want) {
		t.Fatalf("quality order = %v, want %v", gotURLs, want)
	}
	for i := range want {
		if gotURLs[i] != want[i] {
			t.Fatalf("quality order = %v, want %v", gotURLs, want)
		}
	}

	lowest := filter.GroupByProvider(
		prepareForMode([]provider.MediaSource{unknown, hd, sd}, provider.ModeMovies, qualityLowest), "")
	if len(lowest) != 1 || lowest[0].Source.URL != sd.URL {
		t.Fatalf("lowest order = %v, want only %s", urls(lowest), sd.URL)
	}
}

func urls(s []filter.RankedSource) []string {
	out := make([]string, 0, len(s))
	for _, v := range s {
		out = append(out, v.Source.URL)
	}
	return out
}
