package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"kari/internal/config"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/provider/defaults"
	"kari/internal/tmdb"
)

// stubProvider is a minimal provider double for MediaService tests.
type stubProvider struct {
	name    string
	mode    provider.ContentType
	results []provider.SearchResult
	err     error
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Modes() []provider.Mode {
	return []provider.Mode{{Name: s.mode, Priority: 1}}
}
func (s *stubProvider) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return s.results, s.err
}
func (s *stubProvider) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return nil, errors.New("not implemented")
}
func (s *stubProvider) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	return nil, errors.New("not implemented")
}

func newTestRegistry(providers ...provider.Provider) *provider.Registry {
	r := &provider.Registry{}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

func TestMediaServiceSearchStampsProviderAndAggregates(t *testing.T) {
	a := &stubProvider{name: "alpha", mode: provider.ModeMovies, results: []provider.SearchResult{
		{Title: "A1", ID: "1", MediaType: provider.MediaTypeMovie},
	}}
	b := &stubProvider{name: "beta", mode: provider.ModeMovies, results: []provider.SearchResult{
		{Title: "B1", ID: "2", MediaType: provider.MediaTypeMovie},
	}}

	svc := NewMediaService(newTestRegistry(a, b))
	results, used, warnings, err := svc.Search(context.Background(), provider.ModeMovies, "query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 || used != "query" || len(warnings) != 0 {
		t.Fatalf("results=%d used=%q warnings=%v", len(results), used, warnings)
	}
	if results[0].Provider != "alpha" || results[1].Provider != "beta" {
		t.Fatalf("provider stamping wrong: %+v", results)
	}
}
func TestMediaServiceSearchDedupesAnimeByAniListID(t *testing.T) {
	p1 := &stubProvider{
		name: "anikoto",
		mode: provider.ModeAnime,
		results: []provider.SearchResult{
			{Title: "One Piece", ID: "21", Type: provider.ModeAnime, Year: "1999"},
			{Title: "Solo Leveling", ID: "151807", Type: provider.ModeAnime, Year: "2024"},
		},
	}
	p2 := &stubProvider{
		name: "anilight",
		mode: provider.ModeAnime,
		results: []provider.SearchResult{
			{Title: "One Piece", ID: "21", Type: provider.ModeAnime, Year: "1999"},
			{Title: "Other Anime", ID: "999", Type: provider.ModeAnime, Year: "2021"},
		},
	}

	svc := NewMediaService(newTestRegistry(p1, p2))
	results, _, _, err := svc.Search(context.Background(), provider.ModeAnime, "One Piece")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 unique results, got %d: %+v", len(results), results)
	}
	if results[0].ID != "21" || results[0].Provider != "anikoto" {
		t.Errorf("expected first result to be anikoto 21, got %+v", results[0])
	}
	if results[1].ID != "151807" || results[2].ID != "999" {
		t.Errorf("unexpected results order/IDs: %+v", results)
	}
}


func TestMediaServiceSearchCollectsWarningsAndFailsWhenEmpty(t *testing.T) {
	a := &stubProvider{name: "alpha", mode: provider.ModeAnime, err: fmt.Errorf("boom")}
	svc := NewMediaService(newTestRegistry(a))

	results, _, warnings, err := svc.Search(context.Background(), provider.ModeAnime, "q")
	if err == nil {
		t.Fatal("all-providers-failure should surface an error")
	}
	if len(warnings) != 1 || results != nil {
		t.Fatalf("warnings=%v results=%v", warnings, results)
	}
}

func TestMatchesAudioMode(t *testing.T) {
	tests := []struct {
		audio     string
		audioMode string
		want      bool
	}{
		{"", "", true},
		{"sub", "", true},                   // no preference => everything matches
		{"", provider.AudioSub, true},       // untagged episodes always pass
		{"Subbed", provider.AudioSub, true}, // prefix normalization
		{"DUB", provider.AudioDub, true},
		{"sub", provider.AudioDub, false},
		{"dub", provider.AudioSub, false},
	}
	for _, tt := range tests {
		if got := matchesAudioMode(tt.audio, tt.audioMode); got != tt.want {
			t.Errorf("matchesAudioMode(%q,%q)=%v want %v", tt.audio, tt.audioMode, got, tt.want)
		}
	}
}

