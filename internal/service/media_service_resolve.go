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
// reporting changed aggregated snapshots serially through onResult as batches
// arrive. Callbacks finish before Resolve returns and must return promptly.
func (s *MediaService) Resolve(ctx context.Context, mode provider.ContentType, series provider.SearchResult, episode provider.Episode, onResult func(model.ResolvedMedia), opts ResolveOptions) (model.ResolvedMedia, error) {
	allRoutes := s.registry.PlanForMode(mode).Playback
	// mirrorRoutes keeps every registered provider for the live fallback:
	// the ResolutionID filter below narrows the primary batch to the selected
	// provider's native handle, which is exactly why a dead channel needs a
	// title-keyed search on the siblings.
	mirrorRoutes := allRoutes
	// Filter to providers capable of resolving this specific title so
	// provider-specific handles (e.g. live sports, non-TMDB items) do not
	// spawn dead goroutines that falsely report completed or failed resolvers.
	if series.ID != "" {
		resolvable := make([]provider.Route, 0, len(allRoutes))
		for _, route := range allRoutes {
			if _, ok := series.ResolutionID(mode, route.Provider.Name()); ok {
				resolvable = append(resolvable, route)
			}
		}
		if len(resolvable) > 0 {
			allRoutes = resolvable
		}
	}
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
	noteCompleted := func(name string) bool {
		if _, ok := completedSet[name]; ok {
			return false
		}
		completedSet[name] = struct{}{}
		completedResolvers = append(completedResolvers, name)
		return true
	}

	totalTargetResolvers := len(primaryRoutes)
	// Helper to build ResolvedMedia from current aggregated sources.
	// Slices are copied so the snapshot handed to callers never aliases the
	// shared backing arrays that other goroutines keep mutating.
	buildResolved := func() model.ResolvedMedia {
		completedCopy := make([]string, len(completedResolvers))
		copy(completedCopy, completedResolvers)
		playbackCopy := cloneMediaSources(agg.sources)
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

	var mu sync.Mutex
	var publicationMu sync.Mutex
	// Reserve delivery order before changing the aggregate. Locking only around
	// callbacks would still let an older snapshot overtake a newer one.
	publish := func(update func() bool) {
		publicationMu.Lock()
		defer publicationMu.Unlock()
		mu.Lock()
		if !update() || onResult == nil {
			mu.Unlock()
			return
		}
		current := buildResolved()
		mu.Unlock()
		onResult(current)
	}
	addSources := func(name string, batch []provider.MediaSource) bool {
		if !agg.add(name, batch) {
			return false
		}
		agg.sort()
		return true
	}
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
					publish(func() bool { return noteCompleted(p.Name()) })
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

				recordFailure := func(err error) {
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
				}

				updates, finished := startSourceResolution(pCtx, episodeSource, mediaID, mediaEpisode)

				gotSources := false
			streamLoop:
				for {
					select {
					case batch, ok := <-updates:
						if !ok {
							if err := <-finished; err != nil {
								mediaLog.Debug("provider resolve failed", "provider", p.Name(), "err", err)
								recordFailure(err)
							}
							break streamLoop
						}
						if len(batch) == 0 {
							continue
						}
						batch = cloneMediaSources(batch)
						// Live providers declare reachability probes so dead
						// mirrors are dropped before the first snapshot reaches
						// the player. Verification stays inside the provider's
						// own source deadline.
						if verifier, ok := p.(provider.LiveStreamVerifier); ok && len(batch) > 0 {
							batch = verifyLiveSources(pCtx, mode, verifier, batch)
						}
						if len(batch) > 0 {
							batch = discoverManifestAudio(pCtx, batch)
							gotSources = true
						}
						publish(func() bool { return addSources(p.Name(), batch) })
					case <-pCtx.Done():
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
	stillEmpty := len(agg.sources) == 0
	mu.Unlock()
	if stillEmpty && mode == provider.ModeLive && ctx.Err() == nil {
		mediaLog.Debug("live resolve empty; searching other providers by title", "title", series.Title)
		for _, mirror := range s.resolveLiveMirrors(ctx, mode, series, mirrorRoutes, opts.Exclude) {
			publish(func() bool {
				changed := addSources(mirror.provider, mirror.sources)
				completed := noteCompleted(mirror.provider)
				return changed || completed
			})
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
	result := buildResolved()
	mu.Unlock()
	return result, nil
}

// startSourceResolution isolates provider calls from the context-bounded consumer.
// The worker owns only its channels: a late return cannot change health, failures,
// or snapshots. Providers must honor ctx, including on stream sends; an abandoned
// worker cannot be forcibly stopped, but needs no unbounded drain goroutine.
func startSourceResolution(
	ctx context.Context,
	source provider.EpisodeSource,
	mediaID string,
	episode provider.Episode,
) (<-chan []provider.MediaSource, <-chan error) {
	// Four batches absorb short stream bursts without unbounded queuing. One
	// terminal result lets even a late provider return without a waiting reader.
	updates := make(chan []provider.MediaSource, 4)
	finished := make(chan error, 1)
	go func() {
		var err error
		if streaming, ok := source.(provider.StreamingProvider); ok {
			err = streaming.ResolveStream(ctx, mediaID, episode, updates)
		} else {
			var sources []provider.MediaSource
			sources, err = source.ResolveSource(ctx, mediaID, episode)
			if len(sources) > 0 {
				select {
				case updates <- sources:
				case <-ctx.Done():
				}
			}
		}
		finished <- err
		close(updates)
	}()
	return updates, finished
}
