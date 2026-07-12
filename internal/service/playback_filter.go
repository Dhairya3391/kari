package service

import (
	"regexp"
	"strconv"
	"strings"

	"kari/internal/model"
)

func FilterPlaybackIndices(playback []model.PlaybackSource, qualityMode int, languages map[string]bool) []int {
	candidates := make([]int, 0, len(playback))
	for i, source := range playback {
		if strings.EqualFold(source.Resolver, "moviebox") && source.Language != "" {
			if enabled, configured := languages[source.Language]; configured && !enabled {
				continue
			}
		}
		candidates = append(candidates, i)
	}

	switch qualityMode {
	case 1:
		return filterByQuality(playback, candidates, func(q, maxQ, _ int) bool { return q == maxQ })
	case 2:
		return filterByQuality(playback, candidates, func(q, maxQ, minQ int) bool { return maxQ == minQ || q < maxQ })
	case 3:
		return filterByQuality(playback, candidates, func(q, _, minQ int) bool { return q == minQ })
	default:
		return candidates
	}
}

func FilterPlaybackSources(playback []model.PlaybackSource, qualityMode int, languages map[string]bool) []model.PlaybackSource {
	indices := FilterPlaybackIndices(playback, qualityMode, languages)
	sources := make([]model.PlaybackSource, 0, len(indices))
	for _, idx := range indices {
		sources = append(sources, playback[idx])
	}
	return sources
}

func filterByQuality(playback []model.PlaybackSource, candidates []int, keep func(q, maxQ, minQ int) bool) []int {
	type group struct{ indices []int }
	groups := make(map[string]*group)
	order := make([]string, 0, len(candidates))
	for _, idx := range candidates {
		resolver := playback[idx].Resolver
		if groups[resolver] == nil {
			groups[resolver] = &group{}
			order = append(order, resolver)
		}
		groups[resolver].indices = append(groups[resolver].indices, idx)
	}

	result := make([]int, 0, len(candidates))
	for _, resolver := range order {
		indices := groups[resolver].indices
		maxQ, minQ := 0, 99999
		for _, idx := range indices {
			quality := sourceQuality(playback[idx].Label)
			maxQ = max(maxQ, quality)
			if quality > 0 && quality < minQ {
				minQ = quality
			}
		}
		if minQ > maxQ {
			minQ = maxQ
		}
		for _, idx := range indices {
			if keep(sourceQuality(playback[idx].Label), maxQ, minQ) {
				result = append(result, idx)
			}
		}
	}
	return result
}

func sourceQuality(label string) int {
	normalized := strings.ToLower(label)
	if strings.Contains(normalized, "4k") || strings.Contains(normalized, "uhd") {
		return 2160
	}
	match := regexp.MustCompile(`(\d{3,4})p`).FindStringSubmatch(normalized)
	if len(match) < 2 {
		return 0
	}
	quality, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return quality
}
