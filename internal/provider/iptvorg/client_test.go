package iptvorg

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"kari/internal/provider"
)

// catalogFixture is the set of documents served by the test handler.
type catalogFixture struct {
	channels   []iptvChannel
	streams    []iptvStream
	categories []iptvCategory
	countries  []iptvCountry
}

// testFixture mirrors the shapes IPTV-org publishes.
func testFixture() catalogFixture {
	return catalogFixture{
		channels: []iptvChannel{
			{
				ID: "cn.in", Name: "Cartoon Network", AltNames: []string{"CN"},
				Network: "Turner", Country: "IN", Categories: []string{"kids"},
			},
			{
				ID: "bbc.uk", Name: "BBC News", Country: "GB", Categories: []string{"news"},
			},
			{ID: "closed.us", Name: "Closed Channel", Country: "US", Closed: "2024-01-01"},
			{ID: "adult.us", Name: "Adult Channel", Country: "US", IsNSFW: true},
			{ID: "local.us", Name: "Local Only", Country: "US"},
			{ID: "nostream.us", Name: "No Stream", Country: "US"},
		},
		streams: []iptvStream{
			{Channel: "cn.in", URL: "https://cdn.example.com/cartoon/master.m3u8", Quality: "720p"},
			{Channel: "cn.in", URL: "https://cdn.example.com/cartoon/master.m3u8"},
			{Channel: "cn.in", URL: "https://cdn.example.com/cartoon/backup.mp4", Quality: "480p"},
			{Channel: "bbc.uk", URL: "https://news.example.com/bbc/master.m3u8", Quality: "1080p",
				Referrer: "https://news.example.com/", UserAgent: "NewsAgent/1.0"},
			{Channel: "closed.us", URL: "https://closed.example.com/live.m3u8"},
			{Channel: "adult.us", URL: "https://adult.example.com/live.m3u8"},
			{Channel: "local.us", URL: "http://localhost/live.m3u8"},
			{Channel: "", URL: "https://orphan.example.com/live.m3u8"},
		},
		categories: []iptvCategory{{ID: "kids", Name: "Kids"}, {ID: "news", Name: "News"}},
		countries: []iptvCountry{
			{Name: "India", Code: "IN", Languages: []string{"hin", "tam", "eng"}},
			{Name: "United Kingdom", Code: "GB", Languages: []string{"eng"}},
			{Name: "United States", Code: "US", Languages: []string{"eng"}},
		},
	}
}

// catalogServer serves the fixture JSON and counts requests.
func catalogServer(t *testing.T, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	fixture := testFixture()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var payload any
		switch r.URL.Path {
		case "/channels.json":
			payload = fixture.channels
		case "/streams.json":
			payload = fixture.streams
		case "/categories.json":
			payload = fixture.categories
		case "/countries.json":
			payload = fixture.countries
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Fatalf("encode fixture: %v", err)
		}
	}))
}

// newTestClient builds a client pointed at a fixture server.
func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := newClient(endpoints{
		channels:   server.URL + "/channels.json",
		streams:    server.URL + "/streams.json",
		categories: server.URL + "/categories.json",
		countries:  server.URL + "/countries.json",
	}, server.Client())
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	return client
}

func TestNewClientRejectsInvalidEndpoint(t *testing.T) {
	if _, err := newClient(endpoints{channels: "not-a-url"}, nil); err == nil {
		t.Fatal("expected invalid endpoint error")
	}
}

func TestSearchEmptyQueryReturnsPlayableCatalog(t *testing.T) {
	var requests atomic.Int32
	server := catalogServer(t, &requests)
	defer server.Close()
	client := newTestClient(t, server)

	results, err := client.Search(context.Background(), "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(results), results)
	}
	if results[0].Title != "Cartoon Network" || results[1].Title != "BBC News" {
		t.Errorf("country ordering = %q, %q", results[0].Title, results[1].Title)
	}
	if !results[0].Live || results[0].Type != provider.ModeLive || results[0].MediaType != provider.MediaTypeLive {
		t.Errorf("live fields not set: %+v", results[0])
	}
	if results[0].Group != "Channel" {
		t.Errorf("Group = %q, want Channel", results[0].Group)
	}
	if len(results[0].Genres) == 0 || results[0].Genres[0] != "Kids" {
		t.Errorf("Genres = %v, want category first", results[0].Genres)
	}
	// Four documents fetched in one catalog load; the second search is cached.
	if got := requests.Load(); got != 4 {
		t.Errorf("requests = %d, want 4", got)
	}
	if _, err := client.Search(context.Background(), "cartoon", provider.ModeLive); err != nil {
		t.Fatalf("cached Search: %v", err)
	}
	if got := requests.Load(); got != 4 {
		t.Errorf("cached search issued extra requests: %d", got)
	}
}

