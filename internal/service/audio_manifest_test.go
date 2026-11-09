package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"kari/internal/provider"
)

func TestDiscoverManifestAudioAcrossSources(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Referer") != "https://watch.example/" || r.Header.Get("User-Agent") != "test-agent" || r.Header.Get("Cookie") != "session=abc" {
			http.Error(w, "missing playback headers", http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"English\",LANGUAGE=\"en\"\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"हिन्दी, Hindi\",LANGUAGE=\"hi\"\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",LANGUAGE=\"ja\"\n")
	}))
	defer server.Close()

	sources := []provider.MediaSource{
		{URL: server.URL + "/one.m3u8", Referer: "https://watch.example/", UserAgent: "test-agent", CookieHeader: "session=abc"},
		{URL: server.URL + "/two.M3U8?token=x", Referer: "https://watch.example/", UserAgent: "test-agent", CookieHeader: "session=abc"},
		{URL: server.URL + "/proxy?token=x", Type: provider.SourceTypeHLS, AudioLabel: "Provider label", Referer: "https://watch.example/", UserAgent: "test-agent", CookieHeader: "session=abc"},
		{URL: server.URL + "/clip.mp4"},
		{URL: "not a URL"},
	}
	got := discoverManifestAudio(context.Background(), sources)

	want := "English · Japanese · हिन्दी, Hindi"
	if got[0].AudioLabel != want || got[1].AudioLabel != want {
		t.Fatalf("discovered labels = %q and %q, want %q", got[0].AudioLabel, got[1].AudioLabel, want)
	}
	if got[2].AudioLabel != want {
		t.Errorf("manifest label = %q, want it to override provider metadata %q", got[2].AudioLabel, want)
	}
	if got[3].AudioLabel != "" || got[4].AudioLabel != "" {
		t.Errorf("non-HLS or malformed sources unexpectedly got labels: %+v", got[3:])
	}
	if requests.Load() != 3 {
		t.Errorf("manifest requests = %d, want 3", requests.Load())
	}
}

func TestHLSEmptyOrNonMasterManifestHasNoAudioLabels(t *testing.T) {
	for _, manifest := range []string{
		"",
		"#EXTM3U\n#EXTINF:4.0,\nsegment.ts\n",
		"#EXTM3U\n#EXT-X-MEDIA:TYPE=SUBTITLES,NAME=\"English\",LANGUAGE=\"en\"\n",
	} {
		if got := hlsAudioLabels(manifest); len(got) != 0 {
			t.Errorf("hlsAudioLabels(%q) = %v, want none", manifest, got)
		}
	}
}

func TestHLSAudioLabelsPreferLanguageForGenericNames(t *testing.T) {
	manifest := "#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"Audio\",LANGUAGE=\"hi\"\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"Unknown\",LANGUAGE=\"und\"\n"
	got := hlsAudioLabels(manifest)
	if len(got) != 1 || got[0] != "Hindi" {
		t.Fatalf("hlsAudioLabels() = %v, want [Hindi]", got)
	}
}
