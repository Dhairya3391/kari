package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *modelImpl) renderMangaSources(width int) string {
	if m.selectedSeries == nil {
		return "Select a manga source"
	}
	st := NewStyles(m.currentAccent())
	rows := []string{st.Bold.Render(m.selectedSeries.Title), "Choose a source", ""}
	for i, source := range m.selectedSeries.SourceOptions {
		name := m.registry.DisplayName(source.Provider)
		label := fmt.Sprintf("  %s", name)
		if i == m.mangaSourceIndex {
			label = st.Cursor.Render("› ") + st.Current.Render(name)
		}
		rows = append(rows, label)
	}
	rows = append(rows, "", st.Dim.Render("↑/↓ select · Enter open · Esc back"))
	return lipgloss.NewStyle().Width(width).Render(strings.Join(rows, "\n"))
}
