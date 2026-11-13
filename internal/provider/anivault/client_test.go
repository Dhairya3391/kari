package anivault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
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

// checkPlaybackRequest rejects obsolete endpoints and namespace mix-ups in every
// explicit-ID fixture, including unexpected AniList lookups and Anikoto calls.
func checkPlaybackRequest(t *testing.T, r *http.Request) bool {
	t.Helper()
	paths := []string{
		"/api/animeheaven_stream.php",
		"/api/reanime_stream.php",
		"/api/aniwaves_stream.php",
		"/api/animenosub_stream.php",
		"/api/anizone_stream.php",
		"/api/animesalt_stream.php",
		"/api/watchanimeworld_stream.php",
	}
	if r.Method != http.MethodGet || !slices.Contains(paths, r.URL.Path) {
		t.Errorf("unexpected playback request: %s %s", r.Method, r.URL)
		return false
	}
	query := r.URL.Query()
	if query.Get("hoshikaId") != "58090" || query.Get("ep") != "1" {
		t.Errorf("playback identity = %s, want hoshikaId=58090&ep=1", r.URL)
		return false
	}
	audio := query.Get("audio")
	if audio != provider.AudioSub && audio != provider.AudioDub {
		t.Errorf("audio = %q, want sub or dub", audio)
		return false
	}
	if audio == provider.AudioDub && r.URL.Path == "/api/animeheaven_stream.php" {
		t.Error("AnimeHeaven must never be queried for dub")
		return false
	}
	return true
}

func playbackUnavailable(w http.ResponseWriter) {
	_, _ = fmt.Fprint(w, `{"error":"stream unavailable"}`)
}

func playbackEpisode(audio string) provider.Episode {
	return provider.Episode{
		ID:      "watch/anivault/hoshika/58090/" + audio + "/1",
		Episode: 1,
		Audio:   audio,
	}
}

// startPlayback keeps updates unbuffered so receipt proves incremental delivery,
// rather than merely inspecting buffered results after resolution completes.
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

