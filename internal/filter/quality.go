// Package filter holds Kari's source-filtering and ranking domain logic. It
// turns raw provider sources into a preference-aware, ordered list without
// depending on services, concrete providers, or the TUI.
package filter

import (
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// QualityMode selects the resolution policy used to hard-filter sources.
type QualityMode uint8

// Quality modes. The first four mirror the persisted playback quality
// setting; the last four mirror the explicit download-quality tiers.
const (
	// QualityAll keeps every source unchanged.
	QualityAll QualityMode = iota
	// QualityHighest keeps full-HD-and-better rows when any exist.
	QualityHighest
	// QualityDataSaver prefers 1080p-and-below rows over higher tiers.
	QualityDataSaver
	// QualityLowest keeps the lowest known tier per resolver.
	QualityLowest
	// QualityUHD selects 2160p with a 1080p/720p/480p fallback ladder.
	QualityUHD
	// QualityFHD selects 1080p with a 720p/2160p/480p fallback ladder.
	QualityFHD
	// QualityHD selects 720p with a 1080p/480p/2160p fallback ladder.
	QualityHD
	// QualitySD selects 480p with a 720p/1080p/2160p fallback ladder.
	QualitySD
)

// fullHDHeight is the minimum resolution kept visible by QualityHighest.
const fullHDHeight = 1080

// QualityModeFromLegacy maps a persisted quality integer to a QualityMode.
// Values 0-3 are playback modes and 10-13 are explicit download tiers; any
// other value falls back to QualityAll.
func QualityModeFromLegacy(value int) QualityMode {
	switch value {
	case 1:
		return QualityHighest
	case 2:
		return QualityDataSaver
	case 3:
		return QualityLowest
	case 10:
		return QualityUHD
	case 11:
		return QualityFHD
	case 12:
		return QualityHD
	case 13:
		return QualitySD
	default:
		return QualityAll
	}
}

// SelectTier hard-filters sources according to mode. Adaptive rows always
// survive because their real tier is chosen at playback time, and every
// resolver keeps at least one row so a low-tier-only provider never vanishes.
func SelectTier(sources []provider.MediaSource, mode QualityMode) []provider.MediaSource {
	keep, _ := selectTierKeep(sources, mode)
	return applyKeep(sources, keep)
}

// selectTierKeep computes the per-source hard-filter decisions once so the
// pipeline can keep original indices while sharing exactly the same policy.
// The second result marks rows kept only by the per-resolver guarantee; they
// must not be promoted ahead of rows that actually match the chosen tier.
func selectTierKeep(sources []provider.MediaSource, mode QualityMode) ([]bool, []bool) {
	keep := make([]bool, len(sources))
	if len(sources) == 0 {
		return keep, keep
	}
	if mode == QualityAll {
		for i := range keep {
			keep[i] = true
		}
		return keep, keep
	}

	heights := make([]int, len(sources))
	adaptive := make([]bool, len(sources))
	for i, src := range sources {
		heights[i] = sourceHeight(src)
		adaptive[i] = kit.IsAdaptiveQuality(src.Quality)
	}

	switch mode {
	case QualityHighest:
		markHighest(heights, adaptive, keep)
	case QualityDataSaver:
		markDataSaver(heights, adaptive, keep)
	case QualityLowest:
		markLowest(sources, heights, adaptive, keep)
	default:
		markExplicit(heights, adaptive, mode, keep)
	}
	guaranteed := guaranteeResolver(sources, heights, keep)
	return keep, guaranteed
}

// sourceHeight prefers the declared field and falls back to the parsed label.
func sourceHeight(src provider.MediaSource) int {
	if src.Height > 0 {
		return src.Height
	}
	return kit.QualityHeight(src.Quality)
}

// markHighest ports ranking.KeepHighestVisible: full HD and better win, or
// the best known lower tier when nothing reaches full HD. Adaptive and
// fully-unknown sets stay visible because no source declares a real tier.
func markHighest(heights []int, adaptive []bool, keep []bool) {
	hasFullHD, allUnknown, bestLower := false, true, 0
	for i, height := range heights {
		if adaptive[i] {
			continue
		}
		if height > 0 {
			allUnknown = false
		}
		if height >= fullHDHeight {
			hasFullHD = true
		}
		if height > 0 && height < fullHDHeight && height > bestLower {
			bestLower = height
		}
	}
	for i, height := range heights {
		keep[i] = adaptive[i] || allUnknown || height >= fullHDHeight ||
			(!hasFullHD && bestLower > 0 && height == bestLower)
	}
}

// markDataSaver drops higher tiers only when a real 1080p-or-below tier
// exists, so an all-4K result set never disappears entirely.
func markDataSaver(heights []int, adaptive []bool, keep []bool) {
	lowerExists := false
	for i, height := range heights {
		if !adaptive[i] && height > 0 && height <= fullHDHeight {
			lowerExists = true
			break
		}
	}
	for i, height := range heights {
		keep[i] = adaptive[i] || !lowerExists || height <= fullHDHeight || height == 0
	}
}

// markLowest keeps the lowest known tier within each resolver.
func markLowest(sources []provider.MediaSource, heights []int, adaptive []bool, keep []bool) {
	minimums := make(map[string]int, len(sources))
	for i, src := range sources {
		if adaptive[i] || heights[i] <= 0 {
			continue
		}
		current, ok := minimums[src.Resolver]
		if !ok || heights[i] < current {
			minimums[src.Resolver] = heights[i]
		}
	}
	for i, src := range sources {
		minimum, ok := minimums[src.Resolver]
		keep[i] = adaptive[i] || (ok && heights[i] == minimum)
	}
}

// markExplicit selects the first ladder tier present across the whole set,
// mirroring the legacy download-quality filter's global fallback behaviour.
func markExplicit(heights []int, adaptive []bool, mode QualityMode, keep []bool) {
	for _, target := range qualityLadder(mode) {
		found := false
		for i, height := range heights {
			if !adaptive[i] && matchesQualityTier(height, target) {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		for i, height := range heights {
			if adaptive[i] || matchesQualityTier(height, target) {
				keep[i] = true
			}
		}
		return
	}
	// No ladder tier matched: every row is equally unknown, so keep them all.
	for i := range keep {
		keep[i] = true
	}
}

// qualityLadder returns the fallback order for an explicit quality mode.
func qualityLadder(mode QualityMode) []int {
	switch mode {
	case QualityUHD:
		return []int{2160, 1080, 720, 480}
	case QualityFHD:
		return []int{1080, 720, 2160, 480}
	case QualityHD:
		return []int{720, 1080, 480, 2160}
	case QualitySD:
		return []int{480, 720, 1080, 2160}
	default:
		return nil
	}
}

// matchesQualityTier mirrors the legacy target-resolution matching rules:
// 2160 means "2160p or better", 480 means "anything up to 480p", and the
// middle tiers absorb their neighbouring legacy resolutions.
func matchesQualityTier(height, target int) bool {
	switch target {
	case 2160:
		return height >= 2160
	case 1080:
		return height == 1080 || height == 1440
	case 720:
		return height == 720 || height == 576
	case 480:
		return height > 0 && height <= 480
	default:
		return height == target
	}
}

// guaranteeResolver restores one row per resolver that the tier filter
// removed completely, choosing that resolver's highest declared tier. It
// reports the restored indices so ranking can keep them visible without
// letting them win a preferred-provider promotion meant for tier matches.
func guaranteeResolver(sources []provider.MediaSource, heights []int, keep []bool) []bool {
	guaranteed := make([]bool, len(sources))
	kept := make(map[string]bool, len(sources))
	for i, src := range sources {
		if keep[i] {
			kept[src.Resolver] = true
		}
	}
	best := make(map[string]int, len(sources))
	for i, src := range sources {
		index, ok := best[src.Resolver]
		if !ok {
			best[src.Resolver] = i
			continue
		}
		if heights[i] > heights[index] {
			best[src.Resolver] = i
		}
	}
	for resolver, index := range best {
		if !kept[resolver] {
			keep[index] = true
			guaranteed[index] = true
		}
	}
	return guaranteed
}

// applyKeep rebuilds a source slice from per-source decisions, preserving
// order and returning nil for an empty input.
func applyKeep(sources []provider.MediaSource, keep []bool) []provider.MediaSource {
	if len(sources) == 0 {
		return nil
	}
	out := make([]provider.MediaSource, 0, len(sources))
	for i, src := range sources {
		if keep[i] {
			out = append(out, src)
		}
	}
	return out
}
