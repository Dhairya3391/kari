package atlantic

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/config"
	"kari/internal/provider"
	"kari/internal/tmdb"
)

func encryptAESGCM(t *testing.T, keyHex, plain, prefix string) string {
	t.Helper()
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatalf("hex decode key: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("new gcm: %v", err)
	}
	nonce := make([]byte, 12)
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	sealed := gcm.Seal(nil, nonce, []byte(plain), nil)
	payload := append(nonce, sealed...)
	return prefix + hex.EncodeToString(payload)
}

func TestClientIdentityAndModes(t *testing.T) {
	pool := tmdb.NewKeyPool([]string{"dummy-key"})
	c, err := NewClient(pool)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if got := c.Name(); got != "atlantic" {
		t.Errorf("Name() = %q, want atlantic", got)
	}
	if got := c.Alias(); got != "Atlantic" {
		t.Errorf("Alias() = %q, want Atlantic", got)
	}

	modes := c.Modes()
	if len(modes) != 3 {
		t.Fatalf("len(Modes()) = %d, want 3", len(modes))
	}
	wantModes := map[provider.ContentType]bool{
		provider.ModeMovies:  true,
		provider.ModeTV:      true,
		provider.ModeCartoon: true,
	}
	for _, m := range modes {
		if !wantModes[m.Name] {
			t.Errorf("unexpected mode: %s", m.Name)
		}
		if m.Priority != 2 {
			t.Errorf("mode %s priority = %d, want 2", m.Name, m.Priority)
		}
	}
}

func TestAESGCMDecryption(t *testing.T) {
	origURL := "https://stream.example.com/hls/master.m3u8"

	// Helios
	encHelios := encryptAESGCM(t, heliosKeyHex, origURL, heliosPrefix)
	decHelios, err := decryptAESGCM(heliosKeyHex, encHelios, heliosPrefix)
	if err != nil {
		t.Fatalf("helios decrypt failed: %v", err)
	}
	if decHelios != origURL {
		t.Errorf("helios got %q, want %q", decHelios, origURL)
	}

	// Moscow
	encMoscow := encryptAESGCM(t, moscowKeyHex, origURL, moscowPrefix)
	decMoscow, err := decryptAESGCM(moscowKeyHex, encMoscow, moscowPrefix)
	if err != nil {
		t.Fatalf("moscow decrypt failed: %v", err)
	}
	if decMoscow != origURL {
		t.Errorf("moscow got %q, want %q", decMoscow, origURL)
	}

	// Planets
	encPlanets := encryptAESGCM(t, planetsKeyHex, origURL, planetsPrefix)
	decPlanets, err := decryptAESGCM(planetsKeyHex, encPlanets, planetsPrefix)
	if err != nil {
		t.Fatalf("planets decrypt failed: %v", err)
	}
	if decPlanets != origURL {
		t.Errorf("planets got %q, want %q", decPlanets, origURL)
	}

	// Unprefixed passthrough
	raw := "https://plain.example.com/index.m3u8"
	decRaw, err := decryptAESGCM(heliosKeyHex, raw, heliosPrefix)
	if err != nil || decRaw != raw {
		t.Errorf("unprefixed got %q, err %v", decRaw, err)
	}

	// Too short payload
	short := heliosPrefix + "aabbcc"
	if _, err := decryptAESGCM(heliosKeyHex, short, heliosPrefix); err == nil {
		t.Error("expected error for short payload")
	}
}

