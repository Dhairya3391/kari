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
	sources []provider.MediaSource
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
	if len(s.sources) > 0 || s.err != nil {
		return s.sources, s.err
	}
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
		name: "miruro",
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
	agg := newSourceAggregatorForMode("", []provider.Provider{
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

func TestSourceAggregatorMergesResignedSourceVariants(t *testing.T) {
	const (
		playlist = "https://fetch.example/anime/abc/master.m3u8"
		subURL   = "https://subs.example/eng-2.vtt"
	)
	subs := []provider.SubtitleOption{{URL: subURL, Language: "en"}}
	cases := []struct {
		name             string
		firstProvider    string
		secondProvider   string
		first            provider.MediaSource
		second           provider.MediaSource
		wantResolver     string
		wantURL          string
		wantQualityLabel string
		wantQuality      int
		wantSubType      string
	}{
		{
			name:           "later soft metadata fills earlier row",
			firstProvider:  "anikoto",
			secondProvider: "anivault",
			first: provider.MediaSource{
				URL: playlist + "?token=anikoto", Quality: "1080p (Vidstream-2)",
				Referer: "https://megaplay.buzz/", Subtitles: subs,
			},
			second: provider.MediaSource{
				URL: playlist + "?token=anivault", Quality: "Auto (HD-1)",
				Referer: "https://megaplay.buzz/", SubType: provider.SubTypeSoft, Subtitles: subs,
			},
			wantResolver:     "anikoto",
			wantURL:          playlist + "?token=anikoto",
			wantQualityLabel: "1080p (Vidstream-2)",
			wantQuality:      1080,
			wantSubType:      provider.SubTypeSoft,
		},
		{
			name:           "anikoto replaces earlier duplicate provider",
			firstProvider:  "anivault",
			secondProvider: "anikoto",
			first: provider.MediaSource{
				URL: playlist + "?token=anivault", Quality: "Auto (HD-1)",
				Referer: "https://megaplay.buzz/", SubType: provider.SubTypeSoft, Subtitles: subs,
			},
			second: provider.MediaSource{
				URL: playlist + "?token=anikoto", Quality: "1080p (Vidstream-2)",
				Referer: "https://megaplay.buzz/", SubType: provider.SubTypeSoft, Subtitles: subs,
			},
			wantResolver:     "anikoto",
			wantURL:          playlist + "?token=anikoto",
			wantQualityLabel: "1080p (Vidstream-2)",
			wantQuality:      1080,
			wantSubType:      provider.SubTypeSoft,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg := newSourceAggregatorForMode(provider.ModeAnime, []provider.Provider{
				&stubProvider{name: "anikoto", mode: provider.ModeAnime},
				&stubProvider{name: "anivault", mode: provider.ModeAnime},
			}, func(n string) string { return n })
			agg.add(tc.firstProvider, []provider.MediaSource{tc.first})
			agg.add(tc.secondProvider, []provider.MediaSource{tc.second})

			if len(agg.sources) != 1 {
				t.Fatalf("signed variants = %d sources, want 1: %+v", len(agg.sources), agg.sources)
			}
			kept := agg.sources[0]
			if kept.Resolver != tc.wantResolver {
				t.Errorf("kept resolver = %q, want %q", kept.Resolver, tc.wantResolver)
			}
			if kept.URL != tc.wantURL {
				t.Errorf("kept URL = %q, want %q", kept.URL, tc.wantURL)
			}
			if kept.Quality != tc.wantQualityLabel {
				t.Errorf("merged quality label = %q, want %q", kept.Quality, tc.wantQualityLabel)
			}
			if SourceQuality(kept.Quality) != tc.wantQuality {
				t.Errorf("merged quality = %q (%d), want %d", kept.Quality, SourceQuality(kept.Quality), tc.wantQuality)
			}
			if kept.SubType != tc.wantSubType {
				t.Errorf("merged subtype = %q, want %q", kept.SubType, tc.wantSubType)
			}
			if len(kept.Subtitles) != 1 {
				t.Errorf("merged subtitles = %d, want 1", len(kept.Subtitles))
			}
			if len(agg.subs) != 1 {
				t.Fatalf("deduped subtitles = %d, want 1: %+v", len(agg.subs), agg.subs)
			}
			if agg.subs[0].SourceID != kept.TransportIdentity() {
				t.Errorf("subtitle SourceID does not match kept source: %q != %q", agg.subs[0].SourceID, kept.TransportIdentity())
			}
			if agg.subs[0].SourceURL != kept.URL {
				t.Errorf("subtitle SourceURL = %q, want %q", agg.subs[0].SourceURL, kept.URL)
			}
		})
	}
}

func TestSourceAggregatorMergesUserAgentOnlyVariants(t *testing.T) {
	agg := newSourceAggregatorForMode(provider.ModeAnime, nil, func(n string) string { return n })
	const playlist = "https://fetch.example/anime/abc/master.m3u8"
	agg.add("anivault", []provider.MediaSource{{
		URL:     playlist + "?token=anivault",
		Referer: "https://megaplay.buzz/",
	}})
	agg.add("anikoto", []provider.MediaSource{{
		URL:       playlist + "?token=anikoto",
		Referer:   "https://megaplay.buzz/",
		UserAgent: "browser",
	}})

	if len(agg.sources) != 1 {
		t.Fatalf("UA-only variants = %d sources, want 1: %+v", len(agg.sources), agg.sources)
	}
	if agg.sources[0].UserAgent != "browser" {
		t.Errorf("merged UserAgent = %q, want the explicit browser UA", agg.sources[0].UserAgent)
	}
}

func TestSourceAggregatorKeepsDistinctQuerySemantics(t *testing.T) {
	agg := newSourceAggregatorForMode(provider.ModeAnime, nil, func(n string) string { return n })
	agg.add("pengu", []provider.MediaSource{
		{URL: "https://cdn.example.com/master.m3u8?token=one&quality=1080", Quality: "1080p"},
		{URL: "https://cdn.example.com/master.m3u8?token=two&quality=720", Quality: "720p"},
	})
	if len(agg.sources) != 2 {
		t.Fatalf("non-auth query params must stay significant, got %d: %+v", len(agg.sources), agg.sources)
	}
}

func TestSourceAggregatorReindexesAfterSort(t *testing.T) {
	agg := newSourceAggregatorForMode(provider.ModeAnime, []provider.Provider{
		&stubProvider{name: "anikoto", mode: provider.ModeAnime},
		&stubProvider{name: "anivault", mode: provider.ModeAnime},
	}, func(n string) string { return n })
	const playable = "https://fetch.example/anime/abc/master.m3u8"
	agg.add("anivault", []provider.MediaSource{
		{URL: "https://vault-01.uwucdn.top/stream/01/uwu.m3u8", Quality: "1080p", Referer: "https://one.example/"},
		{URL: playable + "?token=one", Quality: "Auto (HD-1)", Referer: "https://two.example/"},
	})
	agg.sort()
	if agg.sources[0].URL != playable+"?token=one" {
		t.Fatalf("sort did not move playable source first: %+v", agg.sources)
	}

	agg.add("anikoto", []provider.MediaSource{{
		URL: playable + "?token=two", Quality: "1080p (Vidstream-2)", Referer: "https://two.example/",
	}})
	if len(agg.sources) != 2 {
		t.Fatalf("sources = %d, want 2 after merging duplicate", len(agg.sources))
	}
	if agg.sources[0].URL != playable+"?token=two" || agg.sources[0].Resolver != "anikoto" {
		t.Errorf("anikoto must replace duplicate at index 0 after sort: %+v", agg.sources)
	}
	if agg.sources[1].URL != "https://vault-01.uwucdn.top/stream/01/uwu.m3u8" {
		t.Errorf("challenged source was modified: %+v", agg.sources[1])
	}
}

func TestCanonicalSourceURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "strips volatile signing params",
			raw:  "https://CDN.example.com/a/master.m3u8?Token=abc&Expires=123&sig=xyz",
			want: "https://cdn.example.com/a/master.m3u8",
		},
		{
			name: "keeps and sorts non-auth params",
			raw:  "https://cdn.example.com/a/master.m3u8?b=2&token=abc&a=1",
			want: "https://cdn.example.com/a/master.m3u8?a=1&b=2",
		},
		{
			name: "trims trailing slash",
			raw:  "https://cdn.example.com/a/",
			want: "https://cdn.example.com/a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalSourceURL(tt.raw); got != tt.want {
				t.Errorf("canonicalSourceURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestSourceAggregatorPreservesTransportBoundSubtitles(t *testing.T) {
	agg := newSourceAggregatorForMode(provider.ModeMovies, nil, func(name string) string { return name })
	agg.add("pengu", []provider.MediaSource{
		{
			URL:       "https://cdn.example.com/video.m3u8",
			Referer:   "https://one.example/",
			UserAgent: "one",
			Subtitles: []provider.SubtitleOption{{
				URL:      "https://cdn.example.com/en.vtt",
				Language: "en",
				Default:  true,
			}},
		},
		{
			URL:       "https://cdn.example.com/video.m3u8",
			Referer:   "https://two.example/",
			UserAgent: "two",
			Subtitles: []provider.SubtitleOption{{
				URL:      "https://cdn.example.com/en.vtt",
				Language: "en",
			}},
		},
	})

	if len(agg.sources) != 2 {
		t.Fatalf("sources = %d, want 2 transport variants", len(agg.sources))
	}
	if len(agg.subs) != 2 {
		t.Fatalf("subtitles = %d, want 2 transport variants", len(agg.subs))
	}
	if agg.subs[0].SourceURL != "https://cdn.example.com/video.m3u8" {
		t.Fatalf("source URL not preserved: %+v", agg.subs[0])
	}
	if !agg.subs[0].Default || agg.subs[0].Referer != "https://one.example/" {
		t.Fatalf("subtitle metadata not preserved: %+v", agg.subs[0])
	}
}

func TestSourceAggregatorPreservesArrivalOrder(t *testing.T) {
	first := &stubProvider{name: "first", mode: provider.ModeTV}
	second := &stubProvider{name: "second", mode: provider.ModeTV}
	agg := newSourceAggregatorForMode("", []provider.Provider{first, second}, func(n string) string { return n })

	agg.add("second", []provider.MediaSource{{URL: "http://hi-2", Quality: "1080p"}})
	agg.add("first", []provider.MediaSource{
		{URL: "http://lo-1", Quality: "480p"},
		{URL: "http://hi-1", Quality: "1080p"},
	})
	agg.sort()

	wantOrder := []string{"http://hi-2", "http://lo-1", "http://hi-1"}
	for i, want := range wantOrder {
		if agg.sources[i].URL != want {
			t.Fatalf("position %d: got %s want %s (order=%+v)", i, agg.sources[i].URL, want, agg.sources)
		}
	}
}

func TestSourceAggregatorSortsHardSubsBeforeSoftSubsPerProvider(t *testing.T) {
	pengu := &stubProvider{name: "pengu", mode: provider.ModeAnime}
	agg := newSourceAggregatorForMode(provider.ModeAnime, []provider.Provider{pengu}, func(n string) string { return n })

	agg.add("pengu", []provider.MediaSource{
		{URL: "http://soft", Quality: "1080p", SubType: provider.SubTypeSoft},
		{URL: "http://hard", Quality: "1080p", SubType: provider.SubTypeHard},
	})
	agg.sort()

	if len(agg.sources) != 2 || agg.sources[0].URL != "http://hard" || agg.sources[1].URL != "http://soft" {
		t.Fatalf("hard sub must sort before soft sub per provider (order=%+v)", agg.sources)
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
		provider.Episode{}, nil, ResolveOptions{})
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
	other := &resolveCaptureWithAudio{name: "miruro", mode: provider.ModeAnime, captureID: &gotID, captureAudio: &gotAudio}

	svc := NewMediaService(newTestRegistry(origin, other))
	_, err := svc.Resolve(context.Background(), provider.ModeAnime,
		provider.SearchResult{Title: "One Piece", ID: "21", Provider: "anikoto", Type: provider.ModeAnime, MediaType: provider.MediaTypeAnime},
		provider.Episode{Episode: 1, Audio: "dub", ID: "watch/anikoto/21/dub/1"}, nil, ResolveOptions{})
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
	return []provider.MediaSource{{URL: "http://miruro-stream", Quality: "1080p"}}, nil
}

// availStub is an anime provider double with controllable availability
// latency and outcome. FetchEpisodes (origin path) and
// FetchAvailableEpisodes (probe path) resolve independently.
type availStub struct {
	name     string
	delay    time.Duration
	eps      []provider.Episode
	err      error
	fetch    []provider.Episode
	fetchErr error
}

func (a *availStub) Name() string { return a.name }
func (a *availStub) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeAnime, Priority: 1}}
}
func (a *availStub) Search(ctx context.Context, q string, m provider.ContentType) ([]provider.SearchResult, error) {
	return nil, provider.ErrNoResults
}
func (a *availStub) FetchEpisodes(ctx context.Context, s provider.SearchResult) ([]provider.Episode, error) {
	return a.fetch, a.fetchErr
}
func (a *availStub) FetchAvailableEpisodes(ctx context.Context, s provider.SearchResult) ([]provider.Episode, error) {
	select {
	case <-time.After(a.delay):
		return a.eps, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (a *availStub) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	return nil, provider.ErrNoSources
}

// titleStub is an availStub that also declares the capacity to name
// episodes, mirroring providers whose rosters ship verified titles
type titleStub struct {
	availStub
	titles map[int]string
	err    error
	calls  int
}

func (b *titleStub) FetchEpisodeTitles(ctx context.Context, s provider.SearchResult) (map[int]string, error) {
	b.calls++
	select {
	case <-time.After(2 * time.Millisecond):
		return b.titles, b.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Canonical titles allow the fastest provider to supply playback handles.
func TestFetchEpisodesUsesFastestAvailableProvider(t *testing.T) {
	slow := &availStub{name: "slow", delay: 20 * time.Millisecond,
		eps: []provider.Episode{{Episode: 1, Title: "Preferred title", ID: "preferred-episode", Audio: "sub"}}}
	fast := &availStub{name: "fast", delay: time.Millisecond,
		eps: []provider.Episode{{Episode: 1, Title: "Fallback title", ID: "fallback-episode", Audio: "sub"}}}

	svc := NewMediaService(newTestRegistry(slow, fast))
	start := time.Now()
	eps, err := svc.FetchEpisodes(context.Background(), provider.ModeAnime,
		provider.SearchResult{Title: "T", ID: "21", Provider: "slow", Type: provider.ModeAnime}, "sub")
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("preferred source took %v, want prompt response", elapsed)
	}
	if len(eps) != 1 || eps[0].Audio != "sub" || eps[0].ID != "fallback-episode" {
		t.Errorf("eps = %+v, want the fast source's filtered sub row", eps)
	}
}

func TestCanonicalAnimeEpisodeTitlesComeFromProviderCapability(t *testing.T) {
	names := &titleStub{availStub: availStub{name: "mirlike"}, titles: map[int]string{6: "Canonical episode title"}}
	svc := NewMediaService(newTestRegistry(names))
	series := provider.SearchResult{Title: "T", ID: "105310", Provider: "mirlike", Type: provider.ModeAnime}

	got := svc.applyCanonicalAnimeEpisodeTitles(context.Background(), provider.ModeAnime, series,
		[]provider.Episode{{Episode: 6, Title: "Streaming-provider title", Audio: provider.AudioDub}})
	if len(got) != 1 || got[0].Title != "Canonical episode title" || got[0].ID != "" {
		t.Fatalf("episodes = %+v", got)
	}
	// Cached per series: the second application must not re-query providers.
	svc.applyCanonicalAnimeEpisodeTitles(context.Background(), provider.ModeAnime, series,
		[]provider.Episode{{Episode: 6, Title: "Streaming-provider title"}})
	if names.calls != 1 {
		t.Errorf("provider called %d times, want 1 via cache", names.calls)
	}
}

func TestCanonicalAnimeEpisodeTitlesAvoidUnverifiedProviderNames(t *testing.T) {
	svc := NewMediaService(newTestRegistry())
	episodes := []provider.Episode{{Episode: 6, Title: "Possibly wrong title"}}
	got := svc.applyCanonicalAnimeEpisodeTitles(context.Background(), provider.ModeAnime, provider.SearchResult{
		MediaType: provider.MediaTypeAnime,
	}, episodes)
	if len(got) != 1 || got[0].Title != "Episode 06" {
		t.Fatalf("episodes = %+v, want a neutral title", got)
	}
}

// When every availability source fails, sub loads fall back to the
// origin episode list; dub loads report the miss.
func TestFetchEpisodesFallsBackWhenAvailabilityFails(t *testing.T) {
	origin := &availStub{name: "origin", err: errors.New("boom"),
		fetch: []provider.Episode{{Episode: 7, Title: "Seven"}}}
	other := &availStub{name: "other", err: errors.New("bust")}

	svc := NewMediaService(newTestRegistry(origin, other))
	eps, err := svc.FetchEpisodes(context.Background(), provider.ModeAnime,
		provider.SearchResult{Title: "T", ID: "21", Provider: "origin", Type: provider.ModeAnime}, "sub")
	if err != nil {
		t.Fatalf("sub fallback: %v", err)
	}
	if len(eps) != 1 || eps[0].Episode != 7 {
		t.Errorf("eps = %+v, want origin fallback row", eps)
	}

	dubSvc := NewMediaService(newTestRegistry(
		&availStub{name: "a", err: provider.ErrNoEpisodes},
		&availStub{name: "b", err: provider.ErrNoEpisodes}))
	_, err = dubSvc.FetchEpisodes(context.Background(), provider.ModeAnime,
		provider.SearchResult{Title: "T", ID: "21", Provider: "a", Type: provider.ModeAnime}, "dub")
	if !errors.Is(err, provider.ErrNoEpisodes) {
		t.Errorf("dub all-fail err = %v, want ErrNoEpisodes", err)
	}
}

// Route timeouts: default when unset, explicit when set, never beyond
// the overall budget.
func TestProviderSourceTimeout(t *testing.T) {
	if got := providerSourceTimeout(provider.Route{}, false); got != provider.DefaultSourceTimeout {
		t.Errorf("no route = %v, want default", got)
	}
	r := provider.Route{Provider: "pengu", SourceTimeout: 40 * time.Second}
	if got := providerSourceTimeout(r, true); got != 40*time.Second {
		t.Errorf("explicit 40s = %v, want 40s", got)
	}
	r.SourceTimeout = 99 * time.Second
	if got := providerSourceTimeout(r, true); got != provider.DefaultSourcesOverall {
		t.Errorf("oversize clamped = %v, want overall", got)
	}
}
func TestLiveAnimeResolution(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live test in short mode")
	}
	cfg := &config.Config{
		TMDBAPIKeys: []string{"test"},
	}
	keyPool := tmdb.NewKeyPool(cfg.TMDBAPIKeys)
	reg, err := defaults.NewDefaultRegistry(keyPool, cfg, nil)
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
	resolved, err := svc.Resolve(ctx, provider.ModeAnime, series, ep, nil, ResolveOptions{})
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
		}, ResolveOptions{})
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

