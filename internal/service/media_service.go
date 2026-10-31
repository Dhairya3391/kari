package service

import (
	"strings"
	"sync"
	"time"

	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/util"

	"golang.org/x/sync/singleflight"
)

const (
	searchCacheTTL        = 5 * time.Minute
	searchCacheMax        = 100
	episodesCacheTTL      = 15 * time.Minute
	episodesCacheMax      = 200
	searchTimeout         = 8 * time.Second
	singleSearchTimeout   = 10 * time.Second
	searchOverallTimeout  = 12 * time.Second
	sourceTimeout         = 20 * time.Second
	sourcesOverallTimeout = 45 * time.Second
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

// MediaService coordinates generic provider fan-out, caches, health checks,
// and cross-provider result aggregation.
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
