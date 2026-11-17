package mapple

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestExtractNextActionID(t *testing.T) {
	const (
		rotated = "406ba2f811e6c73edfbd1fd3395909eb499c78bdb6"
		other   = "60f7809f44881525a6d78abcbe532e3a7d3d8e2513"
	)

	cases := []struct {
		name string
		html string
		want string
	}{
		{
			name: "next flight data with escaped quotes",
			html: `<script>self.__next_f.push([1,"3:\"createServerReference(\\\"` + rotated + `\\\",S.callServer,void 0,S.findSourceMapURL,\\\"fetchSubtitlesAction\\\")\""])</script>`,
			want: rotated,
		},
		{
			name: "raw reference with unrelated action first",
			html: `<script>var a=createServerReference("` + other + `",f.callServer,void 0,f.findSourceMapURL,"fetchFavorites");var z=createServerReference("` + rotated + `",f.callServer,void 0,f.findSourceMapURL,"fetchSubtitlesAction");</script>`,
			want: rotated,
		},
		{
			name: "next action meta tag",
			html: `<meta name="next-action" content="` + rotated + `">`,
			want: rotated,
		},
		{
			name: "action id json field",
			html: `<script>window.__ACTION_STATE__={"actionId":"` + rotated + `","name":"fetchSubtitles"}</script>`,
			want: rotated,
		},
		{
			name: "no marker falls back to known constant",
			html: `<html><body>no server actions on this page</body></html>`,
			want: nextActionSubtitles,
		},
		{
			name: "marker without id falls back to known constant",
			html: `<script>createServerReference("short",f.callServer,void 0,f.findSourceMapURL,"fetchSubtitlesAction")</script>`,
			want: nextActionSubtitles,
		},
		{
			name: "empty html falls back to known constant",
			html: "",
			want: nextActionSubtitles,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractNextActionID(tc.html); got != tc.want {
				t.Errorf("extractNextActionID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMappleSessionRequestHeaders(t *testing.T) {
	type fetchHeaders struct {
		origin, site, mode, dest string
	}
	seen := make(chan fetchHeaders, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch/movie/550":
			w.WriteHeader(http.StatusOK)
		case "/api/ui/session":
			seen <- fetchHeaders{
				origin: r.Header.Get("Origin"),
				site:   r.Header.Get("Sec-Fetch-Site"),
				mode:   r.Header.Get("Sec-Fetch-Mode"),
				dest:   r.Header.Get("Sec-Fetch-Dest"),
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sessionResponse{Token: "test-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New failed: %v", err)
	}
	sessionClient := *client.httpClient
	sessionClient.Jar = jar

	_, _ = client.resolveStreamURL(context.Background(), &sessionClient, srv.URL+"/watch/movie/550", 550, provider.MediaTypeMovie, "")

	select {
	case got := <-seen:
		if got.origin != srv.URL {
			t.Errorf("Origin = %q, want %q", got.origin, srv.URL)
		}
		if got.site != "same-origin" {
			t.Errorf("Sec-Fetch-Site = %q, want same-origin", got.site)
		}
		if got.mode != "cors" {
			t.Errorf("Sec-Fetch-Mode = %q, want cors", got.mode)
		}
		if got.dest != "empty" {
			t.Errorf("Sec-Fetch-Dest = %q, want empty", got.dest)
		}
	default:
		t.Fatal("session request was not observed")
	}
}

func TestMappleHLSRequestHasNoFetchHeaders(t *testing.T) {
	seen := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000,RESOLUTION=1280x720\n/720.m3u8\n"))
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	_, _ = client.fetchMasterSources(context.Background(), client.httpClient, srv.URL+"/master.m3u8", "lane")

	select {
	case got := <-seen:
		for _, name := range []string{"Origin", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest"} {
			if value := got.Get(name); value != "" {
				t.Errorf("HLS request carried %s = %q, want absent", name, value)
			}
		}
	default:
		t.Fatal("master playlist request was not observed")
	}
}

func TestResolveSubtitleActionIDFromChunk(t *testing.T) {
	const rotated = "406ba2f811e6c73edfbd1fd3395909eb499c78bdb6"
	watchHTML := `<html><script src="/_next/static/chunks/app.js"></script>` +
		`<script>self.__next_f.push([1,"2:I[253348,[\"/_next/static/chunks/app.js?dpl=abc\"],\"\"]"])</script>` +
		`<script src="https://static.cloudflareinsights.com/beacon.min.js"></script></html>`

	chunkRequests := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch/movie/550":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, watchHTML)
		case "/_next/static/chunks/app.js":
			chunkRequests <- struct{}{}
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = io.WriteString(w, `var z=(0,S.createServerReference("`+rotated+`",S.callServer,void 0,S.findSourceMapURL,"fetchSubtitlesAction"));`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	watchURL := srv.URL + "/watch/movie/550"
	html, err := fetchWatchPage(context.Background(), client.httpClient, watchURL)
	if err != nil {
		t.Fatalf("fetchWatchPage failed: %v", err)
	}

	got := resolveSubtitleActionID(actionIDSource{
		ctx:           context.Background(),
		sessionClient: client.httpClient,
		siteBase:      srv.URL,
		watchURL:      watchURL,
		html:          html,
	})
	if got != rotated {
		t.Errorf("resolveSubtitleActionID() = %q, want %q", got, rotated)
	}
	select {
	case <-chunkRequests:
	default:
		t.Error("referenced chunk was not fetched")
	}
}
