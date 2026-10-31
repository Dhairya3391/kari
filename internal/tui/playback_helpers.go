package tui

import (
	"sort"
	"strings"

	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/ranking"
)

func (m *modelImpl) rankAndSelectSources() {
	if m.resolved == nil || len(m.resolved.Playback) == 0 {
		m.rankedSources = nil
		m.previewSelectedIndex = 0
		return
	}

	sources := m.filterSourcesByLanguage(m.resolved.Playback)
	// Highest shows FHD/4K rows only, falling back to the best available
	// tier when nothing reaches FHD. Other modes order every row.
	if m.qualityMode == qualityHighest {
		sources = ranking.KeepHighestVisible(sources)
	}
	scored := make([]ranking.ScoredSource, len(sources))
	for i, s := range sources {
		scored[i] = ranking.ScoredSource{
			Source:        s,
			OriginalIndex: i,
		}
	}
	scored = groupSourcesByProvider(scored, m.qualityMode, m.appMode)
	m.rankedSources = hardsubFirst(m.appMode, m.qualityMode, !m.disableAnimeSubtitles, scored)
	m.previewSelectedIndex = 0
}

func groupSourcesByProvider(sources []ranking.ScoredSource, qualityMode int, mode provider.ContentType) []ranking.ScoredSource {
	if len(sources) <= 1 {
		return sources
	}
	groups := make(map[string][]ranking.ScoredSource)
	var provOrder []string
	for _, s := range sources {
		res := s.Source.Resolver
		if _, exists := groups[res]; !exists {
			provOrder = append(provOrder, res)
		}
		groups[res] = append(groups[res], s)
	}

	out := make([]ranking.ScoredSource, 0, len(sources))
	for _, prov := range provOrder {
		group := groups[prov]
		sort.SliceStable(group, func(i, j int) bool {
			srcI, srcJ := group[i].Source, group[j].Source
			if challengedI, challengedJ := provider.ChallengedHost(srcI.URL), provider.ChallengedHost(srcJ.URL); challengedI != challengedJ {
				return !challengedI
			}
			resI := ranking.ParseResolution(srcI.Quality)
			resJ := ranking.ParseResolution(srcJ.Quality)
			if resI != resJ {
				return qualitySortRank(resI, qualityMode) < qualitySortRank(resJ, qualityMode)
			}
			if mode != provider.ModeAnime {
				langI := strings.ToLower(strings.TrimSpace(group[i].Source.Language))
				langJ := strings.ToLower(strings.TrimSpace(group[j].Source.Language))
				if (langI == "" || langI == "en") && (langJ != "" && langJ != "en") {
					return true
				}
				if (langJ == "" || langJ == "en") && (langI != "" && langI != "en") {
					return false
				}
			}
			return false
		})
		out = append(out, group...)
	}
	return out
}

