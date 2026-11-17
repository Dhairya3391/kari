package tui

// The scroll filter is a plain min-interval throttle: one accepted scroll per
// scrollMinInterval, with dropped events never re-arming the gate. That keeps
// dense bursts paced while letting held keys and second flicks register.
// Tests drive injected timestamps so no sleeps or wall-clock timing are used.

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func newTestListWithItems(n int) list.Model {
	items := make([]list.Item, n)
	for i := range items {
		items[i] = rowItem{title: "item", index: i}
	}
	return list.New(items, list.NewDefaultDelegate(), 80, 16)
}

func focusedTestInput() textinput.Model {
	input := textinput.New()
	input.Focus()
	return input
}

func scrollKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func scrollDownAt(m *modelImpl, t time.Time) tea.Msg {
	return m.filterScrollInputAt(tea.KeyMsg{Type: tea.KeyDown}, t)
}

func scrollUpAt(m *modelImpl, t time.Time) tea.Msg {
	return m.filterScrollInputAt(tea.KeyMsg{Type: tea.KeyUp}, t)
}

func scrollTestModel() *modelImpl {
	m := &modelImpl{activeView: viewSearch}
	m.resetScrollGate()
	return m
}

func scrollTestBase() time.Time {
	return time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
}

func TestScrollMinIntervalFirstEventPasses(t *testing.T) {
	m := scrollTestModel()
	if got := scrollDownAt(m, scrollTestBase()); got == nil {
		t.Fatal("first scroll event on a fresh model must pass")
	}
}

func TestScrollMinIntervalBoundary(t *testing.T) {
	base := scrollTestBase()
	tests := []struct {
		name     string
		gap      time.Duration
		wantPass bool
	}{
		{"same instant", 0, false},
		{"one millisecond", time.Millisecond, false},
		{"just below interval", scrollMinInterval - time.Nanosecond, false},
		{"exactly interval", scrollMinInterval, true},
		{"interval plus 20ms", scrollMinInterval + 20*time.Millisecond, true},
		{"one second", time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := scrollTestModel()
			if scrollDownAt(m, base) == nil {
				t.Fatal("priming event must pass")
			}
			got := scrollDownAt(m, base.Add(tt.gap))
			if tt.wantPass && got == nil {
				t.Fatalf("gap %v: expected pass, got drop", tt.gap)
			}
			if !tt.wantPass && got != nil {
				t.Fatalf("gap %v: expected drop, got pass", tt.gap)
			}
		})
	}
}

func TestScrollMinIntervalAcceptedOnlyTimestamp(t *testing.T) {
	m := scrollTestModel()
	base := scrollTestBase()

	if scrollDownAt(m, base) == nil {
		t.Fatal("priming event at base must pass")
	}
	// Drops must not advance the gate: only accepted events update the
	// timestamp, so a long run of drops cannot delay the next real scroll.
	for ms := 2; ms <= 14; ms++ {
		at := base.Add(time.Duration(ms) * time.Millisecond)
		if got := scrollDownAt(m, at); got != nil {
			t.Fatalf("event at +%dms should be dropped, got pass", ms)
		}
	}
	if got := scrollDownAt(m, base.Add(scrollMinInterval)); got == nil {
		t.Fatal("event at +16ms must pass: drops between events must not delay it")
	}
}

func TestScrollMinIntervalDenseBurstIsPaced(t *testing.T) {
	m := scrollTestModel()
	base := scrollTestBase()

	// One event every 4ms: with a 16ms gate, exactly every fourth passes.
	for i := 0; i < 24; i++ {
		at := base.Add(time.Duration(i) * 4 * time.Millisecond)
		got := scrollDownAt(m, at)
		wantPass := i%4 == 0
		if wantPass && got == nil {
			t.Fatalf("event %d at +%dms should pass", i, i*4)
		}
		if !wantPass && got != nil {
			t.Fatalf("event %d at +%dms should be dropped", i, i*4)
		}
	}
}

