package bingr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestBingrCapabilities(t *testing.T) {
	c, err := NewClient(tmdb.NewKeyPool([]string{"dummy-key"}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.Alias() != "Bingr" {
		t.Errorf("Alias() = %q, want Bingr", c.Alias())
	}
	if c.Name() != "bingr" {
		t.Errorf("Name() = %q, want bingr", c.Name())
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

	langs := c.AudioLanguages()
	if len(langs) == 0 {
		t.Fatal("AudioLanguages() returned empty slice")
	}
	hasEn, hasJa := false, false
	for _, l := range langs {
		if l.Code == "en" {
			hasEn = true
		}
		if l.Code == "ja" {
			hasJa = true
		}
	}
	if !hasEn || !hasJa {
		t.Errorf("AudioLanguages() missing en or ja: %+v", langs)
	}
}

// watchPageResponse embeds a sealed payload the way Bingr's watch pages do.
func watchPageResponse(t *testing.T, payload sealedSources) http.HandlerFunc {
	t.Helper()
	sealed := sealJSONForTest(t, payload)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><body><script>self.__next_f.push([1,%q])</script></body></html>`, "22:T,"+sealed)
	}
}

func watchPageWithToken(t *testing.T, payload sealedSources, token string) http.HandlerFunc {
	t.Helper()
	sealed := sealJSONForTest(t, payload)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w,
			`<html><body><script>self.__next_f.push([1,%q])</script><script>\"token\":%q</script></body></html>`,
			"22:T,"+sealed, token)
	}
}

func TestBingrCollectsAllServers(t *testing.T) {
	const token = "test.token_abc123"
	mux := http.NewServeMux()
	mux.HandleFunc("/watch/movie/550", watchPageWithToken(t, sealedSources{
		Provider: sealedProvider{ID: "prism", Label: "Prism"},
		Servers: []sealedServer{
			{ID: "prism"}, {ID: "comet"}, {ID: "flux"},
		},
		Streams: []sealedStream{{URL: "https://cdn.example.com/prism.m3u8", Quality: "1080p"}},
	}, token))
	mux.HandleFunc("/api/sources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != token {
			http.Error(w, "bad token", http.StatusBadRequest)
			return
		}
		backend := r.URL.Query().Get("provider")
		_, _ = fmt.Fprint(w, sealJSONForTest(t, sealedSources{
			Provider: sealedProvider{ID: backend, Label: backend},
			Streams: []sealedStream{{
				URL:     "https://cdn.example.com/" + backend + ".m3u8",
				Quality: "720p",
			}},
		}))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}
	sources, err := c.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	got := make(map[string]bool, len(sources))
	for _, s := range sources {
		got[s.URL] = true
	}
	for _, backend := range []string{"prism", "comet", "flux"} {
		want := "https://cdn.example.com/" + backend + ".m3u8"
		if !got[want] {
			t.Errorf("missing stream from server %q: %+v", backend, got)
		}
	}
	if len(sources) != 3 {
		t.Errorf("got %d sources, want 3 (one per server): %+v", len(sources), sources)
	}
}

func TestBingrResolveMovieDirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/watch/movie/550", watchPageResponse(t, sealedSources{
		Provider: sealedProvider{ID: "prism", Label: "Prism"},
		Streams: []sealedStream{
			{URL: "https://cdn.example.com/movie/550/1080.m3u8", Type: "hls", Quality: "1080p", Language: "Original"},
			{
				URL:     "https://cdn.example.com/movie/550/720.m3u8",
				Type:    "hls",
				Quality: "720p",
				Subtitles: []sealedSubtitle{
					{URL: "https://api.example.com/subs/en.vtt", Lang: "en", Label: "English"},
				},
			},
		},
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}
	sources, err := c.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550, Title: "Fight Club"})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("ResolveSource returned %d sources, want 2", len(sources))
	}
	if sources[0].Backend != "Prism" || sources[0].Height != 1080 || sources[0].Quality != "1080p" {
		t.Errorf("first source = %+v", sources[0])
	}
	if len(sources[1].Subtitles) != 1 || sources[1].Subtitles[0].Language != "en" {
		t.Errorf("second source subtitles = %+v", sources[1].Subtitles)
	}
	// "Original" and "Arabic sub" style labels must not become audio identities.
	for _, s := range sources {
		if s.Language != "" || len(s.Audio.Languages) != 0 {
			t.Errorf("source audio must stay unknown, got %q / %v", s.Language, s.Audio.Languages)
		}
	}
}