func TestAggregatorPreservesArrivalOrderAcrossProviders(t *testing.T) {
	agg := &sourceAggregator{
		mode: provider.ModeMovies,
		sources: []provider.MediaSource{
			{Resolver: "pengu", Quality: "4K [4KHDHub]"},
			{Resolver: "movysx", Quality: "1080p"},
			{Resolver: "pengu", Quality: "1080p [VegaMovies]"},
			{Resolver: "movysx", Quality: "2160p"},
		},
	}

	agg.sort()

	if len(agg.sources) != 4 {
		t.Fatalf("expected 4 sources, got %d", len(agg.sources))
	}

	// Playable sources must stay in their exact arrival order
	if agg.sources[0].Resolver != "pengu" || agg.sources[0].Quality != "4K [4KHDHub]" {
		t.Errorf("sources[0] = %+v, want pengu 4K in arrival order", agg.sources[0])
	}
	if agg.sources[1].Resolver != "movysx" || agg.sources[1].Quality != "1080p" {
		t.Errorf("sources[1] = %+v, want movysx 1080p in arrival order", agg.sources[1])
	}
}

type countingProvider struct {
	stubProvider
	searchCount   int
	episodesCount int
	episodes      []provider.Episode
}

func (c *countingProvider) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	c.searchCount++
	return c.results, c.err
}

