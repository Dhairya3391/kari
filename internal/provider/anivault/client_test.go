package anivault

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"kari/internal/provider"
)

const searchFixture = `{"results":[
	{"id":20,"title":"Naruto","coverImage":"https://cover.example.com/naruto.jpg","format":"TV"},
	{"id":null,"title":"MAL only result","coverImage":"","format":"TV"},
	{"id":936,"title":"Naruto the Movie","coverImage":"https://cover.example.com/movie.jpg","format":"MOVIE"}
]}`

const episodesFixture = `{"episodes":[
	{"num":1,"title":"Enter"},
	{"num":2,"title":""}
]}`

const animeHeavenEpisodesFixture = `{"episodes":[{"num":1,"title":"Heaven Ep"}]}`

const hlsWatchFixture = `{
	"server":"HD-1",
	"source":"anivault",
	"embedUrl":"https://megaplay.buzz/",
	"m3u8":"https://cdn.example.com/master.m3u8?token=abc",
	"hlsProxyUrl":"https://api.example.com/api/proxy/hls?url=https%3A%2F%2Fcdn.example.com%2Fmaster.m3u8%3Ftoken%3Dabc&ref=https%3A%2F%2Fmegaplay.buzz%2F",
	"iframeOnly":false,
	"subtitles":[
		{"url":"https://api.example.com/api/proxy/subtitle?url=https%3A%2F%2Fsubs.example.com%2Fen.vtt&ref=https%3A%2F%2Fmegaplay.buzz%2F","lang":"English","default":true},
		{"url":"https://api.example.com/api/proxy/subtitle?url=https%3A%2F%2Fsubs.example.com%2Fthumbnails.vtt&ref=https%3A%2F%2Fmegaplay.buzz%2F","lang":"Unknown","default":false}
	]
}`

const proxyOnlyWatchFixture = `{
	"server":"HD-1",
	"source":"anivault",
	"embedUrl":"https://megaplay.buzz/",
	"m3u8":null,
	"hlsProxyUrl":"https://api.example.com/api/proxy/hls?url=https%3A%2F%2Fcdn.example.com%2Fmaster.m3u8%3Ftoken%3Dabc&ref=https%3A%2F%2Fmegaplay.buzz%2F",
	"iframeOnly":false,
	"subtitles":[]
}`

const mp4WatchFixture = `{
	"server":"AnimeHeaven",
	"source":"animeheaven",
	"embedUrl":"https://animeheaven.me/gate.php",
	"mp4":"https://cdn.example.com/video.mp4?token=abc",
	"mp4ProxyUrl":"https://api.example.com/api/proxy/video?url=https%3A%2F%2Fcdn.example.com%2Fvideo.mp4%3Ftoken%3Dabc&ref=https%3A%2F%2Fanimeheaven.me%2F",
	"rawStreamUrl":"https://cdn.example.com/video.mp4?token=abc",
	"streamUrl":"https://api.example.com/api/proxy/video?url=https%3A%2F%2Fcdn.example.com%2Fvideo.mp4%3Ftoken%3Dabc&ref=https%3A%2F%2Fanimeheaven.me%2F",
	"iframeOnly":false,
	"subtitles":[]
}`

const iframeOnlyWatchFixture = `{"iframeOnly":true,"subtitles":[]}`

// fixture controls the canned API's failure modes.
type fixture struct {
	anivaultEpisodesStatus int
	anivaultWatchSubStatus int
	animeHeavenWatchStatus int
	iframeOnly             bool
	proxyOnly              bool
}

