package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/provider"
)

func TestAudioLanguagePickerWindowing(t *testing.T) {
	st := NewStyles(lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#7571F9"})

	// 40 languages as seen in real providers
	langs := []string{
		"English", "Hindi", "Tamil", "Telugu", "Spanish", "French", "Portuguese",
		"German", "Italian", "Arabic", "Russian", "Japanese", "Korean", "Chinese",
		"Indonesian", "Turkish", "Malayalam", "Kannada", "Bengali", "Marathi",
		"Gujarati", "Punjabi", "Urdu", "Thai", "Vietnamese", "Malay", "Ukrainian",
		"Polish", "Dutch", "Swedish", "Danish", "Norwegian", "Finnish", "Czech",
		"Greek", "Hebrew", "Persian", "Romanian", "Hungarian", "Filipino",
	}

	enabled := map[string]bool{"English": true, "Hindi": true, "Japanese": true}

	// 1. Top of list: cursor on English (0), height = 15
	dataTop := SettingsData{
		AudioPickerOpen:  true,
		AllLanguages:     langs,
		EnabledLanguages: enabled,
		PickerIndex:      0,
		Height:           15,
	}
	renderedTop := renderAudioLanguagePicker(dataTop, st)
	linesTop := strings.Split(renderedTop, "\n")
	if len(linesTop) > 15 {
		t.Fatalf("rendered lines (%d) exceeded available height (15)", len(linesTop))
	}
	if !strings.Contains(renderedTop, "▌ [✓] English") {
		t.Fatalf("cursor not visible on English at top of list:\n%s", renderedTop)
	}
	if !strings.Contains(renderedTop, "↓") || !strings.Contains(renderedTop, "more") {
		t.Fatalf("expected downward scroll hint at top of list:\n%s", renderedTop)
	}
	if strings.Contains(renderedTop, "↑") {
		t.Fatalf("did not expect upward scroll hint at top of list:\n%s", renderedTop)
	}

	// 2. Bottom of list: cursor on Persian (36), height = 15
	dataBottom := SettingsData{
		AudioPickerOpen:  true,
		AllLanguages:     langs,
		EnabledLanguages: enabled,
		PickerIndex:      36, // Persian
		Height:           15,
	}
	renderedBottom := renderAudioLanguagePicker(dataBottom, st)
	linesBottom := strings.Split(renderedBottom, "\n")
	if len(linesBottom) > 15 {
		t.Fatalf("rendered lines (%d) exceeded available height (15)", len(linesBottom))
	}
	if !strings.Contains(renderedBottom, "▌ [ ] Persian") {
		t.Fatalf("cursor not visible on Persian at bottom of list:\n%s", renderedBottom)
	}
	if !strings.Contains(renderedBottom, "↑") || !strings.Contains(renderedBottom, "more") {
		t.Fatalf("expected upward scroll hint at bottom of list:\n%s", renderedBottom)
	}

	// 3. Middle of list: cursor on Marathi (19), height = 15
	dataMid := SettingsData{
		AudioPickerOpen:  true,
		AllLanguages:     langs,
		EnabledLanguages: enabled,
		PickerIndex:      19, // Marathi
		Height:           15,
	}
	renderedMid := renderAudioLanguagePicker(dataMid, st)
	linesMid := strings.Split(renderedMid, "\n")
	if len(linesMid) > 15 {
		t.Fatalf("rendered lines (%d) exceeded available height (15)", len(linesMid))
	}
	if !strings.Contains(renderedMid, "▌ [ ] Marathi") {
		t.Fatalf("cursor not visible on Marathi in middle of list:\n%s", renderedMid)
	}
	if !strings.Contains(renderedMid, "↑") || !strings.Contains(renderedMid, "more") {
		t.Fatalf("expected upward scroll hint in middle of list:\n%s", renderedMid)
	}
	if !strings.Contains(renderedMid, "↓") || !strings.Contains(renderedMid, "more") {
		t.Fatalf("expected downward scroll hint in middle of list:\n%s", renderedMid)
	}

	// 4. Zero height: renders all without panic
	dataZero := SettingsData{
		AudioPickerOpen:  true,
		AllLanguages:     langs,
		EnabledLanguages: enabled,
		PickerIndex:      0,
		Height:           0,
	}
	renderedZero := renderAudioLanguagePicker(dataZero, st)
	for _, l := range langs {
		if !strings.Contains(renderedZero, l) {
			t.Fatalf("expected language %s in unconstrained rendering", l)
		}
	}
}

func TestAudioPickerKeyboardNavigation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestSwitchModel()
	m.height = 20

	var fakeLangs []provider.AudioLanguage
	for i := range 30 {
		code := fmt.Sprintf("l%d", i)
		name := fmt.Sprintf("Language %d", i)
		fakeLangs = append(fakeLangs, provider.AudioLanguage{Code: code, Display: name})
	}
	m.registry.Register(&langProviderFake{
		fakeProviderForSwitch: fakeProviderForSwitch{name: "p", modes: []provider.Mode{{Name: provider.ModeMovies}}},
		langs:                 fakeLangs,
	})

	m.settingsCategory = CategoryLanguages
	m.settingsIndex = 0
	m.audioPickerOpen = true
	m.audioPickerIndex = 0

	// 1. Move down with "j" / "down"
	mdl, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = mdl.(*modelImpl)
	if m.audioPickerIndex != 1 {
		t.Fatalf("expected audioPickerIndex 1, got %d", m.audioPickerIndex)
	}
	// Regression check: pressing down in picker MUST NOT modify background settings row index
	if m.settingsIndex != 0 {
		t.Fatalf("expected settingsIndex to remain 0, got %d (fallthrough bug!)", m.settingsIndex)
	}

	// 2. Move up with "k"
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	m = mdl.(*modelImpl)
	if m.audioPickerIndex != 0 {
		t.Fatalf("expected audioPickerIndex 0, got %d", m.audioPickerIndex)
	}

	// 3. Move up at boundary stays 0
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	m = mdl.(*modelImpl)
	if m.audioPickerIndex != 0 {
		t.Fatalf("expected audioPickerIndex to stay 0 at top, got %d", m.audioPickerIndex)
	}

	// 4. End / "G" jumps to bottom
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m = mdl.(*modelImpl)
	if m.audioPickerIndex != len(fakeLangs)-1 {
		t.Fatalf("expected audioPickerIndex %d, got %d", len(fakeLangs)-1, m.audioPickerIndex)
	}

	// 5. Home / "g" jumps to top
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m = mdl.(*modelImpl)
	if m.audioPickerIndex != 0 {
		t.Fatalf("expected audioPickerIndex 0, got %d", m.audioPickerIndex)
	}

	// 6. Page down jumps by pageStep
	step := m.pageStep()
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = mdl.(*modelImpl)
	if m.audioPickerIndex != step {
		t.Fatalf("expected audioPickerIndex %d after page down, got %d", step, m.audioPickerIndex)
	}

	// 7. Page up jumps back
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = mdl.(*modelImpl)
	if m.audioPickerIndex != 0 {
		t.Fatalf("expected audioPickerIndex 0 after page up, got %d", m.audioPickerIndex)
	}

	// 8. Unhandled keys (tab, numbers, arrow keys) are trapped and don't leak to background settings
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyTab})
	m = mdl.(*modelImpl)
	if m.settingsCategory != CategoryLanguages {
		t.Fatalf("tab leaked: expected CategoryLanguages, got %v", m.settingsCategory)
	}

	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m = mdl.(*modelImpl)
	if m.settingsCategory != CategoryLanguages {
		t.Fatalf("category jump '3' leaked: expected CategoryLanguages, got %v", m.settingsCategory)
	}

	// 9. Enter closes picker
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if m.audioPickerOpen {
		t.Fatalf("expected audioPickerOpen to be false after enter")
	}
}

func TestAudioPickerKeyBindings(t *testing.T) {
	m := newTestSwitchModel()
	m.activeView = viewSettings
	m.audioPickerOpen = true

	bindings := m.activeKeyBindings()
	var actions []string
	for _, b := range bindings {
		actions = append(actions, b.Action)
	}
	expected := []string{"move", "toggle", "done", "cancel"}
	for _, exp := range expected {
		found := false
		for _, act := range actions {
			if act == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected action %q in bindings %v", exp, actions)
		}
	}
}
