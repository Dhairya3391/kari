package tui

import "kari/internal/model"

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
	if len(indices) > 0 {
		m.selectedPlayback = indices[0]
	}
}
