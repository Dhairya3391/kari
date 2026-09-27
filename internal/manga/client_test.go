package manga

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
)

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
