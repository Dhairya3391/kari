package lightflix

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

type fakeUpstream struct {
	mu sync.Mutex

	streamURL    string
	streamStatus int
	failSource   string
	staleToken   string

	sessionCount int
	initCount    int
	paths        []string
	sealSources  []string
	mediaID      int
	mediaType    string
	tvSlug       string
	sealTvSlug   string
}

func newFakeUpstream(t *testing.T) (*httptest.Server, *fakeUpstream) {
	t.Helper()
	up := &fakeUpstream{
		streamURL: "https://cdn.example.test/playlist/master.m3u8?e=1&s=abc",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/en":
			http.SetCookie(w, &http.Cookie{Name: siteCookieName, Value: "site-cookie", Path: "/", HttpOnly: true})
			_, _ = w.Write([]byte("<html></html>"))

		case r.Method == http.MethodPost && r.URL.Path == "/api/ui/session":
			var body struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			up.mu.Lock()
			up.sessionCount++
			up.paths = append(up.paths, body.Path)
			token := fmt.Sprintf("request-token-%d", up.sessionCount)
			up.mu.Unlock()
			_ = json.NewEncoder(w).Encode(sessionResponse{
				Token:     token,
				ExpiresAt: time.Now().Add(4 * time.Hour).UnixMilli(),
			})

		case r.Method == http.MethodPost && r.URL.Path == "/api/playback-init":
			var body struct {
				MediaID      int          `json:"mediaId"`
				MediaType    string       `json:"mediaType"`
				TvSlug       string       `json:"tv_slug"`
				RequestToken string       `json:"requestToken"`
				Pow          *powSolution `json:"pow"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			up.mu.Lock()
			up.initCount++
			up.mediaID = body.MediaID
			up.mediaType = body.MediaType
			up.tvSlug = body.TvSlug
			stale := up.staleToken != "" && body.RequestToken == up.staleToken
			up.mu.Unlock()
			if stale {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": false,
					"error":   "Invalid request token. Please refresh the page.",
				})
				return
			}
			if body.Pow == nil {
				_ = json.NewEncoder(w).Encode(playbackInitResponse{
					Success:     true,
					RequiresPow: true,
					Pow: powChallenge{
						ChallengeID: "challenge-1",
						Challenge:   "challenge-string",
						Difficulty:  4,
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(playbackInitResponse{
				Success: true,
				Token:   "stream-token",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/api/encrypt":
			var body struct {
				Data struct {
					MediaID   int    `json:"mediaId"`
					MediaType string `json:"mediaType"`
					TvSlug    string `json:"tv_slug"`
					Source    string `json:"source"`
				} `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			up.mu.Lock()
			up.sealSources = append(up.sealSources, body.Data.Source)
			up.sealTvSlug = body.Data.TvSlug
			fail := body.Data.Source == up.failSource
			up.mu.Unlock()
			data := "ok"
			if fail {
				data = "fail"
			}
			_ = json.NewEncoder(w).Encode(sealResponse{URL: "/api/stream-encrypted?data=" + data})

		case r.Method == http.MethodGet && r.URL.Path == "/api/stream-encrypted":
			if r.URL.Query().Get("requestToken") == "" || r.URL.Query().Get("token") == "" {
				http.Error(w, "missing tokens", http.StatusBadRequest)
				return
			}
			if up.streamStatus != 0 {
				w.WriteHeader(up.streamStatus)
				return
			}
			if r.URL.Query().Get("data") == "fail" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": false,
					"error":   "Content not found in streaming databases",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"stream_url": up.streamURL,
					"tmdb_id":    "1108427",
					"media_type": "movie",
					"source":     "s0",
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, up
}

func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	client, err := NewClientWithBase(&tmdb.KeyPool{}, base)
	if err != nil {
		t.Fatalf("NewClientWithBase: %v", err)
	}
	return client
}

func TestLightflixMetadata(t *testing.T) {
	client := newTestClient(t, "https://lightflix.app")
	if got := client.Name(); got != providerID {
		t.Errorf("Name() = %q, want %q", got, providerID)
	}
	if got := client.Alias(); got != providerAlias {
		t.Errorf("Alias() = %q, want %q", got, providerAlias)
	}
	modes := client.Modes()
	if len(modes) != 3 {
		t.Fatalf("Modes() count = %d, want 3", len(modes))
	}
	want := map[provider.ContentType]bool{
		provider.ModeMovies:  true,
		provider.ModeTV:      true,
		provider.ModeCartoon: true,
	}
	for _, mode := range modes {
		if !want[mode.Name] {
			t.Errorf("unexpected mode %q", mode.Name)
		}
		if mode.Priority != 2 {
			t.Errorf("mode %q priority = %d, want 2", mode.Name, mode.Priority)
		}
	}

	for _, base := range []string{"", "not-a-url", "lightflix.app"} {
		if _, err := NewClientWithBase(&tmdb.KeyPool{}, base); err == nil {
			t.Errorf("NewClientWithBase(%q) succeeded, want error", base)
		}
	}
}

func TestParseCoordinates(t *testing.T) {
	tests := []struct {
		name      string
		mediaID   string
		episode   provider.Episode
		wantType  string
		wantSlug  string
		wantPath  string
		wantID    int
		wantError bool
	}{
		{
			name: "movie from media id", mediaID: "1108427",
			wantType: provider.MediaTypeMovie, wantPath: "/en/watch/movie/1108427", wantID: 1108427,
		},
		{
			name: "movie from episode tmdb id", mediaID: "",
			episode:  provider.Episode{TMDBID: 550},
			wantType: provider.MediaTypeMovie, wantPath: "/en/watch/movie/550", wantID: 550,
		},
		{
			name: "tv from episode", mediaID: "94997",
			episode:  provider.Episode{Season: 1, Episode: 2},
			wantType: provider.MediaTypeTV, wantSlug: "1-2",
			wantPath: "/en/watch/tv/94997-1-2", wantID: 94997,
		},
		{
			name: "tv defaults missing numbers", mediaID: "94997",
			episode:  provider.Episode{Episode: 3},
			wantType: provider.MediaTypeTV, wantSlug: "1-3",
			wantPath: "/en/watch/tv/94997-1-3", wantID: 94997,
		},
		{name: "invalid id", mediaID: "abc", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coords, err := parseCoordinates(tt.mediaID, tt.episode)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCoordinates: %v", err)
			}
			if coords.mediaID != tt.wantID || coords.mediaType != tt.wantType ||
				coords.tvSlug != tt.wantSlug || coords.watchPath() != tt.wantPath {
				t.Errorf("coordinates = %+v (path %q), want id=%d type=%s slug=%q path=%q",
					coords, coords.watchPath(), tt.wantID, tt.wantType, tt.wantSlug, tt.wantPath)
			}
		})
	}
}

