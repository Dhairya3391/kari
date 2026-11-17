package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"kari/internal/provider"
	"kari/internal/timeouts"
)

// providerSearchResult is one provider's contribution to a search fan-out. It
// is shared with the backfill collector, which keeps draining the channel
// after the foreground wait ends.
type providerSearchResult struct {
	provider string
	results  []provider.SearchResult
	err      error
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
	s.disabledMu.RLock()
	providerGeneration := s.providerGeneration
	s.disabledMu.RUnlock()
	cacheKey := fmt.Sprintf("%s:%s", mode, strings.ToLower(strings.TrimSpace(query)))
	if s.searchCache != nil {
		if entry, ok := s.searchCache.Get(cacheKey); ok && time.Since(entry.cachedAt) < searchCacheTTL {
			// A previous call may have returned before slow providers finished.
			// Their rows land in searchBackfill after the response was cached,
			// so fold them in now instead of serving the partial set again.
			results := s.mergeSearchBackfill(cacheKey, mode, entry.results)
			if len(results) != len(entry.results) {
				s.searchCache.Set(cacheKey, searchCacheEntry{
					results:  results,
					query:    entry.query,
					warnings: entry.warnings,
					cachedAt: entry.cachedAt,
				})
			}
			return results, entry.query, entry.warnings, nil
		}
	}

	searchCtx, cancelSearch := context.WithTimeout(ctx, searchOverallTimeout)
	defer cancelSearch()

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
	allResults, warnings, err := s.searchProviders(searchCtx, mode, query, primaryRoutes, cacheKey, providerGeneration)
	if len(allResults) == 0 && len(disabledRoutes) > 0 && searchCtx.Err() == nil {
		mediaLog.Debug("search: enabled providers returned no results, attempting fallback to disabled providers",
			"mode", mode, "query", query, "disabled", len(disabledRoutes))
		fallbackResults, fallbackWarnings, fallbackErr := s.searchProviders(searchCtx, mode, query, disabledRoutes, cacheKey, providerGeneration)
		if len(fallbackResults) > 0 {
			allResults = fallbackResults
			warnings = append(warnings, fallbackWarnings...)
			err = fallbackErr
		}
	}

	if len(allResults) > 0 {
		if mode == provider.ModeLive {
			SortLiveResults(allResults, time.Now())
		}
		if s.searchCache != nil && len(warnings) == 0 {
			s.disabledMu.RLock()
			if providerGeneration == s.providerGeneration {
				s.searchCache.Set(cacheKey, searchCacheEntry{
					results:  allResults,
					query:    query,
					warnings: warnings,
					cachedAt: time.Now(),
				})
			}
			s.disabledMu.RUnlock()
		}
		return allResults, query, warnings, nil
	}
	return nil, query, warnings, err
}

func (s *MediaService) searchProviders(ctx context.Context, mode provider.ContentType, query string, routes []provider.Route, cacheKey string, providerGeneration uint64) ([]provider.SearchResult, []string, error) {
	providerBase, cancelProviders := context.WithTimeout(context.Background(), searchOverallTimeout)
	ctx, cancelWait := context.WithTimeout(ctx, searchOverallTimeout)
	defer cancelWait()

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
			pCtx, pCancel := context.WithTimeout(providerBase, timeout)
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
	firstResultTimer := time.NewTimer(searchFirstResultTimeout)
	defer firstResultTimer.Stop()
	var graceTimer *time.Timer
	var graceCh <-chan time.Time
	gotValidResults := false

	remaining := len(routes)
collectResults:
	for remaining > 0 {
		select {
		case res := <-ch:
			remaining--
			resultsMap[res.provider] = res
			if res.err == nil && len(res.results) > 0 && !gotValidResults {
				gotValidResults = true
				firstResultTimer.Stop()
				grace := searchGrace
				if len(res.results) >= searchGraceBulk {
					grace = searchGraceFast
				}
				graceTimer = time.NewTimer(grace)
				defer graceTimer.Stop()
				graceCh = graceTimer.C
			}
		case <-graceCh:
			mediaLog.Debug("search grace period expired; returning fast results", "mode", mode, "query", query)
			break collectResults
		case <-firstResultTimer.C:
			mediaLog.Debug("search first-result timeout; returning available results", "mode", mode, "query", query, "pending", remaining)
			break collectResults
		case <-ctx.Done():
			mediaLog.Warn("search deadline hit while waiting for providers", "pending", remaining, "mode", mode, "query", query)
			break collectResults
		}
	}

	if remaining > 0 && s.searchBackfill != nil {
		collected := make([]provider.SearchResult, 0, len(resultsMap))
		for _, res := range resultsMap {
			for _, r := range res.results {
				r.Provider = res.provider
				collected = append(collected, r)
			}
		}
		go s.collectSearchBackfill(cacheKey, collected, ch, remaining, cancelProviders, providerGeneration)
	} else {
		cancelProviders()
	}

	var (
		allResults []provider.SearchResult
		warnings   []string
	)
	seenTitles := make(map[string]struct{})
	mangaTitleIndex := make(map[string]int)
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
		appendSearchRows(mode, res.results, res.provider, &allResults, seenTitles, mangaTitleIndex)
	}

	allResults = s.mergeSearchBackfill(cacheKey, mode, allResults)

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

