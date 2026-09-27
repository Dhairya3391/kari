package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
	"kari/internal/provider/providertest"
)

// fakeLibrary serves the library listing plus episode items.
func fakeLibrary(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/Items", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("parentId") != "" {
			w.Write([]byte(`{"Items":[
				{"Id":"ep1","Name":"Pilot","ParentIndexNumber":1,"IndexNumber":1,"Type":"Episode"},
				{"Id":"ep2","Name":"Second","ParentIndexNumber":1,"IndexNumber":2,"Type":"Episode"}
			]}`))
			return
		}
		w.Write([]byte(`{"Items":[
			{"Id":"series1","Name":"The Boys","Type":"Series","ProductionYear":2019},
			{"Id":"movie1","Name":"Inception","Type":"Movie","ProductionYear":2010}
		]}`))
	})
	return httptest.NewServer(mux)
}

// TestContractAgainstFakeServer runs the shared contract suite against a
// fake Jellyfin API: Search → Episodes → synthetic stream URL. Live runs
// happen only when JELLYFIN_URL/JELLYFIN_API_KEY are set.
func TestContractAgainstFakeServer(t *testing.T) {
	srv := fakeLibrary(t)
	defer srv.Close()

	client, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	series := provider.SearchResult{Title: "The Boys", ID: "series1", Type: provider.ModeJellyfin, MediaType: provider.MediaTypeTV}
	episode := provider.Episode{Title: "Pilot", ID: "ep1", Season: 1, Episode: 1}
	providertest.Run(t, providertest.Spec{
		Mode:    provider.ModeJellyfin,
		Kinds:   []provider.ContentType{provider.ModeJellyfin},
		Series:  series,
		Episode: episode,
		MediaID: "series1",
		Search: func(ctx context.Context) ([]provider.SearchResult, error) {
			return client.Search(ctx, "boys", provider.ModeJellyfin)
		},
		Items: func(ctx context.Context, s provider.SearchResult) ([]provider.Episode, error) {
			return client.FetchEpisodes(ctx, s)
		},
		Sources: func(ctx context.Context, mediaID string, e provider.Episode) ([]provider.MediaSource, error) {
			return client.ResolveSource(ctx, mediaID, e)
		},
	})
}

// TestAuthRequired proves a missing/invalid key surfaces typed auth,
// never silent empties.
func TestAuthRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "bad-key")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// Library fetch fails (401) so search falls back to hints, which
	// also 401s: the typed HTTP error must surface.
	_, err = client.Search(context.Background(), "boys", provider.ModeJellyfin)
	if err == nil {
		t.Fatal("want an error for 401, got nil")
	}
	var httpErr *provider.HTTPError
	if ok := errors.As(err, &httpErr); !ok || httpErr.Code != http.StatusUnauthorized {
		t.Errorf("want typed 401, got %v", err)
	}
}

// TestCapabilities proves the static contract surface.
func TestCapabilities(t *testing.T) {
	client, err := NewClient("https://jellyfin.example.com", "k")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.Alias() == "" || client.Name() != "jellyfin" {
		t.Errorf("identity wrong: %q %q", client.Alias(), client.Name())
	}
	if len(client.Modes()) != 1 || client.Modes()[0].Name != provider.ModeJellyfin {
		t.Errorf("modes = %+v", client.Modes())
	}
	f := client.Features(provider.ModeJellyfin)
	if !f.AllowEmptyQuery || !f.NoCachedSearches || f.SearchPlaceholder == "" {
		t.Errorf("features = %+v", f)
	}
	if got := client.Features(provider.ModeMovies); got.AllowEmptyQuery {
		t.Errorf("non-jellyfin features = %+v", got)
	}
}

// TestHintsFallback proves a dead library listing falls back to
// server-side hints (movies + series kept, episodes and empty IDs cut).
func TestHintsFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/Items", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/Search/Hints", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"SearchHints":[
			{"Id":"m1","Name":"Inception","Type":"Movie","ProductionYear":2010},
			{"Id":"s1","Name":"The Boys","Type":"Series","Series":"The Boys","SeriesId":"series9"},
			{"Id":"e9","Name":"Pilot","Type":"Episode","Series":"The Boys","SeriesId":"series9"},
			{"Id":"","Name":"Ghost","Type":"Movie"},
			{"Id":"x1","Name":"Weird","Type":"Audio"}
		]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	results, err := client.Search(context.Background(), "inception", provider.ModeJellyfin)
	if err != nil {
		t.Fatalf("hints fallback: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want movie + series rows, got %+v", results)
	}
	if results[0].MediaType != provider.MediaTypeMovie || results[0].Year != "2010" {
		t.Errorf("movie row = %+v", results[0])
	}
	if results[1].Title != "The Boys" || results[1].ID != "series9" || results[1].MediaType != provider.MediaTypeTV {
		t.Errorf("series row = %+v", results[1])
	}
}

// TestMatchScoreRanksExactFirst proves the library ranker prefers exact
// and prefix matches over loose subsequences.
func TestMatchScoreRanksExactFirst(t *testing.T) {
	lib := []provider.SearchResult{
		{Title: "The Boys", ID: "1", Type: provider.ModeJellyfin},
		{Title: "Boys", ID: "2", Type: provider.ModeJellyfin},
		{Title: "Bayside Tales", ID: "3", Type: provider.ModeJellyfin},
	}
	ranked := rankLibrary(lib, "boys")
	if len(ranked) == 0 || ranked[0].ID != "2" {
		t.Errorf("exact match must rank first: %+v", ranked)
	}
	if got := rankLibrary(lib, ""); len(got) != len(lib) {
		t.Errorf("empty query must browse all %d, got %d", len(lib), len(got))
	}
	if got := rankLibrary(lib, "zzz-no-match"); len(got) != 0 {
		t.Errorf("hopeless query must rank nothing, got %+v", got)
	}
}

// TestEmptyQueryBrowsesLibrary proves empty search lists everything.
func TestEmptyQueryBrowsesLibrary(t *testing.T) {
	srv := fakeLibrary(t)
	defer srv.Close()

	client, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	results, err := client.Search(context.Background(), "", provider.ModeJellyfin)
	if err != nil {
		t.Fatalf("empty search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("want 2 library rows, got %+v", results)
	}
	if !strings.Contains(results[0].Title, "Boys") && !strings.Contains(results[1].Title, "Boys") {
		t.Errorf("library must contain The Boys: %+v", results)
	}
}
