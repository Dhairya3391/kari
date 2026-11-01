package famelack

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"kari/internal/provider"
)

// testChannel mirrors the catalog JSON Famelack publishes.
type testChannel struct {
	Nanoid    string              `json:"nanoid"`
	Name      string              `json:"name"`
	Sources   map[string][]string `json:"sources"`
	Languages []string            `json:"languages"`
	Country   string              `json:"country"`
}

// testChannels returns the fixture catalog served by the test handler.
func testChannels() []testChannel {
	return []testChannel{
		{
			Nanoid: "TAW3V6RUdxKtJQ",
			Name:   "Cartoon Network HD+",
			Sources: map[string][]string{
				"streams": {
					"https://drk6xq0vhn.gpcdn.net/live/cn_hd_abr/index.m3u8",
					"https://drk6xq0vhn.gpcdn.net/live/cn_hd_abr/index.m3u8",
					"https://backup.example.com/cn/playlist.m3u8",
				},
			},
			Languages: []string{"hin"},
			Country:   "in",
		},
		{
			Nanoid: "bbcNews",
			Name:   "BBC News",
			Sources: map[string][]string{
				"streams": {"https://news.example.com/bbc/master.m3u8"},
			},
			Languages: []string{"eng"},
			Country:   "gb",
		},
		{
			Nanoid: "youtubeOnly",
			Name:   "YouTube Only",
			Sources: map[string][]string{
				"youtube": {"https://www.youtube-nocookie.com/embed/abc"},
			},
			Country: "us",
		},
		{
			Nanoid: "localOnly",
			Name:   "Local Only",
			Sources: map[string][]string{
				"streams": {"http://localhost/live/index.m3u8"},
			},
			Country: "us",
		},
		{
			Nanoid: "noName",
			Sources: map[string][]string{
				"streams": {"https://example.com/no-name/index.m3u8"},
			},
			Country: "us",
		},
	}
}

// testCountries returns fixture country codes mapped to display names.
func testCountries() map[string]countryMetadata {
	return map[string]countryMetadata{
		"IN": {Country: "India"},
		"GB": {Country: "United Kingdom"},
		"US": {Country: "United States"},
	}
}

// gzipJSON compresses v the same way Famelack publishes its catalogs.
func gzipJSON(t *testing.T, v any) []byte {
	t.Helper()
	payload, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("compress fixture: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close fixture gzip: %v", err)
	}
	return compressed.Bytes()
}

// catalogHandler serves the gzip-compressed channel and country catalogs and
// counts requests so cache behavior can be asserted.
func catalogHandler(t *testing.T, requests *atomic.Int32) http.HandlerFunc {
	t.Helper()
	channels := gzipJSON(t, testChannels())
	countries := gzipJSON(t, testCountries())
	return func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/gzip")
		if r.URL.Path == "/countries.json" {
			_, _ = w.Write(countries)
			return
		}
		_, _ = w.Write(channels)
	}
}

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClientWithURLs(server.URL+"/all.json", server.URL+"/countries.json", server.Client())
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}
	return client
}

func TestFamelackMetadata(t *testing.T) {
	client, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.Name() != "famelack" {
		t.Errorf("Name = %q, want famelack", client.Name())
	}
	if client.Alias() != "Famelack" {
		t.Errorf("Alias = %q, want Famelack", client.Alias())
	}
	modes := client.Modes()
	if len(modes) != 1 || modes[0].Name != provider.ModeLive {
		t.Fatalf("Modes = %+v, want live", modes)
	}
	features := client.Features(provider.ModeLive)
	if !features.AllowEmptyQuery || !features.NoCachedSearches {
		t.Errorf("live features = %+v, want empty-query browse and uncached searches", features)
	}
	if client.Features(provider.ModeTV) != (provider.Features{}) {
		t.Errorf("non-live features should be zero")
	}
}

