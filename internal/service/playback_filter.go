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
		if source.Language != "" {
			if enabled, configured := caseInsensitiveLangLookup(source.Language, languages); configured && !enabled {
				continue
			}
		}
		candidates = append(candidates, i)
	}

	switch qualityMode {
	case 1:
		return filterByQuality(playback, candidates, func(q, maxQ, _, secondQ int) bool {
			if q == maxQ {
				return true
			}
			return maxQ >= 2160 && secondQ > 0 && q == secondQ
		})
	case 2:
		return filterByQuality(playback, candidates, func(q, maxQ, minQ, _ int) bool { return maxQ == minQ || q < maxQ })
	case 3:
		return filterByQuality(playback, candidates, func(q, _, minQ, _ int) bool { return q == minQ })
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

func filterByQuality(playback []model.PlaybackSource, candidates []int, keep func(q, maxQ, minQ, secondQ int) bool) []int {
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
		maxQ, minQ, secondQ := 0, 99999, 0
		for _, idx := range indices {
			quality := SourceQuality(playback[idx].Label)
			maxQ = max(maxQ, quality)
			if quality > 0 && quality < minQ {
				minQ = quality
			}
		}
		if minQ > maxQ {
			minQ = maxQ
		}
		for _, idx := range indices {
			quality := SourceQuality(playback[idx].Label)
			if quality < maxQ && quality > secondQ {
				secondQ = quality
			}
		}
		kept := false
		for _, idx := range indices {
			if keep(SourceQuality(playback[idx].Label), maxQ, minQ, secondQ) {
				result = append(result, idx)
				kept = true
			}
		}
		// Guarantee at least one source per resolver no matter the quality
		// mode — a provider that only offers a low tier (or unparseable
		// labels like CDN names) must never disappear entirely. Fall back to
		// that resolver's highest-quality source.
		if !kept {
			for _, idx := range indices {
				if SourceQuality(playback[idx].Label) == maxQ {
					result = append(result, idx)
					break
				}
			}
		}
	}
	return result
}

var (
	reQualityP   = regexp.MustCompile(`(\d{3,4})p`)
	reQualityNum = regexp.MustCompile(`\b(\d{3,4})\b`)
)

func SourceQuality(label string) int {
	normalized := strings.ToLower(label)
	if strings.Contains(normalized, "4k") || strings.Contains(normalized, "uhd") {
		return 2160
	}
	if m := reQualityP.FindStringSubmatch(normalized); len(m) >= 2 {
		return atoiOrZero(m[1])
	}
	// Bare resolutions from providers that return just "480", "720", "1080".
	if m := reQualityNum.FindStringSubmatch(normalized); len(m) >= 2 {
		return atoiOrZero(m[1])
	}
	return 0
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func caseInsensitiveLangLookup(tag string, languages map[string]bool) (enabled, configured bool) {
	if languages == nil {
		return true, false
	}
	if v, ok := languages[tag]; ok {
		return v, true
	}
	for k, v := range languages {
		if strings.EqualFold(k, tag) {
			return v, true
		}
	}
	return true, false
}
