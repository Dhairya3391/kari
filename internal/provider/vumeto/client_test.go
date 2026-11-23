package vumeto

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
)

// testEndpoints are the production origins; the routed client sends every
// request to the test server while preserving those host names for parsing.
var testEndpoints = Endpoints{
	Site:   "https://vumeto.live",
	Player: "https://player.vumeto.live",
}

// routedClient sends all HTTP traffic to the test server so production host
// names can be exercised offline.
func routedClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	address := strings.TrimPrefix(server.URL, "https://")
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, address)
			},
		},
	}
}

// newTestClient builds a Vumeto client whose HTTP traffic goes to server.
func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClientWithEndpoints(testEndpoints)
	if err != nil {
		t.Fatalf("NewClientWithEndpoints: %v", err)
	}
	client.http = routedClient(t, server)
	return client
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestClientCapabilities(t *testing.T) {
	client, err := NewClientWithEndpoints(testEndpoints)
	if err != nil {
		t.Fatalf("NewClientWithEndpoints: %v", err)
	}
	if client.Name() != "vumeto" || client.Alias() != "Vumeto" {
		t.Fatalf("identity = %q / %q", client.Name(), client.Alias())
	}
	modes := client.Modes()
	if len(modes) != 3 {
		t.Fatalf("modes = %d, want 3", len(modes))
	}
	for _, mode := range modes {
		if mode.Priority <= 0 {
			t.Errorf("mode %s has non-positive priority", mode.Name)
		}
	}
	if _, err := NewClientWithEndpoints(Endpoints{}); err == nil {
		t.Error("empty endpoints should fail construction")
	}
}

func TestSearchFiltersByMode(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("q"); got != "Frieren" {
			t.Errorf("query = %q", got)
		}
		writeJSON(w, `{"items":[
			{"id":1,"kind":"movies","type":"movie","title":" Frieren ","year":"2023","poster":"https://image.tmdb.org/p.jpg","genres":["Animation"]},
			{"id":2,"kind":"series","type":"tv","title":"Frieren Series"},
			{"id":3,"kind":"cartoons","type":"tv","title":"Frieren Cartoon"}
		]}`)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	results, err := client.Search(context.Background(), "Frieren", provider.ModeMovies)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	got := results[0]
	if got.ID != "1" || got.TMDBID != 1 || got.Title != "Frieren" || got.MediaType != provider.MediaTypeMovie || got.Type != provider.ModeMovies {
		t.Errorf("result = %+v", got)
	}
	if got.CoverURL != "https://image.tmdb.org/p.jpg" || len(got.Genres) != 1 {
		t.Errorf("metadata = %+v", got)
	}
	if _, err := client.Search(context.Background(), "  ", provider.ModeMovies); err == nil {
		t.Error("empty query should fail")
	}
	tv, err := client.Search(context.Background(), "Frieren", provider.ModeTV)
	if err != nil || len(tv) != 1 || tv[0].MediaType != provider.MediaTypeTV {
		t.Errorf("tv = %+v, err = %v", tv, err)
	}
	cartoons, err := client.Search(context.Background(), "Frieren", provider.ModeCartoon)
	if err != nil || len(cartoons) != 1 || cartoons[0].Type != provider.ModeCartoon {
		t.Errorf("cartoons = %+v, err = %v", cartoons, err)
	}
}

