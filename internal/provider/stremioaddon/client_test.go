package stremioaddon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestClientLoadsManifestAndResolvesStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "test-token" {
			http.Error(w, "missing addon token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"id":"addon.test","name":"Test Addon","types":["movie","series"],"resources":["catalog","stream"],"catalogs":[{"type":"movie","id":"featured","name":"Featured","extra":[{"name":"search"}]}]}`)
		case "/catalog/movie/featured/search=matrix.json":
			fmt.Fprint(w, `{"metas":[{"id":"tt0133093","type":"movie","name":"The Matrix","releaseInfo":"1999","poster":"https://img.example/matrix.jpg"}]}`)
		case "/catalog/movie/featured.json":
			fmt.Fprint(w, `{"metas":[{"id":"tt0133093","type":"movie","name":"The Matrix"}]}`)
		case "/stream/movie/tt0133093.json", "/stream/series/tt0903747:1:1.json":
			fmt.Fprint(w, `{"streams":[{"name":"1080p","url":"https://cdn.example/video.m3u8"}]}`)
		case "/stream/movie/tmdb%3A12.json", "/stream/movie/tmdb:12.json":
			fmt.Fprint(w, `{"streams":[{"name":"1080p","url":"https://cdn.example/video.m3u8"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json?token=test-token", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	if client.Alias() != "Test Addon" || len(client.Modes()) != 3 {
		t.Fatalf("manifest metadata = %q, %v", client.Alias(), client.Modes())
	}
	results, err := client.Search(context.Background(), "matrix", provider.ModeStremio)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].Title != "The Matrix" || results[0].ID != client.Name()+"/movie/tt0133093" {
		t.Fatalf("Search() = %+v, want addon catalog result", results)
	}
	if _, err := client.FetchEpisodes(context.Background(), results[0]); err != nil {
		t.Fatalf("FetchEpisodes() error = %v", err)
	}
	addonSources, err := client.ResolveSource(context.Background(), results[0].ID, provider.Episode{ID: results[0].ID})
	if err != nil || len(addonSources) != 1 {
		t.Fatalf("ResolveSource(addon catalog item) = %+v, %v", addonSources, err)
	}
	sources, err := client.ResolveSource(context.Background(), "12", provider.Episode{TMDBID: 12})
	if err != nil {
		t.Fatalf("ResolveSource() error = %v", err)
	}
	if len(sources) != 1 || sources[0].Type != provider.SourceTypeHLS {
		t.Fatalf("ResolveSource() = %+v, want one HLS source", sources)
	}
	if err := client.Probe(context.Background()); err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
}

func TestClientBrowsesLiveTVCatalogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"id":"tv.test","name":"Live Addon","types":["tv"],"resources":["catalog","meta","stream"],"catalogs":[{"type":"tv","id":"live","name":"Live Events","extra":[{"name":"search"}]}]}`)
		case "/catalog/tv/live.json", "/catalog/tv/live/search=arsenal.json":
			fmt.Fprint(w, `{"metas":[{"id":"stremevent_1","type":"tv","name":"Arsenal vs Chelsea"}]}`)
		case "/stream/tv/stremevent_1.json":
			fmt.Fprint(w, `{"streams":[{"name":"SV3","title":"HD","url":"https://cdn.example/live.m3u8","behaviorHints":{"proxyHeaders":{"request":{"Referer":"https://addon.example/"}}}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	modes := client.Modes()
	if len(modes) != 1 || modes[0].Name != provider.ModeStremio {
		t.Fatalf("Modes() = %v, want Stremio catalog mode only", modes)
	}
	results, err := client.Search(context.Background(), "arsenal", provider.ModeStremio)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].MediaType != provider.MediaTypeLive {
		t.Fatalf("Search() = %+v, want one live result", results)
	}
	episodes, err := client.FetchEpisodes(context.Background(), results[0])
	if err != nil || len(episodes) != 1 {
		t.Fatalf("FetchEpisodes() = %+v, %v; want one playable row", episodes, err)
	}
	sources, err := client.ResolveSource(context.Background(), results[0].ID, episodes[0])
	if err != nil || len(sources) != 1 {
		t.Fatalf("ResolveSource() = %+v, %v", sources, err)
	}
	if sources[0].Type != provider.SourceTypeHLS || sources[0].Referer != "https://addon.example/" {
		t.Fatalf("ResolveSource() = %+v, want HLS source with addon referer", sources[0])
	}
	if err := client.Probe(context.Background()); err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
}

func TestClientAcceptsAddonWithoutPlayableCatalogStreams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Broken Addon","types":["movie"],"resources":["catalog","stream"],"catalogs":[{"type":"movie","id":"featured"}]}`)
		case "/catalog/movie/featured.json":
			fmt.Fprint(w, `{"metas":[{"id":"movie-1","name":"A Movie"}]}`)
		case "/stream/movie/movie-1.json":
			fmt.Fprint(w, `{"streams":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	if client.Alias() != "Broken Addon" {
		t.Fatalf("Alias() = %q, want manifest name", client.Alias())
	}
	if err := client.Probe(context.Background()); err == nil {
		t.Fatal("Probe() succeeded without a playable catalog stream")
	}
}

func TestClientAcceptsAddonRequiringConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":"Needs setup","types":["movie"],"resources":["stream"],"behaviorHints":{"configurationRequired":true}}`)
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v, want addon accepted", err)
	}
	if client.Alias() != "Needs setup" || len(client.Modes()) == 0 {
		t.Fatalf("addon metadata = %q, %v", client.Alias(), client.Modes())
	}
}

