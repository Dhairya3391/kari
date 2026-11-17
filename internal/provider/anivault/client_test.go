package anivault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kari/internal/provider"
)

func newPlaybackFixture(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !checkPlaybackRequest(t, r) {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := NewClientWithBase(server.URL)
	if err != nil {
		t.Fatalf("NewClientWithBase: %v", err)
	}
	client.http = server.Client()
	client.anilistEndpoint = server.URL + "/graphql"
	return client
}

// checkPlaybackRequest rejects obsolete endpoints and namespace mix-ups in
// every explicit-ID fixture. AnimeHeaven is the only playback endpoint, so a
// subbed resolution makes exactly one request.
func checkPlaybackRequest(t *testing.T, r *http.Request) bool {
	t.Helper()
	if r.Method != http.MethodGet || r.URL.Path != "/api/animeheaven_stream.php" {
		t.Errorf("unexpected playback request: %s %s", r.Method, r.URL)
		return false
	}
	query := r.URL.Query()
	if query.Get("hoshikaId") != "58090" || query.Get("ep") != "1" || query.Get("audio") != provider.AudioSub {
		t.Errorf("playback query = %s, want hoshikaId=58090&ep=1&audio=sub", r.URL)
		return false
	}
	return true
}

func playbackEpisode(audio string) provider.Episode {
	return provider.Episode{
		ID:      "watch/anivault/hoshika/58090/" + audio + "/1",
		Episode: 1,
		Audio:   audio,
	}
}

// startPlayback keeps updates unbuffered so receipt proves incremental
// delivery through ResolveStream rather than inspecting buffered results.
func startPlayback(ctx context.Context, client *Client) (<-chan []provider.MediaSource, <-chan error) {
	updates := make(chan []provider.MediaSource)
	// A terminal result must not block cleanup when an assertion aborts the test.
	done := make(chan error, 1)
	go func() {
		err := client.ResolveStream(ctx, "hoshika/58090", playbackEpisode(provider.AudioSub), updates)
		close(updates)
		done <- err
	}()
	return updates, done
}

// TestPlaybackAnimeHeavenSuccess verifies the single-endpoint resolution and
// that an upstream payload with neither heights nor subtitle tracks still
// yields a hard-subbed direct stream.
func TestPlaybackAnimeHeavenSuccess(t *testing.T) {
	var calls atomic.Int32
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"server":"Fast","mp4":"https://cdn.example/fast.mp4","qualities":[]}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sources, err := client.ResolveSource(ctx, "hoshika/58090", playbackEpisode(provider.AudioSub))
	if err != nil || len(sources) != 1 {
		t.Fatalf("ResolveSource = %+v, %v; want one playable source", sources, err)
	}
	source := sources[0]
	if source.URL != "https://cdn.example/fast.mp4" || source.Type != provider.SourceTypeMP4 {
		t.Errorf("source = %+v, want direct MP4", source)
	}
	if source.SubType != provider.SubTypeHard || source.Quality != "Auto" {
		t.Errorf("source metadata = %+v, want hard subtitles and Auto quality", source)
	}
	if source.Height != 0 || len(source.Subtitles) != 0 {
		t.Errorf("anivault must not invent height or sidecar subtitles: %+v", source)
	}
	if source.Audio.Track != provider.TrackSub {
		t.Errorf("audio track = %q, want sub", source.Audio.Track)
	}
	if source.Backend != "AnimeHeaven · Fast" {
		t.Errorf("backend = %q, want AnimeHeaven · Fast", source.Backend)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("playback requests = %d, want exactly one", got)
	}
}

// TestPlaybackOwnedHandleUsesHoshikaIdentity exercises the six-part owned
// handle independently of the canonical media ID and episode-number fallback.
func TestPlaybackOwnedHandleUsesHoshikaIdentity(t *testing.T) {
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"mp4":"https://cdn.example/owned.mp4"}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	episode := provider.Episode{ID: "watch/anivault/hoshika/58090/sub/1"}
	sources, err := client.ResolveSource(ctx, "151807", episode)
	if err != nil || len(sources) != 1 || sources[0].URL != "https://cdn.example/owned.mp4" {
		t.Fatalf("owned handle resolution = %+v, %v", sources, err)
	}
}

