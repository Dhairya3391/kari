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
		case "/catalog/movie/featured/search=doraemon.json":
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
	results, err := client.Search(context.Background(), "doraemon", provider.ModeStremio)
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

func TestProbeRejectsAddonWithoutPlayableCatalogStreams(t *testing.T) {
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
	if err := client.Probe(context.Background()); err == nil {
		t.Fatal("Probe() succeeded without a playable catalog stream")
	}
}

func TestClientRejectsAddonWithMissingConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"name":"Needs setup","types":["movie"],"resources":["stream"],"behaviorHints":{"configurationRequired":true}}`)
	}))
	defer server.Close()
	_, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err == nil || !strings.Contains(err.Error(), "requires configuration") {
		t.Fatalf("NewClientWithHTTP() error = %v, want configuration error", err)
	}
}

func TestManifestModesRequireStreamResource(t *testing.T) {
	info := manifest{Types: []string{"movie"}, Resources: []json.RawMessage{json.RawMessage(`"catalog"`)}}
	if supportsStreams(info) {
		t.Fatal("supportsStreams() = true without stream resource")
	}
	if got := manifestModes(manifest{Types: []string{"movie", "series"}}); len(got) != 2 {
		t.Fatalf("manifestModes() = %v, want movie and TV modes", got)
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
