package tui

import (
	"strings"

	"kari/internal/filter"
	"kari/internal/provider"
	"kari/internal/service"
)

func (m *modelImpl) rankAndSelectSources() {
	if m.resolved == nil || len(m.resolved.Playback) == 0 {
		m.rankedSources = nil
		m.previewSelectedIndex = 0
		return
	}

	prefs := m.filterScope.For(m.appMode)
	ranked := service.PreparePlaybackSources(m.resolved.Playback, service.PlaybackOptions{
		Mode:                         m.appMode,
		Preferences:                  &prefs,
		PreferredProvider:            m.preferredProviderForMode(m.appMode),
		KeepPreferredSubtitleSources: true,
	})
	if ranked == nil {
		ranked = []filter.RankedSource{}
	}
	m.rankedSources = ranked
	m.previewSelectedIndex = 0
}

// refreshRanking refreshes filtered, preferred-first sources. A manual or
// active playback selection stays on the same transport as sources arrive.
func (m *modelImpl) refreshRanking() {
	trackBest := m.previewSelectedIndex == 0 && !m.manualPlaybackSelected && m.playOpID == 0
	selectedID := ""
	if !trackBest {
		if src, ok := m.selectedPlaybackSource(); ok {
			selectedID = src.TransportIdentity()
		}
	}
	m.rankAndSelectSources()
	if !trackBest && selectedID != "" {
		for i, s := range m.rankedSources {
			if s.Source.TransportIdentity() == selectedID {
				m.previewSelectedIndex = i
				return
			}
		}
	}
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
	if m.rankedSources == nil {
		if m.resolved != nil && len(m.resolved.Playback) > 0 {
			return m.resolved.Playback[0], true
		}
		return provider.MediaSource{}, false
	}
	if len(m.rankedSources) == 0 {
		return provider.MediaSource{}, false
	}
	if m.previewSelectedIndex < 0 || m.previewSelectedIndex >= len(m.rankedSources) {
		return m.rankedSources[0].Source, true
	}
	return m.rankedSources[m.previewSelectedIndex].Source, true
}

func (m *modelImpl) orderedPlaybackSources() []provider.MediaSource {
	if m.rankedSources == nil {
		if m.resolved == nil {
			return nil
		}
		return m.resolved.Playback
	}

	total := len(m.rankedSources)
	if total == 0 {
		return nil
	}
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
