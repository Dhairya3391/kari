package service

import (
	"strings"

	"kari/internal/provider"
	"kari/internal/ranking"
)

// FilterPlaybackIndices returns indices of sources passing the language
// filter and current quality preference (1=highest, 2=mid, 3=lowest).
func FilterPlaybackIndices(playback []provider.MediaSource, qualityMode int, languages map[string]bool) []int {
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
		return filterByQuality(playback, candidates, keepHighestTier)
	case 2:
		return filterByQuality(playback, candidates, keepBelowMax)
	case 3:
		return filterByQuality(playback, candidates, keepLowestTier)
	case 10: // 4K (2160p) -> fallback 1080p -> 720p -> SD
		for _, target := range []int{2160, 1080, 720, 480} {
			if res := filterByTargetResolution(playback, candidates, target); len(res) > 0 {
				return res
			}
		}
		return candidates
	case 11: // FHD (1080p) -> fallback 720p -> 4K -> SD
		for _, target := range []int{1080, 720, 2160, 480} {
			if res := filterByTargetResolution(playback, candidates, target); len(res) > 0 {
				return res
			}
		}
		return candidates
	case 12: // HD (720p) -> fallback 1080p -> SD -> 4K
		for _, target := range []int{720, 1080, 480, 2160} {
			if res := filterByTargetResolution(playback, candidates, target); len(res) > 0 {
				return res
			}
		}
		return candidates
	case 13: // SD (480p / 360p / 240p) -> fallback 720p -> 1080p -> 4K
		for _, target := range []int{480, 720, 1080, 2160} {
			if res := filterByTargetResolution(playback, candidates, target); len(res) > 0 {
				return res
			}
		}
		return candidates
	default:
		return candidates
	}
}

// FilterPlaybackSources is the slice-returning variant of
// FilterPlaybackIndices.
func FilterPlaybackSources(playback []provider.MediaSource, qualityMode int, languages map[string]bool) []provider.MediaSource {
	indices := FilterPlaybackIndices(playback, qualityMode, languages)
	sources := make([]provider.MediaSource, 0, len(indices))
	for _, idx := range indices {
		sources = append(sources, playback[idx])
	}
	return sources
}

// Tier predicates: q is the source tier, maxQ/minQ the resolver's best and
// worst parsed tiers, secondQ the best tier below maxQ.
func keepHighestTier(q, maxQ, _ int, secondQ int) bool {
	if q == maxQ {
		return true
	}
	return maxQ >= 2160 && secondQ > 0 && q == secondQ
}

func keepBelowMax(q, maxQ, minQ, _ int) bool { return maxQ == minQ || q < maxQ }

func keepLowestTier(q, _, minQ, _ int) bool { return q == minQ }

func filterByTargetResolution(playback []provider.MediaSource, candidates []int, targetHeight int) []int {
	var matched []int
	for _, idx := range candidates {
		q := SourceQuality(playback[idx].Quality)
		switch targetHeight {
		case 2160:
			if q >= 2160 {
				matched = append(matched, idx)
			}
		case 1080:
			if q == 1080 || q == 1440 {
				matched = append(matched, idx)
			}
		case 720:
			if q == 720 || q == 576 {
				matched = append(matched, idx)
			}
		case 480:
			if q <= 480 && q > 0 {
				matched = append(matched, idx)
			}
		default:
			if q == targetHeight {
				matched = append(matched, idx)
			}
		}
	}
	return matched
}

func filterByQuality(playback []provider.MediaSource, candidates []int, keep func(q, maxQ, minQ, secondQ int) bool) []int {
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
			quality := SourceQuality(playback[idx].Quality)
			maxQ = max(maxQ, quality)
			if quality > 0 && quality < minQ {
				minQ = quality
			}
		}
		if minQ > maxQ {
			minQ = maxQ
		}
		for _, idx := range indices {
			quality := SourceQuality(playback[idx].Quality)
			if quality < maxQ && quality > secondQ {
				secondQ = quality
			}
		}
		kept := false
		for _, idx := range indices {
			if keep(SourceQuality(playback[idx].Quality), maxQ, minQ, secondQ) {
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
				if SourceQuality(playback[idx].Quality) == maxQ {
					result = append(result, idx)
					break
				}
			}
		}
	}
	return result
}

// SourceQuality extracts a numeric resolution (2160/1080/…) from a quality
// label like "4K [Movy: Server]", "1080p (Vidstream-2)", "Auto (HD1)",
// "HD", "SD", or "360p"; 0 when unparseable. It delegates to
// ranking.ParseResolution so downloads and Preview agree.
func SourceQuality(label string) int {
	return ranking.ParseResolution(label)
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
