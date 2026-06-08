package tui

import (
	"strings"
)

func (m *modelImpl) selectedPlayerName() string {
	if len(m.availablePlayers) == 0 {
		return ""
	}
	idx := m.selectedPlayer
	if idx < 0 || idx >= len(m.availablePlayers) {
		idx = m.defaultPlayerIndex()
	}
	if idx < 0 || idx >= len(m.availablePlayers) {
		return ""
	}
	return strings.TrimSpace(m.availablePlayers[idx])
}

func (m *modelImpl) defaultPlayerIndex() int {
	if len(m.availablePlayers) == 0 {
		return 0
	}
	def := m.players.DefaultPlayer()
	for i, name := range m.availablePlayers {
		if strings.EqualFold(strings.TrimSpace(name), def) {
			return i
		}
	}
	return 0
}
