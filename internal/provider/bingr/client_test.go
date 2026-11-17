package bingr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"kari/internal/provider"
	"kari/internal/tmdb"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixtureEnc is a canned Megaplay getSources token that decrypts to a signed
// CDN master playlist. It matches the fixture used by the kit and anicine
// tests so no crypto needs to run inside the provider tests.
const fixtureEnc = "wdeBruh3qqn_i5wUNnyaPcXqidp1UWP84FfPHzGyKXDMoY_RzXqbC0h49XmoI7d0vYZArA5rcKY-FQxnEk8NNEWezec9dd2jwxp1UbLN43p_7CMxXPDF5BUX86bUm0_Uuw0-dlv_yT9MKsnmlOgDFm3ReCPZPJloNlqBF7aftTk"

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

	for _, expectedMode := range []provider.ContentType{provider.ModeMovies, provider.ModeTV, provider.ModeCartoon, provider.ModeAnime} {
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

	if !c.Features(provider.ModeAnime).AudioSelection {
		t.Errorf("Features(ModeAnime).AudioSelection = false, want true")
	}
	if c.Features(provider.ModeMovies).AudioSelection {
		t.Errorf("Features(ModeMovies).AudioSelection = true, want false")
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

func TestBingrResolveMovieDirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/details/movie/550", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(detailsResponse{
			ID:    550,
			Type:  "movie",
			Title: "Fight Club",
			Year:  1999,
		})
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		var req streamRequestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.ID != "550" || req.T != "movie" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if req.Srv == "s40" {
			_ = json.NewEncoder(w).Encode(streamResponse{
				ScraperName: "DarkMatter",
				Sources: []streamItem{
					{
						URL:     "https://cdn.example.com/movie/550/1080p.m3u8",
						Quality: "1080p",
						Type:    "application/x-mpegurl",
					},
				},
			})
			return
		}
		if req.Srv == "s70" {
			_ = json.NewEncoder(w).Encode(streamResponse{
				ScraperName: "Polaris",
				Sources: []streamItem{
					{
						URL:     "https://cdn.example.com/movie/550/720p.m3u8",
						Quality: "720p",
						Name:    "Hindi — 720p",
					},
				},
				Subtitles: []subtitleItem{
					{
						URL:  "https://api.example.com/subs/en.vtt",
						Lang: "en",
					},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(streamResponse{})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	sources, err := c.ResolveSource(context.Background(), "550", provider.Episode{
		TMDBID:  550,
		Title:   "Fight Club",
		Season:  0,
		Episode: 0,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) < 2 {
		t.Fatalf("ResolveSource returned %d sources, want at least 2", len(sources))
	}

	hasDarkMatter := false
	hasPolaris := false
	for _, s := range sources {
		if s.Backend == "DarkMatter" && strings.HasPrefix(s.URL, "https://cdn.example.com") {
			hasDarkMatter = true
			if s.Height != 1080 || s.Quality != "1080p" {
				t.Errorf("DarkMatter height/quality = %d/%q, want 1080/1080p", s.Height, s.Quality)
			}
		}
		if s.Backend == "Polaris" && s.Height == 720 {
			hasPolaris = true
			// The "Hindi" name is provider text, not a real audio code.
			if s.Language != "" || len(s.Audio.Languages) != 0 {
				t.Errorf("Polaris audio must stay unknown, got %q / %v", s.Language, s.Audio.Languages)
			}
			if len(s.Subtitles) == 0 || s.Subtitles[0].Language != "en" {
				t.Errorf("Polaris stream missing en subtitle: %+v", s.Subtitles)
			}
		}
	}
	if !hasDarkMatter {
		t.Errorf("missing DarkMatter 1080p stream: %+v", sources)
	}
	if !hasPolaris {
		t.Errorf("missing Polaris 720p stream: %+v", sources)
	}
}

// A stalled details endpoint used to hold every shard behind the service's
// long deadline. The metadata lookup is now bounded and falls back to the
// episode title so streams still resolve.
func TestBingrSlowMetadataFallsBackAndStreams(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/details/movie/550", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	titles := make(chan string, len(activeServers))
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		var req streamRequestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		titles <- req.Query.Title
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(streamResponse{
			Sources: []streamItem{{
				URL:     "https://cdn.example.com/movie/550/index.m3u8",
				Quality: "1080p",
				Type:    "application/x-mpegurl",
			}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	start := time.Now()
	sources, err := c.ResolveSource(context.Background(), "550", provider.Episode{
		TMDBID: 550,
		Title:  "Fight Club",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("ResolveSource returned no sources")
	}
	if limit := metaTimeout + time.Second; elapsed > limit {
		t.Fatalf("ResolveSource took %v, want under %v", elapsed, limit)
	}
	close(titles)
	for title := range titles {
		if title != "Fight Club" {
			t.Errorf("stream query title = %q, want episode title fallback", title)
		}
	}
}

// A stalled shard must not keep the provider spinner alive past the shard
// budget; the healthy shards still publish their sources.
func TestBingrStalledShardDoesNotBlockResolve(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/details/movie/550", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(detailsResponse{ID: 550, Type: "movie", Title: "Fight Club", Year: 1999})
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		var req streamRequestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Srv == activeServers[0].id {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(streamResponse{
			Sources: []streamItem{{
				URL:     "https://cdn.example.com/movie/550/index.m3u8",
				Quality: "1080p",
				Type:    "application/x-mpegurl",
			}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	start := time.Now()
	sources, err := c.ResolveSource(context.Background(), "550", provider.Episode{
		TMDBID: 550,
		Title:  "Fight Club",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("ResolveSource returned no sources")
	}
	if limit := shardTimeout + time.Second; elapsed > limit {
		t.Fatalf("ResolveSource took %v, want under %v", elapsed, limit)
	}
}

func TestBingrResolveTVAphelion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/details/tv/1396", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(detailsResponse{
			ID:    1396,
			Type:  "tv",
			Title: "Breaking Bad",
			Year:  2008,
		})
	})
	mux.HandleFunc("/stream/aphelion-tv/1396/1/1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(streamResponse{
			ScraperName: "DarkMatter",
			Sources: []streamItem{
				{
					URL:     "https://cdn.example.com/tv/1396/1/1/index.m3u8",
					Quality: "1080p",
					Type:    "application/x-mpegurl",
				},
			},
		})
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(streamResponse{})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	sources, err := c.ResolveSource(context.Background(), "1396", provider.Episode{
		TMDBID:  1396,
		Title:   "Pilot",
		Season:  1,
		Episode: 1,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("ResolveSource returned 0 sources")
	}
	if sources[0].URL != "https://cdn.example.com/tv/1396/1/1/index.m3u8" {
		t.Errorf("sources[0].URL = %q", sources[0].URL)
	}
}

func TestBingrResolveAnime(t *testing.T) {
	mux := http.NewServeMux()
	// Bingr anime metadata
	mux.HandleFunc("/anime/154587", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(animeMetaResponse{
			ID:       154587,
			IDMal:    52991,
			Title:    "Frieren: Beyond Journey's End",
			Episodes: 28,
			TMDBID:   209867,
			TMDBType: "tv",
		})
	})

	// Direct MegaPlay embeds: sub and dub map to distinct file IDs.
	embedPaths := make(chan string, 4)
	mux.HandleFunc("/stream/mal/52991/1/sub", func(w http.ResponseWriter, _ *http.Request) {
		embedPaths <- "sub"
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><title>File 13461 - MegaPlay</title></head><body><div class="player" data-id="13461"></div></body></html>`)
	})
	mux.HandleFunc("/stream/mal/52991/1/dub", func(w http.ResponseWriter, _ *http.Request) {
		embedPaths <- "dub"
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><title>File 13454 - MegaPlay</title></head><body><div class="player" data-id="13454"></div></body></html>`)
	})

	// getSources requires the embed as Referer and returns an encrypted token.
	getSourcesIDs := make(chan string, 4)
	mux.HandleFunc("/stream/getSources", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") == "" || r.Header.Get("X-Requested-With") == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		fileID := r.URL.Query().Get("id")
		getSourcesIDs <- fileID
		w.Header().Set("Content-Type", "application/json")
		switch fileID {
		case "13461":
			_, _ = fmt.Fprintf(w, `{"enc":%q,"tracks":[{"file":"https://subs.example.com/frieren-en.vtt","label":"English","kind":"captions"}]}`, fixtureEnc)
		case "13454":
			_, _ = fmt.Fprintf(w, `{"enc":%q,"tracks":[{"file":"https://subs.example.com/frieren-pt.vtt","label":"Portuguese","kind":"captions"}]}`, fixtureEnc)
		default:
			http.Error(w, "unknown file", http.StatusNotFound)
		}
	})

	// Bingr stream for TMDB mapped anime
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		var req streamRequestBody
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.ID == "209867" && req.Srv == "s70" {
			_ = json.NewEncoder(w).Encode(streamResponse{
				ScraperName: "Polaris",
				Sources: []streamItem{
					{
						URL:     "https://cdn.example.com/polaris/frieren/original.m3u8",
						Quality: "1080p",
						Name:    "Original — 1080p",
					},
					{
						URL:     "https://cdn.example.com/polaris/frieren/hindi.m3u8",
						Quality: "1080p",
						Name:    "Hindi — 1080p",
					},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(streamResponse{})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	// 1. Resolve SUB
	subSources, err := c.ResolveSource(context.Background(), "154587", provider.Episode{
		ID:      "watch/bingr/154587/sub/1",
		Audio:   provider.AudioSub,
		Episode: 1,
		Season:  1,
	})
	if err != nil {
		t.Fatalf("ResolveSource sub: %v", err)
	}
	if len(subSources) == 0 {
		t.Fatal("subSources is empty")
	}

	hasMegaPlaySub := false
	hasPolarisOriginal := false
	for _, s := range subSources {
		if s.Backend == "MegaPlay" {
			hasMegaPlaySub = true
			if !strings.HasPrefix(s.URL, "https://fetch.nexabloom.top/") || !strings.Contains(s.URL, "?token=") {
				t.Errorf("MegaPlay URL is not signed: %q", s.URL)
			}
			if s.Referer != srv.URL+"/" {
				t.Errorf("MegaPlay referer = %q, want %q", s.Referer, srv.URL+"/")
			}
			if len(s.Subtitles) != 1 || s.Subtitles[0].URL != "https://subs.example.com/frieren-en.vtt" || s.Subtitles[0].Language != "en" {
				t.Errorf("unexpected sub subtitles: %+v", s.Subtitles)
			}
		}
		if s.Backend == "Polaris" && strings.HasSuffix(s.URL, "/original.m3u8") {
			hasPolarisOriginal = true
			if s.Audio.Track != provider.TrackSub {
				t.Errorf("anime shard track = %q, want sub", s.Audio.Track)
			}
		}
	}
	if !hasMegaPlaySub {
		t.Errorf("missing MegaPlay sub stream: %+v", subSources)
	}
	if !hasPolarisOriginal {
		t.Errorf("missing Polaris Original stream: %+v", subSources)
	}

	// 2. Resolve DUB
	dubSources, err := c.ResolveSource(context.Background(), "154587", provider.Episode{
		ID:      "watch/bingr/154587/dub/1",
		Audio:   provider.AudioDub,
		Episode: 1,
		Season:  1,
	})
	if err != nil {
		t.Fatalf("ResolveSource dub: %v", err)
	}
	hasHindiDub := false
	hasMegaPlayDub := false
	for _, s := range dubSources {
		if strings.HasSuffix(s.URL, "/hindi.m3u8") {
			hasHindiDub = true
			if s.Audio.Track != provider.TrackDub || len(s.Audio.Languages) != 0 {
				t.Errorf("Hindi-named shard audio = %+v, want dub with no guessed language", s.Audio)
			}
		}
		if s.Backend == "MegaPlay" {
			hasMegaPlayDub = true
			if len(s.Subtitles) != 1 || s.Subtitles[0].URL != "https://subs.example.com/frieren-pt.vtt" || s.Subtitles[0].Language != "pt" {
				t.Errorf("unexpected dub subtitles: %+v", s.Subtitles)
			}
		}
	}
	if !hasHindiDub {
		t.Errorf("expected Hindi dub source in dubSources: %+v", dubSources)
	}
	if !hasMegaPlayDub {
		t.Errorf("missing MegaPlay dub stream: %+v", dubSources)
	}

	close(embedPaths)
	gotPaths := map[string]bool{}
	for path := range embedPaths {
		gotPaths[path] = true
	}
	if !gotPaths["sub"] || !gotPaths["dub"] {
		t.Errorf("embed paths = %+v, want both sub and dub", gotPaths)
	}
	close(getSourcesIDs)
	gotIDs := map[string]bool{}
	for id := range getSourcesIDs {
		gotIDs[id] = true
	}
	if !gotIDs["13461"] || !gotIDs["13454"] {
		t.Errorf("getSources ids = %+v, want 13461 and 13454", gotIDs)
	}
}

func TestBingrFetchAnimeEpisodes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/anime/154587", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(animeMetaResponse{
			ID:       154587,
			Episodes: 4,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{
		ID:        "154587",
		AniListID: 154587,
		Type:      provider.ModeAnime,
		MediaType: provider.MediaTypeAnime,
	})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 8 { // 4 sub + 4 dub
		t.Fatalf("len(eps) = %d, want 8", len(eps))
	}
	if eps[0].Audio != provider.AudioSub || eps[0].Episode != 1 {
		t.Errorf("eps[0] = %+v", eps[0])
	}
	if eps[1].Audio != provider.AudioDub || eps[1].Episode != 1 {
		t.Errorf("eps[1] = %+v", eps[1])
	}
}

func TestBingrSearchAnimeFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/anime/search", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(animeSearchResponse{
			Results: []animeSearchItem{
				{
					ID:     16498,
					IDMal:  16498,
					Title:  "Attack on Titan",
					Year:   2013,
					Format: "TV",
					Poster: "https://example.com/poster.jpg",
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Points anilistBase at invalid server to trigger fallback
	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, "http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	results, err := c.Search(context.Background(), "Attack on Titan", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Title != "Attack on Titan" || results[0].AniListID != 16498 {
		t.Errorf("results[0] = %+v", results[0])
	}
}

func TestParseMegaplayFileID(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{"data-id attribute", `<html><div data-id="13461"></div></html>`, "13461", false},
		{"title fallback", `<html><title>File 13454 - MegaPlay</title></html>`, "13454", false},
		{"error page", `<html><title>Error - MegaPlay</title></html>`, "", true},
		{"shapeless page", `<html><body>redesigned</body></html>`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMegaplayFileID([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if tt.name == "error page" && !errors.Is(err, provider.ErrAudioUnavailable) {
					t.Errorf("err = %v, want ErrAudioUnavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMegaplayFileID: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseMegaplayFileID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBingrDirectAnimeFallback(t *testing.T) {
	mux := http.NewServeMux()
	// Details returns 404 (not a TMDB movie)
	mux.HandleFunc("/details/movie/199", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	// Stream returns 404/empty
	mux.HandleFunc("/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(streamResponse{})
	})
	// Anime metadata returns valid Spirited Away (AniList 199, MAL 199)
	mux.HandleFunc("/anime/199", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(animeMetaResponse{
			ID:       199,
			IDMal:    199,
			Title:    "Spirited Away",
			Format:   "MOVIE",
			Episodes: 1,
		})
	})
	mux.HandleFunc("/stream/mal/199/1/sub", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><title>File 4242 - MegaPlay</title></head><body><div data-id="4242"></div></body></html>`)
	})
	mux.HandleFunc("/stream/getSources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "4242" {
			http.Error(w, "unknown file", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"enc":%q,"tracks":[]}`, fixtureEnc)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	// Direct resolve with AniList ID and no watch/ prefix
	sources, err := c.ResolveSource(context.Background(), "199", provider.Episode{
		ID:      "199",
		Title:   "Spirited Away",
		Season:  0,
		Episode: 0,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("expected fallback sources for anime movie")
	}
	if !strings.HasPrefix(sources[0].URL, "https://fetch.nexabloom.top/") || !strings.Contains(sources[0].URL, "?token=") {
		t.Errorf("sources[0].URL = %q, want signed direct CDN URL", sources[0].URL)
	}
}

// The MAL embed is the primary index; a title indexed only under its AniList
// id must still resolve through the /stream/ani fallback.
func TestBingrMegaPlayAniListFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/anime/154587", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(animeMetaResponse{
			ID: 154587, IDMal: 52991, Title: "Frieren", Episodes: 1,
		})
	})
	mux.HandleFunc("/stream/mal/52991/1/sub", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/stream/ani/154587/1/sub", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><title>File 777 - MegaPlay</title></head><body><div data-id="777"></div></body></html>`)
	})
	mux.HandleFunc("/stream/getSources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "777" {
			http.Error(w, "unknown file", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"enc":%q,"tracks":[]}`, fixtureEnc)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	sources, err := c.ResolveSource(context.Background(), "154587", provider.Episode{
		ID: "watch/bingr/154587/sub/1", Audio: provider.AudioSub, Episode: 1, Season: 1,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) == 0 || !strings.HasPrefix(sources[0].URL, "https://fetch.nexabloom.top/") {
		t.Fatalf("AniList fallback sources = %+v", sources)
	}
}

// A dead MegaPlay embed must not turn a successful shard-only anime resolve
// into an error: only real shard failures belong in lastErr.
func TestBingrMegaPlayFailureDoesNotMaskShardSources(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/anime/154587", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(animeMetaResponse{
			ID: 154587, IDMal: 52991, Title: "Frieren", Episodes: 1, TMDBID: 209867, TMDBType: "tv",
		})
	})
	mux.HandleFunc("/stream/mal/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/stream/ani/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		var req streamRequestBody
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.ID == "209867" {
			_ = json.NewEncoder(w).Encode(streamResponse{
				ScraperName: "Polaris",
				Sources: []streamItem{{
					URL:     "https://cdn.example.com/polaris/frieren/original.m3u8",
					Quality: "1080p",
					Name:    "Original — 1080p",
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(streamResponse{})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	sources, err := c.ResolveSource(context.Background(), "154587", provider.Episode{
		ID: "watch/bingr/154587/sub/1", Audio: provider.AudioSub, Episode: 1, Season: 1,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) == 0 || sources[0].Backend != "Polaris" {
		t.Fatalf("expected shard-only sources, got %+v", sources)
	}
}

func TestBingrSubtitleDedupe(t *testing.T) {
	c := &Client{}
	sr := streamResponse{
		Sources: []streamItem{
			{URL: "https://cdn.example.com/test.m3u8", Quality: "1080p"},
		},
		Subtitles: []subtitleItem{
			{URL: "https://cdn.example.com/subs/en.vtt", Lang: "en"},
			{URL: "https://cdn.example.com/subs/en.vtt", Lang: "eng"},
			{URL: "https://cdn.example.com/subs/es.vtt", Lang: "spa"},
		},
	}
	sources, err := c.mapStreamResponse(sr, "Test")
	if err != nil {
		t.Fatalf("mapStreamResponse: %v", err)
	}
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
	c := &Client{}
	sr := streamResponse{
		Sources: []streamItem{{URL: "https://cdn.example.com/v.m3u8", Quality: "720p"}},
		Subtitles: []subtitleItem{
			{URL: "https://cdn.example.com/fil.vtt", Lang: "fi", Label: "Filipino"},
			{URL: "https://cdn.example.com/fi.vtt", Lang: "fi", Label: "Finnish"},
		},
	}
	sources, err := c.mapStreamResponse(sr, "Test")
	if err != nil {
		t.Fatalf("mapStreamResponse: %v", err)
	}
	if len(sources) != 1 || len(sources[0].Subtitles) != 2 {
		t.Fatalf("sources/subtitles = %d/%v, want 1/2", len(sources), sources)
	}
	got := []string{sources[0].Subtitles[0].Language, sources[0].Subtitles[1].Language}
	if got[0] != "fil" || got[1] != "fi" {
		t.Errorf("subtitle languages = %v, want [fil fi]", got)
	}
}

func TestBingrAnilistPrefixStripping(t *testing.T) {
	c := &Client{}
	series := provider.SearchResult{
		ID:        "anilist:199",
		Type:      provider.ModeAnime,
		MediaType: provider.MediaTypeMovie,
		Title:     "Spirited Away",
	}
	eps, err := c.fetchAnimeEpisodes(context.Background(), series)
	if err != nil {
		t.Fatalf("fetchAnimeEpisodes: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("expected 2 episodes, got %d", len(eps))
	}
	if eps[0].ID != "watch/bingr/199/sub/1" {
		t.Errorf("expected episode ID watch/bingr/199/sub/1, got %q", eps[0].ID)
	}
}
