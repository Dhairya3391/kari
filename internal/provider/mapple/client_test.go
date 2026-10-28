package mapple

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestMappleModesAndMetadata(t *testing.T) {
	client, err := NewClientWithURL(&tmdb.KeyPool{}, "https://mapple.fun")
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	if client.Name() != "mapple" {
		t.Errorf("Name() = %q, want %q", client.Name(), "mapple")
	}
	if client.Alias() != "Mapple" {
		t.Errorf("Alias() = %q, want %q", client.Alias(), "Mapple")
	}

	modes := client.Modes()
	if len(modes) != 3 {
		t.Fatalf("Modes() count = %d, want 3", len(modes))
	}

	expectedModes := map[provider.ContentType]bool{
		provider.ModeMovies:  true,
		provider.ModeTV:      true,
		provider.ModeCartoon: true,
	}

	for _, m := range modes {
		if !expectedModes[m.Name] {
			t.Errorf("unexpected mode: %s", m.Name)
		}
		if m.Priority != 2 {
			t.Errorf("mode %s priority = %d, want 2", m.Name, m.Priority)
		}
	}
}

func TestSolvePow(t *testing.T) {
	ctx := context.Background()

	// Invalid difficulty guards
	if nonce := solvePow(ctx, "test", -1); nonce != "" {
		t.Errorf("expected empty nonce for negative difficulty, got %q", nonce)
	}
	if nonce := solvePow(ctx, "test", 300); nonce != "" {
		t.Errorf("expected empty nonce for difficulty > 256, got %q", nonce)
	}

	// Context cancellation
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if nonce := solvePow(canceledCtx, "test", 20); nonce != "" {
		t.Errorf("expected empty nonce on canceled context, got %q", nonce)
	}

	// Valid challenge
	challenge := "test_challenge_12345"
	difficulty := 12 // 12 leading zero bits = 1.5 zero bytes

	nonce := solvePow(ctx, challenge, difficulty)
	if nonce == "" {
		t.Fatal("solvePow returned empty nonce")
	}

	sum := sha256.Sum256([]byte(challenge + nonce))
	fullBytes := difficulty / 8
	remBits := difficulty % 8
	for i := range fullBytes {
		if sum[i] != 0 {
			t.Fatalf("byte %d is not zero: %02x", i, sum[i])
		}
	}
	if remBits > 0 {
		mask := byte(0xff << (8 - remBits))
		if (sum[fullBytes] & mask) != 0 {
			t.Fatalf("remaining bits not zero: %02x with mask %02x", sum[fullBytes], mask)
		}
	}
}

func TestMappleResolveMovie(t *testing.T) {
	masterManifest := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080
https://source.example.com/video_1080p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720
https://source.example.com/video_720p.m3u8`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.Header.Get("Next-Action") == nextActionSubtitles:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("0:{\"a\":\"$@1\"}\n1:[{\"url\":\"https://subs.example.com/en.vtt\",\"lang\":\"en\",\"label\":\"English\"}]\n"))

		case r.URL.Path == "/watch/movie/550":
			http.SetCookie(w, &http.Cookie{Name: "_mapple_site", Value: "test-cookie-val"})
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/api/ui/session":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sessionResponse{Token: "test-req-token"})

		case r.URL.Path == "/api/media-session":
			w.Header().Set("Content-Type", "application/json")
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if _, hasPow := req["pow"]; hasPow {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"token":   "test-stream-token",
				})
			} else {
				_ = json.NewEncoder(w).Encode(mediaSessionResponse{
					Success:     true,
					RequiresPow: true,
					Pow: powParams{
						ChallengeID: "chal-1",
						Challenge:   "chal-data-123",
						Difficulty:  8,
					},
				})
			}

		case r.URL.Path == "/api/encrypt":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(encryptResponse{URL: "/api/stream-encrypted?data=enc_payload"})

		case r.URL.Path == "/api/stream-encrypted":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"stream_url": fmt.Sprintf("http://%s/master.m3u8", r.Host),
				},
			})

		case r.URL.Path == "/master.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(masterManifest))

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}

	if len(sources) != 2 {
		t.Fatalf("expected 2 sources (1080p and 720p), got %d", len(sources))
	}

	if sources[0].Quality != "1080p" {
		t.Errorf("sources[0].Quality = %q, want 1080p", sources[0].Quality)
	}
	if sources[1].Quality != "720p" {
		t.Errorf("sources[1].Quality = %q, want 720p", sources[1].Quality)
	}

	if len(sources[0].Subtitles) != 1 || sources[0].Subtitles[0].Language != "en" {
		t.Errorf("subtitles mismatch: %v", sources[0].Subtitles)
	}
}

