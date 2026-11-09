package manga

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gen2brain/avif"

	"kari/internal/provider"
)

// tinyWebP is a 2x2 white WebP page, matching the format Atsu returns.
const tinyWebP = "UklGRiQAAABXRUJQVlA4IBgAAAAwAQCdASoCAAIAAgA0JaQAA3AA/vuUAAA="

func TestDecodeWebP(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(tinyWebP)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	img, err := decode(data)
	if err != nil {
		t.Fatalf("decode webp: %v", err)
	}
	if got := img.Bounds().Dx(); got != 2 {
		t.Errorf("webp width = %d, want 2", got)
	}
}

func TestDecodeAVIF(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 2, 3))
	var data bytes.Buffer
	if err := avif.Encode(&data, source); err != nil {
		t.Fatalf("encode AVIF fixture: %v", err)
	}

	img, err := decode(data.Bytes())
	if err != nil {
		t.Fatalf("decode AVIF: %v", err)
	}
	if got := img.Bounds().Size(); got.X != 2 || got.Y != 3 {
		t.Errorf("AVIF dimensions = %v, want (2, 3)", got)
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode failed: %v", err)
	}
	return buf.Bytes()
}

// imageServers returns primary/fallback servers with scripted statuses
// and a hit counter per server.
func imageServers(t *testing.T, primaryStatus, fallbackStatus int, payload []byte) (string, string, *int, *int) {
	t.Helper()
	primaryHits, fallbackHits := 0, 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits++
		if primaryStatus != http.StatusOK {
			w.WriteHeader(primaryStatus)
			return
		}
		w.Write(payload)
	}))
	t.Cleanup(primary.Close)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		if fallbackStatus != http.StatusOK {
			w.WriteHeader(fallbackStatus)
			return
		}
		w.Write(payload)
	}))
	t.Cleanup(fallback.Close)
	return primary.URL + "/p.jpg", fallback.URL + "/s.jpg", &primaryHits, &fallbackHits
}

func TestFetchImageFallbackOn404(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	payload := pngBytes(t)
	primary, fallback, _, fallbackHits := imageServers(t, http.StatusNotFound, http.StatusOK, payload)

	img, err := NewClient().FetchImage(context.Background(), provider.MangaPage{URL: primary, FallbackURL: fallback})
	if err != nil || img == nil {
		t.Fatalf("img = %v, err = %v", img, err)
	}
	if *fallbackHits != 1 {
		t.Errorf("fallback hits = %d, want 1", *fallbackHits)
	}
}

func TestFetchImageDouble404(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	primary, fallback, _, _ := imageServers(t, http.StatusNotFound, http.StatusNotFound, pngBytes(t))

	_, err := NewClient().FetchImage(context.Background(), provider.MangaPage{URL: primary, FallbackURL: fallback})
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusNotFound {
		t.Errorf("expected 404 HTTPError, got %v", err)
	}
}

func TestFetchImageFatalAborts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	primary, fallback, _, fallbackHits := imageServers(t, http.StatusForbidden, http.StatusOK, pngBytes(t))

	_, err := NewClient().FetchImage(context.Background(), provider.MangaPage{URL: primary, FallbackURL: fallback})
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Errorf("expected 403 HTTPError, got %v", err)
	}
	if *fallbackHits != 0 {
		t.Errorf("fatal error must abort: fallback hits = %d", *fallbackHits)
	}
}

func TestFetchImageSendsOriginAndAVIFAccept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wantReferer := "https://atsu.example/"
	wantOrigin := "https://atsu.example"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != wantReferer || r.Header.Get("Origin") != wantOrigin {
			t.Errorf("Referer/Origin = %q/%q", r.Header.Get("Referer"), r.Header.Get("Origin"))
		}
		if !strings.Contains(r.Header.Get("Accept"), "image/avif") {
			t.Errorf("Accept = %q, want AVIF support", r.Header.Get("Accept"))
		}
		_, _ = w.Write(pngBytes(t))
	}))
	defer server.Close()

	_, err := NewClient().FetchImage(context.Background(), provider.MangaPage{
		URL:     server.URL + "/page.webp",
		Referer: wantReferer,
		Origin:  wantOrigin,
	})
	if err != nil {
		t.Fatalf("FetchImage: %v", err)
	}
}
