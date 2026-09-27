package tui

// The status block (loading/toast) above the footer must never shift
// body content: appearing lines come out of the body's budget, the
// first body lines keep their positions, and the footer stays pinned.

import (
	"strings"
	"testing"
)

func testDims() Dims {
	return ComputeDims(120, 30)
}

// A two-line status block (loading + toast stacked) keeps the footer
// pinned and budgets both lines out of the body.
func TestRenderFrameMultiLineStatusKeepsFooter(t *testing.T) {
	dims := testDims()
	body := strings.Repeat("row\n", 40)
	status := "⠋ Loading episodes...\nToast text"
	out := RenderFrame(dims, "header", body, status, "footer")
	lines := strings.Split(out, "\n")
	if len(lines) != dims.TermHeight {
		t.Fatalf("frame = %d lines, want %d", len(lines), dims.TermHeight)
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if !strings.Contains(last, "footer") {
		t.Errorf("last line must be the footer, got %q", last)
	}
	foundLoading, foundToast := false, false
	for _, ln := range lines {
		if strings.Contains(ln, "Loading episodes") {
			foundLoading = true
		}
		if strings.Contains(ln, "Toast text") {
			foundToast = true
		}
	}
	if !foundLoading || !foundToast {
		t.Errorf("both status lines must render (loading=%v toast=%v)", foundLoading, foundToast)
	}
}

// Body lines keep their positions whether or not a status block shows:
// only the tail is budgeted away, never the head.
func TestRenderFrameStatusDoesNotShiftBody(t *testing.T) {
	dims := testDims()
	var bodyLines []string
	for i := 0; i < 40; i++ {
		bodyLines = append(bodyLines, "row")
	}
	body := strings.Join(bodyLines, "\n")
	plain := strings.Split(RenderFrame(dims, "header", body, "", "footer"), "\n")
	withStatus := strings.Split(RenderFrame(dims, "header", body, "⠋ Loading…", "footer"), "\n")
	// First body row sits at the same index in both frames.
	bodyAt := -1
	for i, ln := range plain {
		if strings.Contains(ln, "row") {
			bodyAt = i
			break
		}
	}
	if bodyAt < 0 {
		t.Fatal("no body row found")
	}
	if !strings.Contains(withStatus[bodyAt], "row") {
		t.Errorf("first body row moved from line %d when status appeared", bodyAt)
	}
}
