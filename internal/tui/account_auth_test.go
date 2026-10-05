package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func TestAniListAuthPromptAndEsc(t *testing.T) {
	m := unavailableTestModel()
	m.authInput = textinput.New()
	m.width = 100
	m.height = 24
	m.activeView = viewSettings
	m.settingsCategory = CategoryAccounts
	m.settingsIndex = 0

	// Trigger AniList auth
	m.anilistAuthURL = "https://anilist.co/api/v2/oauth/authorize?client_id=40882&response_type=token"
	m.authInput.Focus()
	m.authInput.SetValue("")

	data := m.collectSettingsData(m.width, m.height)
	if !data.AniListAuthActive {
		t.Fatal("expected data.AniListAuthActive to be true")
	}

	rendered := RenderSettingsScreen(data)
	if !strings.Contains(rendered, "AniList Authorization Token") {
		t.Fatalf("expected rendered settings to show 'AniList Authorization Token', got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Paste code and press Enter") {
		t.Fatalf("expected rendered settings to show paste instructions, got:\n%s", rendered)
	}

	// Pressing Esc cancels auth
	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := mdl.(*modelImpl)
	if m2.anilistAuthURL != "" {
		t.Fatalf("expected anilistAuthURL to be cleared on Esc, got %q", m2.anilistAuthURL)
	}
}
