package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/provider/kit"

	"golang.org/x/sync/errgroup"
)

// log scopes every line from this package/component.
var mediaLog = logging.With("component", "service.media")

// MediaService orchestrates provider operations for the TUI. Routing
// comes only from the provider routes table (per-provider deadlines,
// strategies, sort preferences); the service never switches on
// provider names.
type MediaService struct {
	registry *provider.Registry
	health   *provider.Health

	lastMu       sync.Mutex
	lastFailures []string
}

// NewMediaService constructs a MediaService.
func NewMediaService(registry *provider.Registry) *MediaService {
	return &MediaService{registry: registry, health: &provider.Health{}}
}

func (s *MediaService) episodeAvailabilitySources(mode provider.ContentType, series provider.SearchResult) []provider.EpisodeAvailabilitySource {
	providers := s.registry.ProvidersForMode(mode)
	var sources []provider.EpisodeAvailabilitySource
	seen := make(map[string]struct{}, len(providers))
	add := func(p provider.Provider) {
		if _, ok := seen[p.Name()]; ok {
			return
		}
		seen[p.Name()] = struct{}{}
		if source, ok := p.(provider.EpisodeAvailabilitySource); ok {
			sources = append(sources, source)
		}
	}
	if origin, ok := s.registry.ProviderByNameForMode(strings.TrimSpace(series.Provider), mode); ok {
		add(origin)
	}
	for _, p := range providers {
		add(p)
	}
	return sources
}

// LastFailures returns the distinct provider names that failed during
// the most recent Resolve call, in first-failure order. Partial results
// are returned together with this list so the UI can toast which
// providers failed (e.g. "✗ pengu timed out").
func (s *MediaService) LastFailures() []string {
	s.lastMu.Lock()
	defer s.lastMu.Unlock()
	return append([]string(nil), s.lastFailures...)
}

// setLastFailures records the failed providers of one Resolve call.
func (s *MediaService) setLastFailures(names []string) {
	s.lastMu.Lock()
	defer s.lastMu.Unlock()
	s.lastFailures = append([]string(nil), names...)
}

// Search fans a query out to every provider supporting the mode under a
// shared deadline and merges results in provider priority order. Each
// provider is additionally bounded by its route's search deadline (at
// most 8s) so a slow provider never delays results beyond it, and
// circuit-broken providers are skipped unless they are the only route.
// Each result's Provider field names the service that produced it, and
// per-provider failures become warnings rather than errors; partial
// results are returned together with the failed-provider list.
func (s *MediaService) Search(ctx context.Context, mode provider.ContentType, query string) ([]provider.SearchResult, string, []string, error) {
	providers := s.registry.ProvidersForMode(mode)
	if len(providers) == 0 {
		return nil, query, nil, fmt.Errorf("no providers available for mode %q", mode)
	}
	routes := provider.RoutesFor(mode)
	routeByName := make(map[string]provider.Route, len(routes))
	for _, r := range routes {
		routeByName[r.Provider] = r
	}

	ctx, cancel := context.WithTimeout(ctx, provider.DefaultSearchOverall)
	defer cancel()

	type providerSearchResult struct {
		provider string
		results  []provider.SearchResult
		err      error
	}

	ch := make(chan providerSearchResult, len(providers))
	for _, p := range providers {
		p := p
		if skipped, reason := s.health.Skipped(p.Name(), len(providers) == 1); skipped {
			mediaLog.Debug("search skipping circuit-broken provider", "provider", p.Name(), "reason", reason)
			ch <- providerSearchResult{provider: p.Name(), err: provider.Wrap(p.Name(), "search", provider.KindUpstream, fmt.Errorf("%s", reason))}
			continue
		}
		r, hasRoute := routeByName[p.Name()]
		timeout := providerSearchTimeout(len(providers), r, hasRoute)
		go func() {
			pCtx, pCancel := context.WithTimeout(ctx, timeout)
			defer pCancel()
			results, err := p.Search(pCtx, query, mode)
			if err != nil {
				if provider.BreakerFailure(err) {
					s.health.RecordFailure(p.Name())
				}
			} else {
				s.health.RecordSuccess(p.Name())
			}
			ch <- providerSearchResult{provider: p.Name(), results: results, err: err}
		}()
	}

	resultsMap := make(map[string]providerSearchResult, len(providers))
	var graceTimer *time.Timer
	var graceCh <-chan time.Time
	gotValidResults := false

collectResults:
	for remaining := len(providers); remaining > 0; {
		select {
		case res := <-ch:
			remaining--
			resultsMap[res.provider] = res
			if res.err == nil && len(res.results) > 0 && !gotValidResults {
				gotValidResults = true
				// Once at least one provider succeeds with results, give other providers up to 1.5s grace period
				graceTimer = time.NewTimer(1500 * time.Millisecond)
				defer graceTimer.Stop()
				graceCh = graceTimer.C
			}
		case <-graceCh:
			mediaLog.Debug("search grace period expired; returning fast results", "mode", mode, "query", query)
			break collectResults
		case <-ctx.Done():
			mediaLog.Warn("search deadline hit while waiting for providers", "pending", remaining, "mode", mode, "query", query)
			break collectResults
		}
	}

	var (
		allResults []provider.SearchResult
		warnings   []string
	)
	seenTitles := make(map[string]struct{})
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
			key := fmt.Sprintf("%s:%s", r.Type, strings.TrimSpace(r.ID))
			if r.ID != "" {
				if _, seen := seenTitles[key]; seen {
					continue
				}
				seenTitles[key] = struct{}{}
			}
			allResults = append(allResults, r)
		}
	}

	if len(allResults) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, query, nil, err
		}
		allNoResults := true
		for _, p := range providers {
			res, ok := resultsMap[p.Name()]
			if !ok || (res.err != nil && !errors.Is(res.err, provider.ErrNoResults)) {
				allNoResults = false
				break
			}
		}
		if allNoResults {
			return nil, query, warnings, provider.ErrNoResults
		}
		if len(warnings) > 0 {
			return nil, query, warnings, errors.New(warnings[0])
		}
		return nil, query, warnings, provider.ErrNoResults
	}

	return allResults, query, warnings, nil
}

