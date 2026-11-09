package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/manga"
	"kari/internal/provider"
)

// stubManga is a scripted MangaSource for service tests.
type stubManga struct {
	name     string
	chapters []provider.MangaChapter
	pages    []provider.MangaPage
}

func (s *stubManga) Name() string { return s.name }
func (s *stubManga) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeManga, Priority: 1}}
}
func (s *stubManga) Search(_ context.Context, _ string, _ provider.ContentType) ([]provider.SearchResult, error) {
	return []provider.SearchResult{{Title: "T", ID: "s1"}}, nil
}
func (s *stubManga) FetchEpisodes(_ context.Context, _ provider.SearchResult) ([]provider.Episode, error) {
	return nil, provider.ErrNoEpisodes
}
func (s *stubManga) ResolveSource(_ context.Context, _ string, _ provider.Episode) ([]provider.MediaSource, error) {
	return nil, provider.ErrNoSources
}
func (s *stubManga) FetchChapters(_ context.Context, _ provider.SearchResult) ([]provider.MangaChapter, error) {
	return s.chapters, nil
}
func (s *stubManga) FetchPages(_ context.Context, _ provider.MangaChapter) ([]provider.MangaPage, error) {
	return s.pages, nil
}

func TestMangaServiceStampsProvider(t *testing.T) {
	registry := &provider.Registry{}
	registry.Register(&stubManga{name: "stub",
		chapters: []provider.MangaChapter{{ID: "c1", Number: "1"}},
	})
	svc := NewMangaService(registry)
	chapters, err := svc.FetchChapters(context.Background(), provider.SearchResult{ID: "s1", Provider: "stub"})
	if err != nil {
		t.Fatalf("FetchChapters failed: %v", err)
	}
	if len(chapters) != 1 || chapters[0].Provider != "stub" {
		t.Errorf("provider not stamped: %+v", chapters)
	}
}

// TestFetchPagesStampsThrough proves page resolution for a chapter.
func TestFetchPagesStampsThrough(t *testing.T) {
	registry := &provider.Registry{}
	registry.Register(&stubManga{name: "stub",
		pages: []provider.MangaPage{{URL: "https://cdn.example.com/p1.jpg"}},
	})
	svc := NewMangaService(registry)
	pages, err := svc.FetchPages(context.Background(), provider.MangaChapter{ID: "c1", Provider: "stub", Number: "1"})
	if err != nil {
		t.Fatalf("FetchPages: %v", err)
	}
	if len(pages) != 1 {
		t.Errorf("pages = %+v", pages)
	}
	if _, err := svc.FetchPages(context.Background(), provider.MangaChapter{ID: "c1", Provider: "nope"}); err == nil {
		t.Error("unknown page provider must error")
	}
}

func TestMangaServiceUnknownProvider(t *testing.T) {
	svc := NewMangaService(&provider.Registry{})
	if _, err := svc.FetchChapters(context.Background(), provider.SearchResult{Provider: "nope"}); err == nil {
		t.Error("expected error for unknown provider")
	}
}

// TestResolveReadableChapterFallsOver verifies cross-source fallback:
// stubA owns the series but serves no pages, stubB serves chapter 1.
func TestResolveReadableChapterFallsOver(t *testing.T) {
	registry := &provider.Registry{}
	registry.Register(&stubManga{name: "stubA",
		chapters: []provider.MangaChapter{{ID: "a1", Number: "1"}},
	})
	registry.Register(&stubManga{name: "stubB",
		chapters: []provider.MangaChapter{{ID: "b1", Number: "1"}},
		pages:    []provider.MangaPage{{URL: "https://cdn.example/p1.jpg"}},
	})
	svc := NewMangaService(registry)
	got, err := svc.ResolveReadableChapter(context.Background(),
		provider.SearchResult{ID: "s1", Title: "T", Provider: "stubA"},
		provider.MangaChapter{ID: "a1", Number: "1"})
	if err != nil {
		t.Fatalf("ResolveReadableChapter failed: %v", err)
	}
	if got.Series.Provider != "stubB" || len(got.Pages) != 1 {
		t.Errorf("fell over to %+v", got.Series)
	}
}

func TestResolveReadableChapterRejectsBrokenImageURL(t *testing.T) {
	var pngBytes bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	if err := png.Encode(&pngBytes, img); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>missing page</html>"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes.Bytes())
	}))
	defer server.Close()

	registry := &provider.Registry{}
	registry.Register(&stubManga{name: "broken",
		chapters: []provider.MangaChapter{{ID: "broken-1", Number: "1"}},
		pages:    []provider.MangaPage{{URL: server.URL + "/bad"}},
	})
	registry.Register(&stubManga{name: "working",
		chapters: []provider.MangaChapter{{ID: "working-1", Number: "1"}},
		pages:    []provider.MangaPage{{URL: server.URL + "/good"}},
	})
	svc := NewMangaService(registry, manga.NewClient())
	got, err := svc.ResolveReadableChapter(context.Background(),
		provider.SearchResult{ID: "series", Title: "T", Provider: "broken"},
		provider.MangaChapter{ID: "broken-1", Number: "1"})
	if err != nil {
		t.Fatalf("ResolveReadableChapter: %v", err)
	}
	if got.Series.Provider != "working" {
		t.Fatalf("selected provider %q, want working", got.Series.Provider)
	}
}

// TestResolveReadableChapterHopeless verifies ErrNoSources when no
// source serves pages.
func TestResolveReadableChapterHopeless(t *testing.T) {
	registry := &provider.Registry{}
	registry.Register(&stubManga{name: "stubA",
		chapters: []provider.MangaChapter{{ID: "a1", Number: "1"}},
	})
	svc := NewMangaService(registry)
	_, err := svc.ResolveReadableChapter(context.Background(),
		provider.SearchResult{ID: "s1", Title: "T", Provider: "stubA"},
		provider.MangaChapter{ID: "a1", Number: "1"})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("expected ErrNoSources, got %v", err)
	}
}
