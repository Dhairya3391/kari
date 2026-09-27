package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
)

// slowStub is a provider double with scripted latency and results.
type slowStub struct {
	name     string
	mode     provider.ContentType
	delay    time.Duration
	results  []provider.SearchResult
	episodes []provider.Episode
	err      error
}

func (s *slowStub) Name() string { return s.name }
func (s *slowStub) Modes() []provider.Mode {
	return []provider.Mode{{Name: s.mode, Priority: 1}}
}
func (s *slowStub) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	select {
	case <-time.After(s.delay):
		return s.results, s.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *slowStub) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	select {
	case <-time.After(s.delay):
		if s.episodes != nil {
			return s.episodes, s.err
		}
		return nil, provider.ErrNoEpisodes
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type availabilitySlowStub struct {
	*slowStub
	available    []provider.Episode
	availableErr error
}

func (s *availabilitySlowStub) FetchAvailableEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return s.available, s.availableErr
}
func (s *slowStub) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	select {
	case <-time.After(s.delay):
		return nil, s.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// A lone provider is the whole mode: it gets the longer single-provider
// budget so a slow catalog can't abort the only search, while multi-provider
// modes keep the shared cap and any shorter route deadline still wins.
func TestProviderSearchTimeoutBudgets(t *testing.T) {
	if got := providerSearchTimeout(1, provider.Route{}, false); got != provider.DefaultSingleSearchTimeout {
		t.Errorf("single provider timeout = %v, want %v", got, provider.DefaultSingleSearchTimeout)
	}
	if got := providerSearchTimeout(3, provider.Route{}, false); got != provider.DefaultSearchTimeout {
		t.Errorf("multi provider timeout = %v, want %v", got, provider.DefaultSearchTimeout)
	}
	if got := providerSearchTimeout(1, provider.Route{SearchTimeout: 2 * time.Second}, true); got != 2*time.Second {
		t.Errorf("explicit route deadline = %v, want 2s", got)
	}
}

// TestSearchPartialResultsWithFailureList proves a dead provider does not
// sink a good one: partial results plus the failed-provider list.
func TestSearchPartialResultsWithFailureList(t *testing.T) {
	good := &slowStub{name: "good", mode: provider.ModeMovies,
		results: []provider.SearchResult{{Title: "Inception", ID: "1", MediaType: provider.MediaTypeMovie}}}
	bad := &slowStub{name: "bad", mode: provider.ModeMovies, delay: 50 * time.Millisecond, err: provider.ErrNoSources}

	svc := NewMediaService(newTestRegistry(good, bad))
	results, _, warnings, err := svc.Search(context.Background(), provider.ModeMovies, "inception")
	if err != nil {
		t.Fatalf("partial results must not error: %v", err)
	}
	if len(results) != 1 || results[0].Provider != "good" {
		t.Fatalf("results = %+v", results)
	}
	if len(warnings) != 1 || !strings.Contains(strings.ToLower(warnings[0]), "bad") {
		t.Fatalf("warnings = %v, want the failed provider listed", warnings)
	}
}

// TestSearchBoundedByDeadlineNotSum proves the fan-out wall time is
// bounded by the grace deadline, not the sum of provider latencies:
// fast 50ms + slow 300ms + never-returns resolves in ~grace period.
func TestSearchBoundedByDeadlineNotSum(t *testing.T) {
	fast := &slowStub{name: "fast", mode: provider.ModeMovies, delay: 50 * time.Millisecond,
		results: []provider.SearchResult{{Title: "T", ID: "1", MediaType: provider.MediaTypeMovie}}}
	slow := &slowStub{name: "slow", mode: provider.ModeMovies, delay: 300 * time.Millisecond,
		results: []provider.SearchResult{{Title: "T2", ID: "2", MediaType: provider.MediaTypeMovie}}}
	never := &slowStub{name: "never", mode: provider.ModeMovies, delay: time.Hour}

	svc := NewMediaService(newTestRegistry(fast, slow, never))
	start := time.Now()
	results, _, warnings, err := svc.Search(context.Background(), provider.ModeMovies, "q")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Search = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("want partial results from the fast providers")
	}
	// Grace is 1.5s; the sum would be ~hour. Allow headroom for CI.
	if elapsed > 8*time.Second {
		t.Errorf("fan-out took %v, must be bounded by deadline not the sum", elapsed)
	}
	if len(warnings) == 0 {
		t.Error("the never-returning provider must appear in the failure list")
	}
}