// newFixture serves canned AniVault API payloads on one httptest server.
func newFixture(t *testing.T, options ...func(*fixture)) *Client {
	t.Helper()
	f := fixture{
		anivaultEpisodesStatus: http.StatusOK,
		anivaultWatchSubStatus: http.StatusOK,
		animeHeavenWatchStatus: http.StatusOK,
	}
	for _, option := range options {
		option(&f)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			http.Error(w, `{"error":"Missing ?q="}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, searchFixture)
	})
	mux.HandleFunc("/api/episodes", func(w http.ResponseWriter, r *http.Request) {
		source := r.URL.Query().Get("source")
		if source == sourceAniVault {
			if f.anivaultEpisodesStatus != http.StatusOK {
				http.Error(w, `{"error":"Not indexed on Anikoto"}`, f.anivaultEpisodesStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, episodesFixture)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, animeHeavenEpisodesFixture)
	})
	mux.HandleFunc("/api/watch/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/watch/"), "/")
		if len(parts) != 4 {
			http.Error(w, `{"error":"bad path"}`, http.StatusBadRequest)
			return
		}
		source, category := parts[0], parts[3]
		switch {
		case source == sourceAniVault && category == provider.AudioDub:
			http.Error(w, `{"error":"No dub stream available"}`, http.StatusNotFound)
		case source == sourceAniVault && f.anivaultWatchSubStatus != http.StatusOK:
			http.Error(w, `{"error":"upstream failed"}`, f.anivaultWatchSubStatus)
		case source == sourceAniVault && f.iframeOnly:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, iframeOnlyWatchFixture)
		case source == sourceAniVault && f.proxyOnly:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, proxyOnlyWatchFixture)
		case source == sourceAniVault:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, hlsWatchFixture)
		case f.animeHeavenWatchStatus != http.StatusOK:
			http.Error(w, `{"error":"upstream failed"}`, f.animeHeavenWatchStatus)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, mp4WatchFixture)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := NewClientWithBase(srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithBase: %v", err)
	}
	return c
}

func TestAniVaultCapabilities(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Name() != "anivault" || c.Alias() != "AniVault" {
		t.Errorf("identity = %q %q", c.Name(), c.Alias())
	}
	if len(c.Modes()) != 1 || c.Modes()[0].Name != provider.ModeAnime {
		t.Errorf("modes = %+v, want anime only", c.Modes())
	}
	if !c.RequiresEpisodeListForMovies() {
		t.Error("movies must go through the episode flow")
	}
	if !c.Features(provider.ModeAnime).AudioSelection {
		t.Error("anime must declare audio selection")
	}
	if c.Features(provider.ModeMovies).AudioSelection {
		t.Error("non-anime modes must not declare audio selection")
	}
}

func TestAniVaultSearchKeepsAniListIDs(t *testing.T) {
	c := newFixture(t)
	results, err := c.Search(context.Background(), "naruto", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (null AniList id skipped)", len(results))
	}
	if results[0].ID != "20" || results[0].Title != "Naruto" {
		t.Errorf("first result = %+v", results[0])
	}
	if results[0].CoverURL != "https://cover.example.com/naruto.jpg" {
		t.Errorf("CoverURL = %q", results[0].CoverURL)
	}
	if results[0].MediaType != provider.MediaTypeAnime {
		t.Errorf("MediaType = %q, want anime", results[0].MediaType)
	}
	if results[1].MediaType != provider.MediaTypeMovie {
		t.Errorf("movie MediaType = %q, want movie", results[1].MediaType)
	}
}

func TestAniVaultSearchEmptyQuery(t *testing.T) {
	c := newFixture(t)
	if _, err := c.Search(context.Background(), "  ", provider.ModeAnime); err == nil {
		t.Error("empty query must fail")
	}
}

func TestAniVaultFetchEpisodes(t *testing.T) {
	c := newFixture(t)
	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "20", Title: "Naruto"})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 4 {
		t.Fatalf("episodes = %d, want 4 (2 episodes × sub/dub)", len(eps))
	}
	if eps[0].Audio != provider.AudioSub || eps[1].Audio != provider.AudioDub {
		t.Errorf("audios = %q %q, want sub dub", eps[0].Audio, eps[1].Audio)
	}
	if eps[0].Title != "Enter" {
		t.Errorf("title = %q, want Enter", eps[0].Title)
	}
	if eps[2].Title != "Episode 2" {
		t.Errorf("untitled episode = %q, want fallback title", eps[2].Title)
	}
	if eps[0].ID != "watch/anivault/20/sub/1" || eps[1].ID != "watch/anivault/20/dub/1" {
		t.Errorf("handles = %q %q", eps[0].ID, eps[1].ID)
	}
	if eps[0].Season != 1 || eps[0].Episode != 1 {
		t.Errorf("episode = S%dE%d, want S1E1", eps[0].Season, eps[0].Episode)
	}
}

func TestAniVaultFetchEpisodesFallsBackToAnimeHeaven(t *testing.T) {
	c := newFixture(t, func(f *fixture) {
		f.anivaultEpisodesStatus = http.StatusNotFound
	})
	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "20", Title: "Naruto"})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 1 {
		t.Fatalf("episodes = %d, want 1 sub-only fallback", len(eps))
	}
	if eps[0].Audio != provider.AudioSub {
		t.Errorf("audio = %q, want sub", eps[0].Audio)
	}
	if eps[0].ID != "watch/animeheaven/20/sub/1" {
		t.Errorf("handle = %q, want animeheaven source", eps[0].ID)
	}
}

func TestAniVaultResolveSub(t *testing.T) {
	c := newFixture(t)
	sources, err := c.ResolveSource(context.Background(), "20", provider.Episode{
		ID:      "watch/anivault/20/sub/1",
		Episode: 1,
		Audio:   provider.AudioSub,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want soft and hard variants", len(sources))
	}
	var soft, hard *provider.MediaSource
	for i := range sources {
		switch sources[i].SubType {
		case provider.SubTypeSoft:
			soft = &sources[i]
		case provider.SubTypeHard:
			hard = &sources[i]
		}
	}
	if soft == nil || hard == nil {
		t.Fatalf("subtypes = %q %q, want soft and hard", sources[0].SubType, sources[1].SubType)
	}
	if soft.URL != "https://cdn.example.com/master.m3u8?token=abc" {
		t.Errorf("soft URL = %q", soft.URL)
	}
	if soft.Quality != "Auto (HD-1)" {
		t.Errorf("soft Quality = %q, want server-tagged auto", soft.Quality)
	}
	if soft.Referer != "https://megaplay.buzz/" {
		t.Errorf("soft Referer = %q", soft.Referer)
	}
	if soft.Type != provider.SourceTypeHLS {
		t.Errorf("soft Type = %q, want hls", soft.Type)
	}
	if len(soft.ExtraArgs) != 0 {
		t.Errorf("extensioned playlists need no demuxer quirk, got %v", soft.ExtraArgs)
	}
	if len(soft.Subtitles) != 1 {
		t.Fatalf("soft subtitles = %d, want 1 (thumbnail track dropped)", len(soft.Subtitles))
	}
	sub := soft.Subtitles[0]
	if sub.URL != "https://subs.example.com/en.vtt" || sub.Language != "en" || !sub.Default {
		t.Errorf("subtitle = %+v", sub)
	}
	if sub.Referer != "https://megaplay.buzz/" {
		t.Errorf("subtitle referer = %q", sub.Referer)
	}
	if hard.URL != "https://cdn.example.com/video.mp4?token=abc" || hard.Type != provider.SourceTypeMP4 {
		t.Errorf("hard source = %+v, want direct mp4", hard)
	}
	if hard.Referer != "https://animeheaven.me/" {
		t.Errorf("hard Referer = %q", hard.Referer)
	}
	if len(hard.Subtitles) != 0 {
		t.Errorf("hard subtitles = %d, want burned-in only", len(hard.Subtitles))
	}
}

func TestAniVaultResolveDubUnavailable(t *testing.T) {
	c := newFixture(t)
	_, err := c.ResolveSource(context.Background(), "20", provider.Episode{
		ID:      "watch/anivault/20/dub/1",
		Episode: 1,
		Audio:   provider.AudioDub,
	})
	if !errors.Is(err, provider.ErrAudioUnavailable) {
		t.Fatalf("err = %v, want ErrAudioUnavailable", err)
	}
}

func TestAniVaultResolveFallsBackToAnimeHeaven(t *testing.T) {
	c := newFixture(t, func(f *fixture) {
		f.anivaultWatchSubStatus = http.StatusBadGateway
	})
	sources, err := c.ResolveSource(context.Background(), "20", provider.Episode{
		ID:      "watch/anivault/20/sub/1",
		Episode: 1,
		Audio:   provider.AudioSub,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(sources))
	}
	s := sources[0]
	if s.Type != provider.SourceTypeMP4 || s.URL != "https://cdn.example.com/video.mp4?token=abc" {
		t.Errorf("source = %+v, want direct mp4", s)
	}
	if s.Referer != "https://animeheaven.me/" {
		t.Errorf("Referer = %q", s.Referer)
	}
	if s.SubType != provider.SubTypeHard {
		t.Errorf("SubType = %q, want hard", s.SubType)
	}
}

func TestAniVaultResolveAnimeHeavenEpisode(t *testing.T) {
	c := newFixture(t)
	sources, err := c.ResolveSource(context.Background(), "20", provider.Episode{
		ID:      "watch/animeheaven/20/sub/1",
		Episode: 1,
		Audio:   provider.AudioSub,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want the hard-sub fallback only", len(sources))
	}
	if sources[0].SubType != provider.SubTypeHard {
		t.Errorf("SubType = %q, want hard", sources[0].SubType)
	}
}

func TestAniVaultResolveCrossProviderEpisode(t *testing.T) {
	c := newFixture(t)
	sources, err := c.ResolveSource(context.Background(), "20", provider.Episode{
		Episode: 1,
		Audio:   provider.AudioSub,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want soft and hard variants", len(sources))
	}
}

func TestAniVaultResolveProxyOnly(t *testing.T) {
	c := newFixture(t, func(f *fixture) {
		f.proxyOnly = true
	})
	sources, err := c.ResolveSource(context.Background(), "20", provider.Episode{
		ID:      "watch/anivault/20/sub/1",
		Episode: 1,
		Audio:   provider.AudioSub,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want soft and hard variants", len(sources))
	}
	var proxied *provider.MediaSource
	for i := range sources {
		if sources[i].URL == "https://cdn.example.com/master.m3u8?token=abc" {
			proxied = &sources[i]
		}
	}
	if proxied == nil {
		t.Fatalf("sources = %+v, want unwrapped direct URL", sources)
	}
	if proxied.Referer != "https://megaplay.buzz/" {
		t.Errorf("Referer = %q", proxied.Referer)
	}
	if proxied.SubType != "" {
		t.Errorf("SubType = %q, want unknown without sidecar tracks", proxied.SubType)
	}
}

func TestAniVaultResolveIframeOnly(t *testing.T) {
	c := newFixture(t, func(f *fixture) {
		f.iframeOnly = true
		f.animeHeavenWatchStatus = http.StatusNotFound
	})
	_, err := c.ResolveSource(context.Background(), "20", provider.Episode{
		ID:      "watch/anivault/20/sub/1",
		Episode: 1,
		Audio:   provider.AudioSub,
	})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("err = %v, want ErrNoSources", err)
	}
}

func TestUnwrapProxy(t *testing.T) {
	direct, ref := unwrapProxy("https://cdn.example.com/master.m3u8?token=abc")
	if direct != "https://cdn.example.com/master.m3u8?token=abc" || ref != "" {
		t.Errorf("direct url = %q ref = %q", direct, ref)
	}
	proxy := "https://api.example.com/api/proxy/hls?url=" + url.QueryEscape("https://cdn.example.com/m.m3u8") + "&ref=" + url.QueryEscape("https://megaplay.buzz/")
	direct, ref = unwrapProxy(proxy)
	if direct != "https://cdn.example.com/m.m3u8" || ref != "https://megaplay.buzz/" {
		t.Errorf("proxy url = %q ref = %q", direct, ref)
	}
	if direct, ref := unwrapProxy(""); direct != "" || ref != "" {
		t.Errorf("empty = %q %q", direct, ref)
	}
}

func TestHlsExtraArgs(t *testing.T) {
	if got := hlsExtraArgs("https://cdn.example.com/master.m3u8?token=abc"); len(got) != 0 {
		t.Errorf("extensioned playlist = %v, want none", got)
	}
	if got := hlsExtraArgs("https://cdn.example.com/hls/abc123"); len(got) != 2 {
		t.Errorf("extensionless playlist = %v, want lavf+hls forcing", got)
	}
}