func (c *countingProvider) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	c.episodesCount++
	return c.episodes, nil
}

func TestMediaServiceSearchAndEpisodesCaching(t *testing.T) {
	p := &countingProvider{
		stubProvider: stubProvider{
			name: "counter",
			mode: provider.ModeMovies,
			results: []provider.SearchResult{
				{Title: "Film A", ID: "100", Provider: "counter", MediaType: provider.MediaTypeMovie},
			},
		},
		episodes: []provider.Episode{
			{Title: "Episode 1", ID: "ep1", Episode: 1},
		},
	}

	svc := NewMediaService(newTestRegistry(p))

	// 1. First search touches provider
	res1, _, _, err := svc.Search(context.Background(), provider.ModeMovies, "Film A")
	if err != nil || len(res1) != 1 {
		t.Fatalf("first search failed: %v", err)
	}
	if p.searchCount != 1 {
		t.Fatalf("expected searchCount=1, got %d", p.searchCount)
	}

	// 2. Second search for same query hits cache
	res2, _, _, err := svc.Search(context.Background(), provider.ModeMovies, "film a")
	if err != nil || len(res2) != 1 {
		t.Fatalf("second search failed: %v", err)
	}
	if p.searchCount != 1 {
		t.Fatalf("expected searchCount=1 on cache hit, got %d", p.searchCount)
	}

	// 3. First episode fetch touches provider
	series := res1[0]
	eps1, err := svc.FetchEpisodes(context.Background(), provider.ModeMovies, series, "")
	if err != nil || len(eps1) != 1 {
		t.Fatalf("first episodes fetch failed: %v", err)
	}
	if p.episodesCount != 1 {
		t.Fatalf("expected episodesCount=1, got %d", p.episodesCount)
	}

	// 4. Second episode fetch hits cache
	eps2, err := svc.FetchEpisodes(context.Background(), provider.ModeMovies, series, "")
	if err != nil || len(eps2) != 1 {
		t.Fatalf("second episodes fetch failed: %v", err)
	}
	if p.episodesCount != 1 {
		t.Fatalf("expected episodesCount=1 on cache hit, got %d", p.episodesCount)
	}
}

