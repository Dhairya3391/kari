package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
)

const enrichmentMaster = "#EXTM3U\n" +
	"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"English\",LANGUAGE=\"en\"\n" +
	"#EXT-X-STREAM-INF:BANDWIDTH=23000000,RESOLUTION=3840x2160,CODECS=\"hvc1.2.4.L153.B0\"\n" +
	"fourk.m3u8\n" +
	"#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS=\"avc1.4d4028,mp4a.40.2\"\n" +
	"fullhd.m3u8\n"

func newEnrichmentServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://watch.example/" {
			http.Error(w, "missing referer", http.StatusForbidden)
			return
		}
		if r.Header.Get("User-Agent") != config.DesktopUserAgent {
			http.Error(w, "missing browser user agent", http.StatusForbidden)
			return
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/media.m3u8"):
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4.0,\nsegment.ts\n")
		case strings.HasSuffix(r.URL.Path, "/broken.m3u8"):
			http.Error(w, "gone", http.StatusNotFound)
		default:
			fmt.Fprint(w, enrichmentMaster)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestEnrichAdaptiveSources(t *testing.T) {
	server := newEnrichmentServer(t, 0)
	tests := []struct {
		name        string
		path        string
		mode        provider.ContentType
		quality     string
		kind        string
		wantQuality string
		wantBitrate int
	}{
		{name: "adaptive HLS master", path: "/master.m3u8", mode: provider.ModeMovies,
			quality: "Auto", kind: provider.SourceTypeHLS,
			wantQuality: "4K (2160p)", wantBitrate: 23_000_000},
		{name: "language suffix preserved", path: "/master.m3u8", mode: provider.ModeAnime,
			quality: "Auto (Hindi)", kind: provider.SourceTypeHLS,
			wantQuality: "4K (2160p) (Hindi)", wantBitrate: 23_000_000},
		{name: "media playlist unchanged", path: "/media.m3u8", mode: provider.ModeMovies,
			quality: "Auto", kind: provider.SourceTypeHLS,
			wantQuality: "Auto", wantBitrate: 0},
		{name: "probe failure keeps Auto", path: "/broken.m3u8", mode: provider.ModeMovies,
			quality: "Auto", kind: provider.SourceTypeHLS,
			wantQuality: "Auto", wantBitrate: 0},
		{name: "non HLS untouched", path: "/clip.mp4", mode: provider.ModeMovies,
			quality: "Auto", kind: provider.SourceTypeMP4,
			wantQuality: "Auto", wantBitrate: 0},
		{name: "explicit quality skipped entirely", path: "/master.m3u8", mode: provider.ModeMovies,
			quality: "1080p", kind: provider.SourceTypeHLS,
			wantQuality: "1080p", wantBitrate: 0},
		{name: "live skipped", path: "/master.m3u8", mode: provider.ModeLive,
			quality: "Auto", kind: provider.SourceTypeHLS,
			wantQuality: "Auto", wantBitrate: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sources := []provider.MediaSource{{
				URL:     server.URL + tc.path,
				Quality: tc.quality,
				Type:    tc.kind,
				Referer: "https://watch.example/",
			}}
			got, published := enrichAdaptiveSources(context.Background(), httpclient.NewWithTimeout(manifestProbeTimeout), sources, tc.mode, nil)
			src := got[0]
			if src.Quality != tc.wantQuality || src.Bitrate != tc.wantBitrate {
				t.Fatalf("quality/bitrate = %q/%d, want %q/%d", src.Quality, src.Bitrate, tc.wantQuality, tc.wantBitrate)
			}
			wantPublished := tc.wantQuality != tc.quality
			if published != wantPublished {
				t.Fatalf("published = %v, want %v", published, wantPublished)
			}
		})
	}
}

// A real Miruro master takes about a second to answer. The enrichment pass
// must still relabel it rather than giving up at a short deadline.
func TestEnrichAdaptiveSourcesWithSlowMaster(t *testing.T) {
	server := newEnrichmentServer(t, 700*time.Millisecond)
	sources := []provider.MediaSource{{
		URL: server.URL + "/master.m3u8", Quality: "Auto", Type: provider.SourceTypeHLS,
		Referer: "https://watch.example/",
	}}
	got, changed := enrichAdaptiveSources(context.Background(), httpclient.NewWithTimeout(manifestClientTimeout), sources, provider.ModeMovies, nil)
	if !changed {
		t.Fatal("slow master was not enriched")
	}
	if got[0].Quality != "4K (2160p)" || got[0].Bitrate != 23_000_000 {
		t.Fatalf("slow master = %q/%d, want 4K (2160p)/23000000", got[0].Quality, got[0].Bitrate)
	}
}