// providerSearchTimeout returns the per-provider search deadline. A lone
// provider is the whole mode — no sibling to hand off to — so it gets the
// longer single-provider budget instead of the shared cap that would abort
// its only search; a route's explicit deadline still wins when shorter.
func providerSearchTimeout(providerCount int, route provider.Route, hasRoute bool) time.Duration {
	timeout := provider.DefaultSearchTimeout
	if providerCount == 1 {
		timeout = provider.DefaultSingleSearchTimeout
	}
	if hasRoute && route.SearchTimeout > 0 && route.SearchTimeout < timeout {
		timeout = route.SearchTimeout
	}
	if timeout > provider.DefaultSearchTimeout && providerCount > 1 {
		timeout = provider.DefaultSearchTimeout
	}
	return timeout
}

// FetchEpisodes retrieves episode results for a series from its originating
// provider. Anime audio availability is sourced from providers that expose
// per-track data, so synthetic dub rows are never returned.
func (s *MediaService) FetchEpisodes(ctx context.Context, mode provider.ContentType, series provider.SearchResult, audioMode string) ([]provider.Episode, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	p, ok := s.registry.ProviderByNameForMode(strings.TrimSpace(series.Provider), mode)
	if !ok {
		mediaLog.Warn("episodes requested from unavailable provider",
			"provider", series.Provider, "mode", mode)
		return nil, fmt.Errorf("the source for this title is unavailable in %s mode right now", mode)
	}

	requestedAudio := audioMode
	if mode == provider.ModeAnime {
		requestedAudio = strings.ToLower(strings.TrimSpace(requestedAudio))
		if requestedAudio != provider.AudioDub {
			requestedAudio = provider.AudioSub
		}

		var lastErr error
		for _, source := range s.episodeAvailabilitySources(mode, series) {
			eps, err := source.FetchAvailableEpisodes(ctx, series)
			if err != nil {
				lastErr = err
				continue
			}
			return filterAvailableEpisodesByAudio(eps, requestedAudio), nil
		}
		if requestedAudio == provider.AudioDub {
			if lastErr != nil {
				return nil, fmt.Errorf("dub availability: %w", lastErr)
			}
			return nil, provider.ErrNoEpisodes
		}
	}

	eps, err := p.FetchEpisodes(ctx, series)
	if err != nil {
		return nil, err
	}
	return filterEpisodesByAudio(eps, requestedAudio), nil
}

func filterAvailableEpisodesByAudio(episodes []provider.Episode, audioMode string) []provider.Episode {
	results := make([]provider.Episode, 0, len(episodes))
	for _, episode := range episodes {
		if strings.TrimSpace(episode.Audio) != "" && matchesAudioMode(episode.Audio, audioMode) {
			results = append(results, episode)
		}
	}
	return results
}

