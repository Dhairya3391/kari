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

func TestTraktDeviceAuthPromptAndCompletion(t *testing.T) {
	m := unavailableTestModel()
	m.width = 100
	m.height = 24
	m.activeView = viewSettings
	m.settingsCategory = CategoryAccounts
	m.settingsIndex = 1

	// Simulate traktCodeMsg arrival
	msg := traktCodeMsg{
		userCode:        "ABCD-1234",
		verificationURL: "https://trakt.tv/activate",
		deviceCode:      "dev-code-123",
		interval:        5,
		expiresIn:       600,
	}

	mdl, _ := m.Update(msg)
	m2 := mdl.(*modelImpl)

	if !m2.traktAuthActive {
		t.Fatal("expected traktAuthActive to be true")
	}
	if m2.traktUserCode != "ABCD-1234" {
		t.Fatalf("expected traktUserCode to be 'ABCD-1234', got %q", m2.traktUserCode)
	}

	data := m2.collectSettingsData(m2.width, m2.height)
	rendered := RenderSettingsScreen(data)
	if !strings.Contains(rendered, "ABCD-1234") {
		t.Fatalf("expected rendered screen to contain user code 'ABCD-1234', got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "https://trakt.tv/activate") {
		t.Fatalf("expected rendered screen to contain verification URL, got:\n%s", rendered)
	}

	// Simulate auth completion
	mdl3, _ := m2.Update(authDoneMsg{service: "Trakt", err: nil})
	m3 := mdl3.(*modelImpl)
	if m3.traktAuthActive {
		t.Fatal("expected traktAuthActive to be false after completion")
	}
	if m3.statusText == "" || !strings.Contains(m3.statusText, "Trakt connected") {
		t.Fatalf("expected success status, got %q", m3.statusText)
	}
}
