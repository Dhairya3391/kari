package weebcentral

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
)

func testClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := NewClientWithBaseURL(srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL failed: %v", err)
	}
	return c
}

const searchFragment = `
<article class="bg-base-300 flex gap-4 p-4">
<a href="https://example.com/series/AAA111/One-Piece">
<picture><img src="https://img.example.com/cover/fallback/AAA111.jpg" alt="One Piece cover"></picture>
</a>
<a href="https://example.com/series/AAA111/One-Piece">
<div>One Piece</div>
</a>
<div>Year:</div><div>1997</div>
<div>Status:</div><div>Ongoing</div>
<div>Tag(s):</div><div>Action,</div><div>Adventure,</div>
</article>
<article class="bg-base-300 flex gap-4 p-4">
<a href="https://example.com/series/BBB222/Other">
<picture><img src="https://img.example.com/cover/fallback/BBB222.jpg" alt="Other Title cover"></picture>
</a>
<a href="https://example.com/series/BBB222/Other">
<div>Other Title</div>
</a>
<div>Year:</div><div>2020</div>
</article>`

func TestSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/data", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("text") == "" || q.Get("display_mode") == "" {
			t.Errorf("missing query params: %v", r.URL.RawQuery)
		}
		if r.Header.Get("HX-Request") == "" {
			t.Errorf("missing HX-Request header")
		}
		w.Write([]byte(searchFragment))
	})
	c := testClient(t, mux)

	results, err := c.Search(context.Background(), "one piece", provider.ModeManga)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (same-id repeats merged)", len(results))
	}
	r := results[0]
	if r.Title != "One Piece" || r.ID != "AAA111" {
		t.Errorf("result = %+v", r)
	}
	if r.Year != "1997" {
		t.Errorf("year = %q", r.Year)
	}
	if !strings.HasSuffix(r.CoverURL, "/cover/fallback/AAA111.jpg") {
		t.Errorf("cover = %q", r.CoverURL)
	}
	if len(r.Genres) != 2 || r.Genres[0] != "Action" || r.Genres[1] != "Adventure" {
		t.Errorf("genres = %v", r.Genres)
	}
	if r.MediaType != provider.MediaTypeManga || r.Type != provider.ModeManga {
		t.Errorf("wrong typing: %+v", r)
	}
}
func TestSearchEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/data", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<div class="empty">nothing here</div>`))
	})
	c := testClient(t, mux)
	if _, err := c.Search(context.Background(), "zzzz", provider.ModeManga); !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("expected ErrNoResults, got %v", err)
	}
}

// TestQuotedAttributes pins tag stripping when attribute values carry
// ">" (data-tip tooltips): the live fragment broke naive stripping and
// leaked markup into year/genre lines.
func TestQuotedAttributes(t *testing.T) {
	seg := `<span class="tooltip" data-tip="A > B">One Piece</span><div>Year:</div><div>1997</div>`
	lines := textLines(seg)
	joined := strings.Join(lines, "|")
	if strings.Contains(joined, "<") || strings.Contains(joined, "data-tip") {
		t.Errorf("markup leaked: %v", lines)
	}
	if seriesYear(seg) != "1997" {
		t.Errorf("year not parsed through quoted attrs: %v", lines)
	}
}

func TestFetchChapters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/series/S1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`
			<a href="/chapters/C3" class="hover:bg-base-300"><span><img></span>
				<span class="grow"><span>Chapter 3</span>
				<span>Last Read 2024-01-01</span></span></a>`))
	})
	mux.HandleFunc("/series/S1/chapter-select", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("current_chapter") != "C3" {
			t.Errorf("current_chapter = %q, want C3", r.URL.Query().Get("current_chapter"))
		}
		w.Write([]byte(`
			<a href="https://example.com/chapters/C1" class="w-full btn bg-base-200">Chapter 1</a>
			<a href="https://example.com/chapters/C2" class="w-full btn bg-base-200">Chapter 2</a>
			<a href="https://example.com/chapters/C3" class="w-full btn bg-base-200">Chapter 3</a>`))
	})
	c := testClient(t, mux)

	chapters, err := c.FetchChapters(context.Background(), provider.SearchResult{ID: "S1", Title: "T"})
	if err != nil {
		t.Fatalf("FetchChapters failed: %v", err)
	}
	if len(chapters) != 3 {
		t.Fatalf("got %d chapters, want 3 (series-page row merged, overlap deduped)", len(chapters))
	}
	if chapters[0].Number != "1" || chapters[2].Number != "3" {
		t.Errorf("chapters not oldest-first: %+v", chapters)
	}
	if got := chapters[0].DisplayLabel(); got != "Ch 1" {
		t.Errorf("label = %q", got)
	}
}

func TestFetchChaptersSeriesPageOnly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/series/S1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="/chapters/C1"><span>Chapter 1</span><span>Last Read x</span></a>`))
	})
	mux.HandleFunc("/series/S1/chapter-select", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	c := testClient(t, mux)

	chapters, err := c.FetchChapters(context.Background(), provider.SearchResult{ID: "S1", Title: "T"})
	if err != nil || len(chapters) != 1 || chapters[0].Number != "1" {
		t.Fatalf("chapters = %+v, err = %v", chapters, err)
	}
}

func TestFetchChaptersNoSeries(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/series/S1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	c := testClient(t, mux)
	if _, err := c.FetchChapters(context.Background(), provider.SearchResult{ID: "S1"}); err == nil {
		t.Errorf("expected error for missing series")
	}
}

func TestFetchPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/chapters/C1/images", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("is_prev") != "False" {
			t.Errorf("is_prev = %q", r.URL.Query().Get("is_prev"))
		}
		w.Write([]byte(`
			<img src="https://cdn.example/m/0001-001.png">
			<img src="https://cdn.example/m/0001-002.png">
			<img src="https://cdn.example/m/0001-002.png">`))
	})
	c := testClient(t, mux)

	pages, err := c.FetchPages(context.Background(), provider.MangaChapter{ID: "C1"})
	if err != nil || len(pages) != 2 {
		t.Fatalf("pages = %+v, err = %v", pages, err)
	}
	if pages[0].URL != "https://cdn.example/m/0001-001.png" {
		t.Errorf("page URL = %q", pages[0].URL)
	}
	if pages[0].Referer == "" || pages[0].UserAgent == "" {
		t.Errorf("page missing request headers: %+v", pages[0])
	}
}

func TestFetchPagesEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/chapters/C1/images", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<div>no images</div>`))
	})
	c := testClient(t, mux)
	if _, err := c.FetchPages(context.Background(), provider.MangaChapter{ID: "C1"}); !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("expected ErrNoSources, got %v", err)
	}
}

func TestParseChapterRows(t *testing.T) {
	rows := parseChapterRows(`
		<a href="/chapters/C1"><span><img></span><span>Chapter 1</span><span>Last Read 2024-01-01</span></a>
		<a href="/chapters/C2">Chapter 12.5: Extra</a>
		<a href="/chapters/C3">Oneshot Special</a>`)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[0].Number != "1" || rows[0].Title != "" {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[1].Number != "12.5" || rows[1].Title != "Extra" {
		t.Errorf("row1 = %+v", rows[1])
	}
	if rows[2].Number != "" || rows[2].Title != "Oneshot Special" {
		t.Errorf("row2 = %+v", rows[2])
	}
}