func filterEpisodesByAudio(episodes []provider.Episode, audioMode string) []provider.Episode {
	results := make([]provider.Episode, 0, len(episodes))
	for _, episode := range episodes {
		if matchesAudioMode(episode.Audio, audioMode) {
			results = append(results, episode)
		}
	}
	return results
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
	providers := s.registry.ProvidersForMode(mode)
	if len(opts.Exclude) > 0 && len(opts.Exclude) < len(providers) {
		kept := providers[:0]
		for _, p := range providers {
			skip := false
			for _, name := range opts.Exclude {
				if strings.EqualFold(p.Name(), name) {
					skip = true
					break
				}
			}
			if !skip {
				kept = append(kept, p)
			}
		}
		providers = kept
	}
	if len(providers) == 0 {
		return model.ResolvedMedia{}, fmt.Errorf("no providers available for mode %q", mode)
	}
	forcedRetry := make(map[string]struct{}, len(opts.Retry))
	for _, name := range opts.Retry {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			forcedRetry[name] = struct{}{}
		}
	}
	// Bound the whole resolve so a slow/hung transport host can't leave the
	// "Preparing playback" screen spinning indefinitely. Each provider is
	// additionally bounded by its route's source deadline (at most 12s).
	ctx, cancel := context.WithTimeout(ctx, provider.DefaultSourcesOverall)
	defer cancel()

	routes := provider.RoutesFor(mode)
	routeByName := make(map[string]provider.Route, len(routes))
	for _, r := range routes {
		routeByName[r.Provider] = r
	}

	agg := newSourceAggregatorForMode(mode, providers, s.registry.DisplayName)

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

	g, _ := errgroup.WithContext(ctx)

	for _, p := range providers {
		p := p
		g.Go(func() error {
			_, force := forcedRetry[strings.ToLower(strings.TrimSpace(p.Name()))]
			if skipped, reason := s.health.Skipped(p.Name(), len(providers) == 1 || force); skipped {
				mediaLog.Debug("resolve skipping circuit-broken provider", "provider", p.Name(), "reason", reason)
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%s: %s", p.Name(), reason))
				audioUnavailableOnly = false
				noteFailed(p.Name())
				mu.Unlock()
				return nil
			}
			timeout := provider.DefaultSourceTimeout
			if r, ok := routeByName[p.Name()]; ok && r.SourceTimeout > 0 && r.SourceTimeout < timeout {
				timeout = r.SourceTimeout
			}
			if timeout > 12*time.Second {
				timeout = 12 * time.Second
			}
			pCtx, pCancel := context.WithTimeout(ctx, timeout)
			defer pCancel()
			// Determine the ID to use for this provider: results from other
			// providers can be resolved cross-provider via TMDB ID (for movies/TV)
			// or AniList ID (for anime).
			mediaID := series.ID
			if p.Name() != series.Provider {
				if series.TMDBID > 0 {
					mediaID = strconv.Itoa(series.TMDBID)
				} else if (mode == provider.ModeAnime || series.Type == provider.ModeAnime) && series.ID != "" {
					mediaID = series.ID
				} else {
					return nil // Cannot resolve with this provider without shared ID
				}
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

			// standard providers return one slice. Both feed the same
			// aggregation path. Provider calls use the per-provider
			// deadline (at most 12s) derived from the overall context.
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
				sources, err := p.ResolveSource(pCtx, mediaID, mediaEpisode)
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
					break streamLoop
				}
			}
			if gotSources {
				s.health.RecordSuccess(p.Name())
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		s.setLastFailures(failedOrder)
		return model.ResolvedMedia{}, err
	}

	if len(agg.sources) == 0 {
		s.setLastFailures(failedOrder)
		if err := ctx.Err(); err != nil {
			return model.ResolvedMedia{}, err
		}
		if len(failures) > 0 {
			mediaLog.Warn("all providers failed to resolve", "providers", len(providers), "failures", strings.Join(failures, " | "))
			if audioUnavailableOnly {
				return model.ResolvedMedia{}, fmt.Errorf("%s: %w", strings.Join(failures, "; "), provider.ErrAudioUnavailable)
			}
			return model.ResolvedMedia{}, errors.New(strings.Join(failures, "; "))
		}
		return model.ResolvedMedia{}, provider.ErrNoSources
	}
	s.setLastFailures(failedOrder)
	return snapshot(), nil
}

// sourceAggregator merges playback sources and subtitle tracks from all
// providers, deduplicating entries as they arrive. It is not safe for
// concurrent use; callers must hold their own lock around add/sort/publish
// sequencing (see Resolve).
type sourceAggregator struct {
	mode        provider.ContentType
	priority    map[string]int // provider name -> priority (lower sorts first)
	displayName func(string) string
	sources     []provider.MediaSource
	seenSources map[string]struct{}
	subs        []model.SubtitleTrack
	seenSubs    map[string]struct{}
	tierCounts  map[string]int
}

// tierKey groups interchangeable mirror rows: same resolver, same
// quality height, same backend, same audio language.
func tierKey(src provider.MediaSource) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(src.Resolver)),
		strings.TrimSpace(strings.ToLower(src.Language)),
		kit.BackendTag(src.Quality),
		strconv.Itoa(SourceQuality(src.Quality)),
	}, "\x00")
}

