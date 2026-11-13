package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"kari/internal/provider"
)

const (
	episodeOverallTimeout = 45 * time.Second
	// episodeTitlesTimeout bounds one parallel title lookup to the fastest
	// usable catalog, including the case where every catalog hangs.
	episodeTitlesTimeout = 3 * time.Second
)

// FetchEpisodes retrieves episode results from the selected provider and
// applies any declared audio-availability and episode-title capabilities.
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
	ctx, cancel := context.WithTimeout(ctx, episodeOverallTimeout)
	defer cancel()

	p, ok := s.registry.ProviderByNameForMode(strings.TrimSpace(series.Provider), mode)
	if !ok {
		mediaLog.Warn("episodes requested from unavailable provider",
			"provider", series.Provider, "mode", mode)
		return nil, fmt.Errorf("the source for this title is unavailable in %s mode right now", mode)
	}
	episodeSource, ok := p.(provider.EpisodeSource)
	if !ok {
		return nil, fmt.Errorf("the source for this title does not support episodes")
	}

	// Naming sources already have catalog-scoped titles in their own
	// rosters. Avoid waiting for a separate availability roster followed by
	// a second title request. Dub still requires verified audio availability.
	if mode == provider.ModeAnime && !strings.EqualFold(strings.TrimSpace(audioMode), provider.AudioDub) {
		if episodes, ok := s.namedEpisodeRoster(ctx, mode, series); ok {
			return filterEpisodesByAudio(episodes, audioMode), nil
		}
	}

	var titleLookup episodeTitleLookup
	var titlesDone chan struct{}
	if mode == provider.ModeAnime && series.MediaType != provider.MediaTypeMovie {
		titlesDone = make(chan struct{})
		go func() {
			defer close(titlesDone)
			titleLookup = s.lookupCanonicalEpisodeTitles(ctx, mode, series)
		}()
		// Join the coordinator even when the roster fails before titles arrive.
		defer func() {
			cancel()
			<-titlesDone
		}()
	}
	applyTitles := func(episodes []provider.Episode) []provider.Episode {
		if titlesDone == nil || len(episodes) == 0 {
			return episodes
		}
		<-titlesDone
		return s.applyEpisodeTitleLookup(series, episodes, titleLookup)
	}

	requestedAudio := audioMode
	availabilitySources := s.episodeAvailabilitySources(mode, series)
	audioSelection := s.registry.Features(mode).AudioSelection || len(availabilitySources) > 0
	if audioSelection && audioMode != "" {
		requestedAudio = strings.ToLower(strings.TrimSpace(requestedAudio))
		if requestedAudio != provider.AudioDub {
			requestedAudio = provider.AudioSub
		}

		// Episode title metadata belongs to the selected source order.
		// Racing providers can attach a different season's titles to
		// otherwise interchangeable episode-number handles.
		var enabledAvail, disabledAvail []provider.EpisodeAvailabilitySource
		for _, as := range availabilitySources {
			if prov, ok := as.(provider.Provider); ok && s.IsProviderDisabled(mode, prov.Name()) {
				disabledAvail = append(disabledAvail, as)
			} else {
				enabledAvail = append(enabledAvail, as)
			}
		}
		probeList := enabledAvail
		if len(probeList) == 0 {
			probeList = availabilitySources
			disabledAvail = nil
		}
		eps, ok, lastErr := s.firstAvailableEpisodes(ctx, probeList, series)
		if !ok && len(disabledAvail) > 0 && ctx.Err() == nil {
			mediaLog.Debug("episodes: enabled availability sources returned no episodes, attempting fallback to disabled sources")
			eps, ok, lastErr = s.firstAvailableEpisodes(ctx, disabledAvail, series)
		}
		if ok {
			eps = applyTitles(eps)
			return filterAvailableEpisodesByAudio(eps, requestedAudio), nil
		} else if requestedAudio == provider.AudioDub {
			if lastErr != nil {
				return nil, fmt.Errorf("dub availability: %w", lastErr)
			}
			return nil, provider.ErrNoEpisodes
		}
	}
	if requestedAudio == provider.AudioDub && mode == provider.ModeAnime && len(availabilitySources) == 0 {
		return nil, provider.ErrNoEpisodes
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	eps, err := episodeSource.FetchEpisodes(ctx, series)
	if err != nil {
		return nil, err
	}
	eps = applyTitles(eps)
	return filterEpisodesByAudio(eps, requestedAudio), nil
}

