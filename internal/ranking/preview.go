package ranking

import (
	"sort"
	"strings"

	"kari/internal/provider"
)

// GroupSourcesByProvider orders each provider's rows by playability and
// quality while preserving the order in which providers were selected.
func GroupSourcesByProvider(sources []ScoredSource, qualityMode int, mode provider.ContentType) []ScoredSource {
	if len(sources) <= 1 {
		return sources
	}
	groups := make(map[string][]ScoredSource)
	var providerOrder []string
	for _, source := range sources {
		name := source.Source.Resolver
		if _, exists := groups[name]; !exists {
			providerOrder = append(providerOrder, name)
		}
		groups[name] = append(groups[name], source)
	}

	ordered := make([]ScoredSource, 0, len(sources))
	for _, name := range providerOrder {
		group := groups[name]
		sort.SliceStable(group, func(i, j int) bool {
			left := group[i].Source
			right := group[j].Source
			leftChallenged := provider.ChallengedHost(left.URL)
			rightChallenged := provider.ChallengedHost(right.URL)
			if leftChallenged != rightChallenged {
				return !leftChallenged
			}
			if left.FallbackOnly != right.FallbackOnly {
				return !left.FallbackOnly
			}
			leftResolution := ParseResolution(left.Quality)
			rightResolution := ParseResolution(right.Quality)
			if leftResolution != rightResolution {
				return qualitySortRank(leftResolution, qualityMode) < qualitySortRank(rightResolution, qualityMode)
			}
			return preferPrimaryAudio(left.Language, right.Language, mode)
		})
		ordered = append(ordered, group...)
	}
	return ordered
}

// HardsubsFirst moves hard-subtitled rows ahead of soft subtitles within each
// provider while preserving provider order and each row's stable rank.
func HardsubsFirst(mode provider.ContentType, qualityMode int, subtitlesEnabled bool, sources []ScoredSource) []ScoredSource {
	if mode != provider.ModeAnime || !subtitlesEnabled || len(sources) <= 1 {
		return sources
	}
	if qualityMode != QualityAll && qualityMode != QualityHighest {
		return sources
	}

	ordered := make([]ScoredSource, 0, len(sources))
	for start := 0; start < len(sources); {
		end := start + 1
		resolver := sources[start].Source.Resolver
		for end < len(sources) && sources[end].Source.Resolver == resolver {
			end++
		}
		var hard, other []ScoredSource
		for _, source := range sources[start:end] {
			isPlayableHardSub := strings.EqualFold(strings.TrimSpace(source.Source.SubType), provider.SubTypeHard) && !source.Source.FallbackOnly &&
				!provider.ChallengedHost(source.Source.URL)
			if isPlayableHardSub {
				hard = append(hard, source)
				continue
			}
			other = append(other, source)
		}
		ordered = append(ordered, hard...)
		ordered = append(ordered, other...)
		start = end
	}
	return ordered
}

func qualitySortRank(resolution, qualityMode int) int {
	const unknownResolution = 1 << 30

	switch qualityMode {
	case QualityLowest:
		if resolution == 0 {
			return unknownResolution
		}
		return resolution
	case QualityDataSaver:
		switch {
		case resolution == 0:
			return unknownResolution
		case resolution <= 1080:
			return 1080 - resolution
		default:
			return 10000 + resolution
		}
	default:
		return -resolution
	}
}

func preferPrimaryAudio(left, right string, mode provider.ContentType) bool {
	if mode == provider.ModeAnime {
		return false
	}
	left = strings.ToLower(strings.TrimSpace(left))
	right = strings.ToLower(strings.TrimSpace(right))
	leftPrimary := left == "" || left == "en" || left == "english"
	rightPrimary := right == "" || right == "en" || right == "english"
	return leftPrimary && !rightPrimary
}