// tierHasRoom reports whether another row of src's tier fits, counting
// it when it does. Callers hold the aggregation lock.
func (a *sourceAggregator) tierHasRoom(src provider.MediaSource) bool {
	if a.tierCounts == nil {
		a.tierCounts = make(map[string]int)
	}
	key := tierKey(src)
	if a.tierCounts[key] >= maxSourcesPerTier {
		return false
	}
	a.tierCounts[key]++
	return true
}

func newSourceAggregator(providers []provider.Provider, displayName func(string) string) *sourceAggregator {
	return newSourceAggregatorForMode("", providers, displayName)
}

// newSourceAggregatorForMode builds an aggregator whose sort preferences
// come from the routes table for mode — no provider-name switches.
func newSourceAggregatorForMode(mode provider.ContentType, providers []provider.Provider, displayName func(string) string) *sourceAggregator {
	priority := make(map[string]int, len(providers))
	for i, p := range providers {
		priority[p.Name()] = i
	}
	return &sourceAggregator{
		mode:        mode,
		priority:    priority,
		displayName: displayName,
		seenSources: make(map[string]struct{}),
		seenSubs:    make(map[string]struct{}),
	}
}

// maxSourcesPerTier caps rows sharing one (resolver, quality height,
// backend, language) identity: upstream mirrors multiply identical
// rows (16 bare "1080p" from one resolver), and the first arrivals are
// already best-first, so the cap cuts noise while keeping fallback
// depth. Distinct backends, heights and dubbed languages each keep
// their own budget and are never collapsed into each other.
const maxSourcesPerTier = 3

// add merges one provider batch. Playback sources and subtitles retain their
// owning source's transport identity so signed or session-bound variants are
// not collapsed before playback.
func (a *sourceAggregator) add(providerName string, batch []provider.MediaSource) {
	for _, src := range batch {
		src.Resolver = providerName
		identity := src.TransportIdentity()
		if identity != "" {
			if _, seen := a.seenSources[identity]; !seen && a.tierHasRoom(src) {
				a.seenSources[identity] = struct{}{}
				a.sources = append(a.sources, src)
			}
		}
		for _, sub := range src.Subtitles {
			subURL := strings.TrimSpace(sub.URL)
			if subURL == "" {
				continue
			}
			key := subURL + "\x00" + identity
			if _, seen := a.seenSubs[key]; seen {
				continue
			}
			a.seenSubs[key] = struct{}{}
			subLang := lang.Normalize(sub.Language)
			referer := strings.TrimSpace(sub.Referer)
			if referer == "" {
				referer = src.Referer
			}
			a.subs = append(a.subs, model.SubtitleTrack{
				Label:     fmt.Sprintf("%s (%s)", lang.Name(subLang), a.displayName(providerName)),
				Language:  subLang,
				URL:       subURL,
				Referer:   referer,
				SourceURL: src.URL,
				SourceID:  identity,
				Default:   sub.Default,
				Resolver:  providerName,
			})
		}
	}
}

// sort orders sources by the routes-table preference for the mode first
// (e.g. Anikoto Vidstream-2 for anime, Movy.sx for movies/tv/cartoon),
// then by highest quality first, breaking ties by provider priority so
// earlier-registered providers surface before fallbacks.
func (a *sourceAggregator) sort() {
	slices.SortStableFunc(a.sources, func(si, sj provider.MediaSource) int {
		ri := provider.BoostRank(a.mode, si.Resolver, si.Quality)
		rj := provider.BoostRank(a.mode, sj.Resolver, sj.Quality)
		if (ri >= 0) != (rj >= 0) {
			if ri >= 0 {
				return -1
			}
			return 1
		}
		if ri >= 0 && rj >= 0 && ri != rj {
			return ri - rj
		}

		leftQuality := SourceQuality(si.Quality)
		rightQuality := SourceQuality(sj.Quality)
		if leftQuality != rightQuality {
			return rightQuality - leftQuality
		}
		return a.priority[si.Resolver] - a.priority[sj.Resolver]
	})
}

func containsSource(sources []provider.MediaSource, candidate provider.MediaSource) bool {
	identity := candidate.TransportIdentity()
	if identity == "" {
		return true
	}
	for _, source := range sources {
		if source.TransportIdentity() == identity {
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
