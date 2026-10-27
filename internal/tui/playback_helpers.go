package tui

import (
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

	m.rankedSources = hardsubFirst(m.appMode, m.qualityMode, !m.disableAnimeSubtitles, scored)
	m.previewSelectedIndex = 0
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
	if len(kept) > 0 {
		return kept
	}
	return sources
}

func (m *modelImpl) sourceLanguageEnabled(src provider.MediaSource) bool {
	raw := strings.TrimSpace(src.Language)
	if raw == "" {
		// Sources with no explicit language tag are considered primary/original (English).
		return m.languageEnabled("en") && m.languageEnabled("English")
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