func TestFamelackSearchBrowsesAndFilters(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, catalogHandler(t, &requests))

	all, err := client.Search(context.Background(), "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search(empty): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Search(empty) returned %d results, want 2 playable channels", len(all))
	}
	if all[0].Title != "Cartoon Network HD+" || all[0].Group != "Channel" || len(all[0].Genres) == 0 || all[0].Genres[0] != "India" {
		t.Errorf("first result = %+v, want the Indian channel", all[0])
	}
	if all[1].Title != "BBC News" || all[1].Group != "Channel" || len(all[1].Genres) == 0 || all[1].Genres[0] != "United Kingdom" {
		t.Errorf("second result = %+v, want the UK channel", all[1])
	}
	if !all[0].Live || all[0].Type != provider.ModeLive || all[0].MediaType != provider.MediaTypeLive {
		t.Errorf("live result metadata = %+v", all[0])
	}
	if all[0].ID != "famelack:TAW3V6RUdxKtJQ" {
		t.Errorf("ID = %q, want prefixed nanoid", all[0].ID)
	}

	tests := []struct {
		query string
		want  string
	}{
		{query: "cartoon", want: "Cartoon Network HD+"},
		{query: "india", want: "Cartoon Network HD+"},
		{query: "in", want: "Cartoon Network HD+"},
		{query: "hindi", want: "Cartoon Network HD+"},
		{query: "gb", want: "BBC News"},
		{query: "united kingdom", want: "BBC News"},
	}
	for _, tt := range tests {
		results, err := client.Search(context.Background(), tt.query, provider.ModeLive)
		if err != nil {
			t.Fatalf("Search(%q): %v", tt.query, err)
		}
		if len(results) != 1 || results[0].Title != tt.want {
			t.Errorf("Search(%q) = %+v, want %q", tt.query, results, tt.want)
		}
	}

	if _, err := client.Search(context.Background(), "nonexistent", provider.ModeLive); !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("Search(nonexistent) err = %v, want ErrNoResults", err)
	}
	if _, err := client.Search(context.Background(), "cartoon", provider.ModeTV); !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("Search(ModeTV) err = %v, want ErrNoResults", err)
	}
}

func TestFamelackSearchCachesCatalog(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, catalogHandler(t, &requests))

	if _, err := client.Search(context.Background(), "cartoon", provider.ModeLive); err != nil {
		t.Fatalf("first search: %v", err)
	}
	if _, err := client.Search(context.Background(), "bbc", provider.ModeLive); err != nil {
		t.Fatalf("second search: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("catalog requests = %d, want 2 (channels + countries)", got)
	}

	// Expire the cache while the upstream fails; the previous catalog
	// must keep the live tab usable.
	client.mu.Lock()
	client.fetchedAt = time.Now().Add(-directoryTTL)
	client.mu.Unlock()

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "upstream down", http.StatusInternalServerError)
	}))
	defer failing.Close()
	client.channelsURL = failing.URL + "/all.json"

	results, err := client.Search(context.Background(), "cartoon", provider.ModeLive)
	if err != nil {
		t.Fatalf("search with stale cache: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Cartoon Network HD+" {
		t.Errorf("stale cache results = %+v", results)
	}
}

func TestFamelackCountryMetadataFallback(t *testing.T) {
	channels := gzipJSON(t, testChannels())
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		if r.URL.Path == "/countries.json" {
			http.Error(w, "metadata unavailable", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(channels)
	}))

	results, err := client.Search(context.Background(), "cartoon", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results[0].Genres) == 0 || results[0].Genres[0] != "IN" {
		t.Errorf("Genres = %v, want uppercase country code fallback", results[0].Genres)
	}
}

func TestFamelackEpisodesAndResolve(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, catalogHandler(t, &requests))

	results, err := client.Search(context.Background(), "cartoon", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	episodes, err := client.FetchEpisodes(context.Background(), results[0])
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(episodes) != 1 || episodes[0].ID != results[0].ID {
		t.Fatalf("FetchEpisodes = %+v", episodes)
	}

	sources, err := client.ResolveSource(context.Background(), results[0].ID, episodes[0])
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("ResolveSource returned %d sources, want 2 deduplicated streams", len(sources))
	}
	for _, source := range sources {
		if source.Type != provider.SourceTypeHLS {
			t.Errorf("source type = %q, want HLS", source.Type)
		}
		if source.Quality != "Auto" {
			t.Errorf("quality = %q, want Auto", source.Quality)
		}
	}

	// The episode handle is the fallback when the media ID is empty.
	if _, err := client.ResolveSource(context.Background(), "", provider.Episode{ID: results[0].ID}); err != nil {
		t.Errorf("ResolveSource via episode handle: %v", err)
	}

	if _, err := client.ResolveSource(context.Background(), "famelack:missing", provider.Episode{}); !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("unknown channel err = %v, want ErrNoSources", err)
	}
	if _, err := client.ResolveSource(context.Background(), "", provider.Episode{}); !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("empty handle err = %v, want ErrNoSources", err)
	}
	if _, err := client.FetchEpisodes(context.Background(), provider.SearchResult{}); !errors.Is(err, provider.ErrNoEpisodes) {
		t.Errorf("empty series err = %v, want ErrNoEpisodes", err)
	}
}

func TestFamelackCatalogErrors(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusNotFound)
		}))
		if _, err := client.Search(context.Background(), "", provider.ModeLive); err == nil {
			t.Fatal("expected an HTTP error")
		}
	})

	t.Run("invalid gzip", func(t *testing.T) {
		client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not gzip"))
		}))
		if _, err := client.Search(context.Background(), "", provider.ModeLive); err == nil {
			t.Fatal("expected a gzip error")
		}
	})
}
