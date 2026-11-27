package q62

import (
	"strings"
	"testing"

	"kari/internal/provider"
)

func TestQ62ResolveMovie(t *testing.T) {
	env := newTestEnv(t)
	sources, err := env.client.ResolveSource(t.Context(), "MOVIE1", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 3 {
		t.Fatalf("sources = %d, want 2 tiers + Auto: %+v", len(sources), sources)
	}

	master := testProxyBase + "/stream/MOVIE1/TOKEN/vod/master.m3u8?token=abc&expires=99"
	if sources[0].URL != testProxyBase+"/stream/MOVIE1/TOKEN/vod/1080p.m3u8?token=abc&expires=99" {
		t.Errorf("top tier URL = %q", sources[0].URL)
	}
	if sources[0].Height != 1080 || sources[0].Quality != "1080p" {
		t.Errorf("top tier = %+v", sources[0])
	}
	if sources[2].URL != master || sources[2].Quality != "Auto" {
		t.Errorf("master row = %+v", sources[2])
	}
	for _, source := range sources {
		if source.Backend != "FHD_1080P/WEB" {
			t.Errorf("backend = %q, want FHD_1080P/WEB", source.Backend)
		}
		if source.Type != provider.SourceTypeHLS {
			t.Errorf("type = %q, want hls", source.Type)
		}
		// The adaptive master and the video-only tier need the master URL for
		// rendition probing; the muxed tier must not attach its own audio again.
		if source.Quality == "Auto" || source.Quality == "720p" {
			if source.ManifestURL != master {
				t.Errorf("%s manifest URL = %q, want %q", source.Quality, source.ManifestURL, master)
			}
		} else if source.ManifestURL != "" {
			t.Errorf("muxed tier manifest URL = %q, want empty", source.ManifestURL)
		}
		if len(source.Audio.Languages) != 1 || source.Audio.Languages[0] != "en" {
			t.Errorf("audio languages = %v, want [en]", source.Audio.Languages)
		}
		if len(source.Subtitles) != 1 {
			t.Fatalf("subtitles = %+v", source.Subtitles)
		}
		if want := testProxyBase + "/stream/MOVIE1/TOKEN/vod/en-US-full.vtt?token=abc&expires=99"; source.Subtitles[0].URL != want {
			t.Errorf("subtitle URL = %q, want %q", source.Subtitles[0].URL, want)
		}
		if source.Subtitles[0].Language != "en" {
			t.Errorf("subtitle language = %q, want en", source.Subtitles[0].Language)
		}
	}
}

func TestQ62ResolveTVEpisode(t *testing.T) {
	env := newTestEnv(t)
	sources, err := env.client.ResolveSource(t.Context(), "SHOW1", provider.Episode{
		ID: "SEASON1" + episodeHandleSep + "EP1", Season: 1, Episode: 1, TMDBID: 247718,
	})
	if err != nil {
		t.Fatalf("ResolveSource tv: %v", err)
	}
	if len(sources) != 3 {
		t.Fatalf("sources = %d, want 3: %+v", len(sources), sources)
	}
	if !strings.Contains(sources[0].URL, "/stream/EP1/TOKEN/vod/") {
		t.Errorf("episode source URL = %q", sources[0].URL)
	}
}

func TestQ62ResolveByTMDBIDFallback(t *testing.T) {
	env := newTestEnv(t)
	// A numeric media ID means another provider supplied the title. The
	// catalog index must map it before Q62 can address its own content.
	sources, err := env.client.ResolveSource(t.Context(), "27205", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("ResolveSource fallback: %v", err)
	}
	if len(sources) == 0 || !strings.Contains(sources[0].URL, "/stream/MOVIE1/TOKEN/") {
		t.Fatalf("fallback sources = %+v", sources)
	}
	if env.client.index == nil || env.client.index.movies[27205] != "MOVIE1" {
		t.Errorf("index = %+v", env.client.index)
	}
}

func TestQ62ResolveTVEpisodeByNumber(t *testing.T) {
	env := newTestEnv(t)
	// A foreign episode handle has no season|episode IDs; resolution falls
	// back to numbering from Q62's own season listing.
	sources, err := env.client.ResolveSource(t.Context(), "SHOW1", provider.Episode{
		ID: "foreign-handle", Season: 1, Episode: 1, TMDBID: 247718,
	})
	if err != nil {
		t.Fatalf("ResolveSource by number: %v", err)
	}
	if len(sources) == 0 || !strings.Contains(sources[0].URL, "/stream/EP1/TOKEN/") {
		t.Fatalf("sources = %+v", sources)
	}
}

func TestQ62ResolveStream(t *testing.T) {
	env := newTestEnv(t)
	updates := make(chan []provider.MediaSource, 2)
	if err := env.client.ResolveStream(t.Context(), "MOVIE1", provider.Episode{TMDBID: 27205}, updates); err != nil {
		t.Fatalf("ResolveStream: %v", err)
	}
	close(updates)
	total := 0
	for batch := range updates {
		total += len(batch)
	}
	if total != 3 {
		t.Errorf("streamed sources = %d, want 3", total)
	}
}

func TestQ62MediaURL(t *testing.T) {
	env := newTestEnv(t)
	descriptor := "/stream/A/B/vod/master.m3u8?token=t&expires=1"
	if got, want := env.client.mediaURL(descriptor, ""), testProxyBase+descriptor; got != want {
		t.Errorf("master URL = %q, want %q", got, want)
	}
	if got, want := env.client.mediaURL(descriptor, "en.vtt"), testProxyBase+"/stream/A/B/vod/en.vtt?token=t&expires=1"; got != want {
		t.Errorf("subtitle URL = %q, want %q", got, want)
	}
	if got := env.client.mediaURL("  ", "en.vtt"); got != "" {
		t.Errorf("empty descriptor = %q, want empty", got)
	}
}

func TestQ62ReleaseRank(t *testing.T) {
	cases := []struct {
		name string
		a, b releaseSummary
		want bool
	}{
		{"4k above fhd", releaseSummary{Resolution: "UHD_4K"}, releaseSummary{Resolution: "FHD_1080P"}, true},
		{"cam last", releaseSummary{Resolution: "CAM"}, releaseSummary{Resolution: "SD"}, false},
		{"br above web", releaseSummary{Resolution: "FHD_1080P", RipType: "BR"}, releaseSummary{Resolution: "FHD_1080P", RipType: "WEB"}, true},
	}
	for _, tc := range cases {
		if got := releaseRank(tc.a) > releaseRank(tc.b); got != tc.want {
			t.Errorf("%s: rank(a) > rank(b) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