// TestPlaybackCurrentEndpoints ensures explicit Hoshika identities bypass AniList
// and fan out only to the current site's seven playback integrations.
func TestPlaybackCurrentEndpoints(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]int)
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/api/reanime_stream.php" {
			_, _ = fmt.Fprint(w, `{"server":"Fast","mp4":"https://cdn.example/fast.mp4"}`)
			return
		}
		playbackUnavailable(w)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sources, err := client.ResolveSource(ctx, "hoshika/58090", playbackEpisode(provider.AudioSub))
	if err != nil || len(sources) != 1 {
		t.Fatalf("ResolveSource = %+v, %v; want one playable source", sources, err)
	}
	if sources[0].URL != "https://cdn.example/fast.mp4" || sources[0].Type != provider.SourceTypeMP4 {
		t.Errorf("source = %+v, want direct MP4", sources[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 7 {
		t.Errorf("queried endpoints = %v, want all seven current endpoints", seen)
	}
	for path, count := range seen {
		if count != 1 {
			t.Errorf("%s requested %d times, want one initial request", path, count)
		}
	}
}

// TestPlaybackOwnedHandleUsesHoshikaIdentity exercises the six-part owned
// handle independently of the canonical media ID and episode-number fallback.
func TestPlaybackOwnedHandleUsesHoshikaIdentity(t *testing.T) {
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/reanime_stream.php" {
			_, _ = fmt.Fprint(w, `{"mp4":"https://cdn.example/owned.mp4"}`)
			return
		}
		playbackUnavailable(w)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	episode := provider.Episode{ID: "watch/anivault/hoshika/58090/sub/1"}
	sources, err := client.ResolveSource(ctx, "151807", episode)
	if err != nil || len(sources) != 1 || sources[0].URL != "https://cdn.example/owned.mp4" {
		t.Fatalf("owned handle resolution = %+v, %v", sources, err)
	}
}

// TestPlaybackStreamsBeforeSlowEndpointCompletes guards against buffered-only
// delivery and the retired early-return grace window dropping late sources.
func TestPlaybackStreamsBeforeSlowEndpointCompletes(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/reanime_stream.php":
			_, _ = fmt.Fprint(w, `{"server":"Fast","mp4":"https://cdn.example/fast.mp4"}`)
		case "/api/aniwaves_stream.php":
			select {
			case <-release:
				_, _ = fmt.Fprint(w, `{"server":"Slow","m3u8":"https://cdn.example/slow.m3u8"}`)
			case <-r.Context().Done():
			}
		default:
			playbackUnavailable(w)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	updates, done := startPlayback(ctx, client)
	select {
	case batch, ok := <-updates:
		if !ok || len(batch) != 1 || batch[0].URL != "https://cdn.example/fast.mp4" {
			t.Fatalf("first incremental batch = %+v, open=%v", batch, ok)
		}
	case <-ctx.Done():
		t.Fatal("no source delivered while the slow endpoint was gated")
	}
	select {
	case err := <-done:
		t.Fatalf("resolution returned before the slow endpoint completed: %v", err)
	case <-time.After(1700 * time.Millisecond):
		// The old resolver discarded outstanding work after 1500ms.
	case <-ctx.Done():
		t.Fatal("context expired while holding the slow endpoint")
	}
	unblock()
	remaining := make([]provider.MediaSource, 0)
	for batch := range updates {
		remaining = append(remaining, batch...)
	}
	if err := <-done; err != nil {
		t.Fatalf("ResolveStream: %v", err)
	}
	if len(remaining) != 1 || remaining[0].URL != "https://cdn.example/slow.m3u8" {
		t.Fatalf("late sources = %+v, want the slow endpoint's HLS source", remaining)
	}
}

// TestPlaybackDeadlineRetainsSources checks both resolution entry points when
// another endpoint remains pending until the caller's context deadline.
func TestPlaybackDeadlineRetainsSources(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%t", streaming), func(t *testing.T) {
			client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/reanime_stream.php":
					_, _ = fmt.Fprint(w, `{"mp4":"https://cdn.example/retained.mp4"}`)
				case "/api/aniwaves_stream.php":
					<-r.Context().Done()
				default:
					playbackUnavailable(w)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			sources := make([]provider.MediaSource, 0)
			var err error
			if streaming {
				updates, done := startPlayback(ctx, client)
				for batch := range updates {
					sources = append(sources, batch...)
				}
				err = <-done
			} else {
				sources, err = client.ResolveSource(ctx, "hoshika/58090", playbackEpisode(provider.AudioSub))
			}
			if err != nil {
				t.Fatalf("partial success returned an error: %v", err)
			}
			if len(sources) != 1 || sources[0].URL != "https://cdn.example/retained.mp4" {
				t.Fatalf("sources after deadline = %+v, want retained playable result", sources)
			}
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("resolution stopped before the deadline: %v", ctx.Err())
			}
		})
	}
}

// TestPlaybackCancellationWithoutSources checks that blocked requests terminate
// without inventing a playable result when the caller cancels.
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

