package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
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
	"kari/internal/util"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	searchCacheTTL   = 5 * time.Minute
	searchCacheMax   = 100
	episodesCacheTTL = 15 * time.Minute
	episodesCacheMax = 200
	// episodeTitlesTimeout bounds one canonical-title fan-out: the race
	// itself is parallel and ends with the fastest source, so this hard
	// cap only fires when every naming source hangs.
	episodeTitlesTimeout = 3 * time.Second
)

type searchCacheEntry struct {
	results  []provider.SearchResult
	query    string
	warnings []string
	cachedAt time.Time
}

type episodesCacheEntry struct {
	episodes []provider.Episode
	cachedAt time.Time
}

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

	disabledMu        sync.RWMutex
	disabledProviders map[string]bool

	searchCache   *util.BoundedCache[searchCacheEntry]
	episodesCache *util.BoundedCache[episodesCacheEntry]
	episodeTitles *util.BoundedCache[map[int]string]
	sf            singleflight.Group
}

// NewMediaService constructs a MediaService.
func NewMediaService(registry *provider.Registry) *MediaService {
	return &MediaService{
		registry:      registry,
		health:        &provider.Health{},
		searchCache:   util.NewBoundedCache[searchCacheEntry](searchCacheMax),
		episodesCache: util.NewBoundedCache[episodesCacheEntry](episodesCacheMax),
		episodeTitles: util.NewBoundedCache[map[int]string](200),
	}
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

// SetDisabledProviders updates the set of disabled providers and invalidates
// cached search and episode responses so queries immediately reflect the new filter.
func (s *MediaService) SetDisabledProviders(names []string) {
	s.disabledMu.Lock()
	defer s.disabledMu.Unlock()
	s.disabledProviders = make(map[string]bool, len(names))
	for _, n := range names {
		p := strings.ToLower(strings.TrimSpace(n))
		if p != "" {
			s.disabledProviders[p] = true
		}
	}
	if s.searchCache != nil {
		s.searchCache.Clear()
	}
	if s.episodesCache != nil {
		s.episodesCache.Clear()
	}
}

// IsProviderDisabled reports whether the named provider is disabled for the given mode.
// It checks both the mode-scoped key (e.g. "anime:pengu") and the global key ("pengu").
func (s *MediaService) IsProviderDisabled(mode provider.ContentType, name string) bool {
	s.disabledMu.RLock()
	defer s.disabledMu.RUnlock()
	if len(s.disabledProviders) == 0 {
		return false
	}
	p := strings.ToLower(strings.TrimSpace(name))
	if s.disabledProviders[p] {
		return true
	}
	m := strings.ToLower(strings.TrimSpace(string(mode)))
	if m != "" && s.disabledProviders[m+":"+p] {
		return true
	}
	return false
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
	cacheKey := fmt.Sprintf("%s:%s", mode, strings.ToLower(strings.TrimSpace(query)))
	if s.searchCache != nil {
		if entry, ok := s.searchCache.Get(cacheKey); ok && time.Since(entry.cachedAt) < searchCacheTTL {
			return entry.results, entry.query, entry.warnings, nil
		}
	}

	allProviders := s.registry.ProvidersForMode(mode)
	if len(allProviders) == 0 {
		return nil, query, nil, fmt.Errorf("no providers available for mode %q", mode)
	}

	var enabledProviders, disabledProviders []provider.Provider
	for _, p := range allProviders {
		if s.IsProviderDisabled(mode, p.Name()) {
			disabledProviders = append(disabledProviders, p)
		} else {
			enabledProviders = append(enabledProviders, p)
		}
	}

	primaryProviders := enabledProviders
	if len(primaryProviders) == 0 {
		primaryProviders = allProviders
		disabledProviders = nil
	}

	allResults, warnings, err := s.searchProviders(ctx, mode, query, primaryProviders)
	if len(allResults) == 0 && len(disabledProviders) > 0 && ctx.Err() == nil {
		mediaLog.Debug("search: enabled providers returned no results, attempting fallback to disabled providers",
			"mode", mode, "query", query, "disabled", len(disabledProviders))
		fallbackResults, fallbackWarnings, fallbackErr := s.searchProviders(ctx, mode, query, disabledProviders)
		if len(fallbackResults) > 0 {
			allResults = fallbackResults
			warnings = append(warnings, fallbackWarnings...)
			err = fallbackErr
		}
	}

	if len(allResults) > 0 {
		if s.searchCache != nil && len(warnings) == 0 {
			s.searchCache.Set(cacheKey, searchCacheEntry{
				results:  allResults,
				query:    query,
				warnings: warnings,
				cachedAt: time.Now(),
			})
		}
		return allResults, query, warnings, nil
	}
	return nil, query, warnings, err
}

func (s *MediaService) searchProviders(ctx context.Context, mode provider.ContentType, query string, providers []provider.Provider) ([]provider.SearchResult, []string, error) {
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
				if provider.BreakerFailure(err) && !strings.Contains(err.Error(), "streambase") {
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
				if errors.Is(res.err, provider.ErrNoResults) {
					continue
				}
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
			return nil, warnings, err
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
			return nil, warnings, provider.ErrNoResults
		}
		if len(warnings) > 0 {
			errStr := warnings[0]
			if idx := strings.Index(errStr, "streambase search:"); idx != -1 {
				errStr = errStr[idx:]
			}
			return nil, warnings, errors.New(errStr)
		}
		return nil, warnings, provider.ErrNoResults
	}

	return allResults, warnings, nil
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

// providerSourceTimeout returns the per-provider resolve deadline: the
// route's explicit SourceTimeout when set, else the shared default,
// never beyond the overall budget that bounds the whole fan-out.
func providerSourceTimeout(route provider.Route, hasRoute bool) time.Duration {
	timeout := provider.DefaultSourceTimeout
	if hasRoute && route.SourceTimeout > 0 {
		timeout = route.SourceTimeout
	}
	if timeout > provider.DefaultSourcesOverall {
		timeout = provider.DefaultSourcesOverall
	}
	return timeout
}

// FetchEpisodes retrieves episode results for a series from its originating
// provider. Anime audio availability is sourced from providers that expose
// per-track data, so synthetic dub rows are never returned.
func (s *MediaService) FetchEpisodes(ctx context.Context, mode provider.ContentType, series provider.SearchResult, audioMode string) ([]provider.Episode, error) {
	cacheKey := fmt.Sprintf("%s:%s:%s:%s", mode, series.Provider, series.ID, audioMode)
	if s.episodesCache != nil {
		if entry, ok := s.episodesCache.Get(cacheKey); ok && time.Since(entry.cachedAt) < episodesCacheTTL {
			return entry.episodes, nil
		}
	}

	res, err, _ := s.sf.Do(cacheKey, func() (any, error) {
		if s.episodesCache != nil {
			if entry, ok := s.episodesCache.Get(cacheKey); ok && time.Since(entry.cachedAt) < episodesCacheTTL {
				return entry.episodes, nil
			}
		}
		eps, fetchErr := s.fetchEpisodesUncached(ctx, mode, series, audioMode)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if s.episodesCache != nil && len(eps) > 0 {
			s.episodesCache.Set(cacheKey, episodesCacheEntry{
				episodes: eps,
				cachedAt: time.Now(),
			})
		}
		return eps, nil
	})
	if err != nil {
		return nil, err
	}
	return res.([]provider.Episode), nil
}

func (s *MediaService) fetchEpisodesUncached(ctx context.Context, mode provider.ContentType, series provider.SearchResult, audioMode string) ([]provider.Episode, error) {
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

		// Episode title metadata belongs to the selected source order.
		// Racing providers can attach a different season's titles to
		// otherwise interchangeable episode-number handles.
		availSources := s.episodeAvailabilitySources(mode, series)
		var enabledAvail, disabledAvail []provider.EpisodeAvailabilitySource
		for _, as := range availSources {
			if prov, ok := as.(provider.Provider); ok && s.IsProviderDisabled(mode, prov.Name()) {
				disabledAvail = append(disabledAvail, as)
			} else {
				enabledAvail = append(enabledAvail, as)
			}
		}
		probeList := enabledAvail
		if len(probeList) == 0 {
			probeList = availSources
			disabledAvail = nil
		}
		eps, ok, lastErr := s.firstAvailableEpisodes(ctx, probeList, series)
		if !ok && len(disabledAvail) > 0 && ctx.Err() == nil {
			mediaLog.Debug("episodes: enabled availability sources returned no episodes, attempting fallback to disabled sources")
			eps, ok, lastErr = s.firstAvailableEpisodes(ctx, disabledAvail, series)
		}
		if ok {
			if mode == provider.ModeAnime {
				eps = s.applyCanonicalAnimeEpisodeTitles(ctx, mode, series, eps)
			}
			return filterAvailableEpisodesByAudio(eps, requestedAudio), nil
		} else if requestedAudio == provider.AudioDub {
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
	if mode == provider.ModeAnime {
		eps = s.applyCanonicalAnimeEpisodeTitles(ctx, mode, series, eps)
	}
	return filterEpisodesByAudio(eps, requestedAudio), nil
}

func (s *MediaService) applyCanonicalAnimeEpisodeTitles(ctx context.Context, mode provider.ContentType, series provider.SearchResult, episodes []provider.Episode) []provider.Episode {
	if series.MediaType == provider.MediaTypeMovie {
		return episodes
	}
	if len(episodes) == 0 {
		return episodes
	}
	cacheKey := fmt.Sprintf("titles:%s:%s", series.Type, strings.TrimSpace(series.ID))
	titles, ok := s.episodeTitles.Get(cacheKey)
	if !ok {
		// Bounded hard so a slow naming provider can never delay the
		// episode list; sources race in parallel and siblings are
		// cancelled once the first usable map lands.
		titleCtx, cancel := context.WithTimeout(ctx, episodeTitlesTimeout)
		defer cancel()
		titles, ok = s.firstEpisodeTitles(titleCtx, s.episodeTitleSources(mode, series), series)
		if !ok {
			mediaLog.Debug("canonical anime episode titles unavailable", "provider", series.Provider, "seriesID", series.ID)
			setGenericEpisodeTitles(episodes)
			return episodes
		}
		s.episodeTitles.Set(cacheKey, titles)
	}
	for i := range episodes {
		if title := strings.TrimSpace(titles[episodes[i].Episode]); title != "" {
			episodes[i].Title = title
		} else {
			episodes[i].Title = genericEpisodeTitle(episodes[i].Episode)
		}
	}
	return episodes
}

// episodeTitleSources lists providers that supply their own episode titles
// for mode, origin provider first so the running title usually wins the race.
func (s *MediaService) episodeTitleSources(mode provider.ContentType, series provider.SearchResult) []provider.EpisodeTitlesSource {
	providers := s.registry.ProvidersForMode(mode)
	ordered := make([]provider.Provider, 0, len(providers)+1)
	if origin, ok := s.registry.ProviderByNameForMode(strings.TrimSpace(series.Provider), mode); ok {
		ordered = append(ordered, origin)
	}
	ordered = append(ordered, providers...)
	var out []provider.EpisodeTitlesSource
	seen := make(map[string]struct{}, len(ordered))
	for _, p := range ordered {
		if _, ok := seen[p.Name()]; ok {
			continue
		}
		seen[p.Name()] = struct{}{}
		if src, ok := p.(provider.EpisodeTitlesSource); ok {
			out = append(out, src)
		}
	}
	return out
}

// firstEpisodeTitles returns the first provider-supplied title map and
// cancels sibling lookups so naming latency is bounded by the fastest source.
func (s *MediaService) firstEpisodeTitles(ctx context.Context, sources []provider.EpisodeTitlesSource, series provider.SearchResult) (map[int]string, bool) {
	if len(sources) == 0 {
		return nil, false
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type titleResult struct {
		titles map[int]string
	}
	results := make(chan titleResult, len(sources))
	for _, source := range sources {
		go func(source provider.EpisodeTitlesSource) {
			titles, err := source.FetchEpisodeTitles(childCtx, series)
			if err != nil {
				mediaLog.Debug("episode titles unavailable from source", "source", source, "err", err)
				titles = nil
			}
			results <- titleResult{titles: titles}
		}(source)
	}
	for range sources {
		if result := <-results; len(result.titles) > 0 {
			return result.titles, true
		}
	}
	return nil, false
}

func setGenericEpisodeTitles(episodes []provider.Episode) {
	for i := range episodes {
		episodes[i].Title = genericEpisodeTitle(episodes[i].Episode)
	}
}

func genericEpisodeTitle(number int) string {
	return fmt.Sprintf("Episode %02d", number)
}

// firstAvailableEpisodes returns the first usable episode list and cancels
// sibling lookups so catalog latency is bounded by the fastest source.
func (s *MediaService) firstAvailableEpisodes(ctx context.Context, sources []provider.EpisodeAvailabilitySource, series provider.SearchResult) ([]provider.Episode, bool, error) {
	if len(sources) == 0 {
		return nil, false, nil
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type availabilityResult struct {
		episodes []provider.Episode
		err      error
	}
	results := make(chan availabilityResult, len(sources))
	for _, source := range sources {
		go func(source provider.EpisodeAvailabilitySource) {
			episodes, err := source.FetchAvailableEpisodes(childCtx, series)
			results <- availabilityResult{episodes: episodes, err: err}
		}(source)
	}
	var lastErr error
	for range sources {
		result := <-results
		if result.err == nil && len(result.episodes) > 0 {
			return result.episodes, true, nil
		}
		if result.err != nil {
			lastErr = result.err
		}
	}
	return nil, false, lastErr
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
	allProviders := s.registry.ProvidersForMode(mode)
	if len(opts.Exclude) > 0 && len(opts.Exclude) < len(allProviders) {
		kept := allProviders[:0]
		for _, p := range allProviders {
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
		if len(kept) > 0 {
			allProviders = kept
		}
	}
	if len(allProviders) == 0 {
		return model.ResolvedMedia{}, fmt.Errorf("no providers available for mode %q", mode)
	}

	var enabledProviders, disabledProviders []provider.Provider
	for _, p := range allProviders {
		if s.IsProviderDisabled(mode, p.Name()) {
			disabledProviders = append(disabledProviders, p)
		} else {
			enabledProviders = append(enabledProviders, p)
		}
	}

	primaryProviders := enabledProviders
	if len(primaryProviders) == 0 {
		primaryProviders = allProviders
		disabledProviders = nil
	}
	providers := allProviders
	forcedRetry := make(map[string]struct{}, len(opts.Retry))
	for _, name := range opts.Retry {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			forcedRetry[name] = struct{}{}
		}
	}
	// Bound the whole resolve so a slow/hung transport host can't leave the
	// "Preparing playback" screen spinning indefinitely. Each provider is
	// additionally bounded by its route's source deadline (its explicit
	// SourceTimeout, else the default, never beyond the overall budget).
	ctx, cancel := context.WithTimeout(ctx, provider.DefaultSourcesOverall)
	defer cancel()

	routes := provider.RoutesFor(mode)
	routeByName := make(map[string]provider.Route, len(routes))
	for _, r := range routes {
		routeByName[r.Provider] = r
	}

	agg := newSourceAggregatorForMode(mode, providers, s.registry.DisplayName)

	var completedResolvers []string
	completedSet := make(map[string]struct{})
	noteCompleted := func(name string) {
		if _, ok := completedSet[name]; !ok {
			completedSet[name] = struct{}{}
			completedResolvers = append(completedResolvers, name)
		}
	}

	totalTargetResolvers := len(primaryProviders)
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

	runBatch := func(batch []provider.Provider) error {
		g, _ := errgroup.WithContext(ctx)
		for _, p := range batch {
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
				if skipped, reason := s.health.Skipped(p.Name(), len(allProviders) == 1 || force); skipped {
					mediaLog.Debug("resolve skipping circuit-broken provider", "provider", p.Name(), "reason", reason)
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %s", p.Name(), reason))
					audioUnavailableOnly = false
					noteFailed(p.Name())
					mu.Unlock()
					return nil
				}
				r, hasRoute := routeByName[p.Name()]
				timeout := providerSourceTimeout(r, hasRoute)
				pCtx, pCancel := context.WithTimeout(ctx, timeout)
				defer pCancel()
				mediaID := series.ID
				if p.Name() != series.Provider {
					if mode == provider.ModeAnime || series.Type == provider.ModeAnime {
						if series.ID != "" {
							mediaID = series.ID
						} else {
							return nil
						}
					} else if series.TMDBID > 0 {
						mediaID = strconv.Itoa(series.TMDBID)
					} else {
						return nil
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

	if err := runBatch(primaryProviders); err != nil {
		s.setLastFailures(failedOrder)
		return model.ResolvedMedia{}, err
	}

	mu.Lock()
	sourcesCount := len(agg.sources)
	mu.Unlock()

	if sourcesCount == 0 && len(disabledProviders) > 0 && ctx.Err() == nil {
		mediaLog.Debug("resolve: enabled providers returned no sources, attempting fallback to disabled providers",
			"mode", mode, "disabled", len(disabledProviders))
		mu.Lock()
		totalTargetResolvers = len(primaryProviders) + len(disabledProviders)
		mu.Unlock()
		if err := runBatch(disabledProviders); err != nil {
			s.setLastFailures(failedOrder)
			return model.ResolvedMedia{}, err
		}
	}

	if len(agg.sources) == 0 {
		s.setLastFailures(failedOrder)
		if err := ctx.Err(); err != nil {
			return model.ResolvedMedia{}, err
		}
		if len(failures) > 0 {
			mediaLog.Warn("all providers failed to resolve", "providers", len(allProviders), "failures", strings.Join(failures, " | "))
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
	sourceIndex map[string]int // canonical source identity -> index in sources
	subs        []model.SubtitleTrack
	seenSubs    map[string]struct{}
	tierCounts  map[string]int
}

// isVolatileSourceParam reports whether a query key is a rotated auth or
// signature value rather than part of the media address. Two providers
// resolving the same CDN playlist independently receive different values
// for these keys, but the underlying stream is identical.
func isVolatileSourceParam(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "token", "expires", "expire", "expiry", "exp", "signature", "sig",
		"auth", "hdnts", "policy", "key-pair-id":
		return true
	}
	return false
}

// canonicalSourceURL removes volatile signing parameters and canonicalizes
// host/query ordering so independently signed copies of one stream compare
// equal. Non-auth query parameters stay significant because they can select
// different media or renditions.
func canonicalSourceURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return strings.TrimSuffix(rawURL, "/")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		if isVolatileSourceParam(key) {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return strings.TrimSuffix(parsed.String(), "/")
}

// sourceDedupeIdentity is the identity of the media address and its required
// transport headers, with volatile signing parameters removed. User-Agent is
// deliberately excluded: it is a client hint, not part of the address, and
// providers commonly differ on whether they set it for the same CDN URL. The
// full TransportIdentity still owns playback and subtitle matching.
func sourceDedupeIdentity(src provider.MediaSource) string {
	rawURL := canonicalSourceURL(src.URL)
	if rawURL == "" {
		return ""
	}
	return strings.Join([]string{
		rawURL,
		strings.TrimSpace(src.Referer),
		strings.TrimSpace(src.CookieHeader),
		strings.TrimSpace(src.Language),
		strconv.FormatBool(src.SuppressOrigin),
		strings.Join(src.ExtraArgs, "\x00"),
	}, "\x00")
}

// mergeDuplicateSource folds descriptive metadata from a re-signed copy
// into the row already kept. The kept URL and headers stay untouched so the
// playable transport remains exactly what its provider resolved.
func mergeDuplicateSource(dst *provider.MediaSource, src provider.MediaSource) {
	if dst.SubType == "" {
		dst.SubType = src.SubType
	}
	if dst.Referer == "" {
		dst.Referer = src.Referer
	}
	if dst.UserAgent == "" {
		dst.UserAgent = src.UserAgent
	}
	if dst.CookieHeader == "" {
		dst.CookieHeader = src.CookieHeader
	}
	if dst.Language == "" {
		dst.Language = src.Language
	}
	if len(dst.ExtraArgs) == 0 {
		dst.ExtraArgs = src.ExtraArgs
	}
	if SourceQuality(src.Quality) > SourceQuality(dst.Quality) {
		dst.Quality = src.Quality
	}
	var missing []provider.SubtitleOption
	for _, sub := range src.Subtitles {
		if !hasSubtitleOption(dst.Subtitles, sub) && !hasSubtitleOption(missing, sub) {
			missing = append(missing, sub)
		}
	}
	if len(missing) > 0 {
		merged := make([]provider.SubtitleOption, 0, len(dst.Subtitles)+len(missing))
		merged = append(merged, dst.Subtitles...)
		merged = append(merged, missing...)
		dst.Subtitles = merged
	}
}

// hasSubtitleOption reports whether options already carries the same
// caption track (URL plus language).
func hasSubtitleOption(options []provider.SubtitleOption, candidate provider.SubtitleOption) bool {
	candidateURL := strings.TrimSpace(candidate.URL)
	candidateLang := strings.ToLower(strings.TrimSpace(candidate.Language))
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.URL), candidateURL) &&
			strings.ToLower(strings.TrimSpace(option.Language)) == candidateLang {
			return true
		}
	}
	return false
}

// shouldPreferSource reports whether candidate should replace existing as the
// canonical row for duplicate streams. Lower provider priority index takes
// precedence, ensuring primary/faster native providers (like anicine) win over fallbacks.
func (a *sourceAggregator) shouldPreferSource(existing, candidate provider.MediaSource) bool {
	pCand, hasCand := a.priority[candidate.Resolver]
	pExist, hasExist := a.priority[existing.Resolver]
	if hasCand && hasExist && pCand < pExist {
		return true
	}
	return false
}

// retargetSubtitles updates existing subtitles whose source was replaced by
// a preferred duplicate provider, keeping their SourceID and SourceURL valid.
func (a *sourceAggregator) retargetSubtitles(oldID, newID, newURL, newResolver string) {
	if oldID == "" || newID == "" {
		return
	}
	for i := range a.subs {
		if a.subs[i].SourceID == oldID {
			if oldID != newID {
				oldSubURL := a.subs[i].URL
				delete(a.seenSubs, oldSubURL+"\x00"+oldID)
				a.seenSubs[oldSubURL+"\x00"+newID] = struct{}{}
				a.subs[i].SourceID = newID
				a.subs[i].SourceURL = newURL
			}
			if newResolver != "" {
				a.subs[i].Resolver = newResolver
				a.subs[i].Label = fmt.Sprintf("%s (%s)", lang.Name(a.subs[i].Language), a.displayName(newResolver))
			}
		}
	}
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
		sourceIndex: make(map[string]int),
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

// add merges one provider batch. Independently re-signed copies of the same
// CDN media collapse into one row, while the kept row's full transport
// identity still owns its subtitles so playback headers and source matching
// stay exact.
func (a *sourceAggregator) add(providerName string, batch []provider.MediaSource) {
	if a.sourceIndex == nil {
		a.sourceIndex = make(map[string]int)
	}
	// In anime mode: within each provider's batch, hard subs sort before soft subs.
	if a.mode == provider.ModeAnime {
		slices.SortStableFunc(batch, func(x, y provider.MediaSource) int {
			xh := strings.EqualFold(strings.TrimSpace(x.SubType), provider.SubTypeHard) && !provider.ChallengedHost(x.URL)
			yh := strings.EqualFold(strings.TrimSpace(y.SubType), provider.SubTypeHard) && !provider.ChallengedHost(y.URL)
			if xh != yh {
				if xh {
					return -1
				}
				return 1
			}
			return 0
		})
	}
	for _, src := range batch {
		src.Resolver = providerName
		identity := src.TransportIdentity()
		if identity == "" {
			continue
		}
		dedupeKey := sourceDedupeIdentity(src)
		kept := src
		if index, ok := a.sourceIndex[dedupeKey]; ok {
			if a.shouldPreferSource(a.sources[index], src) {
				old := a.sources[index]
				oldIdentity := old.TransportIdentity()
				kept = src
				mergeDuplicateSource(&kept, old)
				a.sources[index] = kept
				a.retargetSubtitles(oldIdentity, kept.TransportIdentity(), kept.URL, src.Resolver)
			} else {
				old := a.sources[index]
				oldIdentity := old.TransportIdentity()
				mergeDuplicateSource(&a.sources[index], src)
				kept = a.sources[index]
				if oldIdentity != kept.TransportIdentity() {
					a.retargetSubtitles(oldIdentity, kept.TransportIdentity(), kept.URL, kept.Resolver)
				}
			}
		} else if a.tierHasRoom(src) {
			a.sourceIndex[dedupeKey] = len(a.sources)
			a.sources = append(a.sources, src)
		}
		keptIdentity := kept.TransportIdentity()
		for _, sub := range src.Subtitles {
			subURL := strings.TrimSpace(sub.URL)
			if subURL == "" {
				continue
			}
			key := subURL + "\x00" + keptIdentity
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
				Label:     fmt.Sprintf("%s (%s)", lang.Name(subLang), a.displayName(kept.Resolver)),
				Language:  subLang,
				URL:       subURL,
				Referer:   referer,
				SourceURL: kept.URL,
				SourceID:  keptIdentity,
				Default:   sub.Default,
				Resolver:  kept.Resolver,
			})
		}
	}
}

// sort preserves the arrival order of playable sources across providers,
// pushing only anti-bot challenged hosts to the bottom. It reindexes the
// dedupe map because sorting reorders the backing slice.
func (a *sourceAggregator) sort() {
	slices.SortStableFunc(a.sources, func(si, sj provider.MediaSource) int {
		return challengedRank(si) - challengedRank(sj)
	})
	a.reindexSources()
}

// reindexSources rebuilds the dedupe map after sorting. add and snapshot both
// run under the service lock, but sort reorders the slice, so stale indices
// would merge metadata into the wrong source on the next provider batch.
func (a *sourceAggregator) reindexSources() {
	if a.sourceIndex == nil {
		a.sourceIndex = make(map[string]int)
	}
	clear(a.sourceIndex)
	for i := range a.sources {
		a.sourceIndex[sourceDedupeIdentity(a.sources[i])] = i
	}
}

// challengedRank orders challenged-host sources below every playable-now
// source, mirroring the ranking package so the aggregate, Preview, and
// playback agree on what plays first.
func challengedRank(s provider.MediaSource) int {
	if provider.ChallengedHost(s.URL) {
		return 1
	}
	return 0
}

func firstPlaybackURL(playback []provider.MediaSource) string {
	if len(playback) == 0 {
		return ""
	}
	return playback[0].URL
}
