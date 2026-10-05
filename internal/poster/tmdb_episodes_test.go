package poster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/tmdb"
)

// newTMDBEpisodeServer serves the minimal /search/tv, /tv/{id}, and
// /tv/{id}/season/{n} surface the fallback walks, with season 1 carrying
// three episodes (one unnamed) and season 2 two.
func newTMDBEpisodeServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/search/tv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":7}]}`))
	})
	mux.HandleFunc("/tv/7", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"seasons":[{"season_number":0,"episode_count":2},{"season_number":1,"episode_count":3},{"season_number":2,"episode_count":2}]}`))
	})
	mux.HandleFunc("/tv/7/season/0", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"episodes":[{"episode_number":1,"name":"Special One"},{"episode_number":2,"name":"Special Two"}]}`))
	})
	mux.HandleFunc("/tv/7/season/1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"episodes":[{"episode_number":1,"name":"Pilot"},{"episode_number":2,"name":""},{"episode_number":3,"name":"Third"}]}`))
	})
	mux.HandleFunc("/tv/7/season/2", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"episodes":[{"episode_number":1,"name":"Fourth"},{"episode_number":2,"name":"Fifth"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Specials are excluded and regular seasons concatenate into the absolute
// numbering anime providers use, skipping unnamed rows.
func TestFetchEpisodeTitlesTMDB(t *testing.T) {
	srv := newTMDBEpisodeServer(t)
	client := &Client{
		http:     srv.Client(),
		keyPool:  tmdb.NewKeyPool([]string{"test-key"}),
		tmdbBase: srv.URL,
	}

	titles, err := client.FetchEpisodeTitlesTMDB(context.Background(), "Naruto", 2002)
	if err != nil {
		t.Fatalf("FetchEpisodeTitlesTMDB failed: %v", err)
	}
	want := map[int]string{1: "Pilot", 3: "Third", 4: "Fourth", 5: "Fifth"}
	if len(titles) != len(want) {
		t.Fatalf("titles = %v, want %v", titles, want)
	}
	for num, title := range want {
		if titles[num] != title {
			t.Errorf("titles[%d] = %q, want %q", num, titles[num], title)
		}
	}
	if _, ok := titles[2]; ok {
		t.Errorf("unnamed episode 2 must be skipped, got %q", titles[2])
	}
}

func TestFetchEpisodeTitlesTMDBSeasonSpecific(t *testing.T) {
	srv := newTMDBEpisodeServer(t)
	client := &Client{
		http:     srv.Client(),
		keyPool:  tmdb.NewKeyPool([]string{"test-key"}),
		tmdbBase: srv.URL,
	}

	// Season 2 requested: must return Season 2 episodes starting at 1 ("Fourth", "Fifth"),
	// NOT Season 1 episodes concatenated.
	titles, err := client.FetchEpisodeTitlesTMDB(context.Background(), "Naruto Season 2", 2004)
	if err != nil {
		t.Fatalf("FetchEpisodeTitlesTMDB failed: %v", err)
	}
	want := map[int]string{1: "Fourth", 2: "Fifth"}
	if len(titles) != len(want) {
		t.Fatalf("titles = %v, want %v", titles, want)
	}
	for num, title := range want {
		if titles[num] != title {
			t.Errorf("titles[%d] = %q, want %q", num, titles[num], title)
		}
	}
}

// Shows whose seasons already carry absolute numbers (Naruto, One Piece)
// must not have the running offset added on top, which would shift every
// season after the first onto the wrong titles.
func TestFetchEpisodeTitlesTMDBAbsoluteNumbering(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/tv", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"id":9}]}`))
	})
	mux.HandleFunc("/tv/9", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"seasons":[{"season_number":1,"episode_count":2},{"season_number":2,"episode_count":2},{"season_number":3,"episode_count":1}]}`))
	})
	mux.HandleFunc("/tv/9/season/1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"episodes":[{"episode_number":1,"name":"One"},{"episode_number":2,"name":"Two"}]}`))
	})
	mux.HandleFunc("/tv/9/season/2", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"episodes":[{"episode_number":3,"name":"Three"},{"episode_number":4,"name":"Four"}]}`))
	})
	mux.HandleFunc("/tv/9/season/3", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"episodes":[{"episode_number":5,"name":"Five"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := &Client{http: srv.Client(), keyPool: tmdb.NewKeyPool([]string{"k"}), tmdbBase: srv.URL}
	titles, err := client.FetchEpisodeTitlesTMDB(context.Background(), "Naruto", 2002)
	if err != nil {
		t.Fatalf("FetchEpisodeTitlesTMDB failed: %v", err)
	}
	want := map[int]string{1: "One", 2: "Two", 3: "Three", 4: "Four", 5: "Five"}
	for num, title := range want {
		if titles[num] != title {
			t.Errorf("titles[%d] = %q, want %q (offset must not be added)", num, titles[num], title)
		}
	}
}

func TestFetchEpisodeTitlesTMDBRequiresTitleAndKeys(t *testing.T) {
	client := &Client{http: http.DefaultClient}
	if _, err := client.FetchEpisodeTitlesTMDB(context.Background(), "  ", 0); err == nil {
		t.Error("expected an error for a blank title")
	}
	if _, err := client.FetchEpisodeTitlesTMDB(context.Background(), "Naruto", 0); err == nil {
		t.Error("expected an error when no TMDB keys are configured")
	}
}