func TestSourceAggregatorDedupesSourcesAndSubtitles(t *testing.T) {
	agg := newSourceAggregator([]provider.Provider{
		&stubProvider{name: "first", mode: provider.ModeTV},
		&stubProvider{name: "second", mode: provider.ModeTV},
	}, func(n string) string { return n })

	batch := []provider.MediaSource{
		{URL: "http://a", Quality: "1080p"},
		{URL: "http://b", Quality: "720p", Referer: "http://ref"},
		// duplicate transport identity of the first entry
		{URL: "http://a", Quality: "1080p"},
		{URL: "", Quality: "junk"}, // blank URL ignored
	}
	agg.add("first", batch)

	subBatch := []provider.MediaSource{{
		URL: "http://c", Quality: "480p",
		Subtitles: []provider.SubtitleOption{
			{URL: "http://sub1", Language: "en"},
			{URL: "http://SUB1", Language: "en"}, // same URL different case still distinct by URL
			{URL: "http://sub1", Language: "en"}, // exact dup dropped
		},
	}}
	agg.add("second", subBatch)

	if len(agg.sources) != 3 {
		t.Fatalf("want 3 unique sources, got %d: %+v", len(agg.sources), agg.sources)
	}
	if agg.sources[0].Resolver != "first" || agg.sources[2].Resolver != "second" {
		t.Fatalf("resolver not stamped: %+v", agg.sources)
	}
	if len(agg.subs) != 2 {
		t.Fatalf("want 2 subtitle tracks (dup URL dropped), got %d", len(agg.subs))
	}
	if agg.subs[0].Label != "English (second)" {
		t.Fatalf("subtitle label wrong: %q", agg.subs[0].Label)
	}
	if agg.subs[0].Resolver != "second" {
		t.Fatalf("subtitle resolver not stamped: %+v", agg.subs[0])
	}
}

func TestSourceAggregatorSortQualityThenPriority(t *testing.T) {
	first := &stubProvider{name: "first", mode: provider.ModeTV}
	second := &stubProvider{name: "second", mode: provider.ModeTV}
	agg := newSourceAggregator([]provider.Provider{first, second}, func(n string) string { return n })

	agg.add("second", []provider.MediaSource{{URL: "http://hi-2", Quality: "1080p"}})
	agg.add("first", []provider.MediaSource{
		{URL: "http://lo-1", Quality: "480p"},
		{URL: "http://hi-1", Quality: "1080p"},
	})
	agg.sort()

	wantOrder := []string{"http://hi-1", "http://hi-2", "http://lo-1"}
	for i, want := range wantOrder {
		if agg.sources[i].URL != want {
			t.Fatalf("position %d: got %s want %s (order=%+v)", i, agg.sources[i].URL, want, agg.sources)
		}
	}
}

