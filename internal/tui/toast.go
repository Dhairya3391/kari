package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ToastType determines the icon and coloring of a toast message.
type ToastType int

const (
	ToastInfo ToastType = iota
	ToastSuccess
	ToastError
)

// Toast represents an active transient notification.
type Toast struct {
	Message   string
	Type      ToastType
	CreatedAt time.Time
	ExpiresAt time.Time
}

// NewToast creates a new Toast message expiring in duration (default ~3s).
func NewToast(msg string, ttype ToastType, duration time.Duration) *Toast {
	if duration <= 0 {
		duration = 3 * time.Second
	}
	now := time.Now()
	return &Toast{
		Message:   msg,
		Type:      ttype,
		CreatedAt: now,
		ExpiresAt: now.Add(duration),
	}
}

// IsExpired checks whether the toast has exceeded its lifetime.
func (t *Toast) IsExpired(now time.Time) bool {
	if t == nil {
		return true
	}
	return now.After(t.ExpiresAt)
}

// Render renders the toast message row.
func (t *Toast) Render(accent lipgloss.AdaptiveColor) string {
	if t == nil || t.Message == "" {
		return ""
	}

	st := NewStyles(accent)
	switch t.Type {
	case ToastSuccess:
		return st.Ok.Render("✓ ") + t.Message
	case ToastError:
		return st.Err.Render("✗ ") + t.Message
	default:
		return st.Dim.Render("• ") + t.Message
	}
}
