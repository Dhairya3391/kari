package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/service"
	"kari/internal/termimg"
)

func liveGuideFixture() ResultsData {
	now := time.Now()
	return ResultsData{
		Query: "news",
		Mode:  provider.ModeLive,
		Results: []provider.SearchResult{
			{Title: "BBC News", Group: "News", Live: true, Provider: "famelack"},
			{Title: "BBC One", Group: "News", Live: true, Provider: "famelack"},
			{Title: "Kickoff", Group: "Football", StartsAt: now.Add(time.Hour), Provider: "famelack"},
			{Title: "ESPN", Group: "Channel", Provider: "famelack"},
		},
		ProviderNames: map[string]string{
			"famelack": "Famelack",
		},
		SelectedIndex: 0,
		PosterBlock:   "CHANNEL_ART",
		Width:         120,
		Height:        30,
		Accent:        ResolveAccent(model.KindLive, "Auto"),
	}
}

func TestLiveGuideLayoutKeepsArtworkInInspector(t *testing.T) {
	out := RenderLiveResultsScreen(liveGuideFixture())

	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[0], "LIVE NOW 2") || !strings.Contains(lines[0], "UPCOMING 1") {
		t.Fatalf("status strip missing counts:\n%s", lines[0])
	}
	if strings.Count(out, "CHANNEL_ART") != 1 {
		t.Fatalf("artwork must appear exactly once, got:\n%s", out)
	}
	for _, line := range lines {
		parts := strings.SplitN(line, " │ ", 2)
		if len(parts) == 2 && strings.Contains(parts[0], "CHANNEL_ART") {
			t.Fatalf("artwork leaked into the list pane:\n%s", out)
		}
	}
	for _, want := range []string{"BBC News", "News · 2", "ON AIR", "UP NEXT"} {
		if !strings.Contains(out, want) {
			t.Errorf("guide missing %q:\n%s", want, out)
		}
	}
}

func TestLiveGuideNarrowDropsInspector(t *testing.T) {
	data := liveGuideFixture()
	data.Width = 60
	data.Height = 30
	out := RenderLiveResultsScreen(data)

	if strings.Contains(out, "CHANNEL_ART") {
		t.Fatalf("narrow guide must not draw artwork:\n%s", out)
	}
	if strings.Contains(out, " │ ") {
		t.Fatalf("narrow guide must not split panes:\n%s", out)
	}
	if !strings.Contains(out, "BBC News") {
		t.Fatalf("narrow guide must still list channels:\n%s", out)
	}
}

func TestLiveArtworkVisibility(t *testing.T) {
	reg := &provider.Registry{}
	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	mdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	m, ok := mdl.(*modelImpl)
	if !ok {
		t.Fatal("expected *modelImpl")
	}
	m.appMode = provider.ModeLive
	m.imagesEnabled = true
	m.imgProtocol = termimg.ProtocolKitty
	m.seriesResults = liveGuideFixture().Results
	m.seriesList.SetItems(seriesToItems(m.seriesResults))
	m.width = 120
	m.height = 40

	if m.searchPosterVisible() {
		t.Error("live mode must not draw the catalog poster above the list")
	}
	if !m.livePosterVisible() {
		t.Error("live mode must draw its inspector artwork")
	}

	m.width = 60
	if m.livePosterVisible() {
		t.Error("narrow terminal must not draw inspector artwork")
	}
}

// A status row shrinks the body the renderer actually uses. Visibility gates
// must use the same shortened height or they count a poster the renderer hides.
func TestLiveArtworkVisibilityHonorsStatusRow(t *testing.T) {
	reg := &provider.Registry{}
	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	m := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "").(*modelImpl)
	m.appMode = provider.ModeLive
	m.imagesEnabled = true
	m.imgProtocol = termimg.ProtocolKitty
	m.seriesResults = liveGuideFixture().Results
	m.seriesList.SetItems(seriesToItems(m.seriesResults))
	m.width = 120
	m.height = 19 // BodyHeight == liveMinInspectorHeight

	if !m.livePosterVisible() {
		t.Fatal("expected the inspector artwork at the height threshold")
	}
	m.setStatus(statusInfo, "Loading")
	if m.livePosterVisible() {
		t.Error("status row must shrink the body below the inspector threshold")
	}
}