func TestEnrichAdaptiveSourcesPublishesProgressively(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "slow") {
			time.Sleep(120 * time.Millisecond)
		}
		fmt.Fprint(w, enrichmentMaster)
	}))
	defer server.Close()

	sources := []provider.MediaSource{
		{URL: server.URL + "/fast.m3u8", Quality: "Auto", Type: provider.SourceTypeHLS},
		{URL: server.URL + "/slow.m3u8", Quality: "Auto", Type: provider.SourceTypeHLS},
	}
	publishes := 0
	_, published := enrichAdaptiveSources(context.Background(), httpclient.NewWithTimeout(manifestProbeTimeout), sources, provider.ModeMovies,
		func([]provider.MediaSource) { publishes++ })
	if !published {
		t.Fatal("expected enrichment to publish snapshots")
	}
	if publishes < 2 {
		t.Fatalf("publishes = %d, want one per finished source plus a final snapshot", publishes)
	}
}

// Anime sub and dub identity lives in Language/SubType/ExtraArgs, never in
// AudioLabel. Both probes and dedupe must leave those fields untouched.
func TestEnrichmentPreservesAnimeSubDubIdentity(t *testing.T) {
	server := newEnrichmentServer(t, 0)
	sources := []provider.MediaSource{
		{
			URL: server.URL + "/sub.m3u8", Quality: "Auto", Type: provider.SourceTypeHLS,
			Referer: "https://watch.example/", Language: "ja", SubType: provider.SubTypeSoft,
			ExtraArgs: []string{"--sub-file=sub.ass"},
		},
		{
			URL: server.URL + "/dub.m3u8", Quality: "Auto", Type: provider.SourceTypeHLS,
			Referer: "https://watch.example/", Language: "en", SubType: provider.SubTypeHard,
			ExtraArgs: []string{"--alang=en"},
		},
	}
	enriched, _ := enrichAdaptiveSources(context.Background(), httpclient.NewWithTimeout(manifestProbeTimeout), sources, provider.ModeAnime, nil)
	enriched = discoverManifestAudio(context.Background(), httpclient.NewWithTimeout(manifestProbeTimeout), enriched)

	wants := []struct {
		language string
		subType  string
		extra    string
	}{
		{language: "ja", subType: provider.SubTypeSoft, extra: "--sub-file=sub.ass"},
		{language: "en", subType: provider.SubTypeHard, extra: "--alang=en"},
	}
	for i, want := range wants {
		got := enriched[i]
		if got.Language != want.language || got.SubType != want.subType || len(got.ExtraArgs) != 1 || got.ExtraArgs[0] != want.extra {
			t.Errorf("source %d identity = %q/%q/%v, want %q/%q/%q", i,
				got.Language, got.SubType, got.ExtraArgs, want.language, want.subType, want.extra)
		}
	}
	if len(dropShadowedTiers(enriched, []bool{true, true})) != 2 {
		t.Fatal("sub and dub rows were collapsed into one")
	}
}

func TestDropShadowedTiers(t *testing.T) {
	master := provider.MediaSource{URL: "master", Quality: "1080p", Backend: "lane", Type: provider.SourceTypeHLS}
	tier := provider.MediaSource{URL: "tier", Quality: "1080p", Backend: "lane", Type: provider.SourceTypeHLS}
	otherLane := provider.MediaSource{URL: "other", Quality: "1080p", Backend: "other", Type: provider.SourceTypeHLS}
	audio := provider.MediaSource{URL: "dub", Quality: "1080p", Backend: "lane", Language: "hi", Type: provider.SourceTypeHLS}

	got := dropShadowedTiers(
		[]provider.MediaSource{master, tier, otherLane, audio},
		[]bool{true, false, false, false},
	)
	if len(got) != 3 {
		t.Fatalf("sources = %+v, want the duplicate tier dropped", got)
	}
	for _, src := range got {
		if src.URL == "tier" {
			t.Fatal("explicit tier was not dropped")
		}
	}
}
