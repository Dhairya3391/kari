package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"kari/internal/provider"
)

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

	allRoutes := s.registry.PlanForMode(mode).Search
	if len(allRoutes) == 0 {
		return nil, query, nil, fmt.Errorf("no providers available for mode %q", mode)
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
	allResults, warnings, err := s.searchProviders(ctx, mode, query, primaryRoutes)
	if len(allResults) == 0 && len(disabledRoutes) > 0 && ctx.Err() == nil {
		mediaLog.Debug("search: enabled providers returned no results, attempting fallback to disabled providers",
			"mode", mode, "query", query, "disabled", len(disabledRoutes))
		fallbackResults, fallbackWarnings, fallbackErr := s.searchProviders(ctx, mode, query, disabledRoutes)
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

func (s *MediaService) searchProviders(ctx context.Context, mode provider.ContentType, query string, routes []provider.Route) ([]provider.SearchResult, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, searchOverallTimeout)
	defer cancel()

	type providerSearchResult struct {
		provider string
		results  []provider.SearchResult
		err      error
	}

	ch := make(chan providerSearchResult, len(routes))
	for _, route := range routes {
		p := route.Provider
		if skipped, reason := s.health.Skipped(p.Name(), len(routes) == 1); skipped {
			mediaLog.Debug("search skipping circuit-broken provider", "provider", p.Name(), "reason", reason)
			ch <- providerSearchResult{provider: p.Name(), err: provider.Wrap(p.Name(), "search", provider.KindUpstream, fmt.Errorf("%s", reason))}
			continue
		}
		timeout := providerSearchTimeout(len(routes), route)
		go func(route provider.Route) {
			p := route.Provider
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
		}(route)
	}

	resultsMap := make(map[string]providerSearchResult, len(routes))
	var graceTimer *time.Timer
	var graceCh <-chan time.Time
	gotValidResults := false

collectResults:
	for remaining := len(routes); remaining > 0; {
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
	for _, route := range routes {
		p := route.Provider
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
		for _, route := range routes {
			p := route.Provider
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
// longer single-provider budget. A provider-declared shorter deadline wins.
func providerSearchTimeout(providerCount int, route provider.Route) time.Duration {
	timeout := searchTimeout
	if providerCount == 1 {
		timeout = singleSearchTimeout
	}
	if deadline := route.Deadlines.Search; deadline > 0 && deadline < timeout {
		timeout = deadline
	}
	if timeout > searchTimeout && providerCount > 1 {
		timeout = searchTimeout
	}
	return timeout
}

// providerSourceTimeout returns the per-provider resolve deadline: the
// provider's declared source deadline when set, else the shared default,
// never beyond the overall budget that bounds the whole fan-out.
func providerSourceTimeout(route provider.Route) time.Duration {
	timeout := sourceTimeout
	if deadline := route.Deadlines.Source; deadline > 0 {
		timeout = deadline
	}
	if timeout > sourcesOverallTimeout {
		timeout = sourcesOverallTimeout
	}
	return timeout
}