// TestPlaybackInitialSourceAndServerDiscovery prevents replaying the initial
// playable server while still resolving every other advertised server.
func TestPlaybackInitialSourceAndServerDiscovery(t *testing.T) {
	var mu sync.Mutex
	requested := make(map[string]int)
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/reanime_stream.php" {
			playbackUnavailable(w)
			return
		}
		server := r.URL.Query().Get("server")
		mu.Lock()
		requested[server]++
		mu.Unlock()
		switch server {
		case "":
			_, _ = fmt.Fprint(w, `{"server":"Primary","mp4":"https://cdn.example/primary.mp4","servers":["Primary","Mirror"]}`)
		case "Mirror":
			_, _ = fmt.Fprint(w, `{"server":"Mirror","mp4":"https://cdn.example/mirror.mp4"}`)
		default:
			t.Errorf("initial playable server requested again: %q", server)
			playbackUnavailable(w)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sources, err := client.ResolveSource(ctx, "hoshika/58090", playbackEpisode(provider.AudioSub))
	if err != nil || len(sources) != 2 {
		t.Fatalf("ResolveSource = %+v, %v; want initial and mirror streams", sources, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requested) != 2 || requested[""] != 1 || requested["Mirror"] != 1 {
		t.Errorf("server requests = %v, want only initial and Mirror", requested)
	}
}

// TestPlaybackAudioServerFiltering exercises server-only discovery for both wire
// shapes, handle audio defaults, and explicit audio overriding an owned handle.
func TestPlaybackAudioServerFiltering(t *testing.T) {
	cases := []struct {
		name    string
		servers string
		audio   string
		handle  string
		want    []string
		lang    string
	}{
		{
			name:    "object sub includes raw",
			servers: `[{"name":"SubOne","type":"sub","lang":"ja"},{"name":"RawOne","type":"raw","lang":"ja"},{"name":"DubOne","type":"dub","lang":"en"}]`,
			handle:  provider.AudioSub,
			want:    []string{"RawOne", "SubOne"},
			lang:    "ja",
		},
		{
			name:    "object explicit dub overrides sub handle",
			servers: `[{"name":"SubOne","type":"sub","lang":"ja"},{"name":"RawOne","type":"raw","lang":"ja"},{"name":"DubOne","type":"dub","lang":"en"}]`,
			audio:   provider.AudioDub,
			handle:  provider.AudioSub,
			want:    []string{"DubOne"},
			lang:    "en",
		},
		{
			name:    "string sub prefixes",
			servers: `["Sub HD-1","Dub HD-2","Shared"]`,
			audio:   provider.AudioSub,
			handle:  provider.AudioDub,
			want:    []string{"Shared", "Sub HD-1"},
		},
		{
			name:    "string dub from handle",
			servers: `["Sub HD-1","Dub HD-2","Shared"]`,
			handle:  provider.AudioDub,
			want:    []string{"Dub HD-2", "Shared"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantAudio := tc.audio
			if wantAudio == "" {
				wantAudio = tc.handle
			}
			var mu sync.Mutex
			requested := make([]string, 0)
			client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("audio"); got != wantAudio {
					t.Errorf("audio = %q, want %q", got, wantAudio)
				}
				if r.URL.Path != "/api/animesalt_stream.php" {
					playbackUnavailable(w)
					return
				}
				server := r.URL.Query().Get("server")
				if server == "" {
					_, _ = fmt.Fprintf(w, `{"servers":%s}`, tc.servers)
					return
				}
				mu.Lock()
				requested = append(requested, server)
				mu.Unlock()
				if tc.lang != "" && r.URL.Query().Get("lang") != tc.lang {
					t.Errorf("server %q lang = %q, want %q", server, r.URL.Query().Get("lang"), tc.lang)
				}
				_, _ = fmt.Fprintf(w, `{"mp4":"https://cdn.example/%s.mp4"}`, url.PathEscape(server))
			})
			episode := playbackEpisode(tc.handle)
			episode.Audio = tc.audio
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			sources, err := client.ResolveSource(ctx, "hoshika/58090", episode)
			if err != nil || len(sources) != len(tc.want) {
				t.Fatalf("ResolveSource = %+v, %v; want %d servers", sources, err, len(tc.want))
			}
			mu.Lock()
			defer mu.Unlock()
			slices.Sort(requested)
			if !slices.Equal(requested, tc.want) {
				t.Errorf("requested servers = %v, want %v", requested, tc.want)
			}
		})
	}
}

// TestPlaybackForeignHandleUsesCanonicalMediaID prevents another provider's
// embedded show ID from being interpreted as an AniList or Hoshika identity.
func TestPlaybackForeignHandleUsesCanonicalMediaID(t *testing.T) {
	for _, handle := range []string{"watch/anikoto/99999/sub/1", "watch/other/hoshika/99999/sub/1"} {
		t.Run(handle, func(t *testing.T) {
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
				default:
					if !checkPlaybackRequest(t, r) {
						http.NotFound(w, r)
						return
					}
					if r.URL.Path == "/api/reanime_stream.php" {
						_, _ = fmt.Fprint(w, `{"mp4":"https://cdn.example/canonical.mp4"}`)
						return
					}
					playbackUnavailable(w)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			episode := provider.Episode{ID: handle, Episode: 1, Audio: provider.AudioSub}
			sources, err := client.ResolveSource(ctx, "151807", episode)
			if err != nil || len(sources) != 1 || sources[0].URL != "https://cdn.example/canonical.mp4" {
				t.Fatalf("canonical resolution = %+v, %v", sources, err)
			}
		})
	}
}

