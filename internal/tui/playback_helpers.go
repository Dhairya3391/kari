package tui

import (
	"strings"

	"kari/internal/model"
	"kari/internal/service"
)

func (m *modelImpl) playbackSelectionIndex() int {
	for _, idx := range m.filteredPlayback() {
		if idx == m.selectedPlayback {
			return idx
		}
	}
	return -1
}

func (m *modelImpl) selectedPlaybackSource() (model.PlaybackSource, bool) {
	idx := m.playbackSelectionIndex()
	if idx < 0 {
		return model.PlaybackSource{}, false
	}
	return m.resolved.Playback[idx], true
}

func (m *modelImpl) orderedPlaybackSources() []model.PlaybackSource {
	if m.resolved == nil {
		return nil
	}

	indices := m.filteredPlayback()
	if len(indices) == 0 {
		return nil
	}

	selected := m.playbackSelectionIndex()
	position := 0
	for i, idx := range indices {
		if idx == selected {
			position = i
			break
		}
	}

	out := make([]model.PlaybackSource, 0, len(indices))
	for offset := range indices {
		idx := indices[(position+offset)%len(indices)]
		out = append(out, m.resolved.Playback[idx])
	}
	return out
}

func (m *modelImpl) ensurePlaybackSelection() {
	if m.playbackSelectionIndex() >= 0 {
		return
	}

	indices := m.filteredPlayback()
	if len(indices) == 0 {
		return
	}

	// Prefer source matching both language and quality from previous selection
	if m.prevSourceLanguage != "" || m.prevSourceQuality > 0 {
		// Try exact match: same language + same quality
		for _, idx := range indices {
			src := m.resolved.Playback[idx]
			if m.prevSourceLanguage != "" && !strings.EqualFold(src.Language, m.prevSourceLanguage) {
				continue
			}
			if m.prevSourceQuality > 0 && service.SourceQuality(src.Label) != m.prevSourceQuality {
				continue
			}
			m.selectedPlayback = idx
			return
		}
		// Try language-only match
		if m.prevSourceLanguage != "" {
			for _, idx := range indices {
				if strings.EqualFold(m.resolved.Playback[idx].Language, m.prevSourceLanguage) {
					m.selectedPlayback = idx
					return
				}
			}
		}
		// Try quality-only match
		if m.prevSourceQuality > 0 {
			for _, idx := range indices {
				if service.SourceQuality(m.resolved.Playback[idx].Label) == m.prevSourceQuality {
					m.selectedPlayback = idx
					return
				}
			}
		}
	}

	m.selectedPlayback = indices[0]
}
