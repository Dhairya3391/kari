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
			eps = s.applyCanonicalEpisodeTitles(ctx, mode, series, eps)
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

	eps, err := episodeSource.FetchEpisodes(ctx, series)
	if err != nil {
		return nil, err
	}
	eps = s.applyCanonicalEpisodeTitles(ctx, mode, series, eps)
	return filterEpisodesByAudio(eps, requestedAudio), nil
}

func (s *MediaService) applyCanonicalEpisodeTitles(ctx context.Context, mode provider.ContentType, series provider.SearchResult, episodes []provider.Episode) []provider.Episode {
	if mode != provider.ModeAnime || series.MediaType == provider.MediaTypeMovie {
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
			mediaLog.Debug("canonical episode titles unavailable", "provider", series.Provider, "seriesID", series.ID)
			if mode == provider.ModeAnime {
				for i := range episodes {
					episodes[i].Title = genericEpisodeTitle(episodes[i].Episode)
				}
			}
			return episodes
		}
		s.episodeTitles.Set(cacheKey, titles)
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