// TestPlaybackDubMakesNoRequests proves dub resolution is rejected locally:
// neither an identity lookup nor the AnimeHeaven stream endpoint may be hit.
func TestPlaybackDubMakesNoRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Errorf("unexpected HTTP request for dub: %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := NewClientWithBase(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	client.anilistEndpoint = server.URL + "/graphql"

	cases := []struct {
		name    string
		episode provider.Episode
	}{
		{
			name:    "explicit dub needs identity lookup",
			episode: provider.Episode{Audio: provider.AudioDub, Episode: 1},
		},
		{
			name:    "dub handle",
			episode: provider.Episode{ID: "watch/anivault/hoshika/58090/dub/1", Episode: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			sources, err := client.ResolveSource(ctx, "151807", tc.episode)
			if len(sources) != 0 || !errors.Is(err, provider.ErrNoSources) {
				t.Fatalf("ResolveSource = %+v, %v; want ErrNoSources", sources, err)
			}
			updates := make(chan []provider.MediaSource, 1)
			err = client.ResolveStream(ctx, "151807", tc.episode, updates)
			if !errors.Is(err, provider.ErrNoSources) || len(updates) != 0 {
				t.Fatalf("ResolveStream = %v, buffered batches = %d; want ErrNoSources without sources", err, len(updates))
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("dub resolution made %d HTTP requests, want zero", got)
	}
}

// TestPlaybackCanonicalIdentityFeedsAnimeHeaven prevents another provider's
// embedded handle from being read as an AniList or Hoshika identity.
func TestPlaybackCanonicalIdentityFeedsAnimeHeaven(t *testing.T) {
	for _, handle := range []string{"watch/anikoto/99999/sub/1", "watch/other/hoshika/99999/sub/1"} {
		t.Run(handle, func(t *testing.T) {
			var streamCalls atomic.Int32
			client := newCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/graphql":
					var request struct {
						Variables struct {
							ID int `json:"id"`
						} `json:"variables"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.Variables.ID != 151807 {
						t.Errorf("AniList ID = %d, want canonical mediaID 151807", request.Variables.ID)
					}
					_, _ = fmt.Fprint(w, `{"data":{"Media":{"id":151807,"idMal":52299,"title":{"english":"Solo Leveling"}}}}`)
				case "/api/hoshika/identity-by-legacy/52299":
					_, _ = fmt.Fprint(w, `{"ok":true,"mapped":true,"data":{"shortId":"58090"}}`)
				case "/api/animeheaven_stream.php":
					streamCalls.Add(1)
					if got := r.URL.Query().Get("hoshikaId"); got != "58090" {
						t.Errorf("resolved ID = %q, want 58090", got)
					}
					_, _ = fmt.Fprint(w, `{"mp4":"https://cdn.example/canonical.mp4"}`)
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			episode := provider.Episode{ID: handle, Episode: 1, Audio: provider.AudioSub}
			sources, err := client.ResolveSource(ctx, "151807", episode)
			if err != nil || len(sources) != 1 || sources[0].URL != "https://cdn.example/canonical.mp4" {
				t.Fatalf("canonical resolution = %+v, %v", sources, err)
			}
			if got := streamCalls.Load(); got != 1 {
				t.Errorf("stream requests = %d, want one", got)
			}
		})
	}
}

// TestPlaybackCancellationWithoutSources checks that a pending stream request
// stops with the caller's error and never invents a playable result.
func TestPlaybackCancellationWithoutSources(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	updates, done := startPlayback(ctx, client)
	select {
	case <-started:
		cancel()
	case <-ctx.Done():
		t.Fatal("playback request never started")
	}
	for batch := range updates {
		t.Errorf("unexpected batch after cancellation: %+v", batch)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveStream error = %v, want context.Canceled", err)
	}
}

// TestPlaybackHTTP200ErrorIsFailure prevents error payloads from advertising
// stale playable URLs.
func TestPlaybackHTTP200ErrorIsFailure(t *testing.T) {
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"error":"upstream unavailable","mp4":"https://cdn.example/stale.mp4"}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sources, err := client.ResolveSource(ctx, "hoshika/58090", playbackEpisode(provider.AudioSub))
	if len(sources) != 0 || !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("ResolveSource = %+v, %v; want ErrNoSources and no stale URLs", sources, err)
	}
}

// TestPlaybackSourceIdentityDeduplication checks that equivalent primary and
// raw stream fields collapse to one source per transport identity.
func TestPlaybackSourceIdentityDeduplication(t *testing.T) {
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"server":"Shared","mp4":"https://cdn.example/shared.mp4","rawStreamUrl":"https://cdn.example/shared.mp4","embedUrl":"https://embed.example/player"}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	updates, done := startPlayback(ctx, client)
	seen := make(map[string]bool)
	for batch := range updates {
		for _, source := range batch {
			identity := source.TransportIdentity()
			if seen[identity] {
				t.Errorf("duplicate source identity delivered: %+v", source)
			}
			seen[identity] = true
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("ResolveStream: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("source identities = %d, want one", len(seen))
	}
}

// TestPlaybackPrimaryProxyFieldsKeepMaster verifies proxy URLs are unwrapped
// even in primary fields, with the real upstream referer retained.
func TestPlaybackPrimaryProxyFieldsKeepMaster(t *testing.T) {
	for _, field := range []string{"mp4", "m3u8"} {
		t.Run(field, func(t *testing.T) {
			target := "https://cdn.example/video.mp4?token=a&b=c"
			wantType := provider.SourceTypeMP4
			if field == "m3u8" {
				target = "https://cdn.example/master.m3u8?token=a&b=c"
				wantType = provider.SourceTypeHLS
			}
			referer := "https://upstream.example/player?id=1"
			proxy := "https://anivault.example/api/proxy/" + field + "?url=" + url.QueryEscape(target) +
				"&ref=" + url.QueryEscape(referer)
			client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w,
					`{"server":"Primary","%s":%q,"embedUrl":"https://wrong.example/embed","qualities":[]}`,
					field,
					proxy,
				)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			sources, err := client.ResolveSource(ctx, "hoshika/58090", playbackEpisode(provider.AudioSub))
			if err != nil || len(sources) != 1 {
				t.Fatalf("ResolveSource = %+v, %v; want one primary stream", sources, err)
			}
			source := sources[0]
			if source.URL != target || source.Referer != referer || source.Type != wantType {
				t.Errorf("mapped source = %+v, want URL=%q referer=%q type=%q", source, target, referer, wantType)
			}
			if field == "m3u8" && source.Quality != "Auto" {
				t.Errorf("master quality = %q, want Auto", source.Quality)
			}
		})
	}
}

// TestPlaybackRejectsIframeOnly prevents non-playable embed responses from
// leaking into the player's direct-stream source list.
func TestPlaybackRejectsIframeOnly(t *testing.T) {
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"iframeOnly":true,"embedUrl":"https://embed.example/player"}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sources, err := client.ResolveSource(ctx, "hoshika/58090", playbackEpisode(provider.AudioSub))
	if len(sources) != 0 || !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("ResolveSource = %+v, %v; want ErrNoSources", sources, err)
	}
}

// TestUnwrapProxy preserves the upstream referer without altering direct URLs.
func TestUnwrapProxy(t *testing.T) {
	target := "https://cdn.example/master.m3u8?token=abc"
	referer := "https://upstream.example/"
	cases := []struct {
		name string
		url  string
		want string
		ref  string
	}{
		{name: "direct", url: target, want: target},
		{
			name: "proxy",
			url:  "https://anivault.example/api/proxy/hls?url=" + url.QueryEscape(target) + "&ref=" + url.QueryEscape(referer),
			want: target,
			ref:  referer,
		},
		{name: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			direct, ref := unwrapProxy(tc.url)
			if direct != tc.want || ref != tc.ref {
				t.Errorf("unwrapProxy(%q) = %q, %q; want %q, %q", tc.url, direct, ref, tc.want, tc.ref)
			}
		})
	}
}

// TestHLSExtraArgs keeps extensionless playlists playable without overriding
// demuxer selection for ordinary HLS master URLs.
func TestHLSExtraArgs(t *testing.T) {
	if got := hlsExtraArgs("https://cdn.example/master.m3u8?token=abc"); len(got) != 0 {
		t.Errorf("extensioned playlist args = %v, want none", got)
	}
	got := hlsExtraArgs("https://cdn.example/hls/abc123")
	if !strings.Contains(strings.Join(got, " "), "--demuxer-lavf-format=hls") {
		t.Errorf("extensionless playlist args = %v, want forced HLS demuxer", got)
	}
}
