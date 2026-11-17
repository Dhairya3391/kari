package filter

import (
	"slices"
	"strings"
	"time"

	"kari/internal/provider"
)

// healthWindow is the recency window within which a provider failure still
// demotes its sources, matching provider.Health.FailedProviders.
const healthWindow = 10 * time.Minute

// RankKey holds a source's integer ordering signals. Prepare compares fields
// in declaration order, and a larger value always ranks higher. Using only
// integers keeps ordering stable and free of floating-point tie surprises.
type RankKey struct {
	// Playable separates challenged hosts from sources that play right now.
	Playable int
	// Health demotes rows from providers that failed recently.
	Health int
	// Demote sinks weak fallback-only lanes below ordinary sources.
	Demote int
	// Audio rewards a matching or dual sub/dub track.
	Audio int
	// Subtitle rewards matching subtitle tracks and preferred hardsubs.
	Subtitle int
	// Quality ranks by the effective resolution for the chosen mode.
	Quality int
	// SubKind ranks declared anime subtitle kinds ahead of unknown ones.
	SubKind int
	// Provider rewards the user's preferred resolver.
	Provider int
	// Order preserves the original position as the final tiebreak.
	Order int
}

// RankedSource is a source paired with its precomputed key and its position
// in the original input slice.
type RankedSource struct {
	Source        provider.MediaSource
	Key           RankKey
	OriginalIndex int
	// Guaranteed marks a row kept only by the per-resolver quality guarantee.
	// It stays visible but must not be promoted by preferred-provider grouping
	// ahead of rows that actually match the selected tier.
	Guaranteed bool
}

