package stremioaddon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

// streamProbeTransport answers ranged stream probes in-process while
// forwarding addon API calls to the test server, so tests never dial a real
// CDN. Blocked and stalled paths mimic the failures seen in production.
type streamProbeTransport struct {
	base http.RoundTripper
}

func (t streamProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	requestPath := req.URL.Path
	if strings.Contains(requestPath, "/slow") {
		return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: context.DeadlineExceeded}
	}
	if strings.Contains(requestPath, "/blocked") {
		return probeResponse(req, http.StatusForbidden, "application/xml", "<Error><Code>AccessDenied</Code></Error>")
	}
	if strings.HasSuffix(requestPath, ".m3u8") {
		return probeResponse(req, http.StatusOK, "application/vnd.apple.mpegurl", "#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:2,\nseg.ts\n")
	}
	if strings.HasSuffix(requestPath, ".ts") || strings.HasSuffix(requestPath, ".mkv") || strings.HasSuffix(requestPath, ".mp4") {
		return probeResponse(req, http.StatusOK, "video/mp2t", "\x47\x40\x00\x10")
	}
	return t.base.RoundTrip(req)
}

func probeResponse(req *http.Request, code int, contentType, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func testStreamClient(server *httptest.Server) *http.Client {
	return &http.Client{Transport: streamProbeTransport{base: server.Client().Transport}}
}

// emptyTitleSearch keeps catalog tests hermetic: they must never dial the
// public title index or TMDB.
type emptyTitleSearch struct{}

func (emptyTitleSearch) Search(context.Context, string, provider.ContentType) ([]provider.SearchResult, error) {
	return nil, provider.ErrNoResults
}

func (emptyTitleSearch) FetchEpisodes(context.Context, provider.SearchResult) ([]provider.Episode, error) {
	return nil, provider.ErrNoEpisodes
}

// stubTitleSearch supplies canned TMDB rows so hybrid search stays hermetic.
type stubTitleSearch struct {
	results  []provider.SearchResult
	episodes []provider.Episode
}

func (s stubTitleSearch) Search(context.Context, string, provider.ContentType) ([]provider.SearchResult, error) {
	return s.results, nil
}

func (s stubTitleSearch) FetchEpisodes(context.Context, provider.SearchResult) ([]provider.Episode, error) {
	return s.episodes, nil
}

// noTitleSearch fails a test that unexpectedly consults TMDB.
type noTitleSearch struct{ t *testing.T }

func (n noTitleSearch) Search(context.Context, string, provider.ContentType) ([]provider.SearchResult, error) {
	n.t.Error("TMDB search called for an empty Stremio query")
	return nil, provider.ErrNoResults
}

func (n noTitleSearch) FetchEpisodes(context.Context, provider.SearchResult) ([]provider.Episode, error) {
	n.t.Error("TMDB episodes called for an addon catalog row")
	return nil, provider.ErrNoEpisodes
}

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

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json?token=test-token", tmdb.NewKeyPool(nil), testStreamClient(server))
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	client.base = emptyTitleSearch{}
	if client.Alias() != "Test Addon" || len(client.Modes()) != 1 {
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

func TestSearchAlwaysUsesAddonCatalogs(t *testing.T) {
	var catalogCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Movie Addon","types":["movie"],"resources":["catalog","stream"],"catalogs":[{"type":"movie","id":"featured"}]}`)
		case "/catalog/movie/featured.json":
			catalogCalls.Add(1)
			fmt.Fprint(w, `{"metas":[{"id":"movie-1","name":"A Movie","type":"movie"}]}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	client.base = noTitleSearch{t: t}
	for _, mode := range []provider.ContentType{provider.ModeStremio, provider.ModeMovies, provider.ModeTV} {
		results, err := client.Search(context.Background(), "", mode)
		if err != nil {
			t.Fatalf("Search(%s) error = %v", mode, err)
		}
		if len(results) != 1 || results[0].Type != provider.ModeStremio {
			t.Fatalf("Search(%s) = %+v, want one addon catalog row", mode, results)
		}
	}
	if got := catalogCalls.Load(); got != 3 {
		t.Fatalf("catalog requests = %d, want 3", got)
	}
}

func TestSearchCombinesAddonCatalogsWithTitleSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Catalog Addon","types":["movie"],"resources":["catalog","stream"],"catalogs":[{"type":"movie","id":"featured"}]}`)
		case "/catalog/movie/featured.json", "/catalog/movie/featured/search=hit.json":
			fmt.Fprint(w, `{"metas":[{"id":"tmdb:42","type":"movie","name":"Catalog Hit"}]}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), server.Client())
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	client.base = stubTitleSearch{
		results: []provider.SearchResult{
			{Title: "Catalog Hit", ID: "42", TMDBID: 42, Type: provider.ModeStremio, MediaType: provider.MediaTypeMovie},
			{Title: "TMDB Only", ID: "84", TMDBID: 84, Type: provider.ModeStremio, MediaType: provider.MediaTypeMovie},
		},
		episodes: []provider.Episode{{ID: "42", Title: "TMDB Only"}},
	}
	results, err := client.Search(context.Background(), "hit", provider.ModeStremio)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search() = %+v, want the catalog hit and the TMDB-only title", results)
	}
	if results[0].ID != client.Name()+"/movie/tmdb:42" || results[1].Title != "TMDB Only" {
		t.Fatalf("Search() = %+v, want catalog row kept and TMDB row appended", results)
	}
	episodes, err := client.FetchEpisodes(context.Background(), results[1])
	if err != nil || len(episodes) != 1 || episodes[0].Title != "TMDB Only" {
		t.Fatalf("FetchEpisodes(TMDB row) = %+v, %v", episodes, err)
	}
}

func TestDropDuplicateTitlesKeepsAddonHandle(t *testing.T) {
	catalogs := []provider.SearchResult{
		{Title: "Catalog Hit", ID: "addon-a/movie/tt123", Year: "2020", MediaType: provider.MediaTypeMovie},
		{Title: "Catalog Hit", ID: "addon-b/movie/other", Year: "2020", MediaType: provider.MediaTypeMovie},
	}
	titles := []provider.SearchResult{
		{Title: "Catalog Hit", ID: "123", TMDBID: 123, Year: "2020", MediaType: provider.MediaTypeMovie},
		{Title: "TMDB Only", ID: "456", TMDBID: 456, Year: "2021", MediaType: provider.MediaTypeMovie},
	}
	got := dropDuplicateTitles(catalogs, titles)
	if len(got) != 1 || got[0].Title != "TMDB Only" {
		t.Fatalf("dropDuplicateTitles() = %+v, want only the TMDB-only title", got)
	}
}

func TestDedupeKeepsDistinctAddonHandles(t *testing.T) {
	rows := []provider.SearchResult{
		{Title: "Live Event", ID: "addon-a/tv/evt-1", Type: provider.ModeStremio, MediaType: provider.MediaTypeLive},
		{Title: "Live Event", ID: "addon-b/tv/evt-2", Type: provider.ModeStremio, MediaType: provider.MediaTypeLive},
		{Title: "Live Event", ID: "7", TMDBID: 7, Type: provider.ModeStremio, MediaType: provider.MediaTypeLive},
		{Title: "Live Event", ID: "8", TMDBID: 7, Type: provider.ModeStremio, MediaType: provider.MediaTypeLive},
	}
	got := dedupeSearchResults(rows)
	want := []string{"addon-a/tv/evt-1", "addon-b/tv/evt-2", "7"}
	if len(got) != len(want) {
		t.Fatalf("dedupeSearchResults() = %+v, want %v", got, want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("dedupeSearchResults()[%d].ID = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestSearchFindsTitlesForDataOnlyAddon(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Stream Only","types":["movie"],"resources":["stream"]}`)
		case "/stream/movie/99.json":
			fmt.Fprint(w, `{"streams":[{"name":"1080p","url":"https://cdn.example/dataonly.mkv"}]}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), testStreamClient(server))
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	client.base = stubTitleSearch{results: []provider.SearchResult{{
		Title: "Data Only Movie", ID: "99", TMDBID: 99,
		Type: provider.ModeStremio, MediaType: provider.MediaTypeMovie,
	}}}
	results, err := client.Search(context.Background(), "data", provider.ModeStremio)
	if err != nil || len(results) != 1 || results[0].Title != "Data Only Movie" {
		t.Fatalf("Search() = %+v, %v, want the TMDB title", results, err)
	}
	sources, err := client.ResolveSource(context.Background(), results[0].ID, provider.Episode{TMDBID: 99})
	if err != nil || len(sources) != 1 || sources[0].URL != "https://cdn.example/dataonly.mkv" {
		t.Fatalf("ResolveSource() = %+v, %v, want the addon stream", sources, err)
	}
}

func TestTMDBSeriesSearchKeepsAddonRouteAndEpisodeMetadata(t *testing.T) {
	client := &Client{name: "addon-series"}
	client.base = stubTitleSearch{
		results: []provider.SearchResult{{
			Title: "Example Series", ID: "42", TMDBID: 42,
			Type: provider.ModeTV, MediaType: provider.MediaTypeTV,
		}},
		episodes: []provider.Episode{{ID: "42:1:1", Season: 1, Episode: 1, Title: "Pilot"}},
	}

	rows, err := client.Search(context.Background(), "example", provider.ModeStremio)
	if err != nil || len(rows) != 1 {
		t.Fatalf("Search() = %+v, %v, want one series row", rows, err)
	}
	wantID := "addon-series/series/tmdb:42"
	if rows[0].ID != wantID || rows[0].MediaType != provider.MediaTypeTV || rows[0].TMDBID != 42 {
		t.Fatalf("series result = %+v, want addon handle %q with TMDB identity", rows[0], wantID)
	}
	rows[0].Provider = "addon-series"
	if got, ok := rows[0].ResolutionID(provider.ModeStremio, "addon-series"); !ok || got != wantID {
		t.Fatalf("addon ResolutionID() = %q, %v, want its scoped handle", got, ok)
	}
	if got, ok := rows[0].ResolutionID(provider.ModeStremio, "another-addon"); !ok || got != "42" {
		t.Fatalf("fallback ResolutionID() = %q, %v, want canonical TMDB ID", got, ok)
	}
	episodes, err := client.FetchEpisodes(context.Background(), rows[0])
	if err != nil || len(episodes) != 1 || episodes[0].Title != "Pilot" {
		t.Fatalf("FetchEpisodes() = %+v, %v, want TMDB episode metadata", episodes, err)
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

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), testStreamClient(server))
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	client.base = emptyTitleSearch{}
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
	if got := manifestModes(info); len(got) != 1 || got[0].Name != provider.ModeStremio {
		t.Fatalf("manifestModes() = %v, want Stremio only", got)
	}
	if got := manifestModes(manifest{}); len(got) != 1 || got[0].Name != provider.ModeStremio {
		t.Fatalf("manifestModes() = %v, want Stremio only", got)
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
	if !supportsStreams(info) || !supportsResource(info, "catalog") || supportsResource(info, "subtitles") {
		t.Fatal("supportsResource() misreported parsed resources")
	}
	modes := manifestModes(info)
	if len(modes) != 1 || modes[0].Name != provider.ModeStremio {
		t.Fatalf("manifestModes() = %v, want Stremio only", modes)
	}
	onlyResource := manifestModes(manifest{Resources: []resourceEntry{{Name: "stream", Types: []string{"movie"}}}})
	if len(onlyResource) != 1 || onlyResource[0].Name != provider.ModeStremio {
		t.Fatalf("manifestModes() from movie resources = %v, want Stremio only", onlyResource)
	}
}

func TestCatalogMediaTypeMapping(t *testing.T) {
	tests := []struct {
		catalogType string
		want        string
	}{
		{"series", provider.MediaTypeTV},
		{"tv", provider.MediaTypeLive},
		{"sport", provider.MediaTypeLive},
		{"channel", provider.MediaTypeLive},
		{"LIVE", provider.MediaTypeLive},
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

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), testStreamClient(server))
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

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), testStreamClient(server))
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

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), testStreamClient(server))
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

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), testStreamClient(server))
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
	client.base = emptyTitleSearch{}
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

func TestResolveSourceProbesStreams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Probe Addon","types":["movie"],"resources":["catalog","stream"],"catalogs":[{"type":"movie","id":"featured"}]}`)
		case "/catalog/movie/featured.json":
			fmt.Fprint(w, `{"metas":[{"id":"movie-1","name":"A Movie"}]}`)
		case "/stream/movie/12.json", "/stream/movie/movie-1.json":
			fmt.Fprint(w, `{"streams":[{"name":"720p","url":"https://cdn.example/good.m3u8"},{"name":"480p","url":"https://cdn.example/blocked.m3u8"},{"name":"360p","url":"https://cdn.example/slow.m3u8"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json", tmdb.NewKeyPool(nil), testStreamClient(server))
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}
	handle := client.Name() + "/movie/movie-1"
	tests := []struct {
		name    string
		resolve func(context.Context) ([]provider.MediaSource, error)
	}{
		{
			name: "ResolveSource by TMDB id",
			resolve: func(ctx context.Context) ([]provider.MediaSource, error) {
				return client.ResolveSource(ctx, "12", provider.Episode{TMDBID: 12})
			},
		},
		{
			name: "ResolveSource by addon handle",
			resolve: func(ctx context.Context) ([]provider.MediaSource, error) {
				return client.ResolveSource(ctx, handle, provider.Episode{ID: handle})
			},
		},
		{
			name: "resolveAddonStreams",
			resolve: func(ctx context.Context) ([]provider.MediaSource, error) {
				return client.resolveAddonStreams(ctx, "movie", "movie-1")
			},
		},
	}
	wantURLs := []string{"https://cdn.example/good.m3u8", "https://cdn.example/slow.m3u8"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sources, err := tt.resolve(context.Background())
			if err != nil {
				t.Fatalf("resolve error = %v", err)
			}
			if len(sources) != len(wantURLs) {
				t.Fatalf("resolved %d sources, want %d: %+v", len(sources), len(wantURLs), sources)
			}
			for i, want := range wantURLs {
				if sources[i].URL != want {
					t.Errorf("source %d URL = %q, want %q", i, sources[i].URL, want)
				}
			}
		})
	}
}

// timeoutError mimics a body-read deadline, which http does not wrap in a
// url.Error. It must stay inconclusive evidence rather than dropping a lane.
type timeoutError struct{}

func (timeoutError) Error() string   { return "context deadline exceeded while reading body" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestDefiniteProbeFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"403 is definite", &provider.HTTPError{Code: http.StatusForbidden}, true},
		{"401 is definite", &provider.HTTPError{Code: http.StatusUnauthorized}, true},
		{"404 is definite", &provider.HTTPError{Code: http.StatusNotFound}, true},
		{"429 is soft", &provider.HTTPError{Code: http.StatusTooManyRequests}, false},
		{"503 is soft", &provider.HTTPError{Code: http.StatusServiceUnavailable}, false},
		{"wrapped deadline is soft", fmt.Errorf("verify live stream: %w", &url.Error{Op: "Get", Err: context.DeadlineExceeded}), false},
		{"body read timeout is soft", fmt.Errorf("verify live stream: read response: %w", timeoutError{}), false},
		{"connection reset is soft", &url.Error{Op: "Get", Err: errors.New("connection reset by peer")}, false},
		{"tls is definite", &url.Error{Op: "Get", Err: errors.New("tls: bad certificate")}, true},
		{"invalid content is definite", errors.New("verify live stream: response is not an HLS playlist"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := definiteProbeFailure(tc.err); got != tc.want {
				t.Fatalf("definiteProbeFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

type countingProbeTransport struct {
	probes  atomic.Int32
	vodSeen atomic.Int32
}

func (t *countingProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.probes.Add(1)
	if strings.Contains(req.URL.Path, ".mkv") {
		t.vodSeen.Add(1)
	}
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func TestProbeSourcesSkipsDirectVOD(t *testing.T) {
	transport := &countingProbeTransport{}
	client := &Client{httpClient: &http.Client{Transport: transport}}
	sources := []provider.MediaSource{
		{URL: "https://cdn.example/movie.mkv", Type: provider.SourceTypeMP4},
		{URL: "https://cdn.example/list.m3u8", Type: provider.SourceTypeHLS},
	}
	got := client.probeSources(context.Background(), sources)
	if len(got) != 1 || got[0].URL != sources[0].URL {
		t.Fatalf("probeSources() = %+v, want only the direct VOD source", got)
	}
	if transport.probes.Load() == 0 {
		t.Fatal("the HLS lane was not probed")
	}
	if vod := transport.vodSeen.Load(); vod != 0 {
		t.Fatalf("direct VOD URL was probed %d times, want 0", vod)
	}
}

func TestCatalogAndStreamAuthRequired(t *testing.T) {
	var openCatalog atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"name":"Locked Addon","types":["movie"],"resources":["catalog","stream"],"catalogs":[{"type":"movie","id":"locked"},{"type":"movie","id":"open"}]}`)
		case "/catalog/movie/locked.json":
			http.Error(w, "login required", http.StatusUnauthorized)
		case "/catalog/movie/open.json":
			if !openCatalog.Load() {
				http.Error(w, "login required", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"metas":[{"id":"movie-1","name":"Open Movie"}]}`)
		case "/stream/movie/movie-1.json":
			http.Error(w, "login required", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json?token=secret123", tmdb.NewKeyPool(nil), testStreamClient(server))
	if err != nil {
		t.Fatalf("NewClientWithHTTP() error = %v", err)
	}

	t.Run("one locked catalog does not hide the others", func(t *testing.T) {
		openCatalog.Store(true)
		results, err := client.Search(context.Background(), "", provider.ModeStremio)
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		if len(results) != 1 || results[0].Title != "Open Movie" {
			t.Fatalf("Search() = %+v, want the open catalog result", results)
		}
	})

	t.Run("all catalogs locked report auth required", func(t *testing.T) {
		openCatalog.Store(false)
		_, err := client.Search(context.Background(), "", provider.ModeStremio)
		if !errors.Is(err, provider.ErrAuthRequired) {
			t.Fatalf("Search() error = %v, want ErrAuthRequired", err)
		}
		if strings.Contains(err.Error(), "secret123") {
			t.Fatalf("Search() error leaked the addon token: %v", err)
		}
	})

	t.Run("locked stream endpoint reports auth required", func(t *testing.T) {
		_, err := client.resolveAddonStreams(context.Background(), "movie", "movie-1")
		if !errors.Is(err, provider.ErrAuthRequired) {
			t.Fatalf("resolveAddonStreams() error = %v, want ErrAuthRequired", err)
		}
	})
}
