package anipm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
)

func TestClientIdentityAndModes(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if c.Name() != "anipm" {
		t.Errorf("Name = %q, want anipm", c.Name())
	}
	if c.Alias() != "Ani.pm" {
		t.Errorf("Alias = %q, want Ani.pm", c.Alias())
	}
	modes := c.Modes()
	if len(modes) != 1 || modes[0].Name != provider.ModeAnime || modes[0].Priority != 2 {
		t.Errorf("Modes = %+v, want ModeAnime at Priority 2", modes)
	}
	if !c.RequiresEpisodeListForMovies() {
		t.Errorf("RequiresEpisodeListForMovies want true")
	}
	features := c.Features(provider.ModeAnime)
	if !features.AudioSelection {
		t.Errorf("Features.AudioSelection want true")
	}
}

func TestSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/anime/search" {
			_ = json.NewEncoder(w).Encode(searchResponse{
				Items: []searchItem{
					{
						ID:           1642,
						Title:        "One Piece",
						Year:         1999,
						Type:         "TV",
						EpisodeCount: 1180,
						AniListID:    "21",
						Genres:       []string{"Action", "Adventure"},
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClientWithBaseURL(srv.URL, srv.URL)
	results, err := c.Search(context.Background(), "One Piece", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.ID != "21" {
		t.Errorf("ID = %q, want 21 (anilist ID)", r.ID)
	}
	if r.Title != "One Piece" {
		t.Errorf("Title = %q, want One Piece", r.Title)
	}
	if r.Year != "1999" {
		t.Errorf("Year = %q, want 1999", r.Year)
	}
}

func TestFetchEpisodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/anime/ani/21" {
			_ = json.NewEncoder(w).Encode(seriesResponse{
				ID:        21,
				Title:     "ONE PIECE",
				SettlarID: 1642,
				Episodes: []episodeItem{
					{Number: 1, Title: "Episode 1", Sub: true, Dub: true},
					{Number: 2, Title: "Episode 2", Sub: true, Dub: false},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClientWithBaseURL(srv.URL, srv.URL)
	episodes, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "21", Title: "One Piece"})
	if err != nil {
		t.Fatalf("FetchEpisodes failed: %v", err)
	}
	if len(episodes) != 3 {
		t.Fatalf("got %d episodes, want 3 (ep1 sub, ep1 dub, ep2 sub)", len(episodes))
	}
	if episodes[0].Episode != 1 || episodes[0].Audio != provider.AudioSub {
		t.Errorf("episodes[0] = %+v", episodes[0])
	}
	if episodes[1].Episode != 1 || episodes[1].Audio != provider.AudioDub {
		t.Errorf("episodes[1] = %+v", episodes[1])
	}
	if episodes[2].Episode != 2 || episodes[2].Audio != provider.AudioSub {
		t.Errorf("episodes[2] = %+v", episodes[2])
	}
}

func TestResolveSource(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/anime/playback-bootstrap/anilist/21":
			_ = json.NewEncoder(w).Encode(bootstrapResponse{
				SettlarSelection:  "sel-token-123",
				EffectiveLanguage: "sub",
			})
		case "/api/anime/settlar/session":
			_ = json.NewEncoder(w).Encode(sessionResponse{
				EmbedURL: srv.URL + "/embed/v1?t=t-token-456",
			})
		case "/api/embed/session":
			if r.URL.Query().Get("t") != "t-token-456" {
				http.Error(w, "bad t", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(playbackSession{
				Title:     "One Piece — Episode 1",
				Source:    "https://media.settlar.io/v1/stream.m3u8",
				AudioLang: "ja",
				Subtitles: []playbackSubtitle{
					{
						URL:     "https://media.settlar.io/v1/sub.vtt",
						Label:   "English",
						SrcLang: "en",
						Default: true,
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL(srv.URL, srv.URL)
	sources, err := c.ResolveSource(context.Background(), "21", provider.Episode{
		Episode: 1,
		Audio:   "sub",
		ID:      "watch/anipm/21/sub/1",
	})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("got %d sources, want 1", len(sources))
	}
	s := sources[0]
	if s.URL != "https://media.settlar.io/v1/stream.m3u8" {
		t.Errorf("URL = %q", s.URL)
	}
	if s.Language != "ja" {
		t.Errorf("Language = %q, want ja", s.Language)
	}
	if len(s.Subtitles) != 1 || s.Subtitles[0].Language != "en" {
		t.Errorf("Subtitles = %+v", s.Subtitles)
	}
}
