package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
)

// MediaService orchestrates provider operations for the TUI.
type MediaService struct {
	registry *provider.Registry
}

// NewMediaService constructs a MediaService.
func NewMediaService(registry *provider.Registry) *MediaService {
	return &MediaService{registry: registry}
}

// Search queries providers for a mode and aggregates results.
func (s *MediaService) Search(ctx context.Context, mode provider.ContentType, query string) ([]model.SearchResult, string, []string, error) {
	providers := s.registry.ProvidersForMode(mode)
	if len(providers) == 0 {
		return nil, query, nil, fmt.Errorf("no providers available for mode %q", mode)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	type providerSearchResult struct {
		provider string
		results  []provider.SearchResult
		used     string
		err      error
	}

	ch := make(chan providerSearchResult, len(providers))
	for _, p := range providers {
		p := p
		go func() {
			results, err := p.Search(ctx, query, mode)
			ch <- providerSearchResult{provider: p.Name(), results: results, used: query, err: err}
		}()
	}

	resultsMap := make(map[string]providerSearchResult, len(providers))
	for i := 0; i < len(providers); i++ {
		res := <-ch
		resultsMap[res.provider] = res
	}

	var (
		allResults []model.SearchResult
		usedQuery  = query
		warnings   []string
	)

	for _, p := range providers {
		res, ok := resultsMap[p.Name()]
		if !ok || res.err != nil {
			if ok {
				warnings = append(warnings, fmt.Sprintf("%s: %v", strings.ToUpper(res.provider), res.err))
			}
			continue
		}
		for _, r := range res.results {
			allResults = append(allResults, model.SearchResult{
				Title:     r.Title,
				URL:       r.ID,
				Provider:  res.provider,
				MediaType: r.MediaType,
				Year:      r.Year,
				TMDBID:    r.TMDBID,
			})
		}
		if strings.TrimSpace(res.used) != "" {
			usedQuery = res.used
		}
	}

	if len(allResults) == 0 && len(warnings) > 0 {
		return nil, usedQuery, warnings, fmt.Errorf("%s search failed: %s", strings.ToUpper(string(mode)), warnings[0])
	}

	return allResults, usedQuery, warnings, nil
}

// FetchEpisodes retrieves episode results for a series.
func (s *MediaService) FetchEpisodes(ctx context.Context, mode provider.ContentType, series model.SearchResult, audioMode string) ([]model.EpisodeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	providerName := strings.ToLower(strings.TrimSpace(series.Provider))
	providers := s.registry.ProvidersForMode(mode)
	var p provider.Provider
	for _, prov := range providers {
		if prov.Name() == providerName {
			p = prov
			break
		}
	}
	if p == nil {
		return nil, fmt.Errorf("provider %q not found for mode %q", providerName, mode)
	}

	eps, err := p.FetchEpisodes(ctx, provider.SearchResult{
		Title:     series.Title,
		ID:        series.URL,
		Type:      mode,
		Year:      series.Year,
		MediaType: series.MediaType,
		TMDBID:    series.TMDBID,
	})
	if err != nil {
		return nil, err
	}

	results := make([]model.EpisodeResult, 0, len(eps))
	for _, e := range eps {
		if audioMode != "" && e.Audio != "" {
			normalizedAudio := strings.ToLower(strings.TrimSpace(e.Audio))
			normalizedTarget := strings.ToLower(strings.TrimSpace(audioMode))

			// Handle common variations
			if strings.HasPrefix(normalizedAudio, "sub") {
				normalizedAudio = "sub"
			} else if strings.HasPrefix(normalizedAudio, "dub") {
				normalizedAudio = "dub"
			}

			if normalizedAudio != normalizedTarget {
				continue
			}
		}
		results = append(results, model.EpisodeResult{
			Title:    e.Title,
			URL:      e.ID,
			Number:   e.Episode,
			Season:   e.Season,
			Provider: p.Name(),
			Filler:   e.Filler,
		})
	}
	return results, nil
}

// Resolve resolves playback sources from ALL supporting providers in parallel.
func (s *MediaService) Resolve(ctx context.Context, mode provider.ContentType, series model.SearchResult, episode model.EpisodeResult, onResult func(model.ResolvedMedia)) (model.ResolvedMedia, error) {
	providers := s.registry.ProvidersForMode(mode)
	if len(providers) == 0 {
		return model.ResolvedMedia{}, fmt.Errorf("no providers available for mode %q", mode)
	}

	var mu sync.Mutex
	var allPlaybackSources []model.PlaybackSource
	var allSubtitleTracks []model.SubtitleTrack
	seenSubs := make(map[string]struct{})

	// Helper to build ResolvedMedia from current aggregated sources
	buildResolved := func(playback []model.PlaybackSource, subs []model.SubtitleTrack) model.ResolvedMedia {
		return model.ResolvedMedia{
			SeriesTitle:   series.Title,
			SeriesURL:     series.URL,
			EpisodeTitle:  episode.Title,
			EpisodeURL:    episode.URL,
			MediaURL:      firstPlaybackURL(playback),
			MediaType:     series.MediaType,
			Year:          series.Year,
			TMDBID:        series.TMDBID,
			SeasonNumber:  episode.Season,
			EpisodeNumber: episode.Number,
			Resolver:      "Aggregated",
			Playback:      playback,
			Subtitles:     subs,
		}
	}

	// Build priority map so sources can be ordered by provider priority
	providerPriority := make(map[string]int, len(providers))
	for i, p := range providers {
		providerPriority[p.Name()] = i
	}

	g, gCtx := errgroup.WithContext(ctx)

	for _, p := range providers {
		p := p
		g.Go(func() error {
			// 1. Determine the ID to use for this provider.
			mediaID := series.URL
			if p.Name() != series.Provider {
				if series.TMDBID > 0 {
					mediaID = strconv.Itoa(series.TMDBID)
				} else {
					return nil // Cannot resolve with this provider without TMDB ID
				}
			}

			mediaEpisode := provider.Episode{
				Title:   episode.Title,
				ID:      episode.URL,
				Season:  episode.Season,
				Episode: episode.Number,
				TMDBID:  series.TMDBID,
			}

			// 2. Handle StreamingProviders (incremental updates)
			if sp, ok := p.(provider.StreamingProvider); ok {
				updates := make(chan []provider.MediaSource, 4)
				go func() {
					defer close(updates)
					if err := sp.ResolveStream(gCtx, mediaID, mediaEpisode, updates); err != nil {
						logging.Debugf("streaming provider %q failed: %v", p.Name(), err)
					}
				}()

				for playback := range updates {
					mu.Lock()
					for _, src := range playback {
						allPlaybackSources = append(allPlaybackSources, model.PlaybackSource{
							Label:        src.Quality,
							URL:          src.URL,
							Referer:      src.Referer,
							Type:         src.Type,
							UserAgent:    src.UserAgent,
							CookieHeader: src.CookieHeader,
							Resolver:     p.Name(),
						})
						// Collect subtitles
						for _, subURL := range src.Subtitles {
							if _, ok := seenSubs[subURL]; !ok {
								seenSubs[subURL] = struct{}{}
								allSubtitleTracks = append(allSubtitleTracks, model.SubtitleTrack{
									Label:    fmt.Sprintf("English (%s)", p.Name()),
									Language: "en",
									URL:      subURL,
									Referer:  src.Referer,
								})
							}
						}
					}
					current := buildResolved(allPlaybackSources, keepBestSubtitle(allSubtitleTracks))
					mu.Unlock()
					if onResult != nil {
						onResult(current)
					}
				}
				return nil
			}

			// 3. Handle Standard Providers
			sources, err := p.ResolveSource(gCtx, mediaID, mediaEpisode)
			if err != nil {
				logging.Debugf("provider %q failed to resolve: %v", p.Name(), err)
				return nil
			}

			mu.Lock()
			for _, src := range sources {
				allPlaybackSources = append(allPlaybackSources, model.PlaybackSource{
					Label:        src.Quality,
					URL:          src.URL,
					Referer:      src.Referer,
					Type:         src.Type,
					UserAgent:    src.UserAgent,
					CookieHeader: src.CookieHeader,
					Resolver:     p.Name(),
				})
				// Collect subtitles
				for _, subURL := range src.Subtitles {
					if _, ok := seenSubs[subURL]; !ok {
						seenSubs[subURL] = struct{}{}
						allSubtitleTracks = append(allSubtitleTracks, model.SubtitleTrack{
							Label:    fmt.Sprintf("English (%s)", p.Name()),
							Language: "en",
							URL:      subURL,
							Referer:  src.Referer,
						})
					}
				}
			}
			current := buildResolved(allPlaybackSources, keepBestSubtitle(allSubtitleTracks))
			mu.Unlock()

			if onResult != nil {
				onResult(current)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return model.ResolvedMedia{}, err
	}

	if len(allPlaybackSources) == 0 {
		return model.ResolvedMedia{}, provider.ErrNoSources
	}

	sort.SliceStable(allPlaybackSources, func(i, j int) bool {
		return providerPriority[allPlaybackSources[i].Resolver] < providerPriority[allPlaybackSources[j].Resolver]
	})

	return buildResolved(allPlaybackSources, keepBestSubtitle(allSubtitleTracks)), nil
}

func keepBestSubtitle(tracks []model.SubtitleTrack) []model.SubtitleTrack {
	if len(tracks) == 0 {
		return nil
	}
	for _, t := range tracks {
		if strings.Contains(t.Label, "(vidking)") {
			t.Label = "English"
			return []model.SubtitleTrack{t}
		}
	}
	track := tracks[0]
	track.Label = "English"
	return []model.SubtitleTrack{track}
}

func firstPlaybackURL(playback []model.PlaybackSource) string {
	if len(playback) == 0 {
		return ""
	}
	return playback[0].URL
}
