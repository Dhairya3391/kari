package vidrift

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kari/internal/config"
	"kari/internal/provider"
	"kari/internal/tmdb"
)

// fixture is an in-process VidRift origin: embed pages, lane responses, and
// an optional AniList GraphQL payload, with request counters so caching and
// lane selection are observable.
type fixture struct {
	baseURL string

	mu        sync.Mutex
	embed     map[string]string
	lane      map[string]string
	delay     map[string]time.Duration
	anilist   string
	embedHits map[string]int
	laneHits  map[string]int
	requests  []string
}

// newFixture starts the fixture server and constructs a client pointed at
// it. Source URLs in payloads stay on external hosts because
// provider.FilterDirectSources rejects localhost.
func newFixture(t *testing.T, langFilter map[string]bool) (*Client, *fixture) {
	t.Helper()
	fx := &fixture{
		embed:     make(map[string]string),
		lane:      make(map[string]string),
		delay:     make(map[string]time.Duration),
		embedHits: make(map[string]int),
		laneHits:  make(map[string]int),
	}
	srv := httptest.NewServer(http.HandlerFunc(fx.serveHTTP))
	t.Cleanup(srv.Close)
	fx.baseURL = srv.URL

	client, err := NewClientWithURLsAndLanguageFilter(tmdb.NewKeyPool([]string{"k"}), srv.URL, srv.URL, langFilter)
	if err != nil {
		t.Fatalf("NewClientWithURLsAndLanguageFilter: %v", err)
	}
	return client, fx
}

