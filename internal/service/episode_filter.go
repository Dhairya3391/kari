package service

import (
	"fmt"
	"slices"
	"strings"

	"kari/internal/provider"
)

// EpisodeSeasons returns distinct season numbers in display order. Unnumbered
// episodes belong to season one for list navigation.
func EpisodeSeasons(episodes []provider.Episode) []int {
	seen := make(map[int]struct{})
	seasons := make([]int, 0)
	for _, episode := range episodes {
		season := episode.Season
		if season <= 0 {
			season = 1
		}
		if _, ok := seen[season]; ok {
			continue
		}
		seen[season] = struct{}{}
		seasons = append(seasons, season)
	}
	slices.Sort(seasons)
	return seasons
}

// FilterEpisodeList applies season selection and text matching while keeping
// indices aligned to the original episode slice.
func FilterEpisodeList(episodes []provider.Episode, activeSeason int, query string) ([]provider.Episode, []int) {
	seasons := EpisodeSeasons(episodes)
	filtered := episodes
	indices := allEpisodeIndices(len(episodes))
	if len(seasons) > 1 {
		seasonIndex := max(0, min(activeSeason, len(seasons)-1))
		target := seasons[seasonIndex]
		seasonEpisodes := make([]provider.Episode, 0, len(episodes))
		seasonIndices := make([]int, 0, len(episodes))
		for index, episode := range episodes {
			season := episode.Season
			if season <= 0 {
				season = 1
			}
			if season == target {
				seasonEpisodes = append(seasonEpisodes, episode)
				seasonIndices = append(seasonIndices, index)
			}
		}
		if len(seasonEpisodes) > 0 {
			filtered = seasonEpisodes
			indices = seasonIndices
		}
	}
	return FilterEpisodesByText(filtered, indices, query)
}

// FilterEpisodesByText matches episode titles and displayed numbers while
// preserving each row's index in the original episode slice.
func FilterEpisodesByText(episodes []provider.Episode, indices []int, query string) ([]provider.Episode, []int) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return episodes, indices
	}

	filtered := make([]provider.Episode, 0, len(episodes))
	filteredIndices := make([]int, 0, len(episodes))
	for index, episode := range episodes {
		originalIndex := index
		if index < len(indices) {
			originalIndex = indices[index]
		}
		number := episode.Episode
		if number == 0 {
			number = originalIndex + 1
		}
		haystack := strings.ToLower(episode.Title) + " " + fmt.Sprint(number)
		if strings.Contains(haystack, query) {
			filtered = append(filtered, episode)
			filteredIndices = append(filteredIndices, originalIndex)
		}
	}
	return filtered, filteredIndices
}

func allEpisodeIndices(count int) []int {
	indices := make([]int, count)
	for index := range indices {
		indices[index] = index
	}
	return indices
}
