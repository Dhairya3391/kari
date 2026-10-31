package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"kari/internal/model"
	"kari/internal/provider"

	"golang.org/x/sync/errgroup"
)

// ResolveOptions tunes one Resolve call.
type ResolveOptions struct {
	// Exclude skips providers that already delivered usable sources:
	// retries only ask the failed ones again, never the successful.
	// Empty (and an exclusion covering everything) resolves normally.
	Exclude []string
	// Retry names providers that should bypass an open health breaker for
	// this call, so a user-requested retry actually probes them again.
	Retry []string
}

// Resolve resolves playback sources from supporting providers in parallel,
// reporting each aggregated snapshot through onResult as batches arrive.
func (s *MediaService) Resolve(ctx context.Context, mode provider.ContentType, series provider.SearchResult, episode provider.Episode, onResult func(model.ResolvedMedia), opts ResolveOptions) (model.ResolvedMedia, error) {
	allRoutes := s.registry.PlanForMode(mode).Playback
	if len(opts.Exclude) > 0 && len(opts.Exclude) < len(allRoutes) {
		kept := allRoutes[:0]
		for _, route := range allRoutes {
			skip := false
			for _, name := range opts.Exclude {
				if strings.EqualFold(route.Provider.Name(), name) {
					skip = true
					break
				}
			}
			if !skip {
				kept = append(kept, route)
			}
		}
		if len(kept) > 0 {
			allRoutes = kept
		}
	}
	if len(allRoutes) == 0 {
		return model.ResolvedMedia{}, fmt.Errorf("no providers available for mode %q", mode)
	}

	var enabledRoutes, disabledRoutes []provider.Route
	for _, route := range allRoutes {
		if s.IsProviderDisabled(mode, route.Provider.Name()) {
			disabledRoutes = append(disabledRoutes, route)
		} else {
			enabledRoutes = append(enabledRoutes, route)
		}
	}

	primaryRoutes := enabledRoutes
	if len(primaryRoutes) == 0 {
		primaryRoutes = allRoutes
		disabledRoutes = nil
	}
	routes := allRoutes
	forcedRetry := make(map[string]struct{}, len(opts.Retry))
	for _, name := range opts.Retry {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			forcedRetry[name] = struct{}{}
		}
	}
	// Bound the whole resolve so a slow/hung transport host can't leave the
	// "Preparing playback" screen spinning indefinitely. Each provider is
	// additionally bounded by its declared source deadline, or the default
	// budget when it declares none.
	ctx, cancel := context.WithTimeout(ctx, sourcesOverallTimeout)
	defer cancel()

	providers := make([]provider.Provider, 0, len(routes))
	for _, route := range routes {
		providers = append(providers, route.Provider)
	}
	agg := newSourceAggregator(providers, s.registry.DisplayName)

	var completedResolvers []string
	completedSet := make(map[string]struct{})
	noteCompleted := func(name string) {
		if _, ok := completedSet[name]; !ok {
			completedSet[name] = struct{}{}
			completedResolvers = append(completedResolvers, name)
		}
	}

	totalTargetResolvers := len(primaryRoutes)
	// Helper to build ResolvedMedia from current aggregated sources.
	// Slices are copied so the snapshot handed to callers never aliases the
	// shared backing arrays that other goroutines keep mutating.
	buildResolved := func() model.ResolvedMedia {
		completedCopy := make([]string, len(completedResolvers))
		copy(completedCopy, completedResolvers)
		playbackCopy := make([]provider.MediaSource, len(agg.sources))
		copy(playbackCopy, agg.sources)
		subsCopy := make([]model.SubtitleTrack, len(agg.subs))
		copy(subsCopy, agg.subs)
		return model.ResolvedMedia{
			SeriesTitle:        series.Title,
			SeriesURL:          series.ID,
			EpisodeTitle:       episode.Title,
			EpisodeURL:         episode.ID,
			MediaURL:           firstPlaybackURL(agg.sources),
			MediaType:          series.MediaType,
			ContentMode:        mode,
			Year:               series.Year,
			TMDBID:             series.TMDBID,
			SeasonNumber:       episode.Season,
			EpisodeNumber:      episode.Episode,
			Resolver:           "Aggregated",
			Playback:           playbackCopy,
			Subtitles:          subsCopy,
			CompletedResolvers: completedCopy,
			TotalResolvers:     totalTargetResolvers,
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
	var failedOrder []string
	// audioUnavailableOnly stays true while every recorded failure is a
	// verified-missing track (not a transport error): then the aggregate
	// error carries the sentinel so callers can hide the episode instead
	// of retrying a title that cannot exist.
	audioUnavailableOnly := true
	failedSet := make(map[string]struct{})
	noteFailed := func(name string) {
		// Caller holds mu.
		if _, ok := failedSet[name]; !ok {
			failedSet[name] = struct{}{}
			failedOrder = append(failedOrder, name)
		}
	}

	runBatch := func(batch []provider.Route) error {
		g, _ := errgroup.WithContext(ctx)
		for _, route := range batch {
			p := route.Provider
			episodeSource := route.Playback
			g.Go(func() error {
				defer func() {
					mu.Lock()
					noteCompleted(p.Name())
					current := snapshot()
					mu.Unlock()
					if onResult != nil {
						onResult(current)
					}
				}()
				_, force := forcedRetry[strings.ToLower(strings.TrimSpace(p.Name()))]
				if skipped, reason := s.health.Skipped(p.Name(), len(routes) == 1 || force); skipped {
					mediaLog.Debug("resolve skipping circuit-broken provider", "provider", p.Name(), "reason", reason)
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %s", p.Name(), reason))
					audioUnavailableOnly = false
					noteFailed(p.Name())
					mu.Unlock()
					return nil
				}
				timeout := providerSourceTimeout(route)
				pCtx, pCancel := context.WithTimeout(ctx, timeout)
				defer pCancel()
				mediaID, ok := series.ResolutionID(mode, p.Name())
				if !ok {
					return nil
				}

				tmdbID := series.TMDBID
				if tmdbID <= 0 && episode.TMDBID > 0 {
					tmdbID = episode.TMDBID
				}
				if tmdbID <= 0 {
					if id, err := strconv.Atoi(strings.TrimSpace(series.ID)); err == nil && id > 0 {
						tmdbID = id
					}
				}

				mediaEpisode := provider.Episode{
					Title:   episode.Title,
					ID:      episode.ID,
					Season:  episode.Season,
					Episode: episode.Episode,
					Audio:   episode.Audio,
					Filler:  episode.Filler,
					TMDBID:  tmdbID,
				}

				var failureOnce sync.Once
				recordFailure := func(err error) {
					failureOnce.Do(func() {
						if provider.BreakerFailure(err) {
							s.health.RecordFailure(p.Name())
						}
						mu.Lock()
						failures = append(failures, fmt.Sprintf("%s: %v", p.Name(), err))
						if !errors.Is(err, provider.ErrAudioUnavailable) {
							audioUnavailableOnly = false
						}
						noteFailed(p.Name())
						mu.Unlock()
					})
				}

				var updates <-chan []provider.MediaSource
				if sp, ok := p.(provider.StreamingProvider); ok {
					ch := make(chan []provider.MediaSource, 4)
					updates = ch
					go func() {
						defer close(ch)
						if err := sp.ResolveStream(pCtx, mediaID, mediaEpisode, ch); err != nil {
							mediaLog.Debug("streaming provider failed", "provider", p.Name(), "err", err)
							recordFailure(err)
						}
					}()
				} else {
					sources, err := episodeSource.ResolveSource(pCtx, mediaID, mediaEpisode)
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

				gotSources := false
			streamLoop:
				for {
					select {
					case batch, ok := <-updates:
						if !ok {
							break streamLoop
						}
						if len(batch) > 0 {
							gotSources = true
						}
						mu.Lock()
						agg.add(p.Name(), batch)
						current := snapshot()
						mu.Unlock()
						if onResult != nil {
							onResult(current)
						}
					case <-pCtx.Done():
						go func() {
							for range updates {
							}
						}()
						if ctx.Err() == nil {
							recordFailure(fmt.Errorf("%s: source deadline exceeded: %w", p.Name(), provider.ErrTimeout))
						}
						break streamLoop
					}
				}
				if gotSources {
					s.health.RecordSuccess(p.Name())
				}
				return nil
			})
		}
		return g.Wait()
	}

	if err := runBatch(primaryRoutes); err != nil {
		s.setLastFailures(failedOrder)
		return model.ResolvedMedia{}, err
	}

	mu.Lock()
	sourcesCount := len(agg.sources)
	mu.Unlock()

	if sourcesCount == 0 && len(disabledRoutes) > 0 && ctx.Err() == nil {
		mediaLog.Debug("resolve: enabled providers returned no sources, attempting fallback to disabled providers",
			"mode", mode, "disabled", len(disabledRoutes))
		mu.Lock()
		totalTargetResolvers = len(primaryRoutes) + len(disabledRoutes)
		mu.Unlock()
		if err := runBatch(disabledRoutes); err != nil {
			s.setLastFailures(failedOrder)
			return model.ResolvedMedia{}, err
		}
	}

	mu.Lock()
	if len(agg.sources) == 0 {
		s.setLastFailures(failedOrder)
		failureList := append([]string(nil), failures...)
		audioOnly := audioUnavailableOnly
		mu.Unlock()
		if err := ctx.Err(); err != nil {
			return model.ResolvedMedia{}, err
		}
		if len(failureList) > 0 {
			mediaLog.Warn("all providers failed to resolve", "providers", len(allRoutes), "failures", strings.Join(failureList, " | "))
			if audioOnly {
				return model.ResolvedMedia{}, fmt.Errorf("%s: %w", strings.Join(failureList, "; "), provider.ErrAudioUnavailable)
			}
			return model.ResolvedMedia{}, errors.New(strings.Join(failureList, "; "))
		}
		return model.ResolvedMedia{}, provider.ErrNoSources
	}
	s.setLastFailures(failedOrder)
	result := snapshot()
	mu.Unlock()
	return result, nil
}
