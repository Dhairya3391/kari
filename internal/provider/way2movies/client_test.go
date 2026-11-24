package way2movies

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"kari/internal/config"
	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestWay2MoviesModesAndMetadata(t *testing.T) {
	client, err := NewClientWithURL(&tmdb.KeyPool{}, "https://scraper.example.test", config.Way2MoviesOrigin)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	if got := client.Name(); got != "way2movies" {
		t.Errorf("Name() = %q, want %q", got, "way2movies")
	}
	if got := client.Alias(); got != "Way2Movies" {
		t.Errorf("Alias() = %q, want %q", got, "Way2Movies")
	}

	modes := client.Modes()
	if len(modes) != 3 {
		t.Fatalf("Modes() count = %d, want 3", len(modes))
	}
	want := map[provider.ContentType]bool{
		provider.ModeMovies:  true,
		provider.ModeTV:      true,
		provider.ModeCartoon: true,
	}
	for _, mode := range modes {
		if !want[mode.Name] {
			t.Errorf("unexpected mode %q", mode.Name)
		}
		if mode.Priority != 2 {
			t.Errorf("mode %q priority = %d, want 2", mode.Name, mode.Priority)
		}
	}
}

func TestParseCoordinates(t *testing.T) {
	tests := []struct {
		name       string
		mediaID    string
		episode    provider.Episode
		wantID     int
		wantType   string
		wantSeason int
		wantEp     int
		wantErr    bool
	}{
		{name: "movie from media ID", mediaID: "27205", wantID: 27205, wantType: provider.MediaTypeMovie, wantSeason: 1, wantEp: 1},
		{name: "tv from episode", episode: provider.Episode{TMDBID: 1396, Season: 2, Episode: 5}, wantID: 1396, wantType: provider.MediaTypeTV, wantSeason: 2, wantEp: 5},
		{name: "invalid ID", mediaID: "nope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, mediaType, season, ep, err := parseCoordinates(tt.mediaID, tt.episode)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseCoordinates error = %v, wantErr %t", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if id != tt.wantID || mediaType != tt.wantType || season != tt.wantSeason || ep != tt.wantEp {
				t.Errorf("parseCoordinates = %d, %s, %d, %d", id, mediaType, season, ep)
			}
		})
	}
}

func TestResolveMovie(t *testing.T) {
	var sawOrigin, sawReferer atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawOrigin.Store(r.Header.Get("Origin") == config.Way2MoviesOrigin)
		sawReferer.Store(r.Header.Get("Referer") == config.Way2MoviesOrigin+"/")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/server/39/movie/27205"):
			_, _ = w.Write([]byte(`{"media_urls":["https://cdn.example.test/720/index.m3u8","https://cdn.example.test/480/index.m3u8"],"languages":["English","Tamil"],"language":"English"}`))
		case strings.HasSuffix(r.URL.Path, "/server/74/movie/27205"):
			_, _ = w.Write([]byte(`{"media_urls":["https://other.example.test/1080/index.m3u8"],"language":"English"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL, config.Way2MoviesOrigin)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	sources, err := client.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}
	if !sawOrigin.Load() || !sawReferer.Load() {
		t.Error("scraper requests must carry the Way2Movies origin and referer")
	}
	if len(sources) != 3 {
		t.Fatalf("got %d sources, want 3: %+v", len(sources), sources)
	}
	byURL := make(map[string]provider.MediaSource, len(sources))
	for _, source := range sources {
		byURL[source.URL] = source
		if source.Type != provider.SourceTypeHLS {
			t.Errorf("source %s type = %q, want hls", source.URL, source.Type)
		}
		wantCodes := []string{"en"}
		wantLabel := "English"
		if strings.HasPrefix(source.URL, "https://cdn.example.test/") {
			wantCodes = []string{"en", "ta"}
			wantLabel = "English · Tamil"
		}
		if strings.Join(source.Audio.Languages, ",") != strings.Join(wantCodes, ",") {
			t.Errorf("source %s audio languages = %v, want %v", source.URL, source.Audio.Languages, wantCodes)
		}
		if source.Language != wantCodes[0] || source.PreferredAudio != wantCodes[0] {
			t.Errorf("source %s language = %q, preferred audio = %q, want %q", source.URL, source.Language, source.PreferredAudio, wantCodes[0])
		}
		if source.AudioLabel != wantLabel {
			t.Errorf("source %s audio label = %q, want %q", source.URL, source.AudioLabel, wantLabel)
		}
	}
	for url, quality := range map[string]string{
		"https://cdn.example.test/720/index.m3u8":    "720p",
		"https://cdn.example.test/480/index.m3u8":    "480p",
		"https://other.example.test/1080/index.m3u8": "1080p",
	} {
		source, ok := byURL[url]
		if !ok {
			t.Errorf("missing source %s", url)
			continue
		}
		if source.Quality != quality || source.Height == 0 {
			t.Errorf("source %s quality = %q, height = %d, want %q", url, source.Quality, source.Height, quality)
		}
	}
}

func TestResolveTV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/server/39/tv/1396/2/5") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"media_urls":["https://cdn.example.test/hls/abc/720/index.m3u8"]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL, config.Way2MoviesOrigin)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	sources, err := client.ResolveSource(context.Background(), "1396", provider.Episode{
		TMDBID: 1396, Season: 2, Episode: 5,
	})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("got %d sources, want 1", len(sources))
	}
	if sources[0].Backend != "Server 39" {
		t.Errorf("backend = %q, want %q", sources[0].Backend, "Server 39")
	}
	if sources[0].Quality != "720p" {
		t.Errorf("quality = %q, want 720p", sources[0].Quality)
	}
}

func TestResolveTypedAudioLanguage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/server/39/tv/11436/10/1") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"media_urls":["https://cdn.example.test/720/index.m3u8"],"languages":["Hindi"],"language":"Hindi"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL, config.Way2MoviesOrigin)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	sources, err := client.ResolveSource(context.Background(), "11436", provider.Episode{
		TMDBID: 11436, Season: 10, Episode: 1,
	})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("got %d sources, want 1", len(sources))
	}
	source := sources[0]
	if strings.Join(source.Audio.Languages, ",") != "hi" {
		t.Errorf("audio languages = %v, want [hi]", source.Audio.Languages)
	}
	if source.Language != "hi" || source.PreferredAudio != "hi" {
		t.Errorf("language = %q, preferred audio = %q, want hi", source.Language, source.PreferredAudio)
	}
	if source.AudioLabel != "Hindi" {
		t.Errorf("audio label = %q, want Hindi", source.AudioLabel)
	}
}

func TestResolveNoSources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL, config.Way2MoviesOrigin)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	_, err = client.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("error = %v, want ErrNoSources", err)
	}
}

func TestResolveFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream down", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClientWithURL(&tmdb.KeyPool{}, srv.URL, config.Way2MoviesOrigin)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}
	_, err = client.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if err == nil || errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("error = %v, want transport failure", err)
	}
}
