package tui

// Regression test for the audit's known bug: episode rows rendered the
// episode code and title concatenated ("01The Name"). There must always be
// a two-space gap between the code and the title.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/model"
	"kari/internal/provider"
)

// Loading episodes show the header only: no inline spinner line and no
// premature "no episodes" flash — feedback lives above the footer.
func TestRenderEpisodesScreenLoadingRendersNoInlineLoader(t *testing.T) {
	data := EpisodesData{
		SeriesTitle:  "ONE PIECE",
		Loading:      true,
		LoadingText:  "Loading episodes...",
		SpinnerFrame: "⣟",
		Width:        110,
		Height:       40,
		Accent:       ResolveAccent(model.KindAnime, "Auto"),
	}
	out := RenderEpisodesScreen(data)
	if strings.Contains(out, "Loading episodes") {
		t.Errorf("loading line must live above the footer, not in body, got:\n%s", out)
	}
	if strings.Contains(out, "No episodes found") {
		t.Errorf("must not flash empty state while loading, got:\n%s", out)
	}
	if !strings.Contains(out, "ONE PIECE") {
		t.Errorf("header must stay, got:\n%s", out)
	}
}

// Long titles with the inspector pane on must never overflow the content
// width: overlong rows once wrapped at the terminal into the pane.
func TestRenderEpisodesScreen_NoOverflowWithPane(t *testing.T) {
	eps := []provider.Episode{
		{Season: 1, Episode: 18, Title: "You're the Weird Creature! Gaimon and His Strange Friends With Strange Faces Everywhere"},
		{Season: 1, Episode: 19, Title: "The Three-Sword Style's Past! Zoro and Kuina's Vow!"},
	}
	data := EpisodesData{
		SeriesTitle:          "ONE PIECE",
		Episodes:             eps,
		HistoryIndex:         nil,
		SelectedIndex:        1,
		SeasonCount:          1,
		SeriesYear:           "1999",
		SeriesKind:           "anime",
		SelectedEpisodeTitle: "19 · The Three-Sword Style's Past! Zoro and Kuina's Vow!",
		PosterBlock:          strings.Repeat("█", 36) + "\n" + strings.Repeat("█", 36) + "\n" + strings.Repeat("█", 36),
		TermWidth:            150,
		Width:                110,
		Height:               40,
		Accent:               ResolveAccent(model.KindAnime, "Auto"),
	}
	out := RenderEpisodesScreen(data)
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > data.Width {
			t.Errorf("line %d width %d exceeds %d: %q", i, w, data.Width, line)
		}
	}
	if !strings.Contains(out, "ONE PIECE") {
		t.Errorf("pane must show the series title, got:\n%s", out)
	}

	// Multi-season runs add the tab strip: 15 seasons must fit the
	// column too, never hiding behind the poster.
	multi := data
	multi.SeasonCount = 15
	multi.ActiveSeason = 14
	out = RenderEpisodesScreen(multi)
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > multi.Width {
			t.Errorf("multi-season line %d width %d exceeds %d: %q", i, w, multi.Width, line)
		}
	}
}

func TestRenderEpisodesScreen_EpisodeCodeTitleGap(t *testing.T) {
	data := EpisodesData{
		SeriesTitle:   "The Boys",
		Episodes:      []provider.Episode{{Season: 1, Episode: 1, Title: "The Name of the Game"}},
		SelectedIndex: 0,
		Width:         70,
		Height:        20,
		Accent:        ResolveAccent(model.KindTV, "Auto"),
	}
	out := RenderEpisodesScreen(data)
	if !strings.Contains(out, "01  The Name of the Game") {
		t.Errorf("expected two-space gap between code and title, got:\n%s", out)
	}
	if strings.Contains(out, "01The Name") {
		t.Errorf("code and title must not be concatenated, got:\n%s", out)
	}
}

func TestRenderEpisodesScreen_MultiSeasonCodes(t *testing.T) {
	data := EpisodesData{
		SeriesTitle: "The Boys",
		Episodes: []provider.Episode{
			{Season: 1, Episode: 5, Title: "Good for the Soul"},
			{Season: 2, Episode: 1, Title: "Season Two Premiere"},
		},
		SelectedIndex: 0,
		SeasonCount:   2,
		ActiveSeason:  0,
		Width:         70,
		Height:        20,
		Accent:        ResolveAccent(model.KindTV, "Auto"),
	}
	out := RenderEpisodesScreen(data)
	if !strings.Contains(out, "S01E05  Good for the Soul") {
		t.Errorf("expected season-aware code, got:\n%s", out)
	}
}