func TestMappleResolveStream(t *testing.T) {
	masterManifest := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1920x1080
https://source.example.com/tv_1080p.m3u8`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.Header.Get("Next-Action") == nextActionSubtitles:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("0:{\"a\":\"$@1\"}\n1:[{\"url\":\"https://subs.example.com/tv_en.vtt\",\"lang\":\"en\",\"label\":\"English\"}]\n"))

		case strings.Contains(r.URL.Path, "/watch/tv/"):
			http.SetCookie(w, &http.Cookie{Name: "_mapple_site", Value: "test-cookie-val"})
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/api/ui/session":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sessionResponse{Token: "test-req-token"})

		case r.URL.Path == "/api/media-session":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mediaSessionResponse{
				Success:     true,
				RequiresPow: false,
				Token:       "direct-stream-token",
			})

		case r.URL.Path == "/api/encrypt":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(encryptResponse{URL: "/api/stream-encrypted?data=enc_payload_tv"})

		case r.URL.Path == "/api/stream-encrypted":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"stream_url": fmt.Sprintf("http://%s/master_tv.m3u8", r.Host),
				},
			})

		case r.URL.Path == "/master_tv.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(masterManifest))

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	updates := make(chan []provider.MediaSource, 2)
	err = client.ResolveStream(context.Background(), "1396", provider.Episode{
		TMDBID:  1396,
		Season:  1,
		Episode: 1,
	}, updates)
	close(updates)

	if err != nil {
		t.Fatalf("ResolveStream failed: %v", err)
	}

	var all []provider.MediaSource
	for batch := range updates {
		all = append(all, batch...)
	}

	if len(all) != 1 {
		t.Fatalf("expected 1 source, got %d", len(all))
	}
	if all[0].Quality != "1080p" {
		t.Errorf("quality = %q, want 1080p", all[0].Quality)
	}
}

func TestMappleResolveTV(t *testing.T) {
	masterManifest := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1920x1080
https://source.example.com/tv_1080p.m3u8`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.Header.Get("Next-Action") == nextActionSubtitles:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("0:{\"a\":\"$@1\"}\n1:[{\"url\":\"https://subs.example.com/tv_en.vtt\",\"lang\":\"en\",\"label\":\"English\"}]\n"))

		case strings.Contains(r.URL.Path, "/watch/tv/"):
			http.SetCookie(w, &http.Cookie{Name: "_mapple_site", Value: "test-cookie-val"})
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/api/ui/session":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sessionResponse{Token: "test-req-token"})

		case r.URL.Path == "/api/media-session":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mediaSessionResponse{
				Success:     true,
				RequiresPow: false,
				Token:       "direct-stream-token",
			})

		case r.URL.Path == "/api/encrypt":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(encryptResponse{URL: "/api/stream-encrypted?data=enc_payload_tv"})

		case r.URL.Path == "/api/stream-encrypted":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"stream_url": fmt.Sprintf("http://%s/master_tv.m3u8", r.Host),
				},
			})

		case r.URL.Path == "/master_tv.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(masterManifest))

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "1396", provider.Episode{
		TMDBID:  1396,
		Season:  1,
		Episode: 1,
	})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}

	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}

	if sources[0].Quality != "1080p" {
		t.Errorf("quality = %q, want 1080p", sources[0].Quality)
	}
	if len(sources[0].Subtitles) != 1 || sources[0].Subtitles[0].Language != "en" {
		t.Errorf("subtitles mismatch: %v", sources[0].Subtitles)
	}
}

func TestMappleResolveFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = client.ResolveSource(ctx, "999999", provider.Episode{TMDBID: 999999})
	if err == nil {
		t.Fatal("expected error on total upstream failure, got nil")
	}
}

func TestMappleParseCoordinates(t *testing.T) {
	tmdbID, mediaType, season, ep, err := parseCoordinates("550", provider.Episode{})
	if err != nil || tmdbID != 550 || mediaType != provider.MediaTypeMovie || season != 1 || ep != 1 {
		t.Errorf("movie coords failed: %d, %s, %d, %d, %v", tmdbID, mediaType, season, ep, err)
	}

	tmdbID, mediaType, season, ep, err = parseCoordinates("", provider.Episode{TMDBID: 273207, Season: 3, Episode: 4})
	if err != nil || tmdbID != 273207 || mediaType != provider.MediaTypeTV || season != 3 || ep != 4 {
		t.Errorf("tv coords failed: %d, %s, %d, %d, %v", tmdbID, mediaType, season, ep, err)
	}
}