func TestMediaServiceSearchFallsBackToDisabledProviders(t *testing.T) {
	enabled := &stubProvider{name: "enabled_prov", mode: provider.ModeAnime, err: provider.ErrNoResults}
	disabled := &stubProvider{name: "disabled_prov", mode: provider.ModeAnime, results: []provider.SearchResult{
		{Title: "Fallback Anime", ID: "100", MediaType: provider.MediaTypeAnime},
	}}

	svc := NewMediaService(newTestRegistry(enabled, disabled))
	svc.SetDisabledProviders([]string{"disabled_prov"})

	if !svc.IsProviderDisabled(provider.ModeAnime, "disabled_prov") {
		t.Fatal("expected disabled_prov to be disabled")
	}
	if svc.IsProviderDisabled(provider.ModeAnime, "enabled_prov") {
		t.Fatal("expected enabled_prov to be enabled")
	}

	results, _, _, err := svc.Search(context.Background(), provider.ModeAnime, "query")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].Provider != "disabled_prov" {
		t.Fatalf("expected 1 result from disabled fallback, got %+v", results)
	}
}

func TestMediaServiceSearchUsesEnabledProvidersWhenAvailable(t *testing.T) {
	enabled := &stubProvider{name: "enabled_prov", mode: provider.ModeAnime, results: []provider.SearchResult{
		{Title: "Primary Anime", ID: "200", MediaType: provider.MediaTypeAnime},
	}}
	disabled := &stubProvider{name: "disabled_prov", mode: provider.ModeAnime, results: []provider.SearchResult{
		{Title: "Fallback Anime", ID: "100", MediaType: provider.MediaTypeAnime},
	}}

	svc := NewMediaService(newTestRegistry(enabled, disabled))
	svc.SetDisabledProviders([]string{"disabled_prov"})

	results, _, _, err := svc.Search(context.Background(), provider.ModeAnime, "query")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].Provider != "enabled_prov" {
		t.Fatalf("expected 1 result from enabled provider only, got %+v", results)
	}
}