func (s *MediaService) namedEpisodeRoster(
	ctx context.Context,
	mode provider.ContentType,
	series provider.SearchResult,
) ([]provider.Episode, bool) {
	for _, source := range s.episodeTitleSources(mode, series) {
		rosterSource, ok := source.(provider.EpisodeSource)
		if !ok || s.IsProviderDisabled(mode, rosterSource.Name()) {
			continue
		}
		id, ok := series.ResolutionID(mode, rosterSource.Name())
		if !ok {
			continue
		}
		catalogSeries := series
		catalogSeries.ID = id
		started := time.Now()
		episodes, err := rosterSource.FetchEpisodes(ctx, catalogSeries)
		if err != nil || len(episodes) == 0 {
			mediaLog.Debug("named episode roster unavailable", "provider", rosterSource.Name(), "err", err)
			continue
		}
		titles := make(map[int]string, len(episodes))
		for _, episode := range episodes {
			if title := strings.TrimSpace(episode.Title); title != "" {
				titles[episode.Episode] = title
			}
		}
		if len(titles) > 0 {
			cacheKey := fmt.Sprintf("titles:%s:%s", series.Type, strings.TrimSpace(series.ID))
			s.episodeTitles.Set(cacheKey, titles)
		}
		mediaLog.Debug("named episode roster ready", "provider", rosterSource.Name(),
			"episodes", len(episodes), "elapsed", time.Since(started))
		return episodes, true
	}
	return nil, false
}

func (s *MediaService) applyCanonicalEpisodeTitles(ctx context.Context, mode provider.ContentType, series provider.SearchResult, episodes []provider.Episode) []provider.Episode {
	if mode != provider.ModeAnime || series.MediaType == provider.MediaTypeMovie {
		return episodes
	}
	if len(episodes) == 0 {
		return episodes
	}
	lookup := s.lookupCanonicalEpisodeTitles(ctx, mode, series)
	return s.applyEpisodeTitleLookup(series, episodes, lookup)
}

type episodeTitleLookup struct {
	titles   map[int]string
	cacheKey string
	cached   bool
}

func (s *MediaService) lookupCanonicalEpisodeTitles(
	ctx context.Context,
	mode provider.ContentType,
	series provider.SearchResult,
) episodeTitleLookup {
	cacheKey := fmt.Sprintf("titles:%s:%s", series.Type, strings.TrimSpace(series.ID))
	if titles, ok := s.episodeTitles.Get(cacheKey); ok {
		return episodeTitleLookup{titles: titles, cacheKey: cacheKey, cached: true}
	}
	// Start the naming budget alongside the roster, not after it finishes.
	titleCtx, cancel := context.WithTimeout(ctx, episodeTitlesTimeout)
	defer cancel()
	titles, _ := s.firstEpisodeTitles(titleCtx, s.episodeTitleSources(mode, series), series)
	return episodeTitleLookup{titles: titles, cacheKey: cacheKey}
}

func (s *MediaService) applyEpisodeTitleLookup(
	series provider.SearchResult,
	episodes []provider.Episode,
	lookup episodeTitleLookup,
) []provider.Episode {
	titles := lookup.titles
	if !lookup.cached {
		if len(titles) == 0 {
			mediaLog.Debug("canonical episode titles unavailable", "provider", series.Provider, "seriesID", series.ID)
			for i := range episodes {
				episodes[i].Title = genericEpisodeTitle(episodes[i].Episode)
			}
			return episodes
		}
		// Do not populate the title cache for failed or empty roster requests.
		s.episodeTitles.Set(lookup.cacheKey, titles)
	}
	for i := range episodes {
		if title := strings.TrimSpace(titles[episodes[i].Episode]); title != "" {
			episodes[i].Title = title
		} else if strings.TrimSpace(episodes[i].Title) == "" {
			episodes[i].Title = genericEpisodeTitle(episodes[i].Episode)
		}
	}
	return episodes
}

// episodeTitleSources lists providers that supply their own episode titles
// for mode, origin provider first so the running title usually wins the race.
func (s *MediaService) episodeTitleSources(mode provider.ContentType, series provider.SearchResult) []provider.EpisodeTitlesSource {
	routes := s.registry.PlanForMode(mode).Search
	ordered := make([]provider.Provider, 0, len(routes)+1)
	if origin, ok := s.registry.ProviderByNameForMode(strings.TrimSpace(series.Provider), mode); ok {
		ordered = append(ordered, origin)
	}
	for _, route := range routes {
		ordered = append(ordered, route.Provider)
	}
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
	// Each source can finish even when a sibling wins or the request ends.
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
		select {
		case <-ctx.Done():
			return nil, false
		case result := <-results:
			if len(result.titles) > 0 {
				return result.titles, true
			}
		}
	}
	return nil, false
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
	// Each source can finish even when a sibling wins or the request ends.
	results := make(chan availabilityResult, len(sources))
	for _, source := range sources {
		go func(source provider.EpisodeAvailabilitySource) {
			episodes, err := source.FetchAvailableEpisodes(childCtx, series)
			results <- availabilityResult{episodes: episodes, err: err}
		}(source)
	}
	var lastErr error
	for range sources {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case result := <-results:
			if result.err == nil && len(result.episodes) > 0 {
				return result.episodes, true, nil
			}
			if result.err != nil {
				lastErr = result.err
			}
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