// collectSearchBackfill keeps reading the fan-out channel after the caller has
// returned early, then caches the complete union. The budget is detached from
// the request context so cancellation of the UI call cannot cancel backfill.
func (s *MediaService) collectSearchBackfill(cacheKey string, collected []provider.SearchResult, ch <-chan providerSearchResult, remaining int, cancelProviders context.CancelFunc, providerGeneration uint64) {
	defer cancelProviders()
	bgCtx, cancelDrain := context.WithTimeout(context.Background(), timeouts.SearchBackfill)
	defer cancelDrain()
	for remaining > 0 {
		select {
		case res := <-ch:
			remaining--
			if res.err != nil {
				continue
			}
			for _, r := range res.results {
				r.Provider = res.provider
				collected = append(collected, r)
			}
		case <-bgCtx.Done():
			remaining = 0
		}
	}
	if len(collected) == 0 {
		return
	}
	// Hold disabledMu before searchBackfillMu (the same order as
	// SetDisabledProviders) so a provider toggle and its cache clear cannot
	// interleave with this write. A collector that started under an older
	// generation must not reintroduce results from a just-disabled provider.
	s.disabledMu.RLock()
	defer s.disabledMu.RUnlock()
	if providerGeneration != s.providerGeneration {
		return
	}
	s.searchBackfillMu.Lock()
	defer s.searchBackfillMu.Unlock()
	if entry, ok := s.searchBackfill.Get(cacheKey); ok {
		collected = unionSearchRows(entry.results, collected)
	}
	s.searchBackfill.Set(cacheKey, searchBackfillEntry{results: collected, cachedAt: time.Now()})
}

// mergeSearchBackfill folds rows collected by earlier queries into the current
// result set, using the same dedup and manga-aggregation rules as the main merge.
func (s *MediaService) mergeSearchBackfill(cacheKey string, mode provider.ContentType, results []provider.SearchResult) []provider.SearchResult {
	if s.searchBackfill == nil {
		return results
	}
	entry, ok := s.searchBackfill.Get(cacheKey)
	if !ok || time.Since(entry.cachedAt) > searchCacheTTL {
		return results
	}
	seen := make(map[string]struct{}, len(results))
	mangaIndex := make(map[string]int, len(results))
	merged := make([]provider.SearchResult, 0, len(results)+len(entry.results))
	appendSearchRows(mode, results, "", &merged, seen, mangaIndex)
	appendSearchRows(mode, entry.results, "", &merged, seen, mangaIndex)
	return merged
}

// SearchBackfillFresh reports whether backfill for key was collected after
// since. Callers caching a search response use it to avoid reusing a snapshot
// taken before slow providers finished.
func (s *MediaService) SearchBackfillFresh(key string, since time.Time) bool {
	if s.searchBackfill == nil {
		return false
	}
	entry, ok := s.searchBackfill.Get(key)
	return ok && entry.cachedAt.After(since)
}

// appendSearchRows merges rows into out with the main search merge's rules:
// dedupe by type:ID, fold same-title manga rows from other providers into
// SourceOptions, and keep every row stamped with provider when given.
func appendSearchRows(mode provider.ContentType, rows []provider.SearchResult, providerName string, out *[]provider.SearchResult, seen map[string]struct{}, mangaIndex map[string]int) {
	for _, r := range rows {
		if providerName != "" {
			r.Provider = providerName
		}
		if mode == provider.ModeManga {
			if r.MediaType == provider.MediaTypeComic {
				continue
			}
			key := strings.ToLower(strings.Join(strings.Fields(r.Title), " ")) + "\x00" + r.MediaType
			if index, exists := mangaIndex[key]; exists {
				current := &(*out)[index]
				if current.Provider != r.Provider {
					if len(current.SourceOptions) == 0 {
						current.SourceOptions = []provider.SearchResult{*current}
						current.SourceOptions[0].SourceOptions = nil
					}
					current.SourceOptions = append(current.SourceOptions, r)
				}
				continue
			}
			mangaIndex[key] = len(*out)
		}
		key := fmt.Sprintf("%s:%s", r.Type, strings.TrimSpace(r.ID))
		if r.ID != "" {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
		}
		*out = append(*out, r)
	}
}

// unionSearchRows folds added into existing, deduplicating by type:ID so two
// backfill collectors under the same key keep each other's rows.
func unionSearchRows(existing, added []provider.SearchResult) []provider.SearchResult {
	if len(existing) == 0 {
		return added
	}
	seen := make(map[string]struct{}, len(existing)+len(added))
	out := make([]provider.SearchResult, 0, len(existing)+len(added))
	for _, r := range existing {
		out = append(out, r)
		if r.ID != "" {
			seen[fmt.Sprintf("%s:%s", r.Type, strings.TrimSpace(r.ID))] = struct{}{}
		}
	}
	for _, r := range added {
		if r.ID != "" {
			key := fmt.Sprintf("%s:%s", r.Type, strings.TrimSpace(r.ID))
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, r)
	}
	return out
}
