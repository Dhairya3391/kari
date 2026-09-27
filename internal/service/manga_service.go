package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"kari/internal/logging"
	"kari/internal/provider"
)

// mangaLog scopes every line from this component.
var mangaLog = logging.With("component", "service.manga")

// MangaService orchestrates manga/comic providers for the TUI: chapter
// listings and page-image URLs for a selected title. Search itself stays
// on MediaService, which is already mode-generic.
type MangaService struct {
	registry *provider.Registry
}

// NewMangaService constructs a MangaService.
func NewMangaService(registry *provider.Registry) *MangaService {
	return &MangaService{registry: registry}
}

// mangaSource resolves the named provider and asserts the MangaSource
// capability, so callers never switch on provider names themselves.
func (s *MangaService) mangaSource(name string) (provider.MangaSource, error) {
	p, ok := s.registry.ProviderByName(name)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	ms, ok := p.(provider.MangaSource)
	if !ok {
		return nil, fmt.Errorf("provider %q does not serve manga", name)
	}
	return ms, nil
}

// FetchChapters lists chapters for the series, oldest first.
func (s *MangaService) FetchChapters(ctx context.Context, series provider.SearchResult) ([]provider.MangaChapter, error) {
	ms, err := s.mangaSource(series.Provider)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	chapters, err := ms.FetchChapters(ctx, series)
	if err != nil {
		return nil, fmt.Errorf("chapters for %q: %w", series.Title, err)
	}
	for i := range chapters {
		chapters[i].Provider = series.Provider
	}
	mangaLog.Debug("chapters done", "title", series.Title, "provider", series.Provider, "count", len(chapters))
	return chapters, nil
}

// ReadableChapter is a chapter with resolved pages plus the series and
// listing context it was found under — which may belong to a different
// provider than the one originally selected.
type ReadableChapter struct {
	Series   provider.SearchResult
	Chapters []provider.MangaChapter
	Index    int
	Pages    []provider.MangaPage
}

// ResolveReadableChapter finds the requested chapter readable on any
// manga provider: the owning provider first, then every other manga
// provider in priority order (title search, same chapter-number match).
// Licensed purges routinely remove uploads on one source while another
// still serves them, so a dead chapter here rarely means dead
// everywhere. At most maxFallbackSeries title candidates are examined
// and the first with resolvable pages wins.
func (s *MangaService) ResolveReadableChapter(ctx context.Context, series provider.SearchResult, chapter provider.MangaChapter) (ReadableChapter, error) {
	const maxFallbackSeries = 3

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	type candidate struct {
		series   provider.SearchResult
		chapters []provider.MangaChapter
		index    int
	}
	var candidates []candidate

	ownChapters, err := s.FetchChapters(ctx, series)
	if err == nil {
		if idx, ok := findChapterNumber(ownChapters, chapter.Number); ok {
			candidates = append(candidates, candidate{series: series, chapters: ownChapters, index: idx})
		}
	}

	for _, p := range s.registry.ProvidersForMode(provider.ModeManga) {
		if len(candidates) >= maxFallbackSeries {
			break
		}
		if p.Name() == series.Provider {
			continue
		}
		ms, ok := p.(provider.MangaSource)
		if !ok {
			continue
		}
		// Per-call bounds inside the 60s overall budget: one slow
		// provider must not starve the remaining fallbacks.
		searchCtx, searchCancel := context.WithTimeout(ctx, 15*time.Second)
		results, err := ms.Search(searchCtx, series.Title, provider.ModeManga)
		searchCancel()
		if err != nil || len(results) == 0 {
			continue
		}
		for _, r := range results[:min(len(results), maxFallbackSeries)] {
			r.Provider = p.Name()
			chCtx, chCancel := context.WithTimeout(ctx, 20*time.Second)
			chapters, err := ms.FetchChapters(chCtx, r)
			chCancel()
			if err != nil {
				continue
			}
			for i := range chapters {
				chapters[i].Provider = p.Name()
			}
			if idx, ok := findChapterNumber(chapters, chapter.Number); ok {
				candidates = append(candidates, candidate{series: r, chapters: chapters, index: idx})
			}
			if len(candidates) >= maxFallbackSeries {
				break
			}
		}
	}

	for _, cand := range candidates {
		ms, err := s.mangaSource(cand.series.Provider)
		if err != nil {
			continue
		}
		pgCtx, pgCancel := context.WithTimeout(ctx, 20*time.Second)
		pages, err := ms.FetchPages(pgCtx, cand.chapters[cand.index])
		pgCancel()
		if err != nil || len(pages) == 0 {
			continue
		}
		mangaLog.Debug("readable chapter found", "title", cand.series.Title, "provider", cand.series.Provider, "chapter", chapter.Number)
		return ReadableChapter{Series: cand.series, Chapters: cand.chapters, Index: cand.index, Pages: pages}, nil
	}
	return ReadableChapter{}, provider.ErrNoSources
}

// findChapterNumber locates a chapter by its catalog number. Empty
// numbers never match (oneshots would collide).
func findChapterNumber(chapters []provider.MangaChapter, number string) (int, bool) {
	number = strings.TrimSpace(number)
	if number == "" {
		return 0, false
	}
	for i, ch := range chapters {
		if strings.TrimSpace(ch.Number) == number {
			return i, true
		}
	}
	return 0, false
}

// FetchPages resolves a chapter's page-image URLs.
func (s *MangaService) FetchPages(ctx context.Context, chapter provider.MangaChapter) ([]provider.MangaPage, error) {
	ms, err := s.mangaSource(chapter.Provider)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pages, err := ms.FetchPages(ctx, chapter)
	if err != nil {
		return nil, fmt.Errorf("pages for chapter %q: %w", chapter.DisplayLabel(), err)
	}
	mangaLog.Debug("pages done", "chapter", chapter.DisplayLabel(), "count", len(pages))
	return pages, nil
}
