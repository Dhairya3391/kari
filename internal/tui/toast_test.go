package tui

import (
	"strings"
	"testing"
	"time"

	"kari/internal/model"
)

func TestToast_RenderWithTimeDissolution(t *testing.T) {
	accent := ResolveAccent(model.KindAnime, "Auto")
	now := time.Now()
	toast := NewToast("Playback started", ToastSuccess, 3*time.Second)

	t.Run("normal rendering well before expiration", func(t *testing.T) {
		renderTime := now.Add(1 * time.Second)
		out := toast.RenderWithTime(accent, renderTime)
		if !strings.Contains(out, "✓") || !strings.Contains(out, "Playback started") {
			t.Errorf("expected standard success toast format, got %q", out)
		}
	})

	t.Run("fading rendering in final 350ms", func(t *testing.T) {
		// 150ms before expiration -> in fade zone
		renderTime := toast.ExpiresAt.Add(-150 * time.Millisecond)
		out := toast.RenderWithTime(accent, renderTime)
		if !strings.Contains(out, "Playback started") {
			t.Errorf("expected toast text during fade out, got %q", out)
		}
	})

	t.Run("expired returns empty string", func(t *testing.T) {
		renderTime := toast.ExpiresAt.Add(10 * time.Millisecond)
		out := toast.RenderWithTime(accent, renderTime)
		if out != "" {
			t.Errorf("expected empty string for expired toast, got %q", out)
		}
	})
}
func TestToast_AutoClearLifecycle(t *testing.T) {
	m := unavailableTestModel()
	cmd := m.setToast("marked 03 complete", ToastSuccess)
	if cmd == nil {
		t.Fatal("setToast must return a non-nil tea.Cmd for auto-dismissal")
	}
	if m.activeToast == nil {
		t.Fatal("activeToast must be set")
	}
	if m.activeToast.Message != "marked 03 complete" {
		t.Errorf("got message %q, want 'marked 03 complete'", m.activeToast.Message)
	}

	// Fast-forward toast past expiration
	m.activeToast.ExpiresAt = time.Now().Add(-10 * time.Millisecond)

	// Processing toastFadeTickMsg after expiration clears activeToast
	mdl, _ := m.Update(toastFadeTickMsg{id: m.toastID})
	m2 := mdl.(*modelImpl)
	if m2.activeToast != nil {
		t.Errorf("activeToast must be nil after expiration tick, got %+v", m2.activeToast)
	}
}