func TestManifestModesWithoutStreamResource(t *testing.T) {
	info := manifest{Types: []string{"movie"}, Resources: []resourceEntry{{Name: "catalog"}}}
	if supportsStreams(info) {
		t.Fatal("supportsStreams() = true without stream resource")
	}
	if got := manifestModes(info); len(got) == 0 {
		t.Fatal("manifestModes() is empty without a stream resource")
	}
	info = manifest{Types: []string{"movie", "series"}, Resources: []resourceEntry{{Name: "catalog"}}}
	if got := manifestModes(info); len(got) != 2 || got[0].Name != provider.ModeMovies || got[1].Name != provider.ModeTV {
		t.Fatalf("manifestModes() = %v, want movie and TV modes", got)
	}
	if got := manifestModes(manifest{}); len(got) != 1 || got[0].Name != provider.ModeStremio {
		t.Fatalf("manifestModes() = %v, want a fallback Stremio mode", got)
	}
}

func TestClientAliasFallbacks(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{"manifest name", `{"id":"addon.id","name":"Named Addon"}`, "Named Addon"},
		{"manifest id", `{"id":"addon.id"}`, "addon.id"},
		{"host", `{}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tt.manifest)
			}))
			defer server.Close()

			client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
			if err != nil {
				t.Fatalf("NewClientWithHTTP() error = %v", err)
			}
			want := tt.want
			if want == "" {
				parsed, err := url.Parse(server.URL)
				if err != nil {
					t.Fatalf("url.Parse() error = %v", err)
				}
				want = parsed.Host
			}
			if client.Alias() != want {
				t.Fatalf("Alias() = %q, want %q", client.Alias(), want)
			}
			if !strings.HasPrefix(client.Name(), "addon-") || client.Name() == client.Alias() {
				t.Fatalf("Name() = %q, want digest-based identity distinct from alias", client.Name())
			}
		})
	}
}

func TestManifestResourceObjectParsing(t *testing.T) {
	var info manifest
	raw := `{"name":"addon","types":["movie"],"resources":["catalog",{"name":"stream","types":["series","anime"],"idPrefixes":["tt"]}],"catalogs":[{"type":"movie","id":"featured"}]}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(info.Resources) != 2 || info.Resources[0].Name != "catalog" {
		t.Fatalf("Resources = %+v, want catalog string and stream object", info.Resources)
	}
	stream := info.Resources[1]
	if stream.Name != "stream" || len(stream.Types) != 2 || stream.Types[0] != "series" || stream.Types[1] != "anime" || len(stream.IDPrefixes) != 1 || stream.IDPrefixes[0] != "tt" {
		t.Fatalf("stream resource = %+v, want name, types and idPrefixes", stream)
	}
	if !supportsStreams(info) || !supportsCatalogs(info) || supportsResource(info, "subtitles") {
		t.Fatal("supportsResource() misreported parsed resources")
	}
	want := []provider.ContentType{provider.ModeMovies, provider.ModeTV, provider.ModeStremio}
	modes := manifestModes(info)
	if len(modes) != len(want) {
		t.Fatalf("manifestModes() = %v, want %v", modes, want)
	}
	for index := range want {
		if modes[index].Name != want[index] {
			t.Fatalf("manifestModes() = %v, want %v", modes, want)
		}
	}
	onlyResource := manifestModes(manifest{Resources: []resourceEntry{{Name: "stream", Types: []string{"movie"}}}})
	if len(onlyResource) != 1 || onlyResource[0].Name != provider.ModeMovies {
		t.Fatalf("manifestModes() from resource types = %v, want movie mode", onlyResource)
	}
}

func TestCatalogMediaTypeMapping(t *testing.T) {
	tests := []struct {
		catalogType string
		want        string
	}{
		{"series", provider.MediaTypeTV},
		{"tv", provider.MediaTypeLive},
		{"anime", provider.MediaTypeAnime},
		{"cartoon", provider.MediaTypeMovie},
		{"", provider.MediaTypeMovie},
	}
	for _, tt := range tests {
		if got := catalogMediaType(tt.catalogType); got != tt.want {
			t.Errorf("catalogMediaType(%q) = %q, want %q", tt.catalogType, got, tt.want)
		}
		if supported := supportedCatalogType(tt.catalogType); supported != (strings.TrimSpace(tt.catalogType) != "") {
			t.Errorf("supportedCatalogType(%q) = %v", tt.catalogType, supported)
		}
	}
}

func TestClientBrowsesUnknownCatalogType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Odd Addon","types":["cartoon"],"resources":["catalog","stream"],"catalogs":[{"type":"cartoon","id":"featured"}]}`)
		case "/catalog/cartoon/featured.json":
			fmt.Fprint(w, `{"metas":[{"id":"odd-1","name":"Odd Show","releaseInfo":"2020","poster":"https://img.example/odd.jpg"}]}`)
		case "/stream/cartoon/odd-1.json":
			fmt.Fprint(w, `{"streams":[{"name":"720p","url":"https://cdn.example/odd.m3u8"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	if len(client.Modes()) == 0 || client.Modes()[0].Name != provider.ModeStremio {
		t.Fatalf("Modes() = %v, want a Stremio mode", client.Modes())
	}
	results, err := client.Search(context.Background(), "", provider.ModeStremio)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].MediaType != provider.MediaTypeMovie {
		t.Fatalf("Search() = %+v, want one movie-typed result", results)
	}
	episodes, err := client.FetchEpisodes(context.Background(), results[0])
	if err != nil || len(episodes) != 1 || episodes[0].ID != "odd-1" {
		t.Fatalf("FetchEpisodes() = %+v, %v", episodes, err)
	}
	sources, err := client.ResolveSource(context.Background(), results[0].ID, episodes[0])
	if err != nil || len(sources) != 1 {
		t.Fatalf("ResolveSource() = %+v, %v", sources, err)
	}
	if err := client.Probe(context.Background()); err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
}

func TestMediaSourcesStreamFields(t *testing.T) {
	sources := mediaSources([]streamItem{
		{
			URL:   "https://cdn.example/video.m3u8",
			Title: "1080p WEB",
			Behavior: streamBehavior{
				Headers:     map[string]string{"Referer": "https://addon.example/", "User-Agent": "Kari test"},
				NotWebReady: true, VideoHash: "abc123", VideoSize: 1024, BingeGroup: "group",
			},
			Subtitles: []streamSubtitle{
				{URL: "https://subs.example/en.vtt", Lang: "English", Label: "Default"},
				{URL: "https://subs.example/hi.vtt", Lang: "hi"},
				{URL: "/relative.vtt", Lang: "en"},
			},
		},
		{ExternalURL: "https://player.example/watch/1", Title: "External"},
		{YTID: "dQw4w9WgXcQ", Title: "YouTube"},
		{InfoHash: "deadbeef", Title: "Torrent"},
	})
	if len(sources) != 1 {
		t.Fatalf("mediaSources() kept %d rows, want only the direct stream", len(sources))
	}
	source := sources[0]
	if source.Quality != "1080p WEB" || source.Height != 1080 {
		t.Fatalf("source quality/height = %q/%d, want 1080p WEB/1080", source.Quality, source.Height)
	}
	if source.Referer != "https://addon.example/" || source.UserAgent != "Kari test" {
		t.Fatalf("source headers = %+v, want carried referer and user agent", source)
	}
	if len(source.Subtitles) != 2 {
		t.Fatalf("source subtitles = %+v, want two direct tracks", source.Subtitles)
	}
	if source.Subtitles[0].Language != "en" || !source.Subtitles[0].Default || source.Subtitles[0].Referer != source.Referer {
		t.Fatalf("first subtitle = %+v, want normalized default English track", source.Subtitles[0])
	}
	if source.Subtitles[1].Language != "hi" || source.Subtitles[1].Default {
		t.Fatalf("second subtitle = %+v, want non-default Hindi track", source.Subtitles[1])
	}
}

func TestResolveSourceMergesSubtitlesResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Subs Addon","types":["movie"],"resources":["catalog","stream","subtitles"],"catalogs":[{"type":"movie","id":"featured"}]}`)
		case "/catalog/movie/featured.json":
			fmt.Fprint(w, `{"metas":[{"id":"movie-1","name":"A Movie"}]}`)
		case "/stream/movie/movie-1.json":
			fmt.Fprint(w, `{"streams":[{"name":"1080p","url":"https://cdn.example/video.m3u8","behaviorHints":{"videoHash":"abc123"}},{"name":"720p","url":"https://cdn.example/other.m3u8"},{"name":"external","externalUrl":"https://player.example/watch/1"}]}`)
		case "/subtitles/movie/movie-1/filename=video.m3u8&videoHash=abc123.json":
			fmt.Fprint(w, `{"subtitles":[{"url":"https://subs.example/en.vtt","lang":"en"},{"url":"https://subs.example/en.vtt","lang":"en"},{"url":"https://subs.example/hi.vtt","lang":"Hindi"}]}`)
		case "/subtitles/movie/movie-1/filename=other.m3u8.json":
			fmt.Fprint(w, `{"subtitles":[{"url":"https://subs.example/fr.vtt","lang":"French"}]}`)
		case "/stream/movie/12.json":
			fmt.Fprint(w, `{"streams":[{"name":"1080p","url":"https://cdn.example/tmdb.m3u8"}]}`)
		case "/subtitles/movie/12/filename=tmdb.m3u8.json":
			fmt.Fprint(w, `{"subtitles":[{"url":"https://subs.example/tmdb-en.vtt","lang":"English"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	results, err := client.Search(context.Background(), "", provider.ModeStremio)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	sources, err := client.ResolveSource(context.Background(), results[0].ID, provider.Episode{ID: results[0].ID})
	if err != nil {
		t.Fatalf("ResolveSource(addon item) error = %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("ResolveSource(addon item) = %+v, want two direct sources", sources)
	}
	first, second := sources[0], sources[1]
	if first.URL != "https://cdn.example/video.m3u8" || second.URL != "https://cdn.example/other.m3u8" {
		t.Fatalf("unexpected source order: %+v", sources)
	}
	if len(first.Subtitles) != 2 || first.Subtitles[0].Referer != client.endpoint ||
		first.Subtitles[0].Language != "en" || first.Subtitles[1].Language != "hi" {
		t.Fatalf("first source subtitles = %+v, want deduped en and hi tracks", first.Subtitles)
	}
	if len(second.Subtitles) != 1 || second.Subtitles[0].Language != "fr" ||
		second.Subtitles[0].URL != "https://subs.example/fr.vtt" {
		t.Fatalf("second source subtitles = %+v, want its own French track", second.Subtitles)
	}
	tmdbSources, err := client.ResolveSource(context.Background(), "12", provider.Episode{TMDBID: 12})
	if err != nil || len(tmdbSources) != 1 {
		t.Fatalf("ResolveSource(tmdb id) = %+v, %v", tmdbSources, err)
	}
	if len(tmdbSources[0].Subtitles) != 1 || tmdbSources[0].Subtitles[0].Language != "en" || tmdbSources[0].Subtitles[0].URL != "https://subs.example/tmdb-en.vtt" {
		t.Fatalf("ResolveSource(tmdb id) subtitles = %+v, want addon track", tmdbSources[0].Subtitles)
	}
}

func TestSubtitlesFailureDoesNotBreakResolution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Subs Addon","types":["movie"],"resources":["stream","subtitles"]}`)
		case "/stream/movie/tt1.json":
			fmt.Fprint(w, `{"streams":[{"name":"1080p","url":"https://cdn.example/video.m3u8"}]}`)
		case "/subtitles/movie/tt1/filename=video.m3u8.json":
			http.Error(w, "no subtitles available", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	sources, err := client.resolveAddonStreams(context.Background(), "movie", "tt1")
	if err != nil || len(sources) != 1 || len(sources[0].Subtitles) != 0 {
		t.Fatalf("resolveAddonStreams() = %+v, %v; want source without subtitles", sources, err)
	}
}

func TestSearchCatalogsEnrichesMeta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Meta Addon","types":["movie"],"resources":["catalog"],"catalogs":[{"type":"movie","id":"featured"}]}`)
		case "/catalog/movie/featured.json":
			fmt.Fprint(w, `{"metas":[{"id":"tmdb:12","name":"Meta Movie","releaseInfo":"2001","poster":"https://img.example/poster.jpg","description":"A description","genres":["Drama","Sci-Fi"],"runtime":"120","imdbRating":"8.1"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	results, err := client.Search(context.Background(), "", provider.ModeStremio)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() = %+v, want one result", results)
	}
	got := results[0]
	if got.ID != client.Name()+"/movie/tmdb:12" || got.TMDBID != 12 {
		t.Fatalf("Search() handle/TMDBID = %q/%d, want addon handle and TMDB 12", got.ID, got.TMDBID)
	}
	if got.Overview != "A description" || got.CoverURL != "https://img.example/poster.jpg" || got.Year != "2001" || got.MediaType != provider.MediaTypeMovie {
		t.Fatalf("Search() meta = %+v, want enriched description, poster, year and media type", got)
	}
	if len(got.Genres) != 2 || got.Genres[0] != "Drama" || got.Genres[1] != "Sci-Fi" {
		t.Fatalf("Search() genres = %v, want Drama and Sci-Fi", got.Genres)
	}
}

func TestFetchEpisodesUsesCatalogKind(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Series Addon","types":["series"],"resources":["catalog","meta","stream"],"catalogs":[{"type":"series","id":"shows"}]}`)
		case "/catalog/series/shows.json":
			fmt.Fprint(w, `{"metas":[{"id":"show-1","name":"Show"}]}`)
		case "/meta/series/show-1.json":
			fmt.Fprint(w, `{"meta":{"id":"show-1","videos":[{"id":"show-1:1:2","title":"Second","season":1,"episode":2,"overview":"Second overview","released":"2020-01-08"},{"id":"show-1:1:1","title":"First","season":1,"episode":1}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	results, err := client.Search(context.Background(), "", provider.ModeStremio)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	episodes, err := client.FetchEpisodes(context.Background(), results[0])
	if err != nil {
		t.Fatalf("FetchEpisodes() error = %v", err)
	}
	if len(episodes) != 2 || episodes[0].ID != "show-1:1:2" || episodes[0].Title != "Second" || episodes[0].Season != 1 || episodes[0].Episode != 2 || episodes[1].Title != "First" {
		t.Fatalf("FetchEpisodes() = %+v, want titled and numbered videos", episodes)
	}
	synthetic, err := client.FetchEpisodes(context.Background(), provider.SearchResult{ID: client.Name() + "/movie/movie-1", Title: "A Movie"})
	if err != nil || len(synthetic) != 1 || synthetic[0].ID != "movie-1" || synthetic[0].Title != "A Movie" {
		t.Fatalf("FetchEpisodes(non-series) = %+v, %v, want synthetic episode", synthetic, err)
	}
}

func TestCustomCatalogTypeResolvesUsingMetaType(t *testing.T) {
	// Addons like HdHub group items under a custom catalog type while each
	// meta declares the real type, which is what the stream endpoint uses.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Custom Addon","types":["movie","series","HdHub"],"resources":["stream","catalog"],"catalogs":[{"type":"HdHub","id":"featured"}]}`)
		case "/catalog/HdHub/featured.json":
			fmt.Fprint(w, `{"metas":[{"id":"tt4154796","type":"movie","name":"Avengers: Endgame","releaseInfo":"2019"}]}`)
		case "/stream/movie/tt4154796.json":
			fmt.Fprint(w, `{"streams":[{"name":"Donate","externalUrl":"https://addon.example/donate"},{"name":"HdHub 2160p","url":"https://cdn.example/endgame.mkv"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	results, err := client.Search(context.Background(), "", provider.ModeStremio)
	if err != nil || len(results) != 1 {
		t.Fatalf("Search() = %+v, %v", results, err)
	}
	if results[0].ID != client.Name()+"/movie/tt4154796" || results[0].MediaType != provider.MediaTypeMovie {
		t.Fatalf("Search() handle/media type = %q/%q, want meta type movie", results[0].ID, results[0].MediaType)
	}
	sources, err := client.ResolveSource(context.Background(), results[0].ID, provider.Episode{ID: results[0].ID})
	if err != nil || len(sources) != 1 || sources[0].URL != "https://cdn.example/endgame.mkv" {
		t.Fatalf("ResolveSource() = %+v, %v, want one playable stream", sources, err)
	}
}

func TestSearchFiltersAddonsThatIgnoreQuery(t *testing.T) {
	const metas = `{"metas":[{"id":"tt1","type":"movie","name":"Heartland"},{"id":"tt2","type":"movie","name":"Heart of the Beast"},{"id":"tt3","type":"movie","name":"The Odyssey"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Loose Search Addon","types":["movie"],"resources":["catalog","stream"],"catalogs":[{"type":"movie","id":"featured","extra":[{"name":"search"}],"extraSupported":["search"]}]}`)
		case "/catalog/movie/featured.json", "/catalog/movie/featured/search=heart.json":
			// Upstream advertises search but returns its whole catalog.
			fmt.Fprint(w, metas)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	results, err := client.Search(context.Background(), "heart", provider.ModeStremio)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search() = %+v, want only the two matching titles", results)
	}
	if results[0].Title != "Heartland" || results[1].Title != "Heart of the Beast" {
		t.Fatalf("Search() = %+v, want locally filtered matches", results)
	}
}

func TestFetchEpisodesUsesMetaForAnimeCatalogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Anime Addon","types":["anime"],"resources":["catalog","meta","stream"],"catalogs":[{"type":"anime","id":"shows"}]}`)
		case "/catalog/anime/shows.json":
			fmt.Fprint(w, `{"metas":[{"id":"show-1","name":"Show"}]}`)
		case "/meta/anime/show-1.json":
			fmt.Fprint(w, `{"meta":{"videos":[{"id":"show-1:1:1","title":"One","season":1,"episode":1},{"id":"show-1:1:2","title":"Two","season":1,"episode":2}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	results, err := client.Search(context.Background(), "", provider.ModeStremio)
	if err != nil || len(results) != 1 || results[0].MediaType != provider.MediaTypeAnime {
		t.Fatalf("Search() = %+v, %v, want one anime result", results, err)
	}
	episodes, err := client.FetchEpisodes(context.Background(), results[0])
	if err != nil || len(episodes) != 2 {
		t.Fatalf("FetchEpisodes() = %+v, %v, want two anime episodes", episodes, err)
	}
	if episodes[0].ID != "show-1:1:1" || episodes[0].Title != "One" || episodes[1].ID != "show-1:1:2" || episodes[1].Title != "Two" {
		t.Fatalf("FetchEpisodes() = %+v, want titled anime videos", episodes)
	}
}

func TestFetchEpisodesFallsBackWithoutMetaResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Anime Addon","types":["anime"],"resources":["catalog","stream"],"catalogs":[{"type":"anime","id":"shows"}]}`)
		case "/catalog/anime/shows.json":
			fmt.Fprint(w, `{"metas":[{"id":"show-1","name":"Show"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	results, err := client.Search(context.Background(), "", provider.ModeStremio)
	if err != nil || len(results) != 1 {
		t.Fatalf("Search() = %+v, %v", results, err)
	}
	episodes, err := client.FetchEpisodes(context.Background(), results[0])
	if err != nil || len(episodes) != 1 || episodes[0].ID != "show-1" || episodes[0].Title != "Show" {
		t.Fatalf("FetchEpisodes() = %+v, %v, want one synthetic episode", episodes, err)
	}
}

func TestCatalogURLPreservesConfigurationQuery(t *testing.T) {
	client := &Client{endpoint: "https://addon.example", query: "token=secret"}
	catalog := addonCatalog{ID: "featured", Type: "movie", ExtraSupported: []string{"search"}}
	endpoint, err := client.catalogURL("catalog", catalog.Type, catalog.ID)
	if err != nil {
		t.Fatalf("catalogURL() error = %v", err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Query().Get("token") != "secret" {
		t.Fatalf("catalogURL() query = %q, want token retained", parsed.RawQuery)
	}
}

func TestMediaSourcesAcceptRemoteAddonProxyURLs(t *testing.T) {
	sources := mediaSources([]streamItem{
		{
			URL: "https://api.kartoons.to/api/stremio/proxy/video.m3u8",
			Behavior: streamBehavior{ProxyHeaders: proxyHeaders{Request: map[string]string{
				"Referer":    "https://addon.example/",
				"User-Agent": "Kari test",
				"Cookie":     "session=abc",
			}}},
		},
		{URL: "http://127.0.0.1:8080/proxy/video.m3u8"},
		{URL: "https://cdn.example/video.m3u8"},
	})
	if len(sources) != 2 {
		t.Fatalf("mediaSources() kept %d URLs, want remote addon proxy and CDN", len(sources))
	}
	if sources[0].URL != "https://api.kartoons.to/api/stremio/proxy/video.m3u8" || sources[0].Type != provider.SourceTypeHLS {
		t.Errorf("remote addon proxy source = %+v", sources[0])
	}
	if sources[0].Referer != "https://addon.example/" || sources[0].UserAgent != "Kari test" || sources[0].CookieHeader != "session=abc" {
		t.Errorf("standard proxy request headers were not carried onto the stream: %+v", sources[0])
	}
}
