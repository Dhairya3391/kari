package vidora

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

// laneHosts are the production origins; the routed client sends every request
// to the test server regardless of host.
var laneHosts = Endpoints{
	Site:    "https://vidora.ac",
	Content: "https://cdn.hls.lol",
	Stream:  "https://stream.hls.lol",
	Shegu:   "https://shegu.hls.lol",
	Pal:     "https://pal.hls.lol",
	Subs:    "https://natsuki.hls.lol",
}

// routedClient sends all HTTP traffic to the test server so production host
// names can be exercised offline.
func routedClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	address := strings.TrimPrefix(server.URL, "https://")
	return &http.Client{
		Transport: &http.Transport{
			// The fixture server presents a self-signed certificate for a
			// host the production origins do not match.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, address)
			},
		},
	}
}

// laneServer serves canned lane payloads and records requested query strings.
func laneServer(t *testing.T) (*httptest.Server, map[string]string) {
	t.Helper()
	var (
		mu      sync.Mutex
		queries = map[string]string{}
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if referer := r.Header.Get("Referer"); referer != "https://vidora.ac/" {
			t.Errorf("%s: Referer = %q", r.URL.Path, referer)
		}
		mu.Lock()
		queries[r.URL.Path] = r.URL.RawQuery
		mu.Unlock()

		switch r.URL.Path {
		case "/content/movie/786892":
			writeJSON(w, `{"found":true,"title":"Fixture","url":"https://totallyacdn.cc/movie.m3u8?sign=a","type":"hls"}`)
		case "/content/tv/94997/2/3":
			writeJSON(w, `{"found":true,"title":"Fixture","url":"https://totallyacdn.cc/tv.m3u8?sign=b","type":"hls"}`)
		case "/movie.m3u8", "/tv.m3u8":
			writePlaylist(w, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\n/long.m3u8\n")
		case "/long.m3u8":
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:10\n")
			for i := 0; i < 40; i++ {
				b.WriteString("#EXTINF:10.000000,\nseg.ts\n")
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			writePlaylist(w, b.String())
		case "/resolve/movie/786892":
			writeJSON(w, `{"found":true,"url":"https://shegu.hls.lol/movie.m3u8","maxHeight":1080}`)
		case "/resolve/tv/2/3/94997":
			writeJSON(w, `{"found":true,"url":"https://shegu.hls.lol/tv.m3u8","maxHeight":720}`)
		case "/vidbox":
			writeJSON(w, `{"sources":{"Vidbox":{"label":"AUTO","server":"Vidbox","type":"hls","url":"https://edge.vidbox.example/master.m3u8","qualities":[{"resolution":"3840x2160"}]}}}`)
		case "/mbox":
			writeJSON(w, `{"server":"Prism","tracks":[{"url":"https://stream.hls.lol/mbox/manifest.mpd?q=1","kind":"dash","quality":"1080p","lang":"English","mode":"dub"}]}`)
		case "/bandit/streams":
			writeJSON(w, `{"found":true,"servers":[{"name":"bandit","streams":[{"quality":"720p","url":"https://api.obj2.top/movie/master.m3u8"}]}]}`)
		case "/subs":
			writeJSON(w, `{"subtitles":[{"url":"https://natsuki.hls.lol/s/1.srt","language":"English","langCode":"en"},{"url":"https://natsuki.hls.lol/s/2.srt","language":"French","langCode":"fr"},{"url":"https://natsuki.hls.lol/s/3.srt","language":"English US","langCode":"en"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, queries
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func writePlaylist(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = w.Write([]byte(body))
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClientWithEndpoints(tmdb.NewKeyPool(nil), laneHosts)
	if err != nil {
		t.Fatalf("NewClientWithEndpoints: %v", err)
	}
	client.httpClient = routedClient(t, server)
	return client
}

func TestClientCapabilities(t *testing.T) {
	client, err := NewClientWithEndpoints(tmdb.NewKeyPool(nil), laneHosts)
	if err != nil {
		t.Fatalf("NewClientWithEndpoints: %v", err)
	}
	if client.Name() != "vidora" || client.Alias() != "Vidora" {
		t.Fatalf("unexpected identity: %q %q", client.Name(), client.Alias())
	}
	modes := map[provider.ContentType]bool{}
	for _, mode := range client.Modes() {
		modes[mode.Name] = mode.Priority > 0
	}
	for _, want := range []provider.ContentType{provider.ModeMovies, provider.ModeTV, provider.ModeCartoon} {
		if !modes[want] {
			t.Fatalf("mode %q missing or zero priority", want)
		}
	}
}

func TestResolveSourceMovie(t *testing.T) {
	server, _ := laneServer(t)
	client := newTestClient(t, server)

	sources, err := client.ResolveSource(context.Background(), "786892", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	got := map[string]provider.MediaSource{}
	for _, source := range sources {
		got[source.Backend] = source
	}
	for _, backend := range []string{backendAphrodite, backendShegu, "Vidbox", "Prism", "bandit"} {
		source, ok := got[backend]
		if !ok {
			t.Fatalf("missing %s source in %+v", backend, sources)
		}
		if source.Referer != "https://vidora.ac/" || source.UserAgent == "" {
			t.Fatalf("%s missing hotlink headers: %+v", backend, source)
		}
	}

	shegu := got[backendShegu]
	if shegu.Quality != "1080p" || shegu.Height != 1080 {
		t.Fatalf("unexpected shegu quality: %+v", shegu)
	}
	prism := got["Prism"]
	if prism.Type != "dash" || prism.Height != 1080 {
		t.Fatalf("unexpected prism source: %+v", prism)
	}
	if prism.AudioLabel != "English" || prism.Language != "en" {
		t.Fatalf("prism audio identity not carried: %+v", prism)
	}
	if !strings.Contains(strings.Join(prism.ExtraArgs, " "), "--demuxer-lavf-format=dash") {
		t.Fatalf("prism DASH row missing demuxer args: %+v", prism.ExtraArgs)
	}
	vidbox := got["Vidbox"]
	if vidbox.Height != 2160 || vidbox.Quality != "Auto (Vidbox)" {
		t.Fatalf("unexpected vidbox source: %+v", vidbox)
	}
	if len(sources[0].Subtitles) != 2 {
		t.Fatalf("expected two deduped subtitle languages, got %+v", sources[0].Subtitles)
	}
}

func TestResolveSourceTVUsesEpisodeCoordinates(t *testing.T) {
	server, queries := laneServer(t)
	client := newTestClient(t, server)

	sources, err := client.ResolveSource(context.Background(), "94997", provider.Episode{Season: 2, Episode: 3, TMDBID: 94997})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	urls := make([]string, 0, len(sources))
	for _, source := range sources {
		urls = append(urls, source.URL)
	}
	joined := strings.Join(urls, " ")
	if !strings.Contains(joined, "totallyacdn.cc/tv.m3u8") || !strings.Contains(joined, "shegu.hls.lol/tv.m3u8") {
		t.Fatalf("TV lanes were not resolved: %v", urls)
	}
	for path, want := range map[string]string{
		"/mbox":           "episodeId=3",
		"/vidbox":         "seasonId=2",
		"/bandit/streams": "episode=3",
		"/subs":           "episode=3",
	} {
		if !strings.Contains(queries[path], want) {
			t.Fatalf("%s query missing %q: %q", path, want, queries[path])
		}
	}
}

// TestResolveSourceRejectsAphroditePlaceholder proves an untitled content
// response is not surfaced. The edge reports found:true with a playable master
// for unknown TMDB ids, but that master belongs to a different title.
func TestResolveSourceRejectsAphroditePlaceholder(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/content/movie/1" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, `{"found":true,"url":"https://totallyacdn.cc/placeholder.m3u8?sign=z","type":"hls"}`)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	_, err := client.ResolveSource(context.Background(), "1", provider.Episode{})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("untitled content response must resolve to no sources, got %v", err)
	}
}

// TestResolveSourceSkipsAphroditePlaceholder proves the CDN's short stand-in
// clip is not surfaced as a playable source. The site only receives real
// playlists after solving its content challenge; without one the edge returns
// a signed URL that plays ~64 seconds of "not available".
func TestResolveSourceSkipsAphroditePlaceholder(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/content/movie/786892":
			writeJSON(w, `{"found":true,"title":"Fixture","url":"https://totallyacdn.cc/master.m3u8?sign=a","type":"hls"}`)
		case "/master.m3u8":
			writePlaylist(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\n/short.m3u8\n")
		case "/short.m3u8":
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:8\n")
			for i := 0; i < 8; i++ {
				b.WriteString("#EXTINF:8.000000,\nseg.jpg\n")
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			writePlaylist(w, b.String())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	_, err := client.ResolveSource(context.Background(), "786892", provider.Episode{})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("placeholder must resolve to no sources, got %v", err)
	}
}

// TestResolveSourceSurvivesOneLane proves a single answering lane is enough:
// every other upstream 404s while Aphrodite still returns its row.
func TestResolveSourceSurvivesOneLane(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/content/movie/786892" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, `{"found":true,"title":"Fixture","url":"https://totallyacdn.cc/only.m3u8?sign=a","type":"hls"}`)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	sources, err := client.ResolveSource(context.Background(), "786892", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 || sources[0].Backend != backendAphrodite {
		t.Fatalf("expected only the Aphrodite row, got %+v", sources)
	}
}

// TestResolveSourceSheguUnknownHeight proves a resolver answer without a
// height keeps the Auto label instead of being reported as "0p".
func TestResolveSourceSheguUnknownHeight(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/resolve/movie/786892" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, `{"found":true,"url":"https://shegu.hls.lol/movie.m3u8"}`)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	sources, err := client.ResolveSource(context.Background(), "786892", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 || sources[0].Backend != backendShegu {
		t.Fatalf("expected only the Shegu row, got %+v", sources)
	}
	if sources[0].Quality != "Auto (Shegu)" {
		t.Fatalf("quality = %q, want Auto (Shegu)", sources[0].Quality)
	}
}

func TestResolveSourceNoSources(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	_, err := client.ResolveSource(context.Background(), "1", provider.Episode{})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("expected ErrNoSources, got %v", err)
	}
}

func TestResolveSourceInvalidID(t *testing.T) {
	client, err := NewClientWithEndpoints(tmdb.NewKeyPool(nil), laneHosts)
	if err != nil {
		t.Fatalf("NewClientWithEndpoints: %v", err)
	}
	if _, err := client.ResolveSource(context.Background(), "not-a-number", provider.Episode{}); err == nil {
		t.Fatal("expected an error for a non-numeric TMDB id")
	}
}

func TestNewClientWithEndpointsRequiresOrigins(t *testing.T) {
	if _, err := NewClientWithEndpoints(tmdb.NewKeyPool(nil), Endpoints{Site: "https://vidora.ac"}); err == nil {
		t.Fatal("expected missing-endpoint error")
	}
}
