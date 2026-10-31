package atsu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
)

func TestAtsuMetadata(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if c.Name() != "atsu" {
		t.Errorf("expected atsu, got %s", c.Name())
	}
	if c.Alias() != "Atsu" {
		t.Errorf("expected Atsu, got %s", c.Alias())
	}

	modes := c.Modes()
	if len(modes) != 1 || modes[0].Name != provider.ModeManga {
		t.Errorf("expected ModeManga, got %+v", modes)
	}

	f := c.Features(provider.ModeManga)
	if f.SearchPlaceholder != "Search manga…" {
		t.Errorf("unexpected SearchPlaceholder: %q", f.SearchPlaceholder)
	}
	if c.Features(provider.ModeAnime).SearchPlaceholder != "" {
		t.Errorf("expected empty features for non-manga mode")
	}

	if _, err := c.FetchEpisodes(context.Background(), provider.SearchResult{}); !errors.Is(err, provider.ErrNoEpisodes) {
		t.Errorf("expected ErrNoEpisodes, got %v", err)
	}
	if _, err := c.ResolveSource(context.Background(), "id", provider.Episode{}); !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("expected ErrNoSources, got %v", err)
	}
}

func TestAtsuSearchAndChapters(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/collections/manga/documents/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("q")
		if q != "Naruto" {
			t.Errorf("expected query Naruto, got %s", q)
		}
		if r.URL.Query().Get("filter_by") != "" {
			t.Errorf("filter_by should not be set, got %s", r.URL.Query().Get("filter_by"))
		}
		fmt.Fprint(w, `{
			"found": 1,
			"hits": [
				{
					"document": {
						"id": "rSmmf",
						"title": "Naruto",
						"englishTitle": "Naruto",
						"poster": "/static/posters/poster.jpg",
						"posterMedium": "/static/posters/poster-medium.avif",
						"year": 1999
					}
				}
			]
		}`)
	})

	mux.HandleFunc("/api/manga/allChapters", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mangaId") != "rSmmf" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"chapters": [
				{"id": "ch2", "number": 2, "title": "Chapter 2", "scanlationName": "Group B"},
				{"id": "ch1", "number": 1, "title": "Chapter 1", "scanlationName": "Group A"}
			]
		}`)
	})

	mux.HandleFunc("/api/read/chapter", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mangaId") != "rSmmf" || r.URL.Query().Get("chapterId") != "ch1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"readChapter": {
				"id": "ch1",
				"pages": [
					{"number": 0, "image": "pages/rSmmf/ch1/0.webp", "width": 800, "height": 1200},
					{"number": 1, "image": "//cdn.atsu.moe/static/pages/rSmmf/ch1/1.webp", "width": 800, "height": 1200}
				]
			}
		}`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClientWithURLs(srv.URL, "https://cdn.atsu.moe", srv.Client())
	if err != nil {
		t.Fatalf("NewClientWithURLs failed: %v", err)
	}

	// 1. Test Search
	results, err := client.Search(context.Background(), "Naruto", provider.ModeManga)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 || results[0].ID != "rSmmf" {
		t.Fatalf("unexpected search results: %+v", results)
	}
	// Verify JPEG poster was preferred over AVIF
	if results[0].CoverURL != "https://cdn.atsu.moe/static/posters/poster.jpg" {
		t.Errorf("unexpected cover URL: %s", results[0].CoverURL)
	}

	// 2. Test FetchChapters (should sort ascending: ch1 then ch2)
	chapters, err := client.FetchChapters(context.Background(), results[0])
	if err != nil {
		t.Fatalf("FetchChapters failed: %v", err)
	}
	if len(chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(chapters))
	}
	if chapters[0].Number != "1" || chapters[1].Number != "2" {
		t.Errorf("chapters not sorted ascending: %+v", chapters)
	}
	if chapters[0].Group != "Group A" || chapters[1].Group != "Group B" {
		t.Errorf("unexpected scanlation group names: %+v", chapters)
	}

	// 3. Test FetchPages (both relative and protocol-relative image URLs resolved properly)
	pages, err := client.FetchPages(context.Background(), chapters[0])
	if err != nil {
		t.Fatalf("FetchPages failed: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(pages))
	}
	if pages[0].URL != "https://cdn.atsu.moe/static/pages/rSmmf/ch1/0.webp" {
		t.Errorf("unexpected page 0 URL: %s", pages[0].URL)
	}
	if pages[1].URL != "https://cdn.atsu.moe/static/pages/rSmmf/ch1/1.webp" {
		t.Errorf("unexpected page 1 URL: %s", pages[1].URL)
	}
}

func TestAtsuSearchEmptyAndErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/manga/documents/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"found": 0, "hits": []}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClientWithURLs(srv.URL, "https://cdn.atsu.moe", srv.Client())
	if err != nil {
		t.Fatalf("NewClientWithURLs failed: %v", err)
	}

	// Empty query returns ErrNoResults
	if _, err := client.Search(context.Background(), "  ", provider.ModeManga); !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("expected ErrNoResults for whitespace query, got %v", err)
	}

	// Zero hits returns ErrNoResults
	if _, err := client.Search(context.Background(), "Nonexistent", provider.ModeManga); !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("expected ErrNoResults for zero hits, got %v", err)
	}

	// FetchChapters with empty series ID returns ErrNoEpisodes
	if _, err := client.FetchChapters(context.Background(), provider.SearchResult{ID: ""}); !errors.Is(err, provider.ErrNoEpisodes) {
		t.Errorf("expected ErrNoEpisodes, got %v", err)
	}

	// FetchPages with malformed handle returns ErrNoSources
	if _, err := client.FetchPages(context.Background(), provider.MangaChapter{ID: "invalid"}); !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("expected ErrNoSources, got %v", err)
	}
}