func TestScrollMinIntervalHeldKeyRepeatPasses(t *testing.T) {
	m := scrollTestModel()
	base := scrollTestBase()

	// OS key repeat at ~33ms is well above the interval: every event passes,
	// so holding a key keeps traversing a long list.
	for i := 0; i < 60; i++ {
		at := base.Add(time.Duration(i) * 33 * time.Millisecond)
		if got := scrollDownAt(m, at); got == nil {
			t.Fatalf("held-key repeat %d was dropped", i)
		}
	}
}

func TestScrollMinIntervalDirectionIndependent(t *testing.T) {
	m := scrollTestModel()
	base := scrollTestBase()

	if scrollDownAt(m, base) == nil {
		t.Fatal("priming down event must pass")
	}
	if got := scrollUpAt(m, base.Add(4*time.Millisecond)); got != nil {
		t.Fatal("up at +4ms must be dropped just like same-direction input")
	}
	if got := scrollUpAt(m, base.Add(scrollMinInterval)); got == nil {
		t.Fatal("up at +16ms must pass regardless of direction")
	}
}

func TestScrollMinIntervalSecondFlickRegisters(t *testing.T) {
	m := scrollTestModel()
	base := scrollTestBase()

	// A dense first flick: only the first event is accepted, the rest drop.
	for i := 0; i < 20; i++ {
		scrollDownAt(m, base.Add(time.Duration(i)*time.Millisecond))
	}
	// The next deliberate flick after a short gap must still register.
	if got := scrollDownAt(m, base.Add(50*time.Millisecond)); got == nil {
		t.Fatal("second flick after a short gap must register")
	}
}

func TestScrollMinIntervalNonScrollKeysDoNotThrottle(t *testing.T) {
	m := scrollTestModel()
	base := scrollTestBase()

	if scrollDownAt(m, base) == nil {
		t.Fatal("priming event must pass")
	}
	acceptedAt := m.scroll.lastAcceptedAt

	for _, key := range []string{"x", "j", "k", "g", "G", "enter"} {
		if got := m.filterScrollInputAt(scrollKey(key), base.Add(4*time.Millisecond)); got == nil {
			t.Fatalf("non-scroll key %q must be returned unchanged", key)
		}
	}
	if got := m.scroll.lastAcceptedAt; !got.Equal(acceptedAt) {
		t.Fatalf("non-scroll keys must not change lastAcceptedAt: got %v, want %v", got, acceptedAt)
	}
	if got := scrollDownAt(m, base.Add(scrollMinInterval)); got == nil {
		t.Fatal("scroll after non-scroll keys must still pass")
	}
}