func TestResolveStreamWithMockServer(t *testing.T) {
	heliosStream := "https://cdn.example.com/helios.m3u8"
	moscowStream := "https://cdn.example.com/moscow.m3u8"
	saturnStream := "https://cdn.example.com/saturn.m3u8"
	athenaStream := "https://cdn.example.com/athena.m3u8"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify required browser headers
		if r.Header.Get("Referer") == "" {
			t.Errorf("missing Referer header on %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != config.DesktopUserAgent {
			t.Errorf("unexpected User-Agent: %s", r.Header.Get("User-Agent"))
		}

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/helios":
			enc := encryptAESGCM(t, heliosKeyHex, heliosStream, heliosPrefix)
			json.NewEncoder(w).Encode(map[string]any{
				"sources": map[string]any{
					"Moscow": map[string]string{"url": enc},
				},
			})
		case r.URL.Path == "/moscow":
			enc := encryptAESGCM(t, moscowKeyHex, moscowStream, moscowPrefix)
			json.NewEncoder(w).Encode(map[string]any{
				"sources": map[string]any{
					"Moscow": map[string]string{"url": enc},
				},
			})
		case r.URL.Path == "/planets":
			enc := encryptAESGCM(t, planetsKeyHex, saturnStream, planetsPrefix)
			json.NewEncoder(w).Encode(map[string]any{
				"sources": map[string]any{
					"Saturn": map[string]string{"url": enc},
				},
			})
		case r.URL.Path == "/cdn":
			json.NewEncoder(w).Encode(map[string]any{
				"ready": true,
				"url":   athenaStream,
			})
		case strings.HasPrefix(r.URL.Path, "/v1/movie/"):
			json.NewEncoder(w).Encode([]map[string]string{
				{"label": "Spanish", "file": "https://subs.example.com/es.vtt"},
			})
		case r.URL.Path == "/subs":
			json.NewEncoder(w).Encode(map[string]any{
				"subtitles": []map[string]string{
					{"url": "https://subs.example.com/fr.vtt", "language": "French"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	pool := tmdb.NewKeyPool([]string{"dummy-key"})
	c, err := NewClientWithURLs(
		pool,
		srv.URL,
		srv.URL,
		srv.URL,
		srv.URL,
		srv.URL,
	)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test ResolveSource
	sources, err := c.ResolveSource(ctx, "550", provider.Episode{TMDBID: 550})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}

	if len(sources) < 4 {
		t.Fatalf("expected at least 4 sources, got %d", len(sources))
	}

	for _, s := range sources {
		if s.Referer != srv.URL+"/" {
			t.Errorf("source Referer = %q, want %q", s.Referer, srv.URL+"/")
		}
		if s.Type != provider.SourceTypeHLS {
			t.Errorf("source Type = %q, want %q", s.Type, provider.SourceTypeHLS)
		}
		if len(s.ExtraArgs) != 3 {
			t.Errorf("source ExtraArgs length = %d, want 3", len(s.ExtraArgs))
		}
		if len(s.Subtitles) == 0 {
			t.Errorf("expected source to carry subtitles")
		}
	}
}

func TestResolveTVEpisode(t *testing.T) {
	tvStream := "https://cdn.example.com/breakingbad_s1e1.m3u8"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/cdn":
			json.NewEncoder(w).Encode(map[string]any{
				"ready": true,
				"url":   tvStream,
			})
		case strings.HasPrefix(r.URL.Path, "/v1/tv/"):
			json.NewEncoder(w).Encode([]map[string]string{
				{"label": "English", "file": "https://subs.example.com/tv_en.vtt"},
			})
		case r.URL.Path == "/subs":
			json.NewEncoder(w).Encode(map[string]any{
				"subtitles": []map[string]string{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	pool := tmdb.NewKeyPool([]string{"dummy-key"})
	c, err := NewClientWithURLs(pool, srv.URL, srv.URL, srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sources, err := c.ResolveSource(ctx, "1396", provider.Episode{TMDBID: 1396, Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if sources[0].URL != tvStream {
		t.Errorf("got URL %q, want %q", sources[0].URL, tvStream)
	}
	if sources[0].Quality != "1080p (Athena)" {
		t.Errorf("got Quality %q, want '1080p (Athena)'", sources[0].Quality)
	}
}

func TestResolveSourceReturnsNoSourcesOnEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/cdn":
			json.NewEncoder(w).Encode(map[string]any{"ready": false})
		case r.URL.Path == "/helios" || r.URL.Path == "/moscow" || r.URL.Path == "/planets":
			json.NewEncoder(w).Encode(map[string]any{"sources": map[string]any{}})
		case r.URL.Path == "/shegu":
			json.NewEncoder(w).Encode(map[string]any{"qualities": []any{}})
		case strings.HasPrefix(r.URL.Path, "/v1/movie/") || r.URL.Path == "/subs":
			json.NewEncoder(w).Encode([]any{})
		default:
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	pool := tmdb.NewKeyPool([]string{"dummy-key"})
	c, err := NewClientWithURLs(pool, srv.URL, srv.URL, srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = c.ResolveSource(ctx, "999999", provider.Episode{TMDBID: 999999})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("expected ErrNoSources, got %v", err)
	}
}
