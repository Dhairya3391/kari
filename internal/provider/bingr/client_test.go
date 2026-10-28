package bingr

import (
	"context"
	"encoding/json"
	"kari/internal/provider"
	"kari/internal/tmdb"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestBingrLanguageFilter(t *testing.T) {
	c, err := NewClientWithLanguageFilter(tmdb.NewKeyPool([]string{"dummy-key"}), map[string]bool{
		"en": true,
		"ja": true,
		"hi": false,
	})
	if err != nil {
		t.Fatalf("NewClientWithLanguageFilter: %v", err)
	}

	if !c.languageEnabled("en") {
		t.Errorf("languageEnabled(en) = false, want true")
	}
	if !c.languageEnabled("ja") {
		t.Errorf("languageEnabled(ja) = false, want true")
	}
	if c.languageEnabled("hi") {
		t.Errorf("languageEnabled(hi) = true, want false")
	}
	if !c.languageEnabled("") {
		t.Errorf("languageEnabled('') = false, want true")
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

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL, nil)
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
	hasPolarisHindi := false
	for _, s := range sources {
		if strings.Contains(s.Quality, "DarkMatter") && strings.HasPrefix(s.URL, "https://cdn.example.com") {
			hasDarkMatter = true
		}
		if strings.Contains(s.Quality, "Polaris") && s.Language == "hi" {
			hasPolarisHindi = true
			if len(s.Subtitles) == 0 || s.Subtitles[0].Language != "en" {
				t.Errorf("Polaris stream missing en subtitle: %+v", s.Subtitles)
			}
		}
	}
	if !hasDarkMatter {
		t.Errorf("missing DarkMatter 1080p stream: %+v", sources)
	}
	if !hasPolarisHindi {
		t.Errorf("missing Polaris Hindi stream: %+v", sources)
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

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL, nil)
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

	// HiAnime / MegaPlay token endpoint
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token": "test-filmu-token",
		})
	})

	// HiAnime / MegaPlay stream endpoint
	mux.HandleFunc("/hianime/megaplay", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-filmu-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("malId") != "52991" || r.URL.Query().Get("ep") != "1" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(megaplayResponse{
			Streams: []megaplayStream{
				{
					Server:  "HiAnime [MegaPlay]",
					URL:     "https://cdn.example.com/anime/frieren/master.m3u8",
					Type:    "m3u8",
					Referer: "https://megaplay.buzz/",
					DubType: "SUB",
					Subtitles: []megaplaySubOption{
						{
							Lang:  "eng",
							Label: "English",
							URL:   "https://hianime.filmu.in/proxy/subtitle?url=https%3A%2F%2Fsubs.example.com%2Ffrieren-en.vtt&referer=https%3A%2F%2Fmegaplay.buzz%2F",
						},
					},
				},
			},
		})
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

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL, nil)
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
		if strings.Contains(s.Quality, "MegaPlay") {
			hasMegaPlaySub = true
			if s.Referer != "https://megaplay.buzz/" {
				t.Errorf("MegaPlay referer = %q", s.Referer)
			}
			if len(s.Subtitles) == 0 || s.Subtitles[0].URL != "https://subs.example.com/frieren-en.vtt" {
				t.Errorf("expected unwrapped subtitle URL, got: %+v", s.Subtitles)
			}
		}
		if strings.Contains(s.Quality, "Polaris") && s.Language == "" {
			hasPolarisOriginal = true
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
	for _, s := range dubSources {
		if s.Language == "hi" {
			hasHindiDub = true
		}
	}
	if !hasHindiDub {
		t.Errorf("expected Hindi dub source in dubSources: %+v", dubSources)
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

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL, nil)
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
	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, "http://127.0.0.1:1", nil)
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

func TestDetectAudioLanguage(t *testing.T) {
	tests := []struct {
		name  string
		label string
		want  string
	}{
		{"Original — 1080p", "", ""},
		{"Arabic sub — 1080p", "", ""},
		{"Hindi — 1080p", "", "hi"},
		{"French — 1080p", "", "fr"},
		{"Spanish — 720p", "", "es"},
		{"Tagalog — 720p", "", "tl"},
		{"English — 1080p", "", "en"},
		{"German — 1080p", "", "de"},
		{"Italian — 1080p", "", "it"},
		{"1080p", "Aphelion", ""},
	}
	for _, tt := range tests {
		got := detectAudioLanguage(tt.name, tt.label)
		if got != tt.want {
			t.Errorf("detectAudioLanguage(%q, %q) = %q, want %q", tt.name, tt.label, got, tt.want)
		}
	}
}

func TestUnwrapProxyURL(t *testing.T) {
	direct := "https://cdn.example.com/subs/test.vtt"
	proxied := "https://hianime.filmu.in/proxy/subtitle?url=" + direct + "&referer=https%3A%2F%2Fmegaplay.buzz%2F"
	if got := unwrapProxyURL(proxied); got != direct {
		t.Errorf("unwrapProxyURL(%q) = %q, want %q", proxied, got, direct)
	}
	if got := unwrapProxyURL(direct); got != direct {
		t.Errorf("unwrapProxyURL(%q) = %q, want %q", direct, got, direct)
	}
	if got := unwrapProxyURL(""); got != "" {
		t.Errorf("unwrapProxyURL('') = %q, want ''", got)
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
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
	})
	mux.HandleFunc("/hianime/megaplay", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(megaplayResponse{
			Streams: []megaplayStream{
				{
					Server:  "HiAnime [MegaPlay]",
					URL:     "https://cdn.example.com/spirited-away/master.m3u8",
					Type:    "m3u8",
					Referer: "https://megaplay.buzz/",
				},
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewClientWithURLs(tmdb.NewKeyPool([]string{"dummy-key"}), srv.URL, srv.URL, srv.URL, nil)
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
	if sources[0].URL != "https://cdn.example.com/spirited-away/master.m3u8" {
		t.Errorf("sources[0].URL = %q", sources[0].URL)
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

func TestFilterAnimeAudioSourcesNoLeakage(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "https://cdn.example.com/sub.m3u8", Language: "ja"},
	}
	kept := filterAnimeAudioSources(sources, provider.AudioDub)
	if len(kept) != 0 {
		t.Fatalf("filterAnimeAudioSources leaked Japanese audio into dub: %+v", kept)
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
