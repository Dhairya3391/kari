package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyLiveStream(t *testing.T) {
	const playlist = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nstream.m3u8\n"
	const mediaPlaylist = "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg0.ts\n"
	const deadVariant = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\ndead.m3u8\n"
	const deadSegment = "#EXTM3U\n#EXTINF:6.0,\nmissing.ts\n"
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		switch r.URL.Path {
		case "/live.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(playlist))
		case "/stream.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(mediaPlaylist))
		case "/seg0.ts":
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write([]byte("\x47\x40\x00\x10"))
		case "/dead-variant.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(deadVariant))
		case "/dead-segment.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(deadSegment))
		case "/html.m3u8":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>not a stream</html>"))
		case "/bad.m3u8":
			http.Error(w, "gone", http.StatusForbidden)
		case "/empty.mp4":
			w.WriteHeader(http.StatusOK)
		case "/video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("\x00\x00\x00\x18ftypmp42"))
		case "/no-type":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(playlist))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cases := []struct {
		name    string
		source  MediaSource
		wantErr bool
	}{
		{name: "hls playlist", source: MediaSource{URL: server.URL + "/live.m3u8", Type: SourceTypeHLS}},
		{name: "hls detected by url", source: MediaSource{URL: server.URL + "/live.m3u8"}},
		{name: "hls detected by content type", source: MediaSource{URL: server.URL + "/no-type"}},
		{name: "hls dead variant", source: MediaSource{URL: server.URL + "/dead-variant.m3u8", Type: SourceTypeHLS}, wantErr: true},
		{name: "hls dead segment", source: MediaSource{URL: server.URL + "/dead-segment.m3u8", Type: SourceTypeHLS}, wantErr: true},
		{name: "hls html error page", source: MediaSource{URL: server.URL + "/html.m3u8", Type: SourceTypeHLS}, wantErr: true},
		{name: "hls forbidden", source: MediaSource{URL: server.URL + "/bad.m3u8", Type: SourceTypeHLS}, wantErr: true},
		{name: "empty body", source: MediaSource{URL: server.URL + "/empty.mp4", Type: SourceTypeMP4}, wantErr: true},
		{name: "direct mp4", source: MediaSource{URL: server.URL + "/video.mp4", Type: SourceTypeMP4}},
		{name: "empty url", source: MediaSource{}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyLiveStream(context.Background(), server.Client(), tc.source)
			if (err != nil) != tc.wantErr {
				t.Fatalf("VerifyLiveStream err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}

	// Transport metadata must reach the probe exactly as playback sends it.
	seenHeaders = nil
	source := MediaSource{
		URL:            server.URL + "/live.m3u8",
		Type:           SourceTypeHLS,
		Referer:        "https://referer.example/",
		UserAgent:      "KariProbe/1.0",
		CookieHeader:   "session=abc",
		SuppressOrigin: false,
	}
	if err := VerifyLiveStream(context.Background(), server.Client(), source); err != nil {
		t.Fatalf("probe with headers: %v", err)
	}
	if got := seenHeaders.Get("Referer"); got != "https://referer.example/" {
		t.Errorf("Referer = %q", got)
	}
	if got := seenHeaders.Get("Origin"); got != "https://referer.example" {
		t.Errorf("Origin = %q", got)
	}
	if got := seenHeaders.Get("User-Agent"); got != "KariProbe/1.0" {
		t.Errorf("User-Agent = %q", got)
	}
	if got := seenHeaders.Get("Cookie"); got != "session=abc" {
		t.Errorf("Cookie = %q", got)
	}
	if got := seenHeaders.Get("Range"); !strings.HasPrefix(got, "bytes=0-") {
		t.Errorf("Range = %q, want ranged GET", got)
	}

	// SuppressOrigin keeps CDNs that reject any Origin header working.
	seenHeaders = nil
	source.SuppressOrigin = true
	if err := VerifyLiveStream(context.Background(), server.Client(), source); err != nil {
		t.Fatalf("probe with suppressed origin: %v", err)
	}
	if got := seenHeaders.Get("Origin"); got != "" {
		t.Errorf("Origin = %q, want empty", got)
	}
}

func TestVerifyLiveStreamNilClient(t *testing.T) {
	if err := VerifyLiveStream(context.Background(), nil, MediaSource{URL: "https://example.com/live.m3u8"}); err == nil {
		t.Fatal("expected error for nil client")
	}
}