func TestMediaServiceSearchMangaQueriesEveryEnabledProvider(t *testing.T) {
	weebcentral := &stubProvider{name: "weebcentral", mode: provider.ModeManga, results: []provider.SearchResult{
		{Title: "One Piece", ID: "1", MediaType: provider.MediaTypeManga},
	}}
	atsu := &stubProvider{name: "atsu", mode: provider.ModeManga, results: []provider.SearchResult{
		{Title: "One Piece", ID: "2", MediaType: provider.MediaTypeManga},
	}}

	svc := NewMediaService(newTestRegistry(weebcentral, atsu))
	results, _, _, err := svc.Search(context.Background(), provider.ModeManga, "One Piece")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 manga results, got %d: %+v", len(results), results)
	}
	if results[0].Provider != "weebcentral" || results[1].Provider != "atsu" {
		t.Fatalf("expected both providers queried, got %+v", results)
	}
}

func TestMediaServiceResolveFallsBackToDisabledProviders(t *testing.T) {
	enabled := &stubProvider{name: "enabled_prov", mode: provider.ModeAnime, err: provider.ErrNoSources}
	disabled := &stubProvider{
		name: "disabled_prov", mode: provider.ModeAnime,
		sources: []provider.MediaSource{
			{URL: "https://cdn.example.com/fallback.m3u8", Quality: "1080p"},
		},
	}

	svc := NewMediaService(newTestRegistry(enabled, disabled))
	svc.SetDisabledProviders([]string{"disabled_prov"})

	series := provider.SearchResult{Title: "Anime", ID: "100", Provider: "enabled_prov"}
	episode := provider.Episode{Episode: 1, Title: "Ep 1"}

	resolved, err := svc.Resolve(context.Background(), provider.ModeAnime, series, episode, nil, ResolveOptions{})
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(resolved.Playback) != 1 || resolved.Playback[0].Resolver != "disabled_prov" {
		t.Fatalf("expected playback from disabled fallback, got %+v", resolved.Playback)
	}
}

func TestMediaServiceModeScopedDisabledProvider(t *testing.T) {
	prov := &stubProvider{name: "shared_prov", mode: provider.ModeAnime}
	svc := NewMediaService(newTestRegistry(prov))

	svc.SetDisabledProviders([]string{"anime:shared_prov"})

	if !svc.IsProviderDisabled(provider.ModeAnime, "shared_prov") {
		t.Fatal("expected shared_prov to be disabled for anime")
	}
	if svc.IsProviderDisabled(provider.ModeMovies, "shared_prov") {
		t.Fatal("expected shared_prov to remain enabled for movies")
	}
}
