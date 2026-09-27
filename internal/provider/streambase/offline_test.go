package streambase

// Offline fixtures for the TMDB-backed base: a host-intercepting
// RoundTripper serves canned TMDB/meilisearch payloads so search,
// cartoon filtering, episode listings, key rotation and error mapping
// all run hermetically.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

// hostFake routes requests by host+path to canned bodies.
type hostFake struct {
	t      *testing.T
	routes map[string]func() (int, string)
}

func (f *hostFake) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.URL.Host + req.URL.Path
	if h, ok := f.routes[key]; ok {
		code, body := h()
		return &http.Response{
			StatusCode: code,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte(body))),
			Request:    req,
		}, nil
	}
	f.t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
	return nil, nil
}

// testBase builds a Base over canned TMDB + meilisearch payloads.
func testBase(t *testing.T) *Base {
	t.Helper()
	fake := &hostFake{t: t, routes: map[string]func() (int, string){
		"api.themoviedb.org/3/search/multi": func() (int, string) {
			return 200, `{"page":1,"total_pages":1,"total_results":4,"results":[
				{"id":11836,"title":"The SpongeBob SquarePants Movie","media_type":"movie","release_date":"2004-11-19","genre_ids":[16,35]},
				{"id":60625,"name":"Rick and Morty","media_type":"tv","first_air_date":"2013-12-02","genre_ids":[16,35]},
				{"id":999,"name":"Some Actor","media_type":"person","genre_ids":[]},
				{"id":1000,"title":"Action Flick","media_type":"movie","release_date":"2020-01-01","genre_ids":[28]}
			]}`
		},
		"api.themoviedb.org/3/tv/1396": func() (int, string) {
			return 200, `{"id":1396,"name":"Breaking Bad","number_of_seasons":2,"seasons":[{"season_number":0},{"season_number":1},{"season_number":2}]}`
		},
		"api.themoviedb.org/3/tv/1396/season/1": func() (int, string) {
			return 200, `{"season_number":1,"episodes":[
				{"episode_number":1,"season_number":1,"name":"Pilot"},
				{"episode_number":2,"season_number":1,"name":""}
			]}`
		},
		"api.themoviedb.org/3/tv/1396/season/2": func() (int, string) {
			return 200, `{"season_number":2,"episodes":[{"episode_number":1,"season_number":2,"name":"Seven Thirty-Seven"}]}`
		},
		"noobsearch.broggl.farm/movies": func() (int, string) {
			return 200, `{"query":"inception","total":1,"results":[
				{"tmdb_id":27205,"title":"Inception","media_type":"tv","year":2010,"original_language":"en"}
			]}`
		},
		"noobsearch.broggl.farm/tv": func() (int, string) {
			return 200, `{"query":"zzz","total":0,"results":[]}`
		},
	}}
	hc := &http.Client{Transport: fake}
	searcher := NewClientWithHTTPClient(&http.Client{Transport: fake})
	b, err := newForTest(tmdb.NewKeyPool([]string{"fake-key"}), hc, searcher)
	if err != nil {
		t.Fatalf("newForTest: %v", err)
	}
	return b
}

// TestCartoonSearchFiltersAnimation proves multi-search filtering:
// animation only, persons and live-action cut, years extracted.
func TestCartoonSearchFiltersAnimation(t *testing.T) {
	b := testBase(t)
	results, err := b.Search(context.Background(), "spongebob", provider.ModeCartoon)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 animation rows, got %+v", results)
	}
	if results[0].MediaType != provider.MediaTypeMovie || results[0].Year != "2004" || results[0].TMDBID != 11836 {
		t.Errorf("movie row = %+v", results[0])
	}
	if results[1].MediaType != provider.MediaTypeTV || results[1].Title != "Rick and Morty" {
		t.Errorf("tv row = %+v", results[1])
	}
	if _, err := b.Search(context.Background(), "  ", provider.ModeCartoon); err == nil {
		t.Error("empty cartoon query must error")
	}
}