// TestPlaybackSourceIdentityDeduplication checks deduplication across batches
// and endpoints without collapsing sources that require different referers.
func TestPlaybackSourceIdentityDeduplication(t *testing.T) {
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/reanime_stream.php", "/api/aniwaves_stream.php":
			_, _ = fmt.Fprint(w, `{"server":"Shared","mp4":"https://cdn.example/shared.mp4","rawStreamUrl":"https://cdn.example/shared.mp4","embedUrl":"https://embed.example/player"}`)
		case "/api/anizone_stream.php":
			_, _ = fmt.Fprint(w, `{"server":"Shared","mp4":"https://cdn.example/shared.mp4","embedUrl":"https://different.example/player"}`)
		default:
			playbackUnavailable(w)
		}
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
	if len(seen) != 2 {
		t.Fatalf("source identities = %d, want same URL with two distinct referers", len(seen))
	}
}

// TestPlaybackPrimaryProxyFieldsKeepMaster verifies proxy URLs are unwrapped
// even in primary fields and video-only quality entries do not replace HLS.
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
				if r.URL.Path != "/api/reanime_stream.php" {
					playbackUnavailable(w)
					return
				}
				_, _ = fmt.Fprintf(w,
					`{"server":"Primary","%s":%q,"embedUrl":"https://wrong.example/embed","qualities":[{"quality":"1080p","url":"https://cdn.example/video-only.m3u8"}]}`,
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

// TestPlaybackHTTP200ErrorIsFailure prevents error payloads from advertising
// stale playable URLs or triggering discovery of their listed servers.
func TestPlaybackHTTP200ErrorIsFailure(t *testing.T) {
	client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server") != "" {
			t.Errorf("server discovery continued after an error payload: %s", r.URL)
		}
		_, _ = fmt.Fprint(w, `{"error":"upstream unavailable","mp4":"https://cdn.example/stale.mp4","servers":["Stale"]}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sources, err := client.ResolveSource(ctx, "hoshika/58090", playbackEpisode(provider.AudioSub))
	if len(sources) != 0 || !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("ResolveSource = %+v, %v; want ErrNoSources and no stale URLs", sources, err)
	}
}

// TestPlaybackDubAudioUnavailableAggregation distinguishes verified missing
// audio across every backend from an outage that leaves availability unknown.
func TestPlaybackDubAudioUnavailableAggregation(t *testing.T) {
	cases := []struct {
		name    string
		blocked bool
	}{
		{name: "all backends list only sub servers"},
		{name: "blocked backend preserves outage", blocked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			requests := make(map[string]int)
			client := newPlaybackFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("audio") != provider.AudioDub {
					t.Errorf("audio = %q, want dub", r.URL.Query().Get("audio"))
				}
				if r.URL.Query().Get("server") != "" {
					t.Errorf("incompatible sub server requested for dub: %s", r.URL)
				}
				mu.Lock()
				requests[r.URL.Path]++
				mu.Unlock()
				if tc.blocked && r.URL.Path == "/api/reanime_stream.php" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = fmt.Fprint(w, `{"error":"blocked"}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"servers":[{"name":"Japanese","type":"sub","lang":"ja"}]}`)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			sources, err := client.ResolveSource(ctx, "hoshika/58090", playbackEpisode(provider.AudioDub))
			if len(sources) != 0 {
				t.Errorf("dub sources = %+v, want none", sources)
			}
			if got := errors.Is(err, provider.ErrAudioUnavailable); got == tc.blocked {
				t.Errorf("ErrAudioUnavailable match = %t, want %t; error: %v", got, !tc.blocked, err)
			}
			if got := errors.Is(err, provider.ErrBlocked); got != tc.blocked {
				t.Errorf("ErrBlocked match = %t, want %t; error: %v", got, tc.blocked, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 6 {
				t.Errorf("dub backend requests = %v, want all six dub backends", requests)
			}
			for path, count := range requests {
				if count != 1 {
					t.Errorf("%s requested %d times, want one initial request", path, count)
				}
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
