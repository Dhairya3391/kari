package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/player"
	"kari/internal/provider"
)

func TestStatusExpiresAfterDuration(t *testing.T) {
	m := unavailableTestModel()
	m.width = 100
	m.height = 24

	// Set status
	m.setStatus(statusSuccess, "Playback finished")
	if m.statusExpiresAt.IsZero() {
		t.Fatal("expected statusExpiresAt to be non-zero after setStatus")
	}

	// Before expiry: visible
	out := m.renderMainView()
	if !strings.Contains(out, "Playback finished") {
		t.Fatalf("expected 'Playback finished' to render before expiry:\n%s", out)
	}

	// After expiry: not visible even if statusText is not empty yet
	m.statusExpiresAt = time.Now().Add(-1 * time.Second)
	outExpired := m.renderMainView()
	if strings.Contains(outExpired, "Playback finished") {
		t.Fatalf("expected 'Playback finished' to disappear after expiry:\n%s", outExpired)
	}

	// When resetStatusMsg arrives, statusText and type are cleared
	mdl, _ := m.Update(resetStatusMsg{id: m.statusID})
	m2 := mdl.(*modelImpl)
	if m2.statusText != "" {
		t.Fatalf("expected statusText to be empty after resetStatusMsg, got %q", m2.statusText)
	}
}

func TestStatusClearsOnEsc(t *testing.T) {
	m := unavailableTestModel()
	m.width = 100
	m.height = 24
	m.activeView = viewPreview
	m.backStack = []viewState{viewEpisodes}

	m.setStatus(statusSuccess, "Playback finished")
	m.setToast("test toast", ToastInfo)

	// Pressing ESC
	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := mdl.(*modelImpl)

	if m2.statusText != "" {
		t.Fatalf("expected statusText to be cleared on ESC, got %q", m2.statusText)
	}
	if m2.activeToast != nil {
		t.Fatalf("expected activeToast to be cleared on ESC, got %v", m2.activeToast)
	}
	if m2.activeView != viewEpisodes {
		t.Fatalf("expected activeView to be viewEpisodes after ESC, got %v", m2.activeView)
	}
}

func TestStatusClearsOnScreenChange(t *testing.T) {
	m := unavailableTestModel()
	m.width = 100
	m.height = 24
	m.activeView = viewSearch

	m.setStatus(statusWarn, "No results found for query")
	m.setToast("some toast", ToastSuccess)

	// Navigating to Settings
	m.pushView(viewSettings)

	if m.statusText != "" {
		t.Fatalf("expected statusText to be cleared on pushView, got %q", m.statusText)
	}
	if m.activeToast != nil {
		t.Fatalf("expected activeToast to be cleared on pushView, got %v", m.activeToast)
	}

	// Navigating back
	m.setStatus(statusInfo, "Settings saved")
	m.goBackOne()

	if m.statusText != "" {
		t.Fatalf("expected statusText to be cleared on goBackOne, got %q", m.statusText)
	}
}

func TestStatusClearsOnModeSwitch(t *testing.T) {
	m := newTestSwitchModel()
	m.width = 100
	m.height = 24
	m.appMode = provider.ModeAnime

	m.setStatus(statusWarn, "Sample anime warning")
	m.setToast("Sample toast", ToastInfo)

	m.cycleMode(false)

	if m.statusText != "" {
		t.Fatalf("expected statusText to be cleared on mode switch, got %q", m.statusText)
	}
	if m.activeToast != nil {
		t.Fatalf("expected activeToast to be cleared on mode switch, got %v", m.activeToast)
	}
}

func TestPlaybackDoneSchedulesTimerAndClearsOnEsc(t *testing.T) {
	m := readySourceModel()
	m.width = 100
	m.height = 24
	m.activeView = viewPreview
	m.backStack = []viewState{viewEpisodes}
	m.playOpID = 10

	// Trigger playDoneMsg
	mdl, cmd := m.Update(playDoneMsg{opID: 10, result: player.PlaybackResult{Completed: true}})
	m2 := mdl.(*modelImpl)

	if m2.statusText != "Playback finished" {
		t.Fatalf("statusText = %q, want 'Playback finished'", m2.statusText)
	}
	if m2.statusExpiresAt.IsZero() {
		t.Fatal("expected statusExpiresAt to be set")
	}
	if cmd == nil {
		t.Fatal("expected a clear timer command to be scheduled on playDoneMsg")
	}

	// Pressing ESC dismisses it
	mdl2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m3 := mdl2.(*modelImpl)

	if m3.statusText != "" {
		t.Fatalf("expected statusText to be cleared on ESC, got %q", m3.statusText)
	}
}