// TestMoviesSearchNormalizesMediaType proves meilisearch hits normalize
// to the requested mode.
func TestMoviesSearchNormalizesMediaType(t *testing.T) {
	b := testBase(t)
	results, err := b.Search(context.Background(), "inception", provider.ModeMovies)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].MediaType != provider.MediaTypeMovie || results[0].TMDBID != 27205 {
		t.Errorf("results = %+v", results)
	}
	if _, err := b.Search(context.Background(), "zzz", provider.ModeTV); !isNoResults(err) {
		t.Errorf("empty index must be ErrNoResults, got %v", err)
	}
}

func isNoResults(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no results")
}

// TestFetchEpisodesMovieSynthetic proves movies resolve without network.
func TestFetchEpisodesMovieSynthetic(t *testing.T) {
	b := testBase(t)
	eps, err := b.FetchEpisodes(context.Background(), provider.SearchResult{
		Title: "Inception", ID: "27205", TMDBID: 27205, MediaType: provider.MediaTypeMovie,
	})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 1 || eps[0].TMDBID != 27205 {
		t.Errorf("episodes = %+v", eps)
	}
	if _, err := b.FetchEpisodes(context.Background(), provider.SearchResult{ID: "not-a-number"}); err == nil {
		t.Error("invalid ID must error")
	}
}

// TestFetchEpisodesTVListsSeasons proves the season fan-out orders
// episodes and backfills blank titles.
func TestFetchEpisodesTVListsSeasons(t *testing.T) {
	b := testBase(t)
	eps, err := b.FetchEpisodes(context.Background(), provider.SearchResult{
		Title: "Breaking Bad", ID: "1396", TMDBID: 1396, MediaType: provider.MediaTypeTV,
	})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 3 {
		t.Fatalf("want 3 episodes, got %+v", eps)
	}
	if eps[0].Title != "Pilot" || eps[0].Season != 1 || eps[0].Episode != 1 {
		t.Errorf("ep[0] = %+v", eps[0])
	}
	if eps[1].Title != "Episode 2" {
		t.Errorf("blank title must backfill, got %+v", eps[1])
	}
	if eps[2].Season != 2 {
		t.Errorf("ep[2] = %+v", eps[2])
	}
}

// TestKeyRotationOn401 proves a rejected key rotates and exhausts with
// a typed auth error.
func TestKeyRotationOn401(t *testing.T) {
	fake := &hostFake{t: t, routes: map[string]func() (int, string){
		"api.themoviedb.org/3/tv/1": func() (int, string) { return 401, `{}` },
	}}
	hc := &http.Client{Transport: fake}
	b, err := newForTest(tmdb.NewKeyPool([]string{"bad-key"}),
		hc, NewClientWithHTTPClient(&http.Client{Transport: fake}))
	if err != nil {
		t.Fatalf("newForTest: %v", err)
	}
	_, err = b.FetchEpisodes(context.Background(), provider.SearchResult{ID: "1", TMDBID: 1, MediaType: provider.MediaTypeTV})
	if err == nil {
		t.Fatal("exhausted keys must error")
	}
	if !isAuthError(err) && !strings.Contains(err.Error(), "auth failed") {
		t.Errorf("err = %v, want auth failure", err)
	}
}

// TestFetchTMDBJSON500 proves server errors surface typed, and New
// validates its dependencies.
func TestFetchTMDBJSON500(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Error("nil key pool must error")
	}
	if _, err := newForTest(tmdb.NewKeyPool([]string{"k"}), nil, nil); err == nil {
		t.Error("nil clients must error")
	}
	fake := &hostFake{t: t, routes: map[string]func() (int, string){
		"api.themoviedb.org/3/tv/9": func() (int, string) { return 500, `boom` },
	}}
	hc := &http.Client{Transport: fake}
	b, err := newForTest(tmdb.NewKeyPool([]string{"k"}),
		hc, NewClientWithHTTPClient(&http.Client{Transport: fake}))
	if err != nil {
		t.Fatalf("newForTest: %v", err)
	}
	if _, err := b.fetchTMDBTVDetails(context.Background(), 9); !isHTTP500(err) {
		t.Errorf("err = %v, want typed 500", err)
	}
}

func isHTTP500(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "500")
}
