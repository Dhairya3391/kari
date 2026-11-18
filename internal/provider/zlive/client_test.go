package zlive

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
)

const testCatalog = `[
	{"id":"f1-uk","name":"Sky Sports F1","region":"UK","quality":"FHD","sport":"F1","order":10,"sources":[{"key":"f1-uk","label":"Primary"}]},
	{"id":"espn","name":"ESPN","region":"USA","quality":"HD","sport":"Sports","order":20,"sources":[{"key":"espn","label":"TimStreams"}]},
	{"id":"no-source","name":"Broken","sport":"Sports","sources":[]}
]`

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClientWithURLs(server.URL, "https://zlive.st")
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}
	return client
}

func catalogServer(t *testing.T, body string, status int) *Client {
	t.Helper()
	return newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/channels.json" {
			t.Errorf("unexpected catalog path %q", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestDescriptor(t *testing.T) {
	client := catalogServer(t, testCatalog, http.StatusOK)
	if client.Name() != "zlive" || client.Alias() != "zlive" {
		t.Fatalf("identity = %q / %q", client.Name(), client.Alias())
	}
	modes := client.Modes()
	if len(modes) != 1 || modes[0].Name != provider.ModeLive || modes[0].Priority != 3 {
		t.Fatalf("modes = %+v", modes)
	}
	features := client.Features(provider.ModeLive)
	if !features.AllowEmptyQuery || !features.NoCachedSearches {
		t.Fatalf("features = %+v", features)
	}
	if client.Features(provider.ModeMovies) != (provider.Features{}) {
		t.Fatal("unexpected features for non-live mode")
	}
	if err := client.VerifyLiveStream(context.Background(), provider.ModeMovies, provider.MediaSource{}); err != nil {
		t.Fatalf("VerifyLiveStream on non-live mode: %v", err)
	}
}

func TestSearch(t *testing.T) {
	client := catalogServer(t, testCatalog, http.StatusOK)

	all, err := client.Search(context.Background(), "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search empty: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(all), all)
	}
	if all[0].Title != "Sky Sports F1" || all[0].ID != "zlive:f1-uk" {
		t.Fatalf("first result = %+v", all[0])
	}
	if all[0].Group != "F1" || !all[0].Live || all[0].MediaType != provider.MediaTypeLive {
		t.Fatalf("first result metadata = %+v", all[0])
	}
	if all[1].Group != "Sports" {
		t.Fatalf("second result group = %q", all[1].Group)
	}

	filtered, err := client.Search(context.Background(), "espn", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != "zlive:espn" {
		t.Fatalf("filtered = %+v", filtered)
	}
	if filtered[0].Overview != "" {
		t.Fatalf("unexpected overview %q", filtered[0].Overview)
	}

	if _, err := client.Search(context.Background(), "missing", provider.ModeLive); !errors.Is(err, provider.ErrNoResults) {
		t.Fatalf("missing query error = %v", err)
	}
	if _, err := client.Search(context.Background(), "", provider.ModeMovies); !errors.Is(err, provider.ErrNoResults) {
		t.Fatalf("wrong mode error = %v", err)
	}
}

func TestFetchEpisodes(t *testing.T) {
	client := catalogServer(t, testCatalog, http.StatusOK)
	episodes, err := client.FetchEpisodes(context.Background(), provider.SearchResult{ID: "zlive:f1-uk", Title: "Sky Sports F1"})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(episodes) != 1 || episodes[0].ID != "zlive:f1-uk" {
		t.Fatalf("episodes = %+v", episodes)
	}
	if _, err := client.FetchEpisodes(context.Background(), provider.SearchResult{}); !errors.Is(err, provider.ErrNoEpisodes) {
		t.Fatalf("empty handle error = %v", err)
	}
}

func TestSearchUsesStaleCatalogOnRefreshFailure(t *testing.T) {
	status := http.StatusOK
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(testCatalog))
		}
	}))
	if _, err := client.Search(context.Background(), "", provider.ModeLive); err != nil {
		t.Fatalf("first Search: %v", err)
	}
	status = http.StatusInternalServerError
	client.fetchedAt = client.fetchedAt.Add(-catalogTTL - time.Second)
	if _, err := client.Search(context.Background(), "", provider.ModeLive); err != nil {
		t.Fatalf("stale Search: %v", err)
	}
}

func TestNewClientWithURLsRejectsInvalidURL(t *testing.T) {
	if _, err := NewClientWithURLs("not a url", "https://zlive.st"); err == nil {
		t.Fatal("expected invalid API URL error")
	}
	if _, err := NewClientWithURLs("", "https://zlive.st"); err == nil {
		t.Fatal("expected empty API URL error")
	}
}

func TestChannelMatches(t *testing.T) {
	ch := channel{ID: "f1-uk", Name: "Sky Sports F1", Region: "UK", Sport: "F1", Tagline: "main feed"}
	for _, query := range []string{"sky", "sports", "f1", "uk", "main feed", "f1-uk"} {
		if !channelMatches(ch, strings.ToLower(query)) {
			t.Errorf("channelMatches(%q) = false", query)
		}
	}
	if channelMatches(ch, "nba") {
		t.Error("channelMatches(nba) = true")
	}
}