func TestResolveUsesCrossProviderTMDBID(t *testing.T) {
	// The series came from "origin"; "other" must receive the TMDB id as
	// mediaID rather than origin's opaque handle.
	var gotID string
	origin := &stubProvider{name: "origin", mode: provider.ModeMovies,
		results: []provider.SearchResult{{Title: "T", ID: "opaque", TMDBID: 42, MediaType: provider.MediaTypeMovie}}}
	other := &resolveCapture{name: "other", mode: provider.ModeMovies, capture: &gotID}

	svc := NewMediaService(newTestRegistry(origin, other))
	_, err := svc.Resolve(context.Background(), provider.ModeMovies,
		provider.SearchResult{Title: "T", ID: "opaque", Provider: "origin", TMDBID: 42, MediaType: provider.MediaTypeMovie},
		provider.Episode{}, nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if gotID != "42" {
		t.Fatalf("cross-provider mediaID = %q, want \"42\"", gotID)
	}
}
func TestResolveUsesCrossProviderAnimeAniListID(t *testing.T) {
	var gotID string
	var gotAudio string
	origin := &stubProvider{name: "anikoto", mode: provider.ModeAnime,
		results: []provider.SearchResult{{Title: "One Piece", ID: "21", Type: provider.ModeAnime, MediaType: provider.MediaTypeAnime}}}
	other := &resolveCaptureWithAudio{name: "anilight", mode: provider.ModeAnime, captureID: &gotID, captureAudio: &gotAudio}

	svc := NewMediaService(newTestRegistry(origin, other))
	_, err := svc.Resolve(context.Background(), provider.ModeAnime,
		provider.SearchResult{Title: "One Piece", ID: "21", Provider: "anikoto", Type: provider.ModeAnime, MediaType: provider.MediaTypeAnime},
		provider.Episode{Episode: 1, Audio: "dub", ID: "watch/anikoto/21/dub/1"}, nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if gotID != "21" {
		t.Fatalf("cross-provider anime mediaID = %q, want \"21\"", gotID)
	}
	if gotAudio != "dub" {
		t.Fatalf("cross-provider anime audio = %q, want \"dub\"", gotAudio)
	}
}

type resolveCaptureWithAudio struct {
	name         string
	mode         provider.ContentType
	captureID    *string
	captureAudio *string
}

func (r *resolveCaptureWithAudio) Name() string { return r.name }
func (r *resolveCaptureWithAudio) Modes() []provider.Mode {
	return []provider.Mode{{Name: r.mode, Priority: 2}}
}
func (r *resolveCaptureWithAudio) Search(ctx context.Context, q string, m provider.ContentType) ([]provider.SearchResult, error) {
	return nil, provider.ErrNoResults
}
func (r *resolveCaptureWithAudio) FetchEpisodes(ctx context.Context, s provider.SearchResult) ([]provider.Episode, error) {
	return nil, provider.ErrNoEpisodes
}
func (r *resolveCaptureWithAudio) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	*r.captureID = mediaID
	*r.captureAudio = episode.Audio
	return []provider.MediaSource{{URL: "http://anilight-stream", Quality: "1080p"}}, nil
}
func TestLiveAnimeResolution(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live test in short mode")
	}
	cfg := &config.Config{
		TMDBAPIKeys: []string{"test"},
	}
	keyPool := tmdb.NewKeyPool(cfg.TMDBAPIKeys)
	reg, err := defaults.NewDefaultRegistry(keyPool, cfg)
	if err != nil {
		t.Fatalf("defaults.NewDefaultRegistry failed: %v", err)
	}

	svc := NewMediaService(reg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Resolve One Piece Ep 1177
	series := provider.SearchResult{
		Title:     "ONE PIECE",
		ID:        "21",
		Provider:  "anikoto",
		Type:      provider.ModeAnime,
		MediaType: provider.MediaTypeAnime,
	}
	ep := provider.Episode{
		Episode: 1177,
		Audio:   "sub",
		ID:      "watch/anikoto/21/sub/1177",
	}
	resolved, err := svc.Resolve(ctx, provider.ModeAnime, series, ep, nil)
	if err != nil {
		t.Logf("Resolve warning (live API): %v", err)
		return
	}
	t.Logf("Resolved sources: %d", len(resolved.Playback))
	for _, s := range resolved.Playback {
		t.Logf("  - Source: [%s] %s %s", s.Resolver, s.Quality, s.URL)
	}
	t.Logf("Resolved subtitles: %d", len(resolved.Subtitles))
	for _, sub := range resolved.Subtitles {
		t.Logf("  - Sub: [%s] %s (%s)", sub.Resolver, sub.Label, sub.Language)
	}
}

type resolveCapture struct {
	name    string
	mode    provider.ContentType
	capture *string
}

func (r *resolveCapture) Name() string { return r.name }
func (r *resolveCapture) Modes() []provider.Mode {
	return []provider.Mode{{Name: r.mode, Priority: 2}}
}
func (r *resolveCapture) Search(ctx context.Context, q string, m provider.ContentType) ([]provider.SearchResult, error) {
	return nil, provider.ErrNoResults
}
func (r *resolveCapture) FetchEpisodes(ctx context.Context, s provider.SearchResult) ([]provider.Episode, error) {
	return nil, provider.ErrNoEpisodes
}
func (r *resolveCapture) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	*r.capture = mediaID
	return []provider.MediaSource{{URL: "http://x", Quality: "720p"}}, nil
}

// streamFake delivers two batches over the streaming channel, simulating a
// provider whose aggregation continues after onResult has seen a snapshot.
type streamFake struct {
	name string
	mode provider.ContentType
}

func (s *streamFake) Name() string { return s.name }
func (s *streamFake) Modes() []provider.Mode {
	return []provider.Mode{{Name: s.mode, Priority: 2}}
}
func (s *streamFake) Search(ctx context.Context, q string, m provider.ContentType) ([]provider.SearchResult, error) {
	return nil, provider.ErrNoResults
}
func (s *streamFake) FetchEpisodes(ctx context.Context, sr provider.SearchResult) ([]provider.Episode, error) {
	return nil, provider.ErrNoEpisodes
}
func (s *streamFake) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	return nil, provider.ErrNoSources
}
func (s *streamFake) ResolveStream(ctx context.Context, mediaID string, episode provider.Episode, updates chan<- []provider.MediaSource) error {
	updates <- []provider.MediaSource{{URL: "http://first", Quality: "720p"}}
	updates <- []provider.MediaSource{{URL: "http://second", Quality: "1080p"}}
	return nil
}