func TestBingrResolveTVFromWatchPage(t *testing.T) {
	var requested string
	mux := http.NewServeMux()
	mux.HandleFunc("/watch/tv/1396/2/5", func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Path
		watchPageResponse(t, sealedSources{
			Provider: sealedProvider{ID: "prism", Label: "Prism"},
			Streams: []sealedStream{{
				URL:     "https://cdn.example.com/tv/1396/2/5/index.m3u8",
				Type:    "hls",
				Quality: "1080p",
			}},
		})(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}
	sources, err := c.ResolveSource(context.Background(), "1396", provider.Episode{
		TMDBID: 1396, Title: "Pilot", Season: 2, Episode: 5,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if requested != "/watch/tv/1396/2/5" {
		t.Errorf("requested path = %q", requested)
	}
	if len(sources) != 1 || sources[0].URL != "https://cdn.example.com/tv/1396/2/5/index.m3u8" {
		t.Errorf("sources = %+v", sources)
	}
}

func TestBingrWatchPageErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr error
	}{
		{
			name: "missing payload",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, `<html><body>no player here</body></html>`)
			},
			wantErr: provider.ErrNoSources,
		},
		{
			name: "upstream error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/watch/movie/550", tt.handler)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL)
			if err != nil {
				t.Fatalf("NewClientWithURLs: %v", err)
			}
			_, err = c.ResolveSource(context.Background(), "550", provider.Episode{TMDBID: 550})
			if err == nil {
				t.Fatal("ResolveSource succeeded, want error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestBingrSubtitleDedupe(t *testing.T) {
	sources := mapSealedSources(&sealedSources{
		Streams: []sealedStream{{
			URL:     "https://cdn.example.com/test.m3u8",
			Quality: "1080p",
			Subtitles: []sealedSubtitle{
				{URL: "https://cdn.example.com/subs/en.vtt", Lang: "en"},
				{URL: "https://cdn.example.com/subs/en.vtt", Lang: "eng"},
				{URL: "https://cdn.example.com/subs/es.vtt", Lang: "spa"},
			},
		}},
	})
	if len(sources) != 1 {
		t.Fatalf("sources count = %d, want 1", len(sources))
	}
	subs := sources[0].Subtitles
	if len(subs) != 2 { // 1 en + 1 es
		t.Fatalf("deduped subtitles count = %d, want 2: %+v", len(subs), subs)
	}
}

// TestSubtitleLanguageUsesLabel proves an ambiguous code is resolved through
// its curated label: upstream "fi" means Filipino, not Finnish.
func TestSubtitleLanguageUsesLabel(t *testing.T) {
	sources := mapSealedSources(&sealedSources{
		Streams: []sealedStream{{
			URL:     "https://cdn.example.com/v.m3u8",
			Quality: "720p",
			Subtitles: []sealedSubtitle{
				{URL: "https://cdn.example.com/fil.vtt", Lang: "fi", Label: "Filipino"},
				{URL: "https://cdn.example.com/fi.vtt", Lang: "fi", Label: "Finnish"},
			},
		}},
	})
	if len(sources) != 1 || len(sources[0].Subtitles) != 2 {
		t.Fatalf("sources/subtitles = %d/%v, want 1/2", len(sources), sources)
	}
	got := []string{sources[0].Subtitles[0].Language, sources[0].Subtitles[1].Language}
	if got[0] != "fil" || got[1] != "fi" {
		t.Errorf("subtitle languages = %v, want [fil fi]", got)
	}
}
