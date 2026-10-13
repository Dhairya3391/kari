package tui

import (
	"strings"

	"kari/internal/provider"
	"kari/internal/ranking"
)

func (m *modelImpl) orderedEnabledLanguages() []string {
	allLangs := m.availableLanguages()
	var enabled []string
	for _, l := range allLangs {
		if m.languageFilter[l.Code] || m.languageFilter[l.Display] {
			enabled = append(enabled, l.Display)
		}
	}
	return enabled
}

func (m *modelImpl) rankAndSelectSources() {
	if m.resolved == nil || len(m.resolved.Playback) == 0 {
		m.rankedSources = nil
		m.previewSelectedIndex = 0
		return
	}

	sources := m.resolved.Playback
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
	if trackBest || selectedURL == "" {
		return
	}
	for i, s := range m.rankedSources {
		if s.Source.URL == selectedURL {
			m.previewSelectedIndex = i
			return
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