func TestLiveFooterBindings(t *testing.T) {
	reg := &provider.Registry{}
	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	mdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	m := mdl.(*modelImpl)
	m.appMode = provider.ModeLive
	m.seriesResults = liveGuideFixture().Results

	var got []string
	for _, b := range m.activeKeyBindings() {
		got = append(got, b.Action)
	}
	for _, want := range []string{"watch", "filter", "jump", "refresh"} {
		found := false
		for _, action := range got {
			if action == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("live footer missing %q, got %v", want, got)
		}
	}
}

func TestLiveRefreshAndJumpKeys(t *testing.T) {
	reg := &provider.Registry{}
	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	mdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	m := mdl.(*modelImpl)
	m.appMode = provider.ModeLive
	m.seriesResults = liveGuideFixture().Results
	m.seriesList.SetItems(seriesToItems(m.seriesResults))

	m.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !m.loading || m.searchOpID == 0 || m.loadingText != "Refreshing live guide..." {
		t.Fatalf("r did not start a refresh: loading=%v opID=%d text=%q", m.loading, m.searchOpID, m.loadingText)
	}
	m.loading = false

	m.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if got := m.selectedSeriesIndex(); got != 2 {
		t.Fatalf("2 jumped to result %d, want 2 (upcoming section)", got)
	}
	m.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if got := m.selectedSeriesIndex(); got != 3 {
		t.Fatalf("3 jumped to result %d, want 3 (channels section)", got)
	}
}

// With enough results to force scrolling, the selected row's position in the
// rendered guide (which inserts section headings and spacers) differs from its
// position in LiveGuide.Rows. The scroll window must use the rendered position
// or the highlighted row can fall outside the viewport.
func TestLiveGuideScrollKeepsSelectionVisible(t *testing.T) {
	now := time.Now()
	nowResults := make([]provider.SearchResult, 0, 15)
	for i := 0; i < 5; i++ {
		nowResults = append(nowResults, provider.SearchResult{Title: fmt.Sprintf("News %d", i), Group: "News", Live: true})
	}
	for i := 0; i < 5; i++ {
		nowResults = append(nowResults, provider.SearchResult{Title: fmt.Sprintf("Match %d", i), Group: "Football", StartsAt: now.Add(time.Hour)})
	}
	for i := 0; i < 5; i++ {
		nowResults = append(nowResults, provider.SearchResult{Title: fmt.Sprintf("Channel %d", i), Group: "Channel"})
	}
	const selected = 13 // "Channel 3", in the last section
	guide := service.BuildLiveGuide(nowResults, selected, now)
	data := ResultsData{
		Results:       nowResults,
		LiveGuide:     &guide,
		SelectedIndex: selected,
		Width:         60,
		Height:        12,
	}

	st := NewStyles(liveGuideFixture().Accent)
	rows, rendered := liveGuideRows(data, now, 50, false, st)
	if rendered < 0 {
		t.Fatal("selected row was not rendered")
	}
	if rendered == guide.SelectedRow {
		t.Fatalf("rendered position %d unexpectedly equals unprepared guide index %d", rendered, guide.SelectedRow)
	}
	const viewport = 6
	visible, start, _ := windowRows(rows, rendered, viewport)
	if rendered < start || rendered >= start+len(visible) {
		t.Fatalf("selected row %d outside visible window [%d,%d)", rendered, start, start+len(visible))
	}

	// The full render path must actually draw the highlighted row. The cursor
	// marker only appears in list rows, so its presence proves the selected
	// row survived the scroll window.
	out := RenderLiveResultsScreen(data)
	if !strings.Contains(out, "▌") {
		t.Fatalf("selected row missing from scrolled guide:\n%s", out)
	}
}

func TestLiveGuideCountsMatchService(t *testing.T) {
	data := liveGuideFixture()
	guide := service.BuildLiveGuide(data.Results, data.SelectedIndex, time.Now())
	if guide.Counts.Now != 2 || guide.Counts.Upcoming != 1 || guide.Counts.Channels != 1 {
		t.Fatalf("unexpected fixture counts: %+v", guide.Counts)
	}
}
