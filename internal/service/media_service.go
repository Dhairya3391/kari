package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"

	"golang.org/x/sync/errgroup"
)

// log scopes every line from this package/component.
var mediaLog = logging.With("component", "service.media")

// MediaService orchestrates provider operations for the TUI.
type MediaService struct {
	registry *provider.Registry
}

// NewMediaService constructs a MediaService.
func NewMediaService(registry *provider.Registry) *MediaService {
	return &MediaService{registry: registry}
}

// Search fans a query out to every provider supporting the mode under a
// shared deadline and merges results in provider priority order. Each
// result's Provider field names the service that produced it, and
// per-provider failures become warnings rather than errors.
func (s *MediaService) Search(ctx context.Context, mode provider.ContentType, query string) ([]provider.SearchResult, string, []string, error) {
	providers := s.registry.ProvidersForMode(mode)
	if len(providers) == 0 {
		return nil, query, nil, fmt.Errorf("no providers available for mode %q", mode)
	}

	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	type providerSearchResult struct {
		provider string
		results  []provider.SearchResult
		err      error
	}

	ch := make(chan providerSearchResult, len(providers))
	for _, p := range providers {
		p := p
		go func() {
			results, err := p.Search(ctx, query, mode)
			ch <- providerSearchResult{provider: p.Name(), results: results, err: err}
		}()
	}

	resultsMap := make(map[string]providerSearchResult, len(providers))
collectResults:
	for i := 0; i < len(providers); i++ {
		select {
		case res := <-ch:
			resultsMap[res.provider] = res
		case <-ctx.Done():
			mediaLog.Warn("search deadline hit while waiting for providers", "pending", len(providers)-i, "mode", mode, "query", query)
			break collectResults
		}
	}

	var (
		allResults []provider.SearchResult
		warnings   []string
	)

	for _, p := range providers {
		res, ok := resultsMap[p.Name()]
		if !ok || res.err != nil {
			if ok {
				warnings = append(warnings, fmt.Sprintf("%s: %v", strings.ToUpper(s.registry.DisplayName(res.provider)), res.err))
			} else {
				warnings = append(warnings, fmt.Sprintf("%s: timed out", strings.ToUpper(s.registry.DisplayName(p.Name()))))
			}
			continue
		}
		for _, r := range res.results {
			r.Provider = res.provider
			allResults = append(allResults, r)
		}
	}

	if len(allResults) == 0 {
		if len(warnings) > 0 {
			return nil, query, warnings, fmt.Errorf("%s search failed: %s", strings.ToUpper(string(mode)), warnings[0])
		}
		if err := ctx.Err(); err != nil {
			return nil, query, nil, err
		}
	}

	return allResults, query, warnings, nil
}

// FetchEpisodes retrieves episode results for a series from its originating
// provider. When audioMode is non-empty, episodes tagged with a different
// audio track ("sub"/"dub") are filtered out.
func (s *MediaService) FetchEpisodes(ctx context.Context, mode provider.ContentType, series provider.SearchResult, audioMode string) ([]provider.Episode, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	p, ok := s.registry.ProviderByNameForMode(strings.TrimSpace(series.Provider), mode)
	if !ok {
		mediaLog.Warn("episodes requested from unavailable provider",
			"provider", series.Provider, "mode", mode)
		return nil, fmt.Errorf("the source for this title is unavailable in %s mode right now", mode)
	}

	eps, err := p.FetchEpisodes(ctx, series)
	if err != nil {
		return nil, err
	}

	results := make([]provider.Episode, 0, len(eps))
	for _, e := range eps {
		if !matchesAudioMode(e.Audio, audioMode) {
			continue
		}
		results = append(results, e)
	}
	return results, nil
}

