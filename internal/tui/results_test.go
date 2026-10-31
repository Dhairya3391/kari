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

// Loading feedback stays in the frame status row while the search input remains
// stable.
func TestSearchLoadingDoesNotRenderInlineLoader(t *testing.T) {
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
	if out != "› frieren" {
		t.Errorf("home screen should contain only the input, got:\n%s", out)
	}
}

func TestResultsSelectedRowHighlight(t *testing.T) {
	accent := ResolveAccent(model.KindAnime, "Auto")
	st := NewStyles(accent)

	data := ResultsData{
		Query: "clover",
		Mode:  provider.ModeAnime,
		Results: []provider.SearchResult{
			{Title: "Black Clover", Year: "2017", MediaType: provider.MediaTypeTV},
			{Title: "Black Clover Season 2", Year: "2026", MediaType: provider.MediaTypeTV},
		},
		SelectedIndex: 1,
		Width:         100,
		Height:        20,
		Accent:        accent,
	}

	out := RenderResultsScreen(data)

	// The selected row's title and year must be rendered with accent highlight (st.Current).
	expectedTitle := st.Current.Render("Black Clover Season 2")
	if !strings.Contains(out, expectedTitle) {
		t.Errorf("selected title must be highlighted with accent color (st.Current), output:\n%s", out)
	}

	expectedYear := st.Current.Render("2026")
	if !strings.Contains(out, expectedYear) {
		t.Errorf("selected year must be highlighted with accent color (st.Current), output:\n%s", out)
	}

	// The unselected row's year must remain dim.
	unselectedYear := st.Dim.Render("2017")
	if !strings.Contains(out, unselectedYear) {
		t.Errorf("unselected year must remain dim, output:\n%s", out)
	}
	if !strings.Contains(out, "TV") {
		t.Errorf("result rows should show media type, output:\n%s", out)
	}
}

func TestResultsOmitRedundantTypeInTVAndMovieModes(t *testing.T) {
	tests := []struct {
		mode      provider.ContentType
		mediaType string
		wantLabel string
	}{
		{mode: provider.ModeTV, mediaType: provider.MediaTypeTV, wantLabel: "TV"},
		{mode: provider.ModeMovies, mediaType: provider.MediaTypeMovie, wantLabel: "Movie"},
	}
	for _, tc := range tests {
		out := RenderResultsScreen(ResultsData{
			Mode:          tc.mode,
			Results:       []provider.SearchResult{{Title: "Example", Year: "2020", MediaType: tc.mediaType}},
			SelectedIndex: 0,
			Width:         64,
			Height:        20,
			Accent:        ResolveAccent(model.KindTV, "Auto"),
		})
		if strings.Contains(out, tc.wantLabel) {
			t.Errorf("%s mode should omit its redundant type label, got:\n%s", tc.mode, out)
		}
	}
}
