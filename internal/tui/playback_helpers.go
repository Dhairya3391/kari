package tui

import "kari/internal/model"

func (m *modelImpl) playbackSelectionIndex() int {
	if m.resolved == nil || len(m.resolved.Playback) == 0 {
		return 0
	}
	if m.selectedPlayback < 0 || m.selectedPlayback >= len(m.resolved.Playback) {
		return 0
	}
	return m.selectedPlayback
}

func (m *modelImpl) selectedPlaybackSource() (model.PlaybackSource, bool) {
	if m.resolved == nil || len(m.resolved.Playback) == 0 {
		return model.PlaybackSource{}, false
	}
	return m.resolved.Playback[m.playbackSelectionIndex()], true
}

func (m *modelImpl) orderedPlaybackSources() []model.PlaybackSource {
	if m.resolved == nil || len(m.resolved.Playback) == 0 {
		return nil
	}
	idx := m.playbackSelectionIndex()

	out := make([]model.PlaybackSource, 0, len(m.resolved.Playback))
	out = append(out, m.resolved.Playback[idx:]...)
	out = append(out, m.resolved.Playback[:idx]...)
	return out
}