func (f *fixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()

	switch {
	case strings.HasPrefix(r.URL.Path, "/embed/"):
		route := strings.TrimPrefix(r.URL.Path, "/embed/")
		f.mu.Lock()
		f.embedHits[route]++
		body, ok := f.embed[route]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)

	case strings.HasPrefix(r.URL.Path, "/api/source/"):
		route := strings.TrimPrefix(r.URL.Path, "/api/source/")
		key := route + "|" + r.URL.Query().Get("provider")
		f.mu.Lock()
		f.laneHits[key]++
		body, ok := f.lane[key]
		delay := f.delay[key]
		f.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if !ok {
			http.Error(w, `{"success":false}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)

	case r.Method == http.MethodPost:
		f.mu.Lock()
		body := f.anilist
		f.mu.Unlock()
		if body == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)

	default:
		http.NotFound(w, r)
	}
}

// embedHTML renders the two server-side payloads the provider parses.
func embedHTML(meta, subtitles string) string {
	return "<!doctype html><html><head><script>\n" +
		"var embedMeta = " + meta + ";\n" +
		"var subtitleTracks = " + subtitles + ";\n" +
		"</script></head><body></body></html>"
}

// laneJSON builds one successful /api/source payload.
func laneJSON(providerName, source string, fellBack bool, streams ...string) string {
	return fmt.Sprintf(
		`{"success":true,"provider":%q,"source":%q,"fellBack":%t,"maxRes":1080,"streams":[%s],"subtitles":[]}`,
		providerName, source, fellBack, strings.Join(streams, ","),
	)
}

// namedStream is a lane stream with a language-bearing label and one rung.
func namedStream(name, rawURL, streamType string, height int) string {
	return fmt.Sprintf(
		`{"url":%q,"type":%q,"provider":"Orion","name":%q,"rungs":[{"width":1920,"height":%d}]}`,
		rawURL, streamType, name, height,
	)
}

// proxyStream is a lane stream delivered only through VidRift's proxy.
func proxyStream(providerName, proxyURL string) string {
	return fmt.Sprintf(
		`{"url":"","proxyUrl":%q,"type":"hls","provider":%q,"direct":false}`,
		proxyURL, providerName,
	)
}

// movieFixture serves a TMDB movie with every general lane plus baked
// BigBang and Vexon addresses.
func movieFixture(t *testing.T, langFilter map[string]bool) (*Client, *fixture) {
	t.Helper()
	client, fx := newFixture(t, langFilter)
	fx.embed["movie/27205"] = embedHTML(
		`{"tmdbId":"27205","type":"movie","season":null,"episode":null,"playbackToken":"tok-movie","anime":false,`+
			`"bigbangUrl":"https://cdn.example.com/bigbang/index.m3u8.png?exp=1&sig=x",`+
			`"bigbangSubtitles":[{"code":"bb-en","lang":"en","label":"English · BigBang","url":"https://cdn.example.com/bigbang/en.vtt"}],`+
			`"evionUrl":"","vexonUrl":"https://cdn.example.com/vexon/index.m3u8","selfhostUrl":null}`,
		`[{"code":"en","label":"English","lang":"en","url":"/api/subtitles/movie/27205/en"},`+
			`{"code":"hi","label":"Hindi","lang":"hi","url":"/api/subtitles/movie/27205/hi"}]`,
	)
	fx.lane["movie/27205|orion"] = laneJSON("orion", "orion", false,
		namedStream("Orion · Original", "https://cdn.example.com/api/orion/orig/master.m3u8", "hls", 1080),
		namedStream("Orion · Hindi", "https://mb.vidrift.net/api/orion/hi/video.mp4", "mp4", 480),
	)
	fx.lane["movie/27205|vela"] = laneJSON("vela", "vela", false,
		proxyStream("Vela", "https://edge.reelvault.click/proxy?u=vela"))
	fx.lane["movie/27205|atlas"] = laneJSON("atlas", "atlas", false)
	fx.lane["movie/27205|titan"] = laneJSON("titan", "titan", false,
		namedStream("Tamil", "https://cdn.example.com/titan.m3u8", "hls", 720))
	fx.lane["movie/27205|lyra"] = laneJSON("lyra", "lyra", false,
		namedStream("English", "https://cdn.example.com/lyra.m3u8", "hls", 1080))
	return client, fx
}

// animeFixture serves an anime episode with both VidRift anime lanes.
func animeFixture(t *testing.T, subFallback, dubFallback bool, langFilter map[string]bool) (*Client, *fixture) {
	t.Helper()
	client, fx := newFixture(t, langFilter)
	fx.embed["anilist/1429/1"] = embedHTML(
		`{"tmdbId":"1429","type":"tv","season":1,"episode":1,"playbackToken":"tok-anime",`+
			`"anime":["anime","animedub"],"bigbangUrl":"https://cdn.example.com/bigbang/index.m3u8",`+
			`"bigbangSubtitles":[],"evionUrl":"","vexonUrl":"","selfhostUrl":null}`,
		`[{"code":"en","label":"English","lang":"en","url":"/api/subtitles/tv/1429/1/1/en"}]`,
	)
	fx.lane["tv/1429/1/1|anime"] = laneJSON("Anime", "anime", subFallback,
		proxyStream("Anime", "https://edge.reelvault.click/proxy?u=sub"))
	if dubFallback {
		fx.lane["tv/1429/1/1|animedub"] = laneJSON("Anime", "anime", true,
			proxyStream("Anime", "https://edge.reelvault.click/proxy?u=dub"))
	} else {
		fx.lane["tv/1429/1/1|animedub"] = laneJSON("Anime Dub", "anime dub", false,
			proxyStream("Anime Dub", "https://edge.reelvault.click/proxy?u=dub"))
	}
	return client, fx
}

func TestCapabilities(t *testing.T) {
	client, err := NewClient(tmdb.NewKeyPool([]string{"k"}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.Name() != "vidrift" || client.Alias() != "VidRift" {
		t.Errorf("identity = %q/%q", client.Name(), client.Alias())
	}
	modes := make(map[provider.ContentType]bool)
	for _, mode := range client.Modes() {
		modes[mode.Name] = true
	}
	for _, want := range []provider.ContentType{provider.ModeMovies, provider.ModeTV, provider.ModeAnime} {
		if !modes[want] {
			t.Errorf("mode %q not declared", want)
		}
	}
	if !client.Features(provider.ModeAnime).AudioSelection {
		t.Error("anime must expose audio selection")
	}
	if client.Features(provider.ModeMovies).AudioSelection {
		t.Error("movies must not expose audio selection")
	}
	if got := client.AudioLanguages(); len(got) == 0 {
		t.Error("AudioLanguages is empty")
	}
	for _, language := range client.AudioLanguages() {
		if language.Code == "" || language.Display == "" {
			t.Errorf("invalid audio language %+v", language)
		}
	}
}

func TestRouteFor(t *testing.T) {
	tests := []struct {
		name    string
		mediaID string
		episode provider.Episode
		want    string
		wantErr bool
	}{
		{
			name:    "movie",
			mediaID: "27205",
			episode: provider.Episode{TMDBID: 27205},
			want:    "movie/27205",
		},
		{
			name:    "tv",
			mediaID: "1399",
			episode: provider.Episode{Season: 1, Episode: 2, TMDBID: 1399},
			want:    "tv/1399/1/2",
		},
		{
			name:    "anime sub",
			mediaID: "1429",
			episode: provider.Episode{Season: 1, Episode: 3, Audio: provider.AudioSub, TMDBID: 1429},
			want:    "anilist/1429/3",
		},
		{
			name:    "anime movie without episode",
			mediaID: "21519",
			episode: provider.Episode{Audio: provider.AudioSub},
			want:    "anilist/21519",
		},
		{
			name:    "anime from foreign episode handle",
			mediaID: "1429",
			episode: provider.Episode{Season: 1, Episode: 5, ID: "watch/anikoto/1429/sub/5", TMDBID: 1429},
			want:    "anilist/1429/5",
		},
		{
			name:    "anime id mismatch",
			mediaID: "1429",
			episode: provider.Episode{Season: 1, Episode: 1, TMDBID: 999},
			want:    "anilist/1429/1",
		},
		{name: "empty", mediaID: " ", episode: provider.Episode{}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := routeFor(tt.mediaID, tt.episode)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("routeFor(%q) = %q, want error", tt.mediaID, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("routeFor(%q): %v", tt.mediaID, err)
			}
			if got != tt.want {
				t.Errorf("routeFor(%q) = %q, want %q", tt.mediaID, got, tt.want)
			}
		})
	}
}

func TestResolveMovieBuildsSources(t *testing.T) {
	client, fx := movieFixture(t, nil)

	sources, err := client.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	// Two Orion, one Vela, one Titan, one Lyra, plus BigBang and Vexon.
	if len(sources) != 7 {
		t.Fatalf("sources = %d, want 7: %+v", len(sources), sources)
	}

	byURL := make(map[string]provider.MediaSource, len(sources))
	for _, source := range sources {
		byURL[source.URL] = source
		if source.Referer != fx.baseURL+"/" {
			t.Errorf("%s referer = %q", source.URL, source.Referer)
		}
		if source.UserAgent != config.DesktopUserAgent {
			t.Errorf("%s user agent = %q", source.URL, source.UserAgent)
		}
		if !source.SuppressOrigin {
			t.Errorf("%s must suppress Origin", source.URL)
		}
	}

	original, ok := byURL["https://cdn.example.com/api/orion/orig/master.m3u8"]
	if !ok {
		t.Fatalf("missing resolved Orion URL in %+v", byURL)
	}
	if original.Language != "" || original.Quality != "1080p" || original.Type != provider.SourceTypeHLS {
		t.Errorf("original = %+v", original)
	}
	if len(original.Subtitles) != 2 {
		t.Errorf("original subtitles = %+v, want en+hi", original.Subtitles)
	}

	hindi, ok := byURL["https://mb.vidrift.net/api/orion/hi/video.mp4"]
	if !ok {
		t.Fatal("missing Orion MP4 stream")
	}
	if hindi.Language != "hi" || hindi.Type != provider.SourceTypeMP4 || hindi.Quality != "480p" {
		t.Errorf("hindi = %+v", hindi)
	}

	vela, ok := byURL["https://edge.reelvault.click/proxy?u=vela"]
	if !ok {
		t.Fatal("missing Vela proxy stream")
	}
	if vela.Language != "" || vela.Quality != "1080p" {
		t.Errorf("vela = %+v", vela)
	}

	titan, ok := byURL["https://cdn.example.com/titan.m3u8"]
	if !ok || titan.Language != "ta" || titan.Quality != "720p" {
		t.Errorf("titan = %+v", titan)
	}
	lyra, ok := byURL["https://cdn.example.com/lyra.m3u8"]
	if !ok || lyra.Language != "en" {
		t.Errorf("lyra = %+v", lyra)
	}

	bigbang, ok := byURL["https://cdn.example.com/bigbang/index.m3u8.png?exp=1&sig=x"]
	if !ok {
		t.Fatal("missing baked BigBang source")
	}
	if bigbang.Quality != "Auto" || len(bigbang.Subtitles) != 1 || bigbang.Subtitles[0].Language != "en" {
		t.Errorf("bigbang = %+v", bigbang)
	}
	if _, ok := byURL["https://cdn.example.com/vexon/index.m3u8"]; !ok {
		t.Error("missing baked Vexon source")
	}

	fx.mu.Lock()
	defer fx.mu.Unlock()
	if fx.embedHits["movie/27205"] != 1 {
		t.Errorf("embed hits = %d, want 1", fx.embedHits["movie/27205"])
	}
	for _, lane := range standardSourceProviders {
		if got := fx.laneHits["movie/27205|"+lane]; got != 1 {
			t.Errorf("lane %s hits = %d, want 1", lane, got)
		}
	}
	for _, request := range fx.requests {
		if strings.HasPrefix(request, "GET /api/source/movie/27205?") && !strings.Contains(request, "token=tok-movie") {
			t.Errorf("lane request missing playback token: %s", request)
		}
	}
}

func TestResolveMovieUsesCaches(t *testing.T) {
	client, fx := movieFixture(t, nil)
	episode := provider.Episode{TMDBID: 27205}

	first, err := client.ResolveSource(context.Background(), "27205", episode)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	second, err := client.ResolveSource(context.Background(), "27205", episode)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("cached resolve returned %d sources, want %d", len(second), len(first))
	}

	fx.mu.Lock()
	defer fx.mu.Unlock()
	if fx.embedHits["movie/27205"] != 1 {
		t.Errorf("embed fetched %d times, want 1", fx.embedHits["movie/27205"])
	}
	for _, lane := range standardSourceProviders {
		if got := fx.laneHits["movie/27205|"+lane]; got != 1 {
			t.Errorf("lane %s fetched %d times, want 1", lane, got)
		}
	}
}

func TestResolveMovieKeepsPartialLaneResults(t *testing.T) {
	client, fx := movieFixture(t, nil)
	client.laneBudget = 200 * time.Millisecond
	fx.delay["movie/27205|lyra"] = time.Second

	sources, err := client.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	for _, source := range sources {
		if source.URL == "https://cdn.example.com/lyra.m3u8" {
			t.Fatal("timed-out lane must not contribute sources")
		}
	}
	if len(sources) == 0 {
		t.Fatal("partial results were discarded")
	}
}

func TestResolveTVUsesSeasonAndEpisode(t *testing.T) {
	client, fx := newFixture(t, nil)
	fx.embed["tv/1399/1/2"] = embedHTML(
		`{"tmdbId":"1399","type":"tv","season":1,"episode":2,"playbackToken":"tok-tv","anime":false,`+
			`"bigbangUrl":"","bigbangSubtitles":[],"evionUrl":"","vexonUrl":"","selfhostUrl":null}`,
		`[]`,
	)
	fx.lane["tv/1399/1/2|orion"] = laneJSON("orion", "orion", false,
		namedStream("Orion · Original", "https://cdn.example.com/api/orion/tv/master.m3u8", "hls", 1080))

	sources, err := client.ResolveSource(context.Background(), "1399", provider.Episode{
		Season: 1, Episode: 2, TMDBID: 1399,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(sources))
	}
	if sources[0].URL != "https://cdn.example.com/api/orion/tv/master.m3u8" {
		t.Errorf("url = %q", sources[0].URL)
	}

	fx.mu.Lock()
	defer fx.mu.Unlock()
	if fx.embedHits["tv/1399/1/2"] != 1 {
		t.Errorf("TV embed hits = %+v", fx.embedHits)
	}
	if fx.laneHits["tv/1399/1/2|orion"] != 1 {
		t.Errorf("TV lane hits = %+v", fx.laneHits)
	}
}

func TestResolveAnimeSubAndDub(t *testing.T) {
	client, fx := animeFixture(t, false, false, nil)

	sub, err := client.ResolveSource(context.Background(), "1429", provider.Episode{
		Season: 1, Episode: 1, Audio: provider.AudioSub, ID: "watch/vidrift/1429/sub/1", TMDBID: 1429,
	})
	if err != nil {
		t.Fatalf("sub resolve: %v", err)
	}
	if len(sub) != 1 {
		t.Fatalf("sub sources = %d, want 1: %+v", len(sub), sub)
	}
	if sub[0].URL != "https://edge.reelvault.click/proxy?u=sub" {
		t.Errorf("sub url = %q", sub[0].URL)
	}
	if sub[0].Language != "ja" {
		t.Errorf("sub language = %q, want ja", sub[0].Language)
	}
	if sub[0].SubType != provider.SubTypeSoft {
		t.Errorf("sub type = %q, want soft", sub[0].SubType)
	}
	if len(sub[0].Subtitles) == 0 {
		t.Error("sub source is missing subtitles")
	}

	dub, err := client.ResolveSource(context.Background(), "1429", provider.Episode{
		Season: 1, Episode: 1, Audio: provider.AudioDub, ID: "watch/vidrift/1429/dub/1", TMDBID: 1429,
	})
	if err != nil {
		t.Fatalf("dub resolve: %v", err)
	}
	if len(dub) != 1 {
		t.Fatalf("dub sources = %d, want 1: %+v", len(dub), dub)
	}
	if dub[0].URL != "https://edge.reelvault.click/proxy?u=dub" {
		t.Errorf("dub url = %q", dub[0].URL)
	}
	if dub[0].Language != "en" {
		t.Errorf("dub language = %q, want en", dub[0].Language)
	}

	fx.mu.Lock()
	defer fx.mu.Unlock()
	for _, lane := range standardSourceProviders {
		if got := fx.laneHits["tv/1429/1/1|"+lane]; got != 0 {
			t.Errorf("anime resolve queried general lane %s %d times", lane, got)
		}
	}
	if got := fx.laneHits["tv/1429/1/1|anime"]; got != 1 {
		t.Errorf("anime lane hits = %d, want 1", got)
	}
	if got := fx.laneHits["tv/1429/1/1|animedub"]; got != 1 {
		t.Errorf("anime dub lane hits = %d, want 1", got)
	}
}

func TestResolveAnimeRejectsFallbacks(t *testing.T) {
	t.Run("dub falls back to sub", func(t *testing.T) {
		client, _ := animeFixture(t, false, true, nil)
		_, err := client.ResolveSource(context.Background(), "1429", provider.Episode{
			Season: 1, Episode: 1, Audio: provider.AudioDub, ID: "watch/vidrift/1429/dub/1", TMDBID: 1429,
		})
		if !errors.Is(err, provider.ErrAudioUnavailable) {
			t.Fatalf("err = %v, want ErrAudioUnavailable", err)
		}
	})

	t.Run("sub falls back to another lane", func(t *testing.T) {
		client, _ := animeFixture(t, true, false, nil)
		_, err := client.ResolveSource(context.Background(), "1429", provider.Episode{
			Season: 1, Episode: 1, Audio: provider.AudioSub, ID: "watch/vidrift/1429/sub/1", TMDBID: 1429,
		})
		if !errors.Is(err, provider.ErrNoSources) {
			t.Fatalf("err = %v, want ErrNoSources", err)
		}
	})
}

func TestResolveAnimeMovieRoute(t *testing.T) {
	client, fx := newFixture(t, nil)
	fx.embed["anilist/21519/1"] = embedHTML(
		`{"tmdbId":"372058","type":"movie","season":null,"episode":null,"playbackToken":"tok-movie-anime",`+
			`"anime":["anime","animedub"],"bigbangUrl":"","bigbangSubtitles":[],"evionUrl":"","vexonUrl":"","selfhostUrl":null}`,
		`[]`,
	)
	fx.lane["movie/372058|anime"] = laneJSON("Anime", "anime", false,
		proxyStream("Anime", "https://edge.reelvault.click/proxy?u=movie-sub"))

	sources, err := client.ResolveSource(context.Background(), "21519", provider.Episode{
		Audio: provider.AudioSub, Episode: 1, ID: "watch/vidrift/21519/sub/1",
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 || sources[0].URL != "https://edge.reelvault.click/proxy?u=movie-sub" {
		t.Fatalf("sources = %+v", sources)
	}

	fx.mu.Lock()
	defer fx.mu.Unlock()
	if fx.embedHits["anilist/21519/1"] != 1 {
		t.Errorf("embed hits = %+v", fx.embedHits)
	}
	if fx.laneHits["movie/372058|anime"] != 1 {
		t.Errorf("lane hits = %+v", fx.laneHits)
	}
}

func TestFilterSourcesByLanguage(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "unknown"},
		{URL: "hindi", Language: "hi"},
		{URL: "tamil", Language: "ta"},
		{URL: "english", Language: "en"},
	}

	client := &Client{langFilter: map[string]bool{"hi": false, "Tamil": false}}
	got := client.filterSourcesByLanguage(sources, false, false)
	if urls := sourceURLs(got); strings.Join(urls, ",") != "unknown,english" {
		t.Errorf("filtered movies = %v, want [unknown english]", urls)
	}

	// A globally disabled language must not hide an explicitly picked anime
	// track, but foreign tracks still never leak in.
	client = &Client{langFilter: map[string]bool{"ja": false, "en": false}}
	got = client.filterSourcesByLanguage([]provider.MediaSource{
		{URL: "ja", Language: "ja"},
		{URL: "en", Language: "en"},
		{URL: "hi", Language: "hi"},
	}, true, false)
	if urls := sourceURLs(got); strings.Join(urls, ",") != "ja" {
		t.Errorf("anime sub = %v, want [ja]", urls)
	}
	got = client.filterSourcesByLanguage([]provider.MediaSource{
		{URL: "ja", Language: "ja"},
		{URL: "en", Language: "en"},
	}, true, true)
	if urls := sourceURLs(got); strings.Join(urls, ",") != "en" {
		t.Errorf("anime dub = %v, want [en]", urls)
	}

	// Non-anime titles never vanish entirely when every language is off.
	client = &Client{langFilter: map[string]bool{"en": false}}
	got = client.filterSourcesByLanguage([]provider.MediaSource{{URL: "en", Language: "en"}}, false, false)
	if len(got) != 1 {
		t.Errorf("all-disabled fallback = %v, want the dropped row", sourceURLs(got))
	}
}

func sourceURLs(sources []provider.MediaSource) []string {
	urls := make([]string, 0, len(sources))
	for i := range sources {
		urls = append(urls, sources[i].URL)
	}
	return urls
}

func TestFetchEpisodesAnime(t *testing.T) {
	client, fx := newFixture(t, nil)
	fx.anilist = `{"data":{"Media":{"id":1,"idMal":2,"title":{"romaji":"Test Anime"},` +
		`"format":"TV","episodes":2,"status":"FINISHED","seasonYear":2024}}}`

	series := provider.SearchResult{
		Title:     "Test Anime",
		ID:        "1",
		AniListID: 1,
		Type:      provider.ModeAnime,
		MediaType: provider.MediaTypeAnime,
		Provider:  "vidrift",
	}
	episodes, err := client.FetchEpisodes(context.Background(), series)
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(episodes) != 4 {
		t.Fatalf("episodes = %d, want sub+dub for 2 episodes", len(episodes))
	}
	if episodes[0].ID != "watch/vidrift/1/sub/1" || episodes[0].Audio != provider.AudioSub {
		t.Errorf("episode 0 = %+v", episodes[0])
	}
	if episodes[1].ID != "watch/vidrift/1/dub/1" || episodes[1].Audio != provider.AudioDub {
		t.Errorf("episode 1 = %+v", episodes[1])
	}
	if episodes[0].Season != 1 || episodes[0].Episode != 1 {
		t.Errorf("episode coordinates = %+v", episodes[0])
	}
	if episodes[3].Episode != 2 {
		t.Errorf("last episode = %+v", episodes[3])
	}
}

func TestFetchEpisodesAnimeMovie(t *testing.T) {
	client, _ := newFixture(t, nil)
	series := provider.SearchResult{
		Title:     "Your Name",
		ID:        "21519",
		AniListID: 21519,
		Type:      provider.ModeAnime,
		MediaType: provider.MediaTypeMovie,
		Provider:  "vidrift",
	}
	episodes, err := client.FetchEpisodes(context.Background(), series)
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(episodes) != 2 {
		t.Fatalf("episodes = %d, want sub+dub", len(episodes))
	}
	for i, audio := range []string{provider.AudioSub, provider.AudioDub} {
		if episodes[i].Audio != audio {
			t.Errorf("episode %d audio = %q, want %q", i, episodes[i].Audio, audio)
		}
		if episodes[i].Episode != 1 {
			t.Errorf("episode %d number = %d, want 1", i, episodes[i].Episode)
		}
	}
}

func TestFetchEpisodesRejectsInvalidAnimeID(t *testing.T) {
	client, _ := newFixture(t, nil)
	_, err := client.FetchEpisodes(context.Background(), provider.SearchResult{
		ID:        "not-a-number",
		AniListID: 1,
		Type:      provider.ModeAnime,
		MediaType: provider.MediaTypeAnime,
	})
	if !errors.Is(err, provider.ErrNoEpisodes) {
		t.Fatalf("err = %v, want ErrNoEpisodes", err)
	}
}

func TestResolveUnsuccessfulLaneReturnsSentinel(t *testing.T) {
	client, fx := newFixture(t, nil)
	fx.embed["anilist/1429/1"] = embedHTML(
		`{"tmdbId":"1429","type":"tv","season":1,"episode":1,"playbackToken":"tok-dub-fail",`+
			`"anime":["anime","animedub"],"bigbangUrl":"","bigbangSubtitles":[],"evionUrl":"","vexonUrl":"","selfhostUrl":null}`,
		`[]`,
	)
	// 200 OK but success: false
	fx.lane["tv/1429/1/1|animedub"] = `{"success":false,"provider":"animedub"}`

	_, err := client.ResolveSource(context.Background(), "1429", provider.Episode{
		Season: 1, Episode: 1, Audio: provider.AudioDub, ID: "watch/vidrift/1429/dub/1", TMDBID: 1429,
	})
	if !errors.Is(err, provider.ErrAudioUnavailable) {
		t.Fatalf("err = %v, want ErrAudioUnavailable", err)
	}
}