func TestResolvedMediaSnapshotIsCopied(t *testing.T) {
	// Regression guard: snapshots handed to onResult must not alias the
	// aggregator's backing arrays — later batches mutating aggregation must
	// leave earlier snapshots untouched.
	svc := NewMediaService(newTestRegistry(&streamFake{name: "stream", mode: provider.ModeMovies}))

	var firstSnap model.ResolvedMedia
	snapCount := 0
	final, err := svc.Resolve(context.Background(), provider.ModeMovies,
		provider.SearchResult{Title: "T", ID: "1", Provider: "stream", MediaType: provider.MediaTypeMovie},
		provider.Episode{}, func(resolved model.ResolvedMedia) {
			if snapCount == 0 {
				firstSnap = resolved
			}
			snapCount++
		})
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if snapCount < 2 {
		t.Fatalf("expected at least 2 snapshots, got %d", snapCount)
	}
	if len(firstSnap.Playback) != 1 || firstSnap.Playback[0].URL != "http://first" {
		t.Fatalf("first snapshot wrong: %+v", firstSnap.Playback)
	}

	// Mutate the final aggregate's backing data via the returned slice; the
	// earlier snapshot must be unaffected.
	for i := range final.Playback {
		final.Playback[i].URL = "http://mutated"
	}
	if firstSnap.Playback[0].URL != "http://first" {
		t.Fatal("onResult snapshot aliases the aggregator's backing array")
	}
}

func TestAggregatorSortPrioritizesVidKing(t *testing.T) {
	agg := &sourceAggregator{
		priority: map[string]int{
			"vidking": 0,
			"pengu":   1,
		},
		sources: []provider.MediaSource{
			{Resolver: "pengu", Quality: "4K [4KHDHub]"},
			{Resolver: "vidking", Quality: "1080p"},
			{Resolver: "pengu", Quality: "1080p [VegaMovies]"},
			{Resolver: "vidking", Quality: "2160p"},
		},
	}

	agg.sort()

	if len(agg.sources) != 4 {
		t.Fatalf("expected 4 sources, got %d", len(agg.sources))
	}

	// VidKing sources must be at the top, sorted by quality
	if agg.sources[0].Resolver != "vidking" || agg.sources[0].Quality != "2160p" {
		t.Errorf("sources[0] = %+v, want vidking 2160p", agg.sources[0])
	}
	if agg.sources[1].Resolver != "vidking" || agg.sources[1].Quality != "1080p" {
		t.Errorf("sources[1] = %+v, want vidking 1080p", agg.sources[1])
	}
	// Pengu sources follow, sorted by quality
	if agg.sources[2].Resolver != "pengu" || agg.sources[2].Quality != "4K [4KHDHub]" {
		t.Errorf("sources[2] = %+v, want pengu 4K", agg.sources[2])
	}
	if agg.sources[3].Resolver != "pengu" || agg.sources[3].Quality != "1080p [VegaMovies]" {
		t.Errorf("sources[3] = %+v, want pengu 1080p", agg.sources[3])
	}
}

func TestAggregatorSortPrioritizesVidstream2Anikoto(t *testing.T) {
	agg := &sourceAggregator{
		priority: map[string]int{
			"anikoto":  0,
			"anilight": 1,
			"reanime":  2,
		},
		sources: []provider.MediaSource{
			{Resolver: "anilight", Quality: "Auto (l)", URL: "http://anilight-stream"},
			{Resolver: "reanime", Quality: "Auto (HD1)", URL: "http://reanime-stream"},
			{Resolver: "anikoto", Quality: "1080p (HD-2)", URL: "http://anikoto-hd2"},
			{Resolver: "anikoto", Quality: "1080p (Vidstream-2)", URL: "http://anikoto-vidstream2"},
		},
	}

	agg.sort()

	if len(agg.sources) != 4 {
		t.Fatalf("expected 4 sources, got %d", len(agg.sources))
	}

	// Vidstream-2 from Anikoto MUST be at position 0
	if agg.sources[0].Resolver != "anikoto" || agg.sources[0].Quality != "1080p (Vidstream-2)" {
		t.Errorf("sources[0] = %+v, want anikoto Vidstream-2", agg.sources[0])
	}
}
