package service

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/timeouts"
	"kari/internal/util"

	"golang.org/x/sync/singleflight"
)

const (
	searchCacheTTL   = 5 * time.Minute
	searchCacheMax   = 100
	episodesCacheTTL = 15 * time.Minute
	episodesCacheMax = 200

	// Operation budgets live in internal/timeouts. The local aliases keep
	// call sites short and let tests reference the same names as before.
	searchTimeout               = timeouts.SearchPerProvider
	singleSearchTimeout         = timeouts.SearchPerProvider
	searchOverallTimeout        = timeouts.SearchOverall
	sourceTimeout               = timeouts.SourcePerProvider
	sourcesOverallTimeout       = timeouts.SourceOverall
	searchGrace                 = timeouts.SearchGrace
	searchGraceFast             = timeouts.SearchGraceFast
	searchFirstResultTimeout    = timeouts.SearchFirstResult
	manifestProbeTimeout        = timeouts.ManifestRequest
	manifestClientTimeout       = timeouts.ManifestRequest
	manifestBatchProbeTimeout   = timeouts.ManifestBatch
	manifestQualityProbeTimeout = timeouts.ManifestQuality

	// searchGraceBulk is the result count that switches a fan-out to the fast
	// window. One provider's full page is normally a complete answer; the
	// remaining stragglers are still collected by the backfill pass.
	searchGraceBulk = 8

	// manifestProbeConcurrency bounds parallel playlist fetches. It must be at
	// least as large as a provider's adaptive row count (Miruro emits ~12) so
	// one batch completes in a single wave.
	manifestProbeConcurrency = 16
)

type searchCacheEntry struct {
	results  []provider.SearchResult
	query    string
	warnings []string
	cachedAt time.Time
}

// searchBackfillEntry holds results that arrived after an early search
// return. It is merged into later queries so slow providers still contribute
// rows instead of being lost to the grace window.
type searchBackfillEntry struct {
	results  []provider.SearchResult
	cachedAt time.Time
}

type episodesCacheEntry struct {
	episodes []provider.Episode
	cachedAt time.Time
}

// log scopes every line from this package/component.
var mediaLog = logging.With("component", "service.media")

// MediaService coordinates generic provider fan-out, caches, health checks,
// and cross-provider result aggregation.
type MediaService struct {
	registry *provider.Registry
	health   *provider.Health

	lastMu       sync.Mutex
	lastFailures []string

	disabledMu         sync.RWMutex
	disabledProviders  map[string]bool
	providerGeneration uint64

	searchCache    *util.BoundedCache[searchCacheEntry]
	searchBackfill *util.BoundedCache[searchBackfillEntry]
	// searchBackfillMu makes each collector's read-union-write of a cache key
	// atomic, so overlapping collectors cannot overwrite one another's rows.
	searchBackfillMu sync.Mutex
	episodesCache    *util.BoundedCache[episodesCacheEntry]
	episodeTitles    *util.BoundedCache[map[int]string]
	sf               singleflight.Group
	// manifestHTTP is shared across every resolve batch so playlist probes
	// reuse connections instead of building a transport per batch.
	manifestHTTP *http.Client
}

// NewMediaService constructs a MediaService.
func NewMediaService(registry *provider.Registry) *MediaService {
	return &MediaService{
		registry:       registry,
		health:         &provider.Health{},
		searchCache:    util.NewBoundedCache[searchCacheEntry](searchCacheMax),
		searchBackfill: util.NewBoundedCache[searchBackfillEntry](searchCacheMax),
		episodesCache:  util.NewBoundedCache[episodesCacheEntry](episodesCacheMax),
		episodeTitles:  util.NewBoundedCache[map[int]string](200),
		manifestHTTP:   httpclient.NewWithTimeout(manifestClientTimeout),
	}
}

func (s *MediaService) episodeAvailabilitySources(mode provider.ContentType, series provider.SearchResult) []provider.EpisodeAvailabilitySource {
	routes := s.registry.PlanForMode(mode).Search
	var sources []provider.EpisodeAvailabilitySource
	seen := make(map[string]struct{}, len(routes))
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
	for _, route := range routes {
		p := route.Provider
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
	s.providerGeneration++
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
	if s.searchBackfill != nil {
		// Same lock order as collectSearchBackfill (disabledMu before
		// searchBackfillMu): a collector cannot write stale rows between the
		// generation bump and this clear.
		s.searchBackfillMu.Lock()
		s.searchBackfill.Clear()
		s.searchBackfillMu.Unlock()
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