// matchesAudioMode reports whether an episode's audio tag satisfies the
// user's sub/dub selection. Empty tags always match.
func matchesAudioMode(audio, audioMode string) bool {
	if audioMode == "" || audio == "" {
		return true
	}
	normalizedAudio := strings.ToLower(strings.TrimSpace(audio))
	normalizedTarget := strings.ToLower(strings.TrimSpace(audioMode))

	// Handle common variations
	if strings.HasPrefix(normalizedAudio, provider.AudioSub) {
		normalizedAudio = provider.AudioSub
	} else if strings.HasPrefix(normalizedAudio, provider.AudioDub) {
		normalizedAudio = provider.AudioDub
	}

	return normalizedAudio == normalizedTarget
}

// Resolve resolves playback sources from ALL supporting providers in parallel,
// reporting each aggregated snapshot through onResult as batches arrive.
func (s *MediaService) Resolve(ctx context.Context, mode provider.ContentType, series provider.SearchResult, episode provider.Episode, onResult func(model.ResolvedMedia)) (model.ResolvedMedia, error) {
	providers := s.registry.ProvidersForMode(mode)
	if len(providers) == 0 {
		return model.ResolvedMedia{}, fmt.Errorf("no providers available for mode %q", mode)
	}
	// Bound the whole resolve so a slow/hung transport host can't leave the
	// "Preparing playback" screen spinning indefinitely.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	agg := newSourceAggregator(providers, s.registry.DisplayName)

	// Helper to build ResolvedMedia from current aggregated sources.
	// Slices are copied so the snapshot handed to callers never aliases the
	// shared backing arrays that other goroutines keep mutating.
	buildResolved := func() model.ResolvedMedia {
		playbackCopy := make([]provider.MediaSource, len(agg.sources))
		copy(playbackCopy, agg.sources)
		subsCopy := make([]model.SubtitleTrack, len(agg.subs))
		copy(subsCopy, agg.subs)
		return model.ResolvedMedia{
			SeriesTitle:   series.Title,
			SeriesURL:     series.ID,
			EpisodeTitle:  episode.Title,
			EpisodeURL:    episode.ID,
			MediaURL:      firstPlaybackURL(agg.sources),
			MediaType:     series.MediaType,
			Year:          series.Year,
			TMDBID:        series.TMDBID,
			SeasonNumber:  episode.Season,
			EpisodeNumber: episode.Episode,
			Resolver:      "Aggregated",
			Playback:      playbackCopy,
			Subtitles:     subsCopy,
		}
	}

	// snapshot sorts the aggregator and builds a detached ResolvedMedia copy.
	// Callers must hold mu; onResult deliberately runs outside the lock so a
	// slow callback can't stall the other provider goroutines mid-fan-out.
	snapshot := func() model.ResolvedMedia {
		agg.sort()
		return buildResolved()
	}

	var mu sync.Mutex
	var failures []string

	g, gCtx := errgroup.WithContext(ctx)

	for _, p := range providers {
		p := p
		g.Go(func() error {
			// Determine the ID to use for this provider: results from other
			// providers can only be resolved cross-provider via TMDB ID.
			mediaID := series.ID
			if p.Name() != series.Provider {
				if series.TMDBID > 0 {
					mediaID = strconv.Itoa(series.TMDBID)
				} else {
					return nil // Cannot resolve with this provider without TMDB ID
				}
			}

			mediaEpisode := provider.Episode{
				Title:   episode.Title,
				ID:      episode.ID,
				Season:  episode.Season,
				Episode: episode.Episode,
				TMDBID:  series.TMDBID,
			}

			recordFailure := func(err error) {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%s: %v", p.Name(), err))
				mu.Unlock()
			}

			// StreamingProviders deliver incremental batches over a channel;
			// standard providers return one slice. Both feed the same
			// aggregation path.
			var updates <-chan []provider.MediaSource
			if sp, ok := p.(provider.StreamingProvider); ok {
				ch := make(chan []provider.MediaSource, 4)
				updates = ch
				go func() {
					defer close(ch)
					if err := sp.ResolveStream(gCtx, mediaID, mediaEpisode, ch); err != nil {
						mediaLog.Debug("streaming provider failed", "provider", p.Name(), "err", err)
						recordFailure(err)
					}
				}()
			} else {
				sources, err := p.ResolveSource(gCtx, mediaID, mediaEpisode)
				if err != nil {
					mediaLog.Debug("provider resolve failed", "provider", p.Name(), "err", err)
					recordFailure(err)
					return nil
				}
				ch := make(chan []provider.MediaSource, 1)
				ch <- sources
				close(ch)
				updates = ch
			}

		streamLoop:
			for {
				select {
				case batch, ok := <-updates:
					if !ok {
						break streamLoop
					}
					mu.Lock()
					agg.add(p.Name(), batch)
					current := snapshot()
					mu.Unlock()
					if onResult != nil {
						onResult(current)
					}
				case <-gCtx.Done():
					go func() {
						for range updates {
						}
					}()
					break streamLoop
				}
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return model.ResolvedMedia{}, err
	}

	if len(agg.sources) == 0 {
		if err := ctx.Err(); err != nil {
			return model.ResolvedMedia{}, err
		}
		if len(failures) > 0 {
			mediaLog.Warn("all providers failed to resolve", "providers", len(providers), "failures", strings.Join(failures, " | "))
		}
		return model.ResolvedMedia{}, provider.ErrNoSources
	}
	return snapshot(), nil
}

// sourceAggregator merges playback sources and subtitle tracks from all
// providers, deduplicating entries as they arrive. It is not safe for
// concurrent use; callers must hold their own lock around add/sort/publish
// sequencing (see Resolve).
type sourceAggregator struct {
	priority    map[string]int // provider name -> priority (lower sorts first)
	displayName func(string) string
	sources     []provider.MediaSource
	subs        []model.SubtitleTrack
	seenSubs    map[string]struct{}
}

func newSourceAggregator(providers []provider.Provider, displayName func(string) string) *sourceAggregator {
	priority := make(map[string]int, len(providers))
	for i, p := range providers {
		priority[p.Name()] = i
	}
	return &sourceAggregator{
		priority:    priority,
		displayName: displayName,
		seenSubs:    make(map[string]struct{}),
	}
}

// add merges one provider batch: playback sources are appended when their
// transport identity (URL+referer+UA+cookies) is new, subtitle tracks when
// their URL is new.
func (a *sourceAggregator) add(providerName string, batch []provider.MediaSource) {
	for _, src := range batch {
		src.Resolver = providerName
		if strings.TrimSpace(src.URL) != "" && !containsSource(a.sources, src) {
			a.sources = append(a.sources, src)
		}
		for _, sub := range src.Subtitles {
			if _, seen := a.seenSubs[sub.URL]; seen {
				continue
			}
			a.seenSubs[sub.URL] = struct{}{}
			subLang := lang.Normalize(sub.Language)
			a.subs = append(a.subs, model.SubtitleTrack{
				Label:    fmt.Sprintf("%s (%s)", lang.Name(subLang), a.displayName(providerName)),
				Language: subLang,
				URL:      sub.URL,
				Referer:  src.Referer,
				Resolver: providerName,
			})
		}
	}
}

// sort orders sources highest quality first, breaking ties by provider
// priority so earlier-registered providers surface before fallbacks.
func (a *sourceAggregator) sort() {
	sort.SliceStable(a.sources, func(i, j int) bool {
		leftQuality := SourceQuality(a.sources[i].Quality)
		rightQuality := SourceQuality(a.sources[j].Quality)
		if leftQuality != rightQuality {
			return leftQuality > rightQuality
		}
		return a.priority[a.sources[i].Resolver] < a.priority[a.sources[j].Resolver]
	})
}

func containsSource(sources []provider.MediaSource, candidate provider.MediaSource) bool {
	for _, source := range sources {
		if source.URL == candidate.URL &&
			source.Referer == candidate.Referer &&
			source.UserAgent == candidate.UserAgent &&
			source.CookieHeader == candidate.CookieHeader {
			return true
		}
	}
	return false
}

func firstPlaybackURL(playback []provider.MediaSource) string {
	if len(playback) == 0 {
		return ""
	}
	return playback[0].URL
}