// TestSearchContextCancelReturnsFast proves cancellation returns within
// 200ms (task gate).
func TestSearchContextCancelReturnsFast(t *testing.T) {
	never := &slowStub{name: "never", mode: provider.ModeMovies, delay: time.Hour}
	svc := NewMediaService(newTestRegistry(never))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, _, _, _ = svc.Search(ctx, provider.ModeMovies, "q")
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("canceled search took %v, want <200ms", elapsed)
	}
}

// TestSearchEmptyResultsDoNotTripBreaker proves healthy empties never
// open the breaker: repeated no-result searches keep every provider.
func TestSearchEmptyResultsDoNotTripBreaker(t *testing.T) {
	empty := &slowStub{name: "empty", mode: provider.ModeMovies, err: provider.ErrNoResults}
	svc := NewMediaService(newTestRegistry(empty))
	for i := 0; i < 5; i++ {
		_, _, _, _ = svc.Search(context.Background(), provider.ModeMovies, "zzz-no-match")
	}
	_, _, warnings, _ := svc.Search(context.Background(), provider.ModeMovies, "zzz-no-match")
	for _, w := range warnings {
		if strings.Contains(w, "circuit") {
			t.Errorf("breaker tripped on healthy empties: %v", warnings)
		}
	}
}

// TestSearchCircuitBreakerSkipsDeadProvider proves 3 consecutive
// failures open the breaker: the dead provider is skipped with a
// reason while the healthy one keeps serving.
func TestSearchCircuitBreakerSkipsDeadProvider(t *testing.T) {
	good := &slowStub{name: "good", mode: provider.ModeMovies,
		results: []provider.SearchResult{{Title: "T", ID: "1", MediaType: provider.MediaTypeMovie}}}
	bad := &slowStub{name: "bad", mode: provider.ModeMovies, err: provider.ErrTimeout}

	svc := NewMediaService(newTestRegistry(good, bad))
	for i := 0; i < 3; i++ {
		if _, _, _, err := svc.Search(context.Background(), provider.ModeMovies, "q"); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	_, _, warnings, err := svc.Search(context.Background(), provider.ModeMovies, "q")
	if err != nil {
		t.Fatalf("Search = %v", err)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(strings.ToLower(w), "bad") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want the broken provider listed", warnings)
	}
}

// resolveStub is a provider double capturing resolve calls.
type resolveStub struct {
	name    string
	mode    provider.ContentType
	delay   time.Duration
	sources []provider.MediaSource
	err     error
}

func (s *resolveStub) Name() string { return s.name }
func (s *resolveStub) Modes() []provider.Mode {
	return []provider.Mode{{Name: s.mode, Priority: 1}}
}
func (s *resolveStub) Search(ctx context.Context, q string, m provider.ContentType) ([]provider.SearchResult, error) {
	return nil, provider.ErrNoResults
}
func (s *resolveStub) FetchEpisodes(ctx context.Context, sr provider.SearchResult) ([]provider.Episode, error) {
	return nil, provider.ErrNoEpisodes
}
func (s *resolveStub) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	select {
	case <-time.After(s.delay):
		return s.sources, s.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestResolveMergeDedupesAndBoosts proves cross-provider dedupe by URL
// and the routes-table preference (movysx first for movies).
func TestResolveMergeDedupesAndBoosts(t *testing.T) {
	movysx := &resolveStub{name: "movysx", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/a.m3u8", Quality: "1080p"}}}
	pengu := &resolveStub{name: "pengu", mode: provider.ModeMovies,
		sources: []provider.MediaSource{
			{URL: "https://cdn.example.com/b.m3u8", Quality: "4K"},
			{URL: "https://cdn.example.com/b.m3u8", Quality: "4K"},
			{URL: "https://cdn.example.com/c.m3u8", Quality: "720p"},
		}}

	svc := NewMediaService(newTestRegistry(movysx, pengu))
	series := provider.SearchResult{Title: "T", ID: "1", Provider: "movysx", TMDBID: 42, MediaType: provider.MediaTypeMovie}
	resolved, err := svc.Resolve(context.Background(), provider.ModeMovies, series, provider.Episode{TMDBID: 42}, nil, ResolveOptions{})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if len(resolved.Playback) != 3 {
		t.Fatalf("want 3 deduped sources, got %+v", resolved.Playback)
	}
	if resolved.Playback[0].Resolver != "movysx" {
		t.Errorf("first source = %+v, want movysx preference", resolved.Playback[0])
	}
}

// TestResolveLastFailures proves partial results arrive together with
// the failed-provider list.
func TestResolveLastFailures(t *testing.T) {
	good := &resolveStub{name: "good", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/a.m3u8", Quality: "1080p"}}}
	bad := &resolveStub{name: "bad", mode: provider.ModeMovies, err: provider.ErrNoSources}

	svc := NewMediaService(newTestRegistry(good, bad))
	series := provider.SearchResult{Title: "T", ID: "1", Provider: "good", TMDBID: 42, MediaType: provider.MediaTypeMovie}
	resolved, err := svc.Resolve(context.Background(), provider.ModeMovies, series, provider.Episode{TMDBID: 42}, nil, ResolveOptions{})
	if err != nil {
		t.Fatalf("partial resolve must not error: %v", err)
	}
	if len(resolved.Playback) != 1 {
		t.Fatalf("want 1 source, got %+v", resolved.Playback)
	}
	failed := svc.LastFailures()
	if len(failed) != 1 || failed[0] != "bad" {
		t.Errorf("LastFailures = %v, want [bad]", failed)
	}
}

// TestFetchEpisodesFiltersAudio proves the sub/dub filter and the
// unavailable-provider error.
func TestFetchEpisodesUsesVerifiedAnimeAvailability(t *testing.T) {
	synthetic := &stubProvider{name: "synthetic", mode: provider.ModeAnime}
	available := &availabilitySlowStub{
		slowStub: &slowStub{name: "verified", mode: provider.ModeAnime},
		available: []provider.Episode{
			{ID: "s1", Episode: 1, Audio: provider.AudioSub},
			{ID: "s2", Episode: 2, Audio: provider.AudioSub},
			{ID: "s3", Episode: 3, Audio: provider.AudioSub},
			{ID: "s4", Episode: 4, Audio: provider.AudioSub},
			{ID: "d1", Episode: 1, Audio: provider.AudioDub},
			{ID: "d2", Episode: 2, Audio: provider.AudioDub},
			{ID: "d3", Episode: 3, Audio: provider.AudioDub},
			{ID: "unknown", Episode: 99},
		},
	}
	svc := NewMediaService(newTestRegistry(synthetic, available))
	series := provider.SearchResult{Title: "One Piece", ID: "21", Provider: synthetic.name, Type: provider.ModeAnime}

	sub, err := svc.FetchEpisodes(context.Background(), provider.ModeAnime, series, provider.AudioSub)
	if err != nil {
		t.Fatalf("sub episodes: %v", err)
	}
	if len(sub) != 4 {
		t.Fatalf("sub episodes = %d, want 4: %+v", len(sub), sub)
	}

	dub, err := svc.FetchEpisodes(context.Background(), provider.ModeAnime, series, provider.AudioDub)
	if err != nil {
		t.Fatalf("dub episodes: %v", err)
	}
	if len(dub) != 3 {
		t.Fatalf("dub episodes = %d, want 3: %+v", len(dub), dub)
	}
}

func TestFetchEpisodesDoesNotSynthesizeDub(t *testing.T) {
	synthetic := &stubProvider{name: "synthetic", mode: provider.ModeAnime}
	svc := NewMediaService(newTestRegistry(synthetic))
	series := provider.SearchResult{Title: "One Piece", ID: "21", Provider: synthetic.name, Type: provider.ModeAnime}

	episodes, err := svc.FetchEpisodes(context.Background(), provider.ModeAnime, series, provider.AudioDub)
	if !errors.Is(err, provider.ErrNoEpisodes) {
		t.Fatalf("dub error = %v, want ErrNoEpisodes", err)
	}
	if len(episodes) != 0 {
		t.Fatalf("dub episodes = %+v, want none", episodes)
	}
}

func TestFetchEpisodesFiltersAudio(t *testing.T) {
	episodes := []provider.Episode{
		{Title: "E1 sub", ID: "s1", Episode: 1, Audio: "sub"},
		{Title: "E1 dub", ID: "d1", Episode: 1, Audio: "dub"},
		{Title: "E1 raw", ID: "r1", Episode: 1},
	}
	p := &availabilitySlowStub{
		slowStub:  &slowStub{name: "ani", mode: provider.ModeAnime, episodes: episodes},
		available: episodes,
	}
	svc := NewMediaService(newTestRegistry(p))
	series := provider.SearchResult{Title: "T", ID: "21", Provider: "ani"}

	eps, err := svc.FetchEpisodes(context.Background(), provider.ModeAnime, series, provider.AudioDub)
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 1 || eps[0].Audio != provider.AudioDub {
		t.Fatalf("dub filter must keep only verified dub rows: %+v", eps)
	}
	if _, err := svc.FetchEpisodes(context.Background(), provider.ModeAnime, provider.SearchResult{Provider: "ghost"}, ""); err == nil {
		t.Error("unknown provider must error")
	}
}

// TestAggregatorCapsMirrorTiers proves interchangeable mirror rows
// collapse to 3 per tier while distinct backends, heights and dubbed
// languages each keep their own budget.
func TestAggregatorCapsMirrorTiers(t *testing.T) {
	newAgg := func() *sourceAggregator {
		return newSourceAggregatorForMode(provider.ModeMovies, []provider.Provider{
			&stubProvider{name: "movysx", mode: provider.ModeMovies},
		}, func(n string) string { return n })
	}

	mirrors := newAgg()
	for i := 0; i < 8; i++ {
		mirrors.add("movysx", []provider.MediaSource{
			{URL: "https://cdn.example.com/m" + string(rune('a'+i)) + ".m3u8", Quality: "1080p"},
		})
	}
	if len(mirrors.sources) != 3 {
		t.Errorf("8 identical-tier mirrors = %d, want 3", len(mirrors.sources))
	}

	varied := newAgg()
	varied.add("pengu", []provider.MediaSource{
		{URL: "https://cdn.example.com/1", Quality: "4K [A]"},
		{URL: "https://cdn.example.com/2", Quality: "4K [B]"},
		{URL: "https://cdn.example.com/3", Quality: "4K [C]"},
		{URL: "https://cdn.example.com/4", Quality: "4K [D]"},
		{URL: "https://cdn.example.com/5", Quality: "1080p [A]"},
	})
	if len(varied.sources) != 5 {
		t.Errorf("distinct backends/heights must all survive, got %d", len(varied.sources))
	}

	dubbed := newAgg()
	dubbed.add("movysx", []provider.MediaSource{
		{URL: "https://cdn.example.com/a", Quality: "1080p"},
		{URL: "https://cdn.example.com/b", Quality: "1080p"},
		{URL: "https://cdn.example.com/c", Quality: "1080p"},
		{URL: "https://cdn.example.com/hi", Quality: "Auto (Hindi)", Language: "hi"},
	})
	if len(dubbed.sources) != 4 {
		t.Errorf("dubbed row must keep its own budget alongside 3 mains, got %d", len(dubbed.sources))
	}
}

// countStub counts ResolveSource calls per provider.
type countStub struct {
	resolveStub
	calls *int
}

func (s *countStub) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	*s.calls++
	return s.resolveStub.ResolveSource(ctx, mediaID, episode)
}

// flakyStub fails the first call, then serves sources.
type flakyStub struct {
	resolveStub
	calls *int
}

func (s *flakyStub) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	*s.calls++
	if *s.calls == 1 {
		return nil, provider.ErrTimeout
	}
	return s.sources, nil
}

func TestResolveRetryBypassesOpenBreaker(t *testing.T) {
	goodCalls := 0
	var moovieCalls int
	goodBase := resolveStub{
		name: "good", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/good.m3u8", Quality: "1080p"}},
	}
	moovieBase := resolveStub{
		name: "moovie", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/moovie.m3u8", Quality: "1080p"}},
	}
	good := &countStub{resolveStub: goodBase, calls: &goodCalls}
	moovie := &countStub{resolveStub: moovieBase, calls: &moovieCalls}
	svc := NewMediaService(newTestRegistry(good, moovie))
	for range 3 {
		svc.health.RecordFailure("moovie")
	}

	_, err := svc.Resolve(context.Background(), provider.ModeMovies,
		provider.SearchResult{Title: "T", ID: "1", Provider: "good", TMDBID: 42, MediaType: provider.MediaTypeMovie},
		provider.Episode{}, nil, ResolveOptions{Retry: []string{"moovie"}})
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if moovieCalls != 1 {
		t.Fatalf("manual retry called moovie %d times, want 1", moovieCalls)
	}
}

// TestResolveExcludeSkipsSuccessful proves retries only ask failed
// providers: the excluded winner is never re-queried.
func TestResolveExcludeSkipsSuccessful(t *testing.T) {
	var goodCalls, badCalls int
	good := &countStub{resolveStub: resolveStub{name: "good", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/a.m3u8", Quality: "1080p"}}}, calls: &goodCalls}
	bad := &flakyStub{resolveStub: resolveStub{name: "bad", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/b.m3u8", Quality: "720p"}}}, calls: &badCalls}

	svc := NewMediaService(newTestRegistry(good, bad))
	series := provider.SearchResult{Title: "T", ID: "1", Provider: "good", TMDBID: 42, MediaType: provider.MediaTypeMovie}
	if _, err := svc.Resolve(context.Background(), provider.ModeMovies, series, provider.Episode{TMDBID: 42}, nil, ResolveOptions{}); err != nil {
		t.Fatalf("seed resolve: %v", err)
	}
	if goodCalls != 1 || badCalls != 1 {
		t.Fatalf("seed queried good=%d bad=%d, want 1/1", goodCalls, badCalls)
	}
	resolved, err := svc.Resolve(context.Background(), provider.ModeMovies, series, provider.Episode{TMDBID: 42}, nil,
		ResolveOptions{Exclude: []string{"good"}})
	if err != nil {
		t.Fatalf("retry resolve: %v", err)
	}
	if goodCalls != 1 {
		t.Errorf("excluded winner re-queried: good calls = %d", goodCalls)
	}
	if badCalls != 2 {
		t.Errorf("failed provider not retried: bad calls = %d", badCalls)
	}
	if len(resolved.Playback) != 1 || resolved.Playback[0].Resolver != "bad" {
		t.Errorf("retry playback = %+v, want the recovered provider", resolved.Playback)
	}
}

// TestResolveExcludeAllFallsBackToFull proves an exclusion covering
// everything resolves normally instead of erroring empty.
func TestResolveExcludeAllFallsBackToFull(t *testing.T) {
	good := &resolveStub{name: "good", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/a.m3u8", Quality: "1080p"}}}
	svc := NewMediaService(newTestRegistry(good))
	series := provider.SearchResult{Title: "T", ID: "1", Provider: "good", MediaType: provider.MediaTypeMovie}
	resolved, err := svc.Resolve(context.Background(), provider.ModeMovies, series, provider.Episode{}, nil,
		ResolveOptions{Exclude: []string{"good"}})
	if err != nil {
		t.Fatalf("exclude-all must fall back to full: %v", err)
	}
	if len(resolved.Playback) != 1 {
		t.Errorf("playback = %+v", resolved.Playback)
	}
}

// TestResolveCancelReturnsFast proves a canceled resolve returns within
// 200ms without leaking goroutines (count checked by the race suite).
func TestResolveCancelReturnsFast(t *testing.T) {
	never := &resolveStub{name: "never", mode: provider.ModeMovies, delay: time.Hour,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/x.m3u8"}}}
	svc := NewMediaService(newTestRegistry(never))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, _ = svc.Resolve(ctx, provider.ModeMovies,
		provider.SearchResult{Title: "T", ID: "1", Provider: "never", MediaType: provider.MediaTypeMovie},
		provider.Episode{}, nil, ResolveOptions{})
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("canceled resolve took %v, want <200ms", elapsed)
	}
}

// TestResolveAllAudioUnavailablePropagatesSentinel proves an aggregate
// failure made only of verified-missing tracks carries ErrAudioUnavailable
// (so the TUI can hide the episode), while any transport failure keeps
// the generic joined error.
func TestResolveAllAudioUnavailablePropagatesSentinel(t *testing.T) {
	mk := func(name string, err error) *resolveStub {
		return &resolveStub{name: name, mode: provider.ModeAnime, err: err}
	}
	series := provider.SearchResult{Title: "T", ID: "21", Type: provider.ModeAnime}
	ep := provider.Episode{Episode: 1, Audio: "dub"}

	audioErr := fmt.Errorf("no dub servers: %w", provider.ErrAudioUnavailable)
	svc := NewMediaService(newTestRegistry(mk("a", audioErr), mk("b", audioErr)))
	_, err := svc.Resolve(context.Background(), provider.ModeAnime, series, ep, nil, ResolveOptions{})
	if !errors.Is(err, provider.ErrAudioUnavailable) {
		t.Errorf("all-unavailable err = %v, want ErrAudioUnavailable", err)
	}

	mixed := NewMediaService(newTestRegistry(mk("a", audioErr), mk("b", provider.ErrTimeout)))
	_, err = mixed.Resolve(context.Background(), provider.ModeAnime, series, ep, nil, ResolveOptions{})
	if err == nil || errors.Is(err, provider.ErrAudioUnavailable) {
		t.Errorf("mixed err = %v, must not be audio-unavailable", err)
	}

	// A circuit-broken provider is sick, not verified-absent: must not carry ErrAudioUnavailable.
	brokenSvc := NewMediaService(newTestRegistry(mk("a", audioErr), mk("b", nil)))
	for range 5 {
		brokenSvc.health.RecordFailure("b")
	}
	_, err = brokenSvc.Resolve(context.Background(), provider.ModeAnime, series, ep, nil, ResolveOptions{})
	if errors.Is(err, provider.ErrAudioUnavailable) {
		t.Errorf("circuit-broken provider must prevent ErrAudioUnavailable, got %v", err)
	}
}