// Prepare hard-filters sources by quality and language, then scores and
// sorts the survivors according to Preferences. failed maps resolver names to
// their last failure time; now makes the health window deterministic in tests.
func Prepare(sources []provider.MediaSource, prefs Preferences, mode provider.ContentType, failed map[string]time.Time, now time.Time) []RankedSource {
	if len(sources) == 0 {
		return nil
	}
	// Filter by language first so tier selection and the per-resolver
	// guarantee operate only on allowed sources. Filtering after tier
	// selection can drop a resolver's only tier match and leave it with no
	// visible row even when another allowed tier exists.
	languageKeep := languageFilterKeep(sources, mode, prefs.Audio)
	eligible := make([]provider.MediaSource, 0, len(sources))
	eligibleIndex := make([]int, 0, len(sources))
	for i, src := range sources {
		if languageKeep[i] {
			eligible = append(eligible, src)
			eligibleIndex = append(eligibleIndex, i)
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	tierKeep, guaranteed := selectTierKeep(eligible, prefs.Quality)

	ranked := make([]RankedSource, 0, len(eligible))
	for j, src := range eligible {
		if !tierKeep[j] {
			continue
		}
		i := eligibleIndex[j]
		ranked = append(ranked, RankedSource{
			Source:        src,
			Key:           rankSource(src, prefs, mode, failed, now, i),
			OriginalIndex: i,
			Guaranteed:    guaranteed[j],
		})
	}
	if len(ranked) == 0 {
		return nil
	}
	slices.SortStableFunc(ranked, func(a, b RankedSource) int {
		return compareRankKeys(a.Key, b.Key)
	})
	return ranked
}

// GroupByProvider groups ranked rows by resolver while preserving the order
// in which resolvers were first seen. The preferred resolver's whole group is
// moved to the front; rows inside each group keep their existing order.
func GroupByProvider(ranked []RankedSource, preferred string) []RankedSource {
	if len(ranked) <= 1 {
		return ranked
	}
	groups := make(map[string][]RankedSource, len(ranked))
	order := make([]string, 0, len(ranked))
	for _, row := range ranked {
		name := row.Source.Resolver
		if _, ok := groups[name]; !ok {
			order = append(order, name)
		}
		groups[name] = append(groups[name], row)
	}

	if preferred != "" {
		target := strings.TrimSpace(preferred)
		for i, name := range order {
			if !strings.EqualFold(name, target) {
				continue
			}
			// A group with no tier-matching row survived only via the
			// per-resolver guarantee. Promoting it would put a lower-quality
			// row ahead of a real tier match, which is wrong for downloads and
			// for the default preview pick.
			hasTierMatch := false
			for _, row := range groups[name] {
				if !row.Guaranteed {
					hasTierMatch = true
					break
				}
			}
			if !hasTierMatch {
				break
			}
			reordered := make([]string, 0, len(order))
			reordered = append(reordered, name)
			reordered = append(reordered, order[:i]...)
			reordered = append(reordered, order[i+1:]...)
			order = reordered
			break
		}
	}

	out := make([]RankedSource, 0, len(ranked))
	for _, name := range order {
		out = append(out, groups[name]...)
	}
	return out
}

func rankSource(src provider.MediaSource, prefs Preferences, mode provider.ContentType, failed map[string]time.Time, now time.Time, index int) RankKey {
	playable, demote := 0, 0
	if provider.ChallengedHost(src.URL) {
		playable = -1
	}
	if src.FallbackOnly {
		demote = -1
	}
	return RankKey{
		Playable: playable,
		Health:   healthRank(src.Resolver, failed, now),
		Demote:   demote,
		Audio:    audioMatchRank(src, prefs.Track, mode),
		Subtitle: SubtitleScore(src, prefs.Subtitles, mode),
		Quality:  qualityRank(sourceHeight(src), prefs.Quality),
		SubKind:  subKindRank(src, mode),
		Provider: providerRank(src.Resolver, prefs.PreferredProvider),
		Order:    -index,
	}
}

// compareRankKeys applies the ranking hierarchy as a cascade of integer
// differences, returning at the first non-zero field.
func compareRankKeys(a, b RankKey) int {
	if a.Playable != b.Playable {
		return b.Playable - a.Playable
	}
	if a.Health != b.Health {
		return b.Health - a.Health
	}
	if a.Demote != b.Demote {
		return b.Demote - a.Demote
	}
	if a.Audio != b.Audio {
		return b.Audio - a.Audio
	}
	if a.Subtitle != b.Subtitle {
		return b.Subtitle - a.Subtitle
	}
	if a.Quality != b.Quality {
		return b.Quality - a.Quality
	}
	if a.SubKind != b.SubKind {
		return b.SubKind - a.SubKind
	}
	if a.Provider != b.Provider {
		return b.Provider - a.Provider
	}
	return b.Order - a.Order
}

func healthRank(resolver string, failed map[string]time.Time, now time.Time) int {
	if len(failed) == 0 {
		return 0
	}
	last, ok := failureTime(resolver, failed)
	if !ok {
		return 0
	}
	reference := now
	if reference.IsZero() {
		reference = time.Now()
	}
	if reference.Sub(last) < healthWindow {
		return -1
	}
	return 0
}

// failureTime tolerates the casing and whitespace differences between a
// source resolver and the names recorded by the health circuit breaker.
func failureTime(resolver string, failed map[string]time.Time) (time.Time, bool) {
	if last, ok := failed[resolver]; ok {
		return last, true
	}
	trimmed := strings.ToLower(strings.TrimSpace(resolver))
	if last, ok := failed[trimmed]; ok {
		return last, true
	}
	for key, last := range failed {
		if strings.EqualFold(key, resolver) {
			return last, true
		}
	}
	return time.Time{}, false
}

// audioMatchRank scores the typed Audio.Track. A dual track and a dual
// preference both match either sub or dub, so dual sources stay competitive
// for every anime audio choice.
func audioMatchRank(src provider.MediaSource, preferred provider.AudioTrack, mode provider.ContentType) int {
	target := preferred
	if target == provider.TrackUnknown && mode == provider.ModeAnime {
		target = provider.TrackSub
	}
	track := declaredTrack(src)
	if track == provider.TrackDual {
		return 2
	}
	if target == provider.TrackUnknown {
		return 0
	}
	if target == provider.TrackDual && (track == provider.TrackSub || track == provider.TrackDub) {
		return 2
	}
	if track == target {
		return 2
	}
	if track == provider.TrackUnknown {
		return 0
	}
	return -1
}

// declaredTrack uses the typed track when present and falls back to the
// legacy language/quality/resolver labels before giving up.
func declaredTrack(src provider.MediaSource) provider.AudioTrack {
	if src.Audio.Track != provider.TrackUnknown {
		return src.Audio.Track
	}
	haystack := strings.ToLower(src.Language + " " + src.Quality + " " + src.Resolver)
	switch {
	case strings.Contains(haystack, provider.AudioDub):
		return provider.TrackDub
	case strings.Contains(haystack, provider.AudioSub), src.SubType == provider.SubTypeHard:
		return provider.TrackSub
	default:
		return provider.TrackUnknown
	}
}

// unknownQualityRank is the worst possible rank in Lowest mode. Without it,
// an untiered row (height 0) would score 0 and outrank every known tier whose
// rank is negative, making it the default pick for a "lowest quality" user.
const unknownQualityRank = -(1 << 30)

// qualityRank converts a resolution into a mode-aware ordering value. Lower
// quality and data-saver modes invert or penalize the raw height.
func qualityRank(height int, mode QualityMode) int {
	switch mode {
	case QualityLowest:
		if height <= 0 {
			return unknownQualityRank
		}
		return -height
	case QualityDataSaver:
		if height > fullHDHeight {
			return height - 100000
		}
		return height
	case QualityFHD:
		return -absInt(height - 1080)
	case QualityHD:
		return -absInt(height - 720)
	case QualitySD:
		return -absInt(height - 480)
	default:
		return height
	}
}

func subKindRank(src provider.MediaSource, mode provider.ContentType) int {
	if mode != provider.ModeAnime || len(src.Subtitles) > 0 {
		return 0
	}
	switch src.SubType {
	case provider.SubTypeHard:
		return 2
	case provider.SubTypeSoft:
		return 0
	default:
		return 1
	}
}

func providerRank(resolver, preferred string) int {
	if preferred == "" {
		return 0
	}
	if strings.EqualFold(strings.TrimSpace(resolver), strings.TrimSpace(preferred)) {
		return 1
	}
	return 0
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