func TestSearchMatchesChannelMetadata(t *testing.T) {
	var requests atomic.Int32
	server := catalogServer(t, &requests)
	defer server.Close()
	client := newTestClient(t, server)

	cases := []struct {
		query string
		want  []string
	}{
		{"cartoon", []string{"Cartoon Network"}},
		{"cn", []string{"Cartoon Network"}},
		{"turner", []string{"Cartoon Network"}},
		{"india", []string{"Cartoon Network"}},
		{"in", []string{"Cartoon Network"}},
		{"hindi", []string{"Cartoon Network"}},
		{"kids", []string{"Cartoon Network"}},
		{"news", []string{"BBC News"}},
		{"gb", []string{"BBC News"}},
		{"english", []string{"Cartoon Network", "BBC News"}},
	}
	for _, tc := range cases {
		results, err := client.Search(context.Background(), tc.query, provider.ModeLive)
		if err != nil {
			t.Fatalf("Search(%q): %v", tc.query, err)
		}
		if len(results) != len(tc.want) {
			t.Fatalf("Search(%q) = %d results, want %d: %+v", tc.query, len(results), len(tc.want), results)
		}
		for i, want := range tc.want {
			if results[i].Title != want {
				t.Errorf("Search(%q)[%d].Title = %q, want %q", tc.query, i, results[i].Title, want)
			}
		}
	}
	if _, err := client.Search(context.Background(), "nonexistent", provider.ModeLive); !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("Search(nonexistent) err = %v, want ErrNoResults", err)
	}
}

func TestSearchWrongMode(t *testing.T) {
	var requests atomic.Int32
	server := catalogServer(t, &requests)
	defer server.Close()
	client := newTestClient(t, server)

	if _, err := client.Search(context.Background(), "", provider.ModeAnime); !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("err = %v, want ErrNoResults", err)
	}
}

func TestResolveSource(t *testing.T) {
	var requests atomic.Int32
	server := catalogServer(t, &requests)
	defer server.Close()
	client := newTestClient(t, server)

	sources, err := client.ResolveSource(context.Background(), "iptvorg:cn.in", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2 (duplicates removed): %+v", len(sources), sources)
	}
	if sources[0].URL != "https://cdn.example.com/cartoon/master.m3u8" || sources[0].Type != provider.SourceTypeHLS {
		t.Errorf("first source = %+v", sources[0])
	}
	if sources[0].Quality != "720p" {
		t.Errorf("quality = %q, want 720p", sources[0].Quality)
	}
	if sources[0].AudioLabel != "Hindi · Tamil · English" {
		t.Errorf("audio label = %q, want country languages", sources[0].AudioLabel)
	}
	if sources[0].Language != "" {
		t.Errorf("Language = %q, want empty (multi-language live source)", sources[0].Language)
	}
	if sources[1].Type != provider.SourceTypeMP4 || sources[1].Quality != "480p" {
		t.Errorf("second source = %+v", sources[1])
	}

	// Bare handles and episode IDs both resolve for history compatibility.
	sources, err = client.ResolveSource(context.Background(), "bbc.uk", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource(bare): %v", err)
	}
	if len(sources) != 1 || sources[0].Referer != "https://news.example.com/" || sources[0].UserAgent != "NewsAgent/1.0" {
		t.Errorf("transport headers lost: %+v", sources)
	}

	sources, err = client.ResolveSource(context.Background(), "", provider.Episode{ID: "iptvorg:cn.in"})
	if err != nil || len(sources) != 2 {
		t.Errorf("episode handle resolve = %v, %d sources", err, len(sources))
	}

	if _, err := client.ResolveSource(context.Background(), "iptvorg:missing", provider.Episode{}); !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("missing channel err = %v, want ErrNoSources", err)
	}
	if _, err := client.ResolveSource(context.Background(), "", provider.Episode{}); !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("empty handle err = %v, want ErrNoSources", err)
	}
}

func TestBuildCatalogSkipsEmptyDocuments(t *testing.T) {
	if _, err := buildCatalog(nil, nil, nil, nil); !errors.Is(err, provider.ErrUpstreamChanged) {
		t.Errorf("err = %v, want ErrUpstreamChanged", err)
	}
}
