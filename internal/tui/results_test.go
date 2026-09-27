package tui

// Loading feedback lives in the fixed row above the footer — screens
// must not render inline spinner lines that shift content when they
// appear, nor flash empty-state messages while data is still loading.

import (
	"strings"
	"testing"

	"kari/internal/model"
	"kari/internal/provider"
)

func resultsLoadingData() ResultsData {
	return ResultsData{
		Query:        "frieren",
		Mode:         provider.ModeAnime,
		Loading:      true,
		LoadingText:  "Searching...",
		SpinnerFrame: "⣟",
		Width:        100,
		Height:       24,
		Accent:       ResolveAccent(model.KindAnime, "Auto"),
	}
}

// Loading results show the header only: no inline spinner line, no
// premature "no results" flash.
func TestResultsLoadingRendersNoInlineLoader(t *testing.T) {
	out := RenderResultsScreen(resultsLoadingData())
	if strings.Contains(out, "Searching") {
		t.Errorf("loading line must live above the footer, not in body, got:\n%s", out)
	}
	if strings.Contains(out, "No results in") {
		t.Errorf("must not flash empty state while loading, got:\n%s", out)
	}
	if !strings.Contains(out, "frieren") {
		t.Errorf("header query must stay, got:\n%s", out)
	}
}

// The search home screen is just the input box while loading.
func TestSearchLoadingRendersInputOnly(t *testing.T) {
	out := RenderSearchScreen(SearchData{
		InputView:    "› frieren",
		InputFocused: true,
		Loading:      true,
		LoadingText:  "Searching...",
		SpinnerFrame: "⣟",
		Width:        100,
		Accent:       ResolveAccent(model.KindAnime, "Auto"),
	})
	if strings.Contains(out, "Searching") {
		t.Errorf("loading line must live above the footer, not in body, got:\n%s", out)
	}
	if !strings.Contains(out, "frieren") {
		t.Errorf("input must stay, got:\n%s", out)
	}
}
