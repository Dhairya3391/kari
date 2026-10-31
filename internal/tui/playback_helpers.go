package tui

import (
	"strings"

	"kari/internal/provider"
	"kari/internal/service"
)

func (m *modelImpl) rankAndSelectSources() {
	if m.resolved == nil || len(m.resolved.Playback) == 0 {
		m.rankedSources = nil
		m.previewSelectedIndex = 0
		return
	}

	m.rankedSources = service.PreparePlaybackSources(m.resolved.Playback, service.PlaybackOptions{
		Mode:                  m.appMode,
		QualityMode:           m.qualityMode,
		EnabledAudioLanguages: m.languageFilter,
		SubtitlesEnabled:      !m.disableAnimeSubtitles,
	})
	m.previewSelectedIndex = 0
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
