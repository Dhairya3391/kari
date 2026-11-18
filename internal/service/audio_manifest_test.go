package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"kari/internal/httpclient"
	"kari/internal/provider"
)

func TestDiscoverManifestAudioAcrossSources(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Referer") != "https://watch.example/" || r.Header.Get("Origin") != "https://watch.example" || r.Header.Get("User-Agent") != "test-agent" || r.Header.Get("Cookie") != "session=abc" {
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
	got := discoverManifestAudio(context.Background(), httpclient.NewWithTimeout(manifestProbeTimeout), sources)

	want := "English · Hindi · Japanese"
	for _, index := range []int{0, 1, 2} {
		if got[index].AudioLabel != want {
			t.Errorf("source %d label = %q, want %q", index, got[index].AudioLabel, want)
		}
		if got[index].PreferredAudio != "en" {
			t.Errorf("source %d preferred audio = %q, want the first declared code en", index, got[index].PreferredAudio)
		}
		if codes := got[index].Audio.Languages; !slices.Equal(codes, []string{"en", "hi", "ja"}) {
			t.Errorf("source %d audio codes = %v, want [en hi ja]", index, codes)
		}
	}
	if got[2].AudioLabel == "Provider label" {
		t.Error("manifest audio did not override the provider-supplied label")
	}
	if got[3].AudioLabel != "" || got[4].AudioLabel != "" {
		t.Errorf("non-HLS or malformed sources unexpectedly got labels: %+v", got[3:])
	}
	if requests.Load() != 3 {
		t.Errorf("manifest requests = %d, want 3", requests.Load())
	}
}

func TestManifestAudioOverridesProviderLanguage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"English\",LANGUAGE=\"en\",DEFAULT=YES\n")
	}))
	defer server.Close()

	sources := []provider.MediaSource{{
		URL:      server.URL + "/variant.m3u8",
		Type:     provider.SourceTypeHLS,
		Language: "ja",
	}}
	got := discoverManifestAudio(context.Background(), httpclient.NewWithTimeout(manifestProbeTimeout), sources)

	if got[0].PreferredAudio != "en" || got[0].AudioPreference() != "en" {
		t.Errorf("audio preference = %q/%q, want the manifest's en to win over the provider's ja",
			got[0].PreferredAudio, got[0].AudioPreference())
	}
	if codes := got[0].Audio.Languages; !slices.Equal(codes, []string{"en"}) {
		t.Errorf("audio codes = %v, want [en]", codes)
	}
	if got[0].AudioLabel != "English" {
		t.Errorf("audio label = %q, want English", got[0].AudioLabel)
	}
}

func TestHLSEmptyOrNonMasterManifestHasNoAudioTracks(t *testing.T) {
	for _, manifest := range []string{
		"",
		"#EXTM3U\n#EXTINF:4.0,\nsegment.ts\n",
		"#EXTM3U\n#EXT-X-MEDIA:TYPE=SUBTITLES,NAME=\"English\",LANGUAGE=\"en\"\n",
	} {
		if got := hlsAudioTracks(manifest); len(got) != 0 {
			t.Errorf("hlsAudioTracks(%q) = %v, want none", manifest, got)
		}
	}
}

func TestHLSAudioTracksPreferLanguageForGenericNames(t *testing.T) {
	manifest := "#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"Audio\",LANGUAGE=\"hi\"\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"Unknown\",LANGUAGE=\"und\"\n"
	got := hlsAudioTracks(manifest)
	if len(got) != 1 || got[0].Code != "hi" {
		t.Fatalf("hlsAudioTracks() = %v, want one track with code hi", got)
	}
}

func TestHLSAudioTracksTrustLanguageOverMisleadingName(t *testing.T) {
	// A mislabeled NAME must not become the displayed or selected language.
	manifest := "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"English\",LANGUAGE=\"hi\"\n"
	got := hlsAudioTracks(manifest)
	if len(got) != 1 || got[0].Code != "hi" {
		t.Fatalf("hlsAudioTracks() = %v, want the LANGUAGE code hi, not the NAME English", got)
	}
}

func TestHLSAudioTracksNormalizeRegionalCodes(t *testing.T) {
	manifest := "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"English\",LANGUAGE=\"en-US\"\n"
	got := hlsAudioTracks(manifest)
	if len(got) != 1 || got[0].Code != "en" {
		t.Fatalf("hlsAudioTracks() = %v, want the base code en", got)
	}
}

func TestHLSAudioTracksDefaultRenditionComesFirst(t *testing.T) {
	manifest := "#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"Hindi\",LANGUAGE=\"hi\"\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"English\",LANGUAGE=\"en\",DEFAULT=YES\n"
	got := hlsAudioTracks(manifest)
	codes := make([]string, 0, len(got))
	for _, track := range got {
		codes = append(codes, track.Code)
	}
	if !slices.Equal(codes, []string{"en", "hi"}) {
		t.Fatalf("hlsAudioTracks() codes = %v, want [en hi] with the default first", codes)
	}
}

func TestDiscoverManifestAudioProbesMasterURL(t *testing.T) {
	var probed atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probed.Store(r.URL.Path)
		if r.URL.Path == "/master.m3u8" {
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"English\",LANGUAGE=\"en\"\n")
			return
		}
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:4.0,\nvariant.ts\n")
	}))
	defer server.Close()

	sources := []provider.MediaSource{{
		URL:         server.URL + "/variant.m3u8",
		ManifestURL: server.URL + "/master.m3u8",
	}}
	got := discoverManifestAudio(context.Background(), httpclient.NewWithTimeout(manifestProbeTimeout), sources)
	if got[0].AudioLabel != "English" || got[0].PreferredAudio != "en" {
		t.Fatalf("master probe = %q/%q, want English/en", got[0].AudioLabel, got[0].PreferredAudio)
	}
	if path, _ := probed.Load().(string); path != "/master.m3u8" {
		t.Errorf("probed path = %q, want the master playlist", path)
	}
}