func TestLeadingZeroBits(t *testing.T) {
	tests := []struct {
		digest []byte
		want   int
	}{
		{digest: []byte{0xff}, want: 0},
		{digest: []byte{0x7f}, want: 1},
		{digest: []byte{0x00, 0x80}, want: 8},
		{digest: []byte{0x00, 0x00, 0x0f}, want: 20},
		{digest: []byte{0x00, 0x00, 0x00}, want: 24},
	}
	for _, tt := range tests {
		if got := leadingZeroBits(tt.digest); got != tt.want {
			t.Errorf("leadingZeroBits(%x) = %d, want %d", tt.digest, got, tt.want)
		}
	}
}

func TestSolvePow(t *testing.T) {
	challenge := powChallenge{ChallengeID: "cid", Challenge: "abc", Difficulty: 4}
	solution, err := solvePow(context.Background(), challenge)
	if err != nil {
		t.Fatalf("solvePow: %v", err)
	}
	if solution.ChallengeID != "cid" {
		t.Errorf("challenge ID = %q, want cid", solution.ChallengeID)
	}
	digest := sha256.Sum256([]byte(challenge.Challenge + solution.Nonce))
	if got := leadingZeroBits(digest[:]); got < challenge.Difficulty {
		t.Errorf("nonce %q yields %d leading bits, want >= %d", solution.Nonce, got, challenge.Difficulty)
	}

	if _, err := solvePow(context.Background(), powChallenge{}); err == nil {
		t.Error("expected error for empty challenge")
	}
	if _, err := solvePow(context.Background(), powChallenge{ChallengeID: "cid", Challenge: "abc", Difficulty: 257}); err == nil {
		t.Error("expected error for impossible difficulty")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := solvePow(ctx, challenge); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled solvePow error = %v, want context.Canceled", err)
	}
}

func TestLightflixResolveMovie(t *testing.T) {
	srv, up := newFakeUpstream(t)
	client := newTestClient(t, srv.URL)

	sources, err := client.ResolveSource(context.Background(), "1108427", provider.Episode{TMDBID: 1108427})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("source count = %d, want 1", len(sources))
	}
	got := sources[0]
	if got.URL != up.streamURL || got.Quality != "Auto" || got.Backend != "Lumen" || got.Type != provider.SourceTypeHLS {
		t.Errorf("unexpected source: %+v", got)
	}
	if got.Referer != srv.URL+"/" {
		t.Errorf("source Referer = %q, want %q", got.Referer, srv.URL+"/")
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if up.mediaID != 1108427 || up.mediaType != provider.MediaTypeMovie {
		t.Errorf("playback-init coordinates = %d/%s", up.mediaID, up.mediaType)
	}
	if len(up.paths) == 0 || up.paths[0] != "/en/watch/movie/1108427" {
		t.Errorf("session paths = %v", up.paths)
	}
	if len(up.sealSources) != 1 || up.sealSources[0] != "s0" {
		t.Errorf("seal sources = %v", up.sealSources)
	}
}

func TestLightflixResolveTV(t *testing.T) {
	srv, up := newFakeUpstream(t)
	client := newTestClient(t, srv.URL)

	_, err := client.ResolveSource(context.Background(), "94997", provider.Episode{Season: 1, Episode: 2})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if up.mediaID != 94997 || up.mediaType != provider.MediaTypeTV || up.tvSlug != "1-2" {
		t.Errorf("playback-init coordinates = %d/%s/%s", up.mediaID, up.mediaType, up.tvSlug)
	}
	if up.sealTvSlug != "1-2" {
		t.Errorf("seal tv_slug = %q, want 1-2", up.sealTvSlug)
	}
	if len(up.paths) == 0 || up.paths[0] != "/en/watch/tv/94997-1-2" {
		t.Errorf("session paths = %v", up.paths)
	}
}

