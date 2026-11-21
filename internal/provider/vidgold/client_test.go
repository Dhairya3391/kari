package vidgold

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

// rewriteTransport routes the public test CDN host to a local httptest server
// so HLS probes stay offline while their URLs remain "direct" for filtering.
type rewriteTransport struct {
	target string
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "cdn.example.com" {
		cloned := req.Clone(req.Context())
		target := *req.URL
		target.Scheme = "http"
		target.Host = rt.target
		cloned.URL = &target
		return http.DefaultTransport.RoundTrip(cloned)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestVidGoldCapabilities(t *testing.T) {
	c, err := NewClient(tmdb.NewKeyPool([]string{"dummy-key"}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.Alias() != "VidGold" {
		t.Errorf("Alias() = %q, want VidGold", c.Alias())
	}
	if c.Name() != "vidgold" {
		t.Errorf("Name() = %q, want vidgold", c.Name())
	}

	for _, expectedMode := range []provider.ContentType{provider.ModeMovies, provider.ModeTV, provider.ModeCartoon} {
		found := false
		for _, m := range c.Modes() {
			if m.Name == expectedMode && m.Priority == 2 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("mode %q missing at priority 2: %+v", expectedMode, c.Modes())
		}
	}
}

func TestVidGoldResolveMovie(t *testing.T) {
	const playerBase = "https://player.vidgold.net"

	mux := http.NewServeMux()
	mux.HandleFunc("/movie", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("id"); got != "550" {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if got := r.URL.Query().Get("mode"); got != "json" {
			http.Error(w, "bad mode", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Origin"); got != playerBase {
			http.Error(w, "missing origin", http.StatusForbidden)
			return
		}
		if got := r.Header.Get("Referer"); got != playerBase+"/embed/movie/550" {
			http.Error(w, "missing referer", http.StatusForbidden)
			return
		}
		if r.Header.Get("Sec-Ch-Ua") == "" {
			http.Error(w, "missing sec-ch-ua", http.StatusForbidden)
			return
		}

		master := fmt.Sprintf("http://%s/master.m3u8", r.Host)
		switch r.URL.Query().Get("sources") {
		case "s1":
			writeJSON(t, w, map[string]any{
				"meta": map[string]any{
					"spoken_languages": []map[string]string{
						{"iso_639_1": "en", "name": "English"},
						{"iso_639_1": "fr", "name": "French"},
					},
				},
				"source": map[string]any{
					"source": "s1",
					"label":  "Aurum",
					"url":    master,
				},
				"subtitles": []map[string]string{
					{"label": "English", "file": "https://subs.example.com/en.vtt"},
					{"label": "Spanish", "file": "https://subs.example.com/es.vtt"},
				},
			})
		case "s3":
			// Same master as s1: the second lane must not duplicate its rows.
			writeJSON(t, w, map[string]any{
				"source": map[string]any{
					"source": "s3",
					"label":  "Aurum",
					"url":    master,
				},
				"subtitles": []map[string]string{
					{"label": "English", "file": "https://subs.example.com/en.vtt"},
				},
			})
		case "s2":
			writeJSON(t, w, map[string]any{
				"source": map[string]any{
					"source": "s2",
					"label":  "Bullion",
					"url":    "https://cdn.example.com/s2/master.m3u8",
					"qualities": []map[string]string{
						{"quality": "1080p", "codec": "h264", "url": "https://cdn.example.com/s2/1080.mp4", "type": "mp4"},
						{"quality": "480p", "codec": "h264", "url": "https://cdn.example.com/s2/480.mp4", "type": "mp4"},
					},
				},
				"subtitles": []map[string]string{
					{"label": "Brazilian3", "file": "https://subs.example.com/pt.vtt"},
					{"label": "English Hi", "file": "https://subs.example.com/en-hi.vtt"},
				},
			})
		case "s4":
			// The Auto URL is a master playlist; its 4K tier must be surfaced.
			writeJSON(t, w, map[string]any{
				"source": map[string]any{
					"source": "s4",
					"label":  "Sovereign",
					"url":    "https://cdn.example.com/s4/master.m3u8",
					"h":      1080,
					"qualities": []map[string]string{
						{"quality": "Auto", "codec": "h264", "url": "https://cdn.example.com/s4/auto.m3u8", "type": "hls"},
					},
				},
			})
		case "s5":
			// A single media playlist yields one adaptive row.
			writeJSON(t, w, map[string]any{
				"source": map[string]any{
					"source": "s5",
					"label":  "Nugget",
					"url":    "https://cdn.example.com/s5/master.m3u8",
					"qualities": []map[string]string{
						{"quality": "Auto", "codec": "h264", "url": "https://cdn.example.com/s5/auto.m3u8", "type": "hls"},
					},
				},
			})
		case "s7":
			http.Error(w, "lane unavailable", http.StatusInternalServerError)
		default:
			writeJSON(t, w, map[string]any{"meta": nil, "source": nil, "subtitles": []any{}})
		}
	})

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != playerBase+"/" {
			http.Error(w, "missing stream referer", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-VERSION:6
#EXT-X-STREAM-INF:BANDWIDTH=16000000,RESOLUTION=3840x2160
https://cdn.example.com/s1/2160.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
https://cdn.example.com/s1/1080.m3u8
`))
	})

	mux.HandleFunc("/s4/auto.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-VERSION:6
#EXT-X-STREAM-INF:BANDWIDTH=16000000,RESOLUTION=3840x2160
https://cdn.example.com/s4/2160.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
https://cdn.example.com/s4/1080.m3u8
`))
	})

	mux.HandleFunc("/s5/auto.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:6
#EXTINF:6.0,
https://cdn.example.com/s5/seg0.ts
#EXT-X-ENDLIST
`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, playerBase)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}
	client.httpClient.Transport = rewriteTransport{target: strings.TrimPrefix(srv.URL, "http://")}

	sources, err := client.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}

	want := []struct {
		quality string
		backend string
	}{
		{"4K (2160p)", "Aurum"},
		{"1080p", "Aurum"},
		{"4K (2160p)", "Sovereign"},
		{"1080p", "Sovereign"},
		{"Auto", "Sovereign"},
		{"1080p", "Bullion"},
		{"480p", "Bullion"},
		{"Auto", "Nugget"},
	}
	if len(sources) != len(want) {
		t.Fatalf("expected %d sources, got %d: %+v", len(want), len(sources), sources)
	}
	for i, expected := range want {
		if sources[i].Quality != expected.quality {
			t.Errorf("sources[%d].Quality = %q, want %q", i, sources[i].Quality, expected.quality)
		}
		if sources[i].Backend != expected.backend {
			t.Errorf("sources[%d].Backend = %q, want %q", i, sources[i].Backend, expected.backend)
		}
		if expected.quality == "4K (2160p)" {
			if sources[i].Height != 2160 {
				t.Errorf("sources[%d].Height = %d, want 2160", i, sources[i].Height)
			}
			if sources[i].Bitrate != 16000000 {
				t.Errorf("sources[%d].Bitrate = %d, want 16000000", i, sources[i].Bitrate)
			}
		}
		if sources[i].Referer != playerBase+"/" {
			t.Errorf("sources[%d].Referer = %q, want player referer", i, sources[i].Referer)
		}
		if sources[i].SuppressOrigin {
			t.Errorf("sources[%d].SuppressOrigin = true, want Origin derived from referer", i)
		}
		if len(sources[i].Subtitles) != 4 {
			t.Errorf("sources[%d] subtitles = %d, want 4", i, len(sources[i].Subtitles))
		}
	}

	if !strings.HasSuffix(sources[0].URL, "/s1/2160.m3u8") {
		t.Errorf("sources[0].URL = %q, want the 4K variant", sources[0].URL)
	}
	if got := sources[0].Audio.Languages; len(got) != 2 || got[0] != "en" || got[1] != "fr" {
		t.Errorf("sources[0].Audio.Languages = %v, want [en fr] from meta.spoken_languages", got)
	}
	if sources[0].Audio.Track != provider.TrackUnknown {
		t.Errorf("sources[0].Audio.Track = %q, want unknown", sources[0].Audio.Track)
	}
	if !strings.HasSuffix(sources[2].URL, "/s4/2160.m3u8") {
		t.Errorf("sources[2].URL = %q, want the 4K variant from the HLS quality", sources[2].URL)
	}
	if sources[5].Type != provider.SourceTypeMP4 {
		t.Errorf("sources[5].Type = %q, want mp4", sources[5].Type)
	}
	if sources[7].Type != provider.SourceTypeHLS {
		t.Errorf("sources[7].Type = %q, want hls", sources[7].Type)
	}

	gotLangs := make([]string, 0, len(sources[0].Subtitles))
	for _, sub := range sources[0].Subtitles {
		gotLangs = append(gotLangs, sub.Language)
	}
	if got := strings.Join(gotLangs, ","); got != "en,es,pt,en" {
		t.Errorf("subtitle languages = %q, want en,es,pt,en", got)
	}
}

func TestVidGoldResolveTVEpisode(t *testing.T) {
	const playerBase = "https://player.vidgold.net"

	mux := http.NewServeMux()
	mux.HandleFunc("/tv", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("id") != "1396" || q.Get("season") != "2" || q.Get("episode") != "5" || q.Get("mode") != "json" {
			http.Error(w, "bad TV query params", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Referer"); got != playerBase+"/embed/tv/1396/2/5" {
			http.Error(w, "bad referer", http.StatusForbidden)
			return
		}
		if q.Get("sources") != "s2" {
			writeJSON(t, w, map[string]any{"meta": nil, "source": nil, "subtitles": []any{}})
			return
		}
		writeJSON(t, w, map[string]any{
			"source": map[string]any{
				"source": "s2",
				"label":  "Bullion",
				"url":    "https://cdn.example.com/tv/master.m3u8",
				"qualities": []map[string]string{
					{"quality": "1080p", "codec": "h264", "url": "https://cdn.example.com/tv/1080.mp4", "type": "mp4"},
				},
			},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, playerBase)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "1396", provider.Episode{
		TMDBID:  1396,
		Season:  2,
		Episode: 5,
	})
	if err != nil {
		t.Fatalf("ResolveSource TV failed: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d: %+v", len(sources), sources)
	}
	if sources[0].Backend != "Bullion" || sources[0].Quality != "1080p" {
		t.Errorf("source = %+v, want Bullion 1080p", sources[0])
	}
}

func TestVidGoldNoSources(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/movie", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"meta": nil, "source": nil, "subtitles": []any{}})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, "https://player.vidgold.net")
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	_, err = client.ResolveSource(context.Background(), "999", provider.Episode{TMDBID: 999})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("expected ErrNoSources, got %v", err)
	}
}

func TestVidGoldNonDirectSourcesBecomeErrNoSources(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/movie", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"source": map[string]any{
				"source": "s1",
				"label":  "Aurum",
				"qualities": []map[string]string{
					// MP4 avoids an HLS probe; localhost is not direct-playable.
					{"quality": "1080p", "url": "http://localhost/master.mp4", "type": "mp4"},
				},
			},
			"subtitles": []any{},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, "https://player.vidgold.net")
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("expected ErrNoSources for non-direct sources, got sources=%+v err=%v", sources, err)
	}
}

func TestVidGoldAPIErrorsSurface(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/movie", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusForbidden)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, "https://player.vidgold.net")
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	_, err = client.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
	if err == nil {
		t.Fatal("expected an error when every lane fails")
	}
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *provider.HTTPError, got %T: %v", err, err)
	}
}

func TestVidGoldLaneNotFoundIsNoSources(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/movie", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such title", http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), srv.URL, "https://player.vidgold.net")
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	_, err = client.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("expected ErrNoSources for an absent title, got %v", err)
	}
	if provider.BreakerFailure(err) {
		t.Fatalf("an absent title must not count as a provider failure: %v", err)
	}
}

func TestQualityLabelAndType(t *testing.T) {
	if got := qualityLabel(apiQuality{Quality: "Auto"}, 2160); got != "4K (2160p)" {
		t.Errorf("auto 2160 label = %q, want 4K (2160p)", got)
	}
	if got := qualityLabel(apiQuality{Quality: "Auto"}, 0); got != "Auto" {
		t.Errorf("auto unknown label = %q, want Auto", got)
	}
	if got := qualityLabel(apiQuality{Quality: "Auto"}, 1); got != "Auto" {
		t.Errorf("auto flag-like height label = %q, want Auto", got)
	}
	if got := qualityLabel(apiQuality{Quality: "1080p"}, 0); got != "1080p" {
		t.Errorf("explicit label = %q, want 1080p", got)
	}
	if got := qualityType(apiQuality{Type: "mp4"}, "https://cdn.example.com/a"); got != provider.SourceTypeMP4 {
		t.Errorf("type = %q, want mp4", got)
	}
	if got := qualityType(apiQuality{}, "https://cdn.example.com/a.m3u8"); got != provider.SourceTypeHLS {
		t.Errorf("inferred type = %q, want hls", got)
	}
}

func TestCollectSubtitlesDedupeByLanguageAndSource(t *testing.T) {
	results := []laneResult{{
		subtitles: []apiSubtitle{
			{Label: "English", File: "https://subs.example.com/en-1.vtt", Source: "subdl"},
			{Label: "English", File: "https://subs.example.com/en-2.vtt", Source: "subdl"},
			{Label: "English", File: "https://subs.example.com/en-3.vtt", Source: "opensubtitles"},
			{Label: "French", File: "https://subs.example.com/fr.vtt", Source: "subdl"},
		},
	}}
	subs := collectSubtitles(results)
	if len(subs) != 3 {
		t.Fatalf("subtitles = %+v, want 3 unique language/source tracks", subs)
	}
	if subs[0].Language != "en" || subs[2].Language != "fr" {
		t.Errorf("subtitle languages = %q %q, want en fr", subs[0].Language, subs[2].Language)
	}
}

func TestLaneSourcesUsesUpstreamLabelAsBackend(t *testing.T) {
	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"key"}), "https://api.example.com", "https://player.example.com")
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}
	sources := c.laneSources(context.Background(), laneResult{
		server: server{key: "s1", label: "Hardcoded"},
		source: &apiSource{
			Label:     "Upstream Lane",
			URL:       "https://cdn.example.com/master.m3u8",
			Qualities: []apiQuality{{Quality: "1080p", URL: "https://cdn.example.com/1080.mp4", Type: "mp4"}},
		},
	}, nil)
	if len(sources) != 1 || sources[0].Backend != "Upstream Lane" {
		t.Fatalf("sources = %+v, want Upstream Lane backend", sources)
	}
	if sources[0].Height != 1080 {
		t.Errorf("Height = %d, want 1080", sources[0].Height)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