func TestFilterEpisodesByText(t *testing.T) {
	eps := []provider.Episode{
		{Season: 1, Episode: 1, Title: "The Name of the Game"},
		{Season: 1, Episode: 2, Title: "Cherry"},
		{Season: 1, Episode: 3, Title: ""},
	}
	idxs := []int{0, 1, 2}

	filtered, fidx := FilterEpisodesByText(eps, idxs, "cherry")
	if len(filtered) != 1 || filtered[0].Episode != 2 || fidx[0] != 1 {
		t.Errorf("title filter failed: %+v %+v", filtered, fidx)
	}
	filtered, fidx = FilterEpisodesByText(eps, idxs, "3")
	if len(filtered) != 1 || fidx[0] != 2 {
		t.Errorf("number filter failed: %+v %+v", filtered, fidx)
	}
	filtered, _ = FilterEpisodesByText(eps, idxs, "")
	if len(filtered) != 3 {
		t.Errorf("empty query must return all, got %d", len(filtered))
	}
	filtered, _ = FilterEpisodesByText(eps, idxs, "zzz")
	if len(filtered) != 0 {
		t.Errorf("expected no match, got %+v", filtered)
	}
}

func TestRenderEpisodesScreen_FilterNarrowsRows(t *testing.T) {
	data := EpisodesData{
		SeriesTitle: "The Boys",
		Episodes: []provider.Episode{
			{Season: 1, Episode: 1, Title: "The Name of the Game"},
			{Season: 1, Episode: 2, Title: "Cherry"},
		},
		SelectedIndex: 0,
		FilterQuery:   "cherry",
		Filtering:     true,
		Width:         70,
		Height:        20,
		Accent:        ResolveAccent(model.KindTV, "Auto"),
	}
	out := RenderEpisodesScreen(data)
	if !strings.Contains(out, "/cherry") {
		t.Errorf("expected filter prompt, got:\n%s", out)
	}
	if strings.Contains(out, "The Name of the Game") {
		t.Errorf("filtered-out row must not render, got:\n%s", out)
	}
	if !strings.Contains(out, "Cherry") {
		t.Errorf("matching row must render, got:\n%s", out)
	}
}
func TestDistinctSeasonNumbersSingleSeason(t *testing.T) {
	// A series like Black Clover Season 2 where all episodes have Season: 2
	eps := []provider.Episode{
		{Season: 2, Episode: 1, Title: "The Battle Begins"},
		{Season: 2, Episode: 2, Title: "Episode 2"},
	}

	seasons := distinctSeasonNumbers(eps)
	if len(seasons) != 1 || seasons[0] != 2 {
		t.Fatalf("seasons = %v, want [2]", seasons)
	}

	// Filter to season should return all episodes with 0-based indices
	filtered, idxs := filterToSeason(eps, len(seasons), 0)
	if len(filtered) != 2 || len(idxs) != 2 {
		t.Fatalf("filtered = %v, idxs = %v", filtered, idxs)
	}

	// Render season tabs must be empty for 1 distinct season
	tabStrip := RenderSeasonTabs(len(seasons), 0, ResolveAccent(model.KindAnime, "Auto"), 80)
	if tabStrip != "" {
		t.Fatalf("expected empty season tab strip for 1 season, got:\n%s", tabStrip)
	}

	// RenderEpisodesScreen must not render season tabs and must render episode numbers like 01
	data := EpisodesData{
		SeriesTitle:   "Black Clover Season 2",
		Episodes:      eps,
		SelectedIndex: 0,
		SeasonCount:   len(seasons),
		ActiveSeason:  0,
		Width:         70,
		Height:        20,
		Accent:        ResolveAccent(model.KindAnime, "Auto"),
	}
	out := RenderEpisodesScreen(data)
	if strings.Contains(out, "season   1") || strings.Contains(out, "season   2") {
		t.Fatalf("must not render season tabs when show has 1 distinct season:\n%s", out)
	}
	if !strings.Contains(out, "01  The Battle Begins") {
		t.Fatalf("expected '01  The Battle Begins', got:\n%s", out)
	}
}

func TestDistinctSeasonNumbersMultiSeason(t *testing.T) {
	// A multi-season series like Breaking Bad where episodes span Season 1 and 2
	eps := []provider.Episode{
		{Season: 1, Episode: 1, Title: "Pilot"},
		{Season: 1, Episode: 2, Title: "Cat's in the Bag..."},
		{Season: 2, Episode: 1, Title: "Seven Thirty-Seven"},
	}

	seasons := distinctSeasonNumbers(eps)
	if len(seasons) != 2 || seasons[0] != 1 || seasons[1] != 2 {
		t.Fatalf("seasons = %v, want [1 2]", seasons)
	}

	// Filter to Season 1
	filtered1, idxs1 := filterToSeason(eps, len(seasons), 0)
	if len(filtered1) != 2 || idxs1[0] != 0 || idxs1[1] != 1 {
		t.Fatalf("filtered1 = %v, idxs1 = %v", filtered1, idxs1)
	}

	// Filter to Season 2
	filtered2, idxs2 := filterToSeason(eps, len(seasons), 1)
	if len(filtered2) != 1 || idxs2[0] != 2 || filtered2[0].Title != "Seven Thirty-Seven" {
		t.Fatalf("filtered2 = %v, idxs2 = %v", filtered2, idxs2)
	}
}
