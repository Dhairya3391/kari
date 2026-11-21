package q62

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

// fakeSearcher stands in for the shared TMDB/noobsearch base so Search can be
// tested without network access. Results carry TMDB IDs, as the real base does.
type fakeSearcher struct{}

func (fakeSearcher) Search(_ context.Context, _ string, mode provider.ContentType) ([]provider.SearchResult, error) {
	switch mode {
	case provider.ModeMovies:
		return []provider.SearchResult{{Title: "Inception", ID: "27205", TMDBID: 27205, Type: mode, MediaType: provider.MediaTypeMovie, Year: "2010"}}, nil
	case provider.ModeTV:
		return []provider.SearchResult{{Title: "MobLand", ID: "247718", TMDBID: 247718, Type: mode, MediaType: provider.MediaTypeTV}}, nil
	default:
		return nil, provider.ErrNoResults
	}
}

// The proxy base in tests is HTTP and uses a non-local host so generated
// sources survive the direct-URL filter while a custom dialer still lands
// every request on the local mux.
const testProxyBase = "http://proxy.q62.test"

const (
	movieCatalog = `{"content":[{"id":"MOVIE1","tmdbId":27205,"releaseDate":"2010-07-15",` +
		`"translations":{"en":{"title":"Inception","overview":"Dreams within dreams."}},` +
		`"tags":[{"key":"action","name":"Action","localizations":{"en":"Action"}}]}],` +
		`"page":{"totalElements":1,"totalPages":1}}`
	showCatalog = `{"content":[{"id":"SHOW1","tmdbId":247718,"firstAirDate":"2025-03-30",` +
		`"translations":{"en":{"title":"MobLand","overview":"Two families at war."}}}],` +
		`"page":{"totalElements":1,"totalPages":1}}`
	seasonList  = `[{"id":"SEASON1","tvShowId":"SHOW1","seasonNumber":1,"comingSoon":false}]`
	episodeList = `[{"id":"EP1","seasonId":"SEASON1","episodeNumber":1,` +
		`"translations":{"en":{"title":"Pilot"}}},` +
		`{"id":"EP2","seasonId":"SEASON1","episodeNumber":2,"comingSoon":true}]`
	releaseList = `[{"id":"REL1","resolution":"FHD_1080P","ripType":"WEB","audioLocales":["en-US"]}]`
	// Q62's real masters declare an AUDIO group even though every variant is
	// muxed, so the fixture keeps that attribute to guard the codec check.
	masterPlaylist = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,URI="audio.m3u8?token=abc&expires=99",GROUP-ID="audio",LANGUAGE="en",NAME="English",DEFAULT=YES
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.64002a,mp4a.40.2",AUDIO="audio"
1080p.m3u8?token=abc&expires=99
#EXT-X-STREAM-INF:BANDWIDTH=1500000,RESOLUTION=1280x720,CODECS="avc1.4d4020,mp4a.40.2",AUDIO="audio"
720p.m3u8?token=abc&expires=99
`
)

type testEnv struct {
	client *Client
	srv    *httptest.Server
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	env := &testEnv{}
	mux := http.NewServeMux()
	env.srv = httptest.NewServer(mux)
	t.Cleanup(env.srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool(nil), env.srv.URL, "https://q62flix.ws", testProxyBase)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}
	client.base = fakeSearcher{}
	client.httpClient = cdnClient(env.srv)
	env.client = client
	mux.HandleFunc("/", env.serve)
	return env
}

// cdnClient sends every host to the test server so fixtures can use CDN-like
// media URLs that still pass the provider's direct-URL filter.
func cdnClient(srv *httptest.Server) *http.Client {
	addr := srv.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}

func (e *testEnv) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/movie":
		fmt.Fprint(w, movieCatalog)
	case "/tvshow":
		fmt.Fprint(w, showCatalog)
	case "/tvshow/SHOW1/season":
		fmt.Fprint(w, seasonList)
	case "/season/SEASON1/episode":
		fmt.Fprint(w, episodeList)
	case "/movie/MOVIE1/releases", "/season/SEASON1/episode/EP1/releases":
		fmt.Fprint(w, releaseList)
	case "/movie/MOVIE1/release", "/season/SEASON1/episode/EP1/release":
		assetID := "EP1"
		if strings.HasPrefix(r.URL.Path, "/movie/") {
			assetID = "MOVIE1"
		}
		descriptor := fmt.Sprintf("/stream/%s/TOKEN/vod/master.m3u8?token=abc&expires=99", assetID)
		fmt.Fprintf(w, `{"id":"REL1","m3u8Url":%q,"originalLanguage":"en-US",`+
			`"audios":[{"language":"en-US","channels":2}],`+
			`"subtitles":[{"language":"en-US","type":"FULL","relativePath":"en-US-full.vtt"}]}`, descriptor)
	default:
		if strings.HasSuffix(r.URL.Path, "/master.m3u8") {
			fmt.Fprint(w, masterPlaylist)
			return
		}
		http.NotFound(w, r)
	}
}

func TestQ62Capabilities(t *testing.T) {
	client, err := NewClient(tmdb.NewKeyPool(nil))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.Name() != "q62" || client.Alias() != "Q62" {
		t.Errorf("identity = %q/%q, want q62/Q62", client.Name(), client.Alias())
	}
	for _, want := range []provider.ContentType{provider.ModeMovies, provider.ModeTV} {
		found := false
		for _, mode := range client.Modes() {
			if mode.Name == want && mode.Priority == 2 {
				found = true
			}
		}
		if !found {
			t.Errorf("mode %q missing at priority 2: %+v", want, client.Modes())
		}
	}
}

func TestQ62NewClientRejectsEmptyURLs(t *testing.T) {
	if _, err := NewClientWithURLs(tmdb.NewKeyPool(nil), "", "https://q62flix.ws", testProxyBase); err == nil {
		t.Fatal("empty API base must be rejected")
	}
}

func TestQ62Search(t *testing.T) {
	env := newTestEnv(t)
	ctx := t.Context()

	movies, err := env.client.Search(ctx, "inception", provider.ModeMovies)
	if err != nil {
		t.Fatalf("Search movies: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("movie results = %d, want 1", len(movies))
	}
	got := movies[0]
	if got.ID != "27205" || got.TMDBID != 27205 || got.Title != "Inception" ||
		got.MediaType != provider.MediaTypeMovie || got.Year != "2010" {
		t.Errorf("movie result = %+v", got)
	}

	shows, err := env.client.Search(ctx, "mobland", provider.ModeTV)
	if err != nil {
		t.Fatalf("Search tv: %v", err)
	}
	if len(shows) != 1 || shows[0].ID != "247718" || shows[0].MediaType != provider.MediaTypeTV {
		t.Fatalf("tv results = %+v", shows)
	}
}

func TestQ62FetchEpisodesFromSharedSearchID(t *testing.T) {
	env := newTestEnv(t)
	// A shared-base result carries a numeric TMDB handle; Q62 maps it through
	// the lazily built catalog index before calling its own episode API.
	eps, err := env.client.FetchEpisodes(t.Context(), provider.SearchResult{
		ID: "247718", TMDBID: 247718, MediaType: provider.MediaTypeTV, Title: "MobLand",
	})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 1 || eps[0].ID != "SEASON1"+episodeHandleSep+"EP1" {
		t.Fatalf("episodes = %+v", eps)
	}
}

func TestQ62FetchEpisodes(t *testing.T) {
	env := newTestEnv(t)
	eps, err := env.client.FetchEpisodes(t.Context(), provider.SearchResult{
		ID: "SHOW1", TMDBID: 247718, MediaType: provider.MediaTypeTV, Title: "MobLand",
	})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 1 {
		t.Fatalf("episodes = %d, want 1 (coming-soon skipped): %+v", len(eps), eps)
	}
	if eps[0].ID != "SEASON1"+episodeHandleSep+"EP1" || eps[0].Season != 1 || eps[0].Episode != 1 {
		t.Errorf("episode = %+v", eps[0])
	}
	if eps[0].Title != "Pilot" {
		t.Errorf("title = %q, want Pilot", eps[0].Title)
	}
}

func TestQ62FetchEpisodesMovie(t *testing.T) {
	env := newTestEnv(t)
	eps, err := env.client.FetchEpisodes(t.Context(), provider.SearchResult{
		ID: "MOVIE1", TMDBID: 27205, MediaType: provider.MediaTypeMovie, Title: "Inception",
	})
	if err != nil {
		t.Fatalf("FetchEpisodes movie: %v", err)
	}
	if len(eps) != 1 || eps[0].ID != "MOVIE1" {
		t.Fatalf("movie episodes = %+v", eps)
	}
}
