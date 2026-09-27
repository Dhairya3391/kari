package poster

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/util"
)

func pngPayload(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode failed: %v", err)
	}
	return buf.Bytes()
}

func testURLClient() *Client {
	return &Client{
		http:     http.DefaultClient,
		imgCache: util.NewBoundedCache[image.Image](8),
	}
}

// Every TMDB poster fetches at full original resolution — no per-type
// switch that a mismatched media type could silently downgrade. The size
// rides in the cache key so upgrades never serve stale smaller art.
func TestTMDBPosterSizeSelection(t *testing.T) {
	if tmdbPosterSize != "original" {
		t.Errorf("tmdbPosterSize = %q, want original", tmdbPosterSize)
	}
	if k1, k2 := cacheKey(42, "T", "original"), cacheKey(42, "T", "w500"); k1 == k2 {
		t.Errorf("cache keys must differ by size: %q", k1)
	}
	if k1, k2 := cacheKey(0, "Frieren", ""), cacheKey(0, "Frieren", ""); k1 == "" || k1 != k2 {
		t.Errorf("anilist keys must stay stable: %q %q", k1, k2)
	}
}

func TestFetchImageURLSendsSiteReferer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	payload := pngPayload(t)
	var gotReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("Referer")
		w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	img, err := testURLClient().FetchImageURL(context.Background(), srv.URL+"/cover/fallback/abc.jpg", "https://weebcentral.com/")
	if err != nil || img == nil {
		t.Fatalf("img = %v, err = %v", img, err)
	}
	if gotReferer == "" {
		t.Errorf("direct cover download must send a site Referer for hotlink protection")
	}
}