func TestLightflixCachesRequestToken(t *testing.T) {
	srv, up := newFakeUpstream(t)
	client := newTestClient(t, srv.URL)

	for i := 0; i < 2; i++ {
		if _, err := client.ResolveSource(context.Background(), "1108427", provider.Episode{TMDBID: 1108427}); err != nil {
			t.Fatalf("ResolveSource #%d: %v", i, err)
		}
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.sessionCount != 1 {
		t.Errorf("session requests = %d, want 1", up.sessionCount)
	}
}

func TestLightflixLaneFallback(t *testing.T) {
	srv, up := newFakeUpstream(t)
	up.failSource = "s0"
	client := newTestClient(t, srv.URL)

	sources, err := client.ResolveSource(context.Background(), "1108427", provider.Episode{TMDBID: 1108427})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 || sources[0].Backend != "Spectrum" {
		t.Fatalf("fallback source = %+v, want Spectrum lane", sources)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.sealSources) != 2 || up.sealSources[0] != "s0" || up.sealSources[1] != "s2" {
		t.Errorf("seal sources = %v, want [s0 s2]", up.sealSources)
	}
}

func TestLightflixRefreshesRejectedSession(t *testing.T) {
	srv, up := newFakeUpstream(t)
	up.staleToken = "request-token-1"
	client := newTestClient(t, srv.URL)

	sources, err := client.ResolveSource(context.Background(), "1108427", provider.Episode{TMDBID: 1108427})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("source count = %d, want 1", len(sources))
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if up.sessionCount != 2 {
		t.Errorf("session requests = %d, want 2", up.sessionCount)
	}
}

func TestLightflixStreamFailure(t *testing.T) {
	srv, up := newFakeUpstream(t)
	up.streamURL = "/api/playback-unavailable/1108427/master.m3u8"
	client := newTestClient(t, srv.URL)

	if _, err := client.ResolveSource(context.Background(), "1108427", provider.Episode{TMDBID: 1108427}); err == nil {
		t.Fatal("expected error for non-direct stream URL, got nil")
	}
}

func TestLightflixStreamErrorRedactsTokens(t *testing.T) {
	// 403 reaches the HTTPError branch; 429 is retried and returns a
	// transport error whose message quotes the full request URL.
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			srv, up := newFakeUpstream(t)
			up.streamStatus = status
			client := newTestClient(t, srv.URL)

			_, err := client.ResolveSource(context.Background(), "1108427", provider.Episode{TMDBID: 1108427})
			if err == nil {
				t.Fatal("expected error from stream endpoint")
			}
			for _, leaked := range []string{"requestToken=", "token=", "data="} {
				if strings.Contains(err.Error(), leaked) {
					t.Errorf("error leaks credentials: %v", err)
				}
			}
			var httpErr *provider.HTTPError
			if status == http.StatusForbidden {
				if !errors.As(err, &httpErr) {
					t.Fatalf("error %v is not a provider.HTTPError", err)
				}
				if httpErr.URL != srv.URL+"/api/stream-encrypted" {
					t.Errorf("error URL = %q, want redacted endpoint", httpErr.URL)
				}
			}
		})
	}
}

func TestLightflixHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/en" {
			http.SetCookie(w, &http.Cookie{Name: siteCookieName, Value: "site-cookie", Path: "/"})
			return
		}
		http.Error(w, "session denied", http.StatusForbidden)
	}))
	defer srv.Close()
	client := newTestClient(t, srv.URL)

	_, err := client.ResolveSource(context.Background(), "1108427", provider.Episode{TMDBID: 1108427})
	if err == nil {
		t.Fatal("expected error on upstream failure")
	}
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("error %v is not a provider.HTTPError", err)
	}
}

func TestLightflixCanceledContext(t *testing.T) {
	srv, _ := newFakeUpstream(t)
	client := newTestClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.ResolveSource(ctx, "1108427", provider.Episode{TMDBID: 1108427}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestIsSessionError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{err: nil, want: false},
		{err: &provider.HTTPError{Code: http.StatusUnauthorized}, want: true},
		{err: errors.New("invalid request token. please refresh the page"), want: true},
		{err: errors.New("playback session required"), want: true},
		{err: errors.New("Content not found in streaming databases"), want: false},
	}
	for _, tt := range tests {
		if got := isSessionError(tt.err); got != tt.want {
			t.Errorf("isSessionError(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestStreamSourcesOrder(t *testing.T) {
	sources := streamSources()
	if len(sources) != 2 || sources[0].id != "s0" || sources[1].id != "s2" {
		t.Errorf("streamSources() = %+v, want s0 then s2", sources)
	}
	if !strings.Contains(sources[0].name, "Lumen") {
		t.Errorf("primary lane name = %q, want Lumen", sources[0].name)
	}
}