func TestScrollMinIntervalWheelTranslation(t *testing.T) {
	// A fresh model per event: two back-to-back FilterScrollInput calls on one
	// model would drop the second under the 16ms gate, hiding the translation.
	// Two items with the cursor off each boundary so the redundant-navigation
	// filter does not drop the translated key.
	upModel := &modelImpl{activeView: viewSearch, seriesList: newTestListWithItems(2)}
	upModel.seriesList.Select(1)
	upModel.resetScrollGate()
	up := FilterScrollInput(upModel, tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	upKey, ok := up.(tea.KeyMsg)
	if !ok || upKey.Type != tea.KeyUp {
		t.Fatalf("wheel up should translate to KeyUp, got %#v", up)
	}

	downModel := &modelImpl{activeView: viewSearch, seriesList: newTestListWithItems(2)}
	downModel.seriesList.Select(0)
	downModel.resetScrollGate()
	down := FilterScrollInput(downModel, tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	downKey, ok := down.(tea.KeyMsg)
	if !ok || downKey.Type != tea.KeyDown {
		t.Fatalf("wheel down should translate to KeyDown, got %#v", down)
	}
}

// TestScrollWheelDroppedAtSearchBoundary proves redundant wheel events at the
// ends of the results list are filtered before Update, so a burst held at the
// top cannot queue up work that freezes the UI when scrolling back down.
func TestScrollWheelDroppedAtSearchBoundary(t *testing.T) {
	m := &modelImpl{activeView: viewSearch, seriesList: newTestListWithItems(3)}
	m.resetScrollGate()

	// At the top: wheel up is dropped, wheel down passes.
	if got := FilterScrollInput(m, tea.MouseMsg{Button: tea.MouseButtonWheelUp}); got != nil {
		t.Errorf("wheel up at top = %v, want dropped", got)
	}
	m.resetScrollGate()
	if got := FilterScrollInput(m, tea.MouseMsg{Button: tea.MouseButtonWheelDown}); got == nil {
		t.Error("wheel down at top was dropped, want it to pass")
	}

	// At the bottom: wheel down is dropped, wheel up passes.
	m.seriesList.Select(len(m.seriesList.Items()) - 1)
	m.resetScrollGate()
	if got := FilterScrollInput(m, tea.MouseMsg{Button: tea.MouseButtonWheelDown}); got != nil {
		t.Errorf("wheel down at bottom = %v, want dropped", got)
	}
	m.resetScrollGate()
	if got := FilterScrollInput(m, tea.MouseMsg{Button: tea.MouseButtonWheelUp}); got == nil {
		t.Error("wheel up at bottom was dropped, want it to pass")
	}
}

func TestScrollDirectionSpecialCases(t *testing.T) {
	m := &modelImpl{activeView: viewHistory, historyFiltering: true}
	if got := scrollInputDirection(m, tea.KeyMsg{Type: tea.KeyCtrlU}); got != "" {
		t.Fatalf("ctrl+u while history filtering must not scroll, got %q", got)
	}
	m = &modelImpl{activeView: viewEpisodes, selectMode: true}
	if got := scrollInputDirection(m, tea.KeyMsg{Type: tea.KeyCtrlD}); got != "" {
		t.Fatalf("ctrl+d in episode select mode must not scroll, got %q", got)
	}
	m = &modelImpl{activeView: viewSearch}
	if got := scrollInputDirection(m, scrollKey("j")); got != "" {
		t.Fatalf("typed j must not be classified as scroll, got %q", got)
	}
	if got := scrollInputDirection(m, tea.KeyMsg{Type: tea.KeyPgDown}); got != "down" {
		t.Fatalf("pgdown must map to down, got %q", got)
	}
}

func TestScrollInputActiveGates(t *testing.T) {
	tests := []struct {
		name string
		m    *modelImpl
		want bool
	}{
		{"search with results", &modelImpl{activeView: viewSearch, seriesList: newTestListWithItems(1)}, true},
		{"search with focused query", &modelImpl{activeView: viewSearch, queryInput: focusedTestInput()}, false},
		{"help overlay", &modelImpl{activeView: viewSettings, showHelp: true}, true},
		{"anime status modal", &modelImpl{activeView: viewSearch, animeStatusPickerOpen: true}, false},
		{"addon input", &modelImpl{activeView: viewSettings, addonInputOpen: true}, false},
		{"calendar filtering", &modelImpl{activeView: viewCalendar, calendarFiltering: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.scrollInputActive(); got != tt.want {
				t.Fatalf("scrollInputActive = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScrollGateResetOnViewChange(t *testing.T) {
	m := scrollTestModel()
	base := scrollTestBase()

	if scrollDownAt(m, base) == nil {
		t.Fatal("priming event must pass")
	}
	if got := scrollDownAt(m, base.Add(4*time.Millisecond)); got != nil {
		t.Fatal("event at +4ms must be dropped before reset")
	}
	m.resetScrollGate()
	if got := scrollDownAt(m, base.Add(5*time.Millisecond)); got == nil {
		t.Fatal("event at +5ms must pass after reset clears the accepted timestamp")
	}
}