// qualitySortRank maps a parsed resolution to an ascending sort key for the
// active quality mode. Lower keys sort first. Rows without a parseable
// resolution always sort last so "unknown" never outranks a real height.
func qualitySortRank(resolution, qualityMode int) int {
	const unknownResolution = 1 << 30

	switch qualityMode {
	case qualityLowest:
		if resolution == 0 {
			return unknownResolution
		}
		return resolution
	case qualityDataSaver:
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

func (m *modelImpl) filterSourcesByLanguage(sources []provider.MediaSource) []provider.MediaSource {
	if m.appMode == provider.ModeAnime || len(m.languageFilter) == 0 {
		return sources
	}
	var kept []provider.MediaSource
	for _, s := range sources {
		if m.sourceLanguageEnabled(s) {
			kept = append(kept, s)
		}
	}
	return kept
}

func (m *modelImpl) sourceLanguageEnabled(src provider.MediaSource) bool {
	raw := strings.TrimSpace(src.Language)
	if raw == "" {
		if idx := strings.Index(src.Quality, "("); idx >= 0 {
			if end := strings.Index(src.Quality[idx:], ")"); end >= 0 {
				tag := strings.TrimSpace(src.Quality[idx+1 : idx+end])
				if code := lang.Normalize(tag); code != "" {
					raw = code
				}
			}
		}
		if raw == "" {
			// Sources with no explicit language tag are considered primary/original (English).
			return m.languageEnabled("en") && m.languageEnabled("English")
		}
	}
	code := lang.Normalize(raw)
	display := lang.Name(code)
	for _, k := range []string{raw, code, display} {
		if k == "" {
			continue
		}
		if !m.languageEnabled(k) || !m.languageEnabled(strings.ToLower(k)) {
			return false
		}
	}
	return true
}

// hardsubFirst stable-sorts hard-subtitled rows before soft-subtitled rows
// within each provider itself, preserving the overall provider arrival order.
func hardsubFirst(mode provider.ContentType, qualityMode int, subsEnabled bool, sources []ranking.ScoredSource) []ranking.ScoredSource {
	if mode != provider.ModeAnime || !subsEnabled || len(sources) <= 1 {
		return sources
	}
	if qualityMode != qualityAll && qualityMode != qualityHighest {
		return sources
	}
	out := make([]ranking.ScoredSource, 0, len(sources))
	i := 0
	for i < len(sources) {
		prov := sources[i].Source.Resolver
		j := i + 1
		for j < len(sources) && sources[j].Source.Resolver == prov {
			j++
		}
		var top, rest []ranking.ScoredSource
		for _, s := range sources[i:j] {
			if strings.EqualFold(strings.TrimSpace(s.Source.SubType), provider.SubTypeHard) &&
				!provider.ChallengedHost(s.Source.URL) {
				top = append(top, s)
			} else {
				rest = append(rest, s)
			}
		}
		out = append(out, top...)
		out = append(out, rest...)
		i = j
	}
	return out
}

// refreshRanking recomputes the ranked order from the current Playback
// snapshot. Providers stream in progressively, so every merge must
// re-rank, or the first snapshot's order sticks and later arrivals never
// appear in Preview. A cursor sitting on the auto-selected top row
// (index 0) tracks the new top pick; a cursor the user moved is kept on
// the same source URL.
func (m *modelImpl) refreshRanking() {
	trackBest := m.previewSelectedIndex == 0
	selectedURL := ""
	if !trackBest {
		if src, ok := m.selectedPlaybackSource(); ok {
			selectedURL = src.URL
		}
	}
	m.rankAndSelectSources()
	if !trackBest && selectedURL != "" {
		for i, s := range m.rankedSources {
			if s.Source.URL == selectedURL {
				m.previewSelectedIndex = i
				return
			}
		}
	}
	// On autoplay, if the user hasn't manually moved the cursor, prefer the same
	// provider/resolver that the previous episode played from if available.
	if m.autoPlayAfterResolve && m.prevSourceResolver != "" && !m.manualPlaybackSelected {
		bestIdx := -1
		for i, s := range m.rankedSources {
			if strings.EqualFold(s.Source.Resolver, m.prevSourceResolver) {
				bestIdx = i
				break
			}
		}
		if bestIdx >= 0 {
			m.previewSelectedIndex = bestIdx
		}
	}
}

func (m *modelImpl) selectedPlaybackSource() (provider.MediaSource, bool) {
	if len(m.rankedSources) == 0 {
		if m.resolved != nil && len(m.resolved.Playback) > 0 {
			return m.resolved.Playback[0], true
		}
		return provider.MediaSource{}, false
	}
	if m.previewSelectedIndex < 0 || m.previewSelectedIndex >= len(m.rankedSources) {
		return m.rankedSources[0].Source, true
	}
	return m.rankedSources[m.previewSelectedIndex].Source, true
}

func (m *modelImpl) orderedPlaybackSources() []provider.MediaSource {
	if len(m.rankedSources) == 0 {
		if m.resolved == nil {
			return nil
		}
		return m.resolved.Playback
	}

	total := len(m.rankedSources)
	start := m.previewSelectedIndex
	if start < 0 || start >= total {
		start = 0
	}

	out := make([]provider.MediaSource, 0, total)
	for i := range total {
		idx := (start + i) % total
		out = append(out, m.rankedSources[idx].Source)
	}
	return out
}