func TestFetchEpisodes(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/title":
			writeJSON(w, `{"item":{"id":10,"seasons":[{"number":1,"count":2},{"number":2,"count":1}]}}`)
		case "/api/season":
			if r.URL.Query().Get("season") == "1" {
				writeJSON(w, `{"episodes":[{"number":1,"title":"Pilot"},{"number":2,"title":"Second"}]}`)
				return
			}
			writeJSON(w, `{"episodes":[{"number":1,"title":"Final"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	movie, err := client.FetchEpisodes(context.Background(), provider.SearchResult{ID: "10", MediaType: provider.MediaTypeMovie, Title: "Film"})
	if err != nil || len(movie) != 1 || movie[0].Episode != 0 {
		t.Fatalf("movie episodes = %+v, err = %v", movie, err)
	}
	tv, err := client.FetchEpisodes(context.Background(), provider.SearchResult{ID: "10", MediaType: provider.MediaTypeTV})
	if err != nil || len(tv) != 3 || tv[2].Season != 2 || tv[2].Episode != 1 {
		t.Fatalf("tv episodes = %+v, err = %v", tv, err)
	}
	if _, err := client.FetchEpisodes(context.Background(), provider.SearchResult{ID: "bad"}); !errors.Is(err, provider.ErrNoEpisodes) {
		t.Errorf("invalid id error = %v", err)
	}
}

func TestResolvePlayerKeepsOnlyDirectSources(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed/movie/55":
			_, _ = w.Write([]byte(`<html><head><meta name="vumeto-session" content="fixture-session"></head></html>`))
		case "/_player/resolve":
			if r.Header.Get("X-Vumeto-Session") != "fixture-session" {
				t.Errorf("session header = %q", r.Header.Get("X-Vumeto-Session"))
			}
			if r.URL.Query().Get("mode") != "all" || r.URL.Query().Get("type") != "movie" {
				t.Errorf("resolve query = %q", r.URL.RawQuery)
			}
			writeJSON(w, `{"sources":[
				{"url":"/media/relay/index.m3u8","type":"application/x-mpegurl","provider":"uwu"},
				{"url":"https://edge.example.com/movie/index.m3u8","type":"application/x-mpegurl","quality":"2160p","provider":"stream-library","subtitles":[{"url":"https://edge.example.com/en.vtt","lang":"en"}]},
				{"url":"https://edge.example.com/movie/720.mp4","type":"video/mp4","label":"720p","provider":"streamm4u"}
			]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	sources, err := client.ResolveSource(context.Background(), "55", provider.Episode{TMDBID: 55})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}
	if sources[0].Backend != "Astra" || sources[0].Type != provider.SourceTypeHLS || sources[0].Height != 2160 {
		t.Errorf("first source = %+v", sources[0])
	}
	if len(sources[0].Subtitles) != 1 || sources[0].Subtitles[0].Language != "en" {
		t.Errorf("subtitles = %+v", sources[0].Subtitles)
	}
	if sources[1].Type != provider.SourceTypeMP4 || sources[1].Quality != "720p" {
		t.Errorf("second source = %+v", sources[1])
	}
}

func TestResolvePlayerNormalizesSeasonlessEpisode(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed/tv/10/1/3":
			_, _ = w.Write([]byte(`<meta name="vumeto-session" content="s"></meta>`))
		case "/_player/resolve":
			q := r.URL.Query()
			if q.Get("season") != "1" || q.Get("episode") != "3" {
				t.Errorf("resolve query = %q, want normalized season 1 episode 3", r.URL.RawQuery)
			}
			writeJSON(w, `{"sources":[{"url":"https://edge.example.com/tv/master.m3u8","provider":"stream-library"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	sources, err := client.ResolveSource(context.Background(), "10", provider.Episode{TMDBID: 10, Season: 0, Episode: 3})
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources = %+v, err = %v", sources, err)
	}
}

func TestResolvePlayerTVCoordinates(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed/tv/10/2/3":
			_, _ = w.Write([]byte(`<meta name="vumeto-session" content="s"></meta>`))
		case "/_player/resolve":
			q := r.URL.Query()
			if q.Get("type") != provider.MediaTypeTV || q.Get("season") != "2" || q.Get("episode") != "3" {
				t.Errorf("resolve query = %q", r.URL.RawQuery)
			}
			writeJSON(w, `{"sources":[{"url":"https://edge.example.com/tv/master.m3u8","type":"application/x-mpegurl","provider":"stream-library"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	sources, err := client.ResolveSource(context.Background(), "10", provider.Episode{TMDBID: 10, Season: 2, Episode: 3})
	if err != nil || len(sources) != 1 || sources[0].URL != "https://edge.example.com/tv/master.m3u8" {
		t.Fatalf("sources = %+v, err = %v", sources, err)
	}
}
