package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/provider"
	"kari/internal/termimg"
)

// cmdContainsClearScreen reports whether cmd, including any command nested
// in a tea.Batch, is tea.ClearScreen. Update batches the clear with whatever
// command the message handler already returned, so tests must unwrap the
// batch instead of comparing the top-level result.
func cmdContainsClearScreen(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, batchCmd := range msg {
			if cmdContainsClearScreen(batchCmd) {
				return true
			}
		}
		return false
	default:
		return msg == tea.ClearScreen()
	}
}

func TestOverlayImageChangeRequestsScreenClear(t *testing.T) {
	for _, protocol := range []termimg.Protocol{termimg.ProtocolSixel, termimg.ProtocolIterm} {
		m := &modelImpl{
			activeView:    viewSearch,
			width:         120,
			height:        40,
			imgProtocol:   protocol,
			imagesEnabled: true,
			searchPoster:  "IMAGE-POSTER",
			seriesResults: []provider.SearchResult{{Title: "Frieren"}},
		}

		// Leaving the search view hides the poster: the stale placement
		// must be cleared before the next frame.
		_, cmd := m.Update(historyLoadedMsg{})
		if !cmdContainsClearScreen(cmd) {
			t.Fatalf("%v: hiding an image must request a screen clear", protocol)
		}
	}
}

func TestOverlayNoImageSkipsScreenClear(t *testing.T) {
	m := &modelImpl{
		activeView:    viewSearch,
		width:         120,
		height:        40,
		imgProtocol:   termimg.ProtocolSixel,
		imagesEnabled: true,
	}

	// Nothing was drawn, so leaving the view has no stale pixels to clear.
	if _, cmd := m.Update(historyLoadedMsg{}); cmdContainsClearScreen(cmd) {
		t.Error("view change without an image requested a screen clear")
	}
}

func TestNonOverlayImageDoesNotScreenClear(t *testing.T) {
	// Kitty placements are deleted by id and blocks are plain text; neither
	// needs a full clear.
	for _, protocol := range []termimg.Protocol{termimg.ProtocolKitty, termimg.ProtocolBlocks, termimg.ProtocolNone} {
		m := &modelImpl{
			activeView:    viewSearch,
			width:         120,
			height:        40,
			imgProtocol:   protocol,
			imagesEnabled: true,
			searchPoster:  "IMAGE-POSTER",
			seriesResults: []provider.SearchResult{{Title: "Frieren"}},
		}

		if _, cmd := m.Update(historyLoadedMsg{}); cmdContainsClearScreen(cmd) {
			t.Errorf("%v: view change requested a screen clear", protocol)
		}
	}
}

func TestOverlayReaderPageTurnRequestsScreenClear(t *testing.T) {
	for _, protocol := range []termimg.Protocol{termimg.ProtocolSixel, termimg.ProtocolIterm} {
		m := &modelImpl{
			activeView:    viewReader,
			keys:          defaultKeyMap(),
			width:         120,
			height:        40,
			imgProtocol:   protocol,
			imagesEnabled: true,
			// Three pages so page 1 is not the chapter end (no scrobble).
			pages:        make([]provider.MangaPage, 3),
			readerPage:   0,
			readerCols:   120,
			readerRows:   38,
			readerRender: map[int]string{0: "PAGE-ZERO", 1: "PAGE-ONE", 2: "PAGE-TWO"},
		}

		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRight})
		if !cmdContainsClearScreen(cmd) {
			t.Fatalf("%v: turning a page must request a screen clear", protocol)
		}
	}
}

func TestOverlayReaderHelpRequestsScreenClear(t *testing.T) {
	for _, protocol := range []termimg.Protocol{termimg.ProtocolSixel, termimg.ProtocolIterm} {
		m := &modelImpl{
			activeView:   viewReader,
			keys:         defaultKeyMap(),
			width:        120,
			height:       40,
			imgProtocol:  protocol,
			readerPage:   0,
			readerRender: map[int]string{0: "PAGE-ART"},
		}

		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
		if !m.showHelp {
			t.Fatalf("%v: ? must open the reader help overlay", protocol)
		}
		if !cmdContainsClearScreen(cmd) {
			t.Fatalf("%v: opening help must clear the page placement", protocol)
		}
	}
}

// TestOverlayPreviewMetaChangeRequestsScreenClear pins the preview fix:
// Sixel/iTerm2 posters share cells with the metadata block, which fills in
// asynchronously. Rewriting those rows erases the part of the image under
// them, so a metadata change must trigger the full repaint that redraws the
// poster (observed as a poster sliced into horizontal strips on Windows
// Terminal).
func TestOverlayPreviewMetaChangeRequestsScreenClear(t *testing.T) {
	for _, protocol := range []termimg.Protocol{termimg.ProtocolSixel, termimg.ProtocolIterm} {
		m := &modelImpl{
			activeView:        viewPreview,
			width:             120,
			height:            40,
			imgProtocol:       protocol,
			imagesEnabled:     true,
			previewPoster:     "IMAGE-POSTER",
			previewPosterOpID: 3,
			previewOverview:   "old plot",
		}

		before := m.overlayLayoutKey()
		if before == "" {
			t.Fatalf("%v: preview poster should produce an overlay layout key", protocol)
		}

		_, cmd := m.Update(previewDetailsMsg{opID: 3, overview: "new plot"})
		if !cmdContainsClearScreen(cmd) {
			t.Fatalf("%v: preview metadata change must request a screen clear", protocol)
		}
		if m.overlayLayoutKey() == before {
			t.Errorf("%v: metadata change left the overlay layout key unchanged", protocol)
		}
	}
}

// TestOverlayPreviewChurnDoesNotScreenClear keeps the clear off the steady
// state: sources arriving and status/spinner ticks render below or outside
// the poster, so they must not force a repaint.
func TestOverlayPreviewChurnDoesNotScreenClear(t *testing.T) {
	m := &modelImpl{
		activeView:        viewPreview,
		width:             120,
		height:            40,
		imgProtocol:       termimg.ProtocolSixel,
		imagesEnabled:     true,
		previewPoster:     "IMAGE-POSTER",
		previewPosterOpID: 3,
		previewOverview:   "plot",
	}

	before := m.overlayLayoutKey()
	m.loadingProviders = 3
	m.totalProviders = 5
	m.statusText = "searching providers"
	m.previewSkipInfo = ""
	if got := m.overlayLayoutKey(); got != before {
		t.Error("source/status churn changed the overlay layout key")
	}
}

// TestReaderHelpReplacesPage pins the reader help contract: ? draws the help
// overlay in the page slot, so the page beneath it is no longer an on-screen
// placement (Kitty must delete it, Sixel/iTerm2 must clear).
func TestReaderHelpReplacesPage(t *testing.T) {
	m := &modelImpl{
		activeView:   viewReader,
		width:        120,
		height:       40,
		showHelp:     true,
		readerPage:   0,
		readerRender: map[int]string{0: "PAGE-ART"},
	}

	if m.readerPageVisible() {
		t.Error("reader page must not count as visible while help replaces it")
	}
	if got := m.overlayLayoutKey(); got != "" {
		t.Errorf("overlay layout key = %q, want empty with only a hidden page", got)
	}
}

func TestReaderHelpRendersInPageSlot(t *testing.T) {
	m := mangaTestModel()
	m.activeView = viewReader
	m.showHelp = true
	m.pages = []provider.MangaPage{{URL: "u"}}
	m.readerRender[0] = "PAGE-ART"

	out := m.renderReaderFullscreen()
	if strings.Contains(out, "PAGE-ART") {
		t.Error("reader page must be replaced by the help overlay")
	}
	if !strings.Contains(out, "Reader") {
		t.Errorf("help overlay missing from the reader page slot:\n%s", out)
	}
	if got := strings.Count(out, "\n") + 1; got != m.height {
		t.Errorf("reader help = %d lines, want %d", got, m.height)
	}
}

func TestOverlayLayoutKeyTracksImageOnly(t *testing.T) {
	m := &modelImpl{
		activeView:  viewReader,
		width:       120,
		height:      40,
		imgProtocol: termimg.ProtocolSixel,
		readerPage:  0,
		readerCols:  100,
		readerRows:  38,
		readerRender: map[int]string{
			0: "PAGE-ZERO",
			1: "PAGE-ONE",
		},
	}
	first := m.overlayLayoutKey()
	if first == "" {
		t.Fatal("reader page should produce an overlay layout key")
	}

	// Status/spinner churn must not force a clear.
	m.statusText = "syncing"
	if got := m.overlayLayoutKey(); got != first {
		t.Error("non-image state changed the overlay layout key")
	}

	// Turning the page moves the placement, so the key must change.
	m.readerPage = 1
	if got := m.overlayLayoutKey(); got == first {
		t.Error("page change left the overlay layout key unchanged")
	}
}

// TestPosterVisibilityWidthGates pins the visibility helpers to the width
// conditions in the renderers. Counting a layout-hidden poster as an
// on-screen placement would make a plain resize clear the screen for an
// image that was never drawn.
func TestPosterVisibilityWidthGates(t *testing.T) {
	keyIncludes := func(m *modelImpl, slot string) bool {
		return strings.Contains(m.overlayLayoutKey(), "|"+slot+":")
	}

	t.Run("search results", func(t *testing.T) {
		build := func(width int) *modelImpl {
			return &modelImpl{
				activeView:       viewSearch,
				width:            width,
				height:           40,
				imgProtocol:      termimg.ProtocolSixel,
				imagesEnabled:    true,
				searchPoster:     "ART",
				searchPosterOpID: 3,
				seriesResults:    []provider.SearchResult{{Title: "Frieren"}},
			}
		}
		// The results renderer needs TermWidth >= 90 or content width >= 70,
		// which starts at terminal width 78.
		if m := build(77); m.searchPosterVisible() || keyIncludes(m, "search") {
			t.Error("width 77 must hide the results poster")
		}
		if m := build(78); !m.searchPosterVisible() || !keyIncludes(m, "search") {
			t.Error("width 78 must show the results poster")
		}
		if m := build(90); !m.searchPosterVisible() || !keyIncludes(m, "search") {
			t.Error("width 90 must show the results poster")
		}

		// An empty result list renders the empty state, which has no poster.
		m := build(120)
		m.seriesResults = nil
		if m.searchPosterVisible() || keyIncludes(m, "search") {
			t.Error("empty results must hide the poster")
		}
	})

	t.Run("episodes", func(t *testing.T) {
		build := func(width int) *modelImpl {
			return &modelImpl{
				activeView:       viewEpisodes,
				width:            width,
				height:           40,
				imgProtocol:      termimg.ProtocolSixel,
				imagesEnabled:    true,
				searchPoster:     "ART",
				searchPosterOpID: 3,
			}
		}
		if m := build(79); m.searchPosterVisible() || keyIncludes(m, "search") {
			t.Error("width 79 must hide the episodes poster")
		}
		if m := build(80); !m.searchPosterVisible() || !keyIncludes(m, "search") {
			t.Error("width 80 must show the episodes poster")
		}
	})

	t.Run("preview", func(t *testing.T) {
		build := func(width int) *modelImpl {
			return &modelImpl{
				activeView:        viewPreview,
				width:             width,
				height:            40,
				imgProtocol:       termimg.ProtocolSixel,
				imagesEnabled:     true,
				previewPoster:     "ART",
				previewPosterOpID: 3,
			}
		}
		// Mirrors showPreviewPoster: TermWidth >= 80 or content width >= 70,
		// which starts at terminal width 78.
		if m := build(77); m.previewPosterVisible() || keyIncludes(m, "preview") {
			t.Error("width 77 must hide the preview poster")
		}
		if m := build(78); !m.previewPosterVisible() || !keyIncludes(m, "preview") {
			t.Error("width 78 must show the preview poster")
		}
	})

	t.Run("history", func(t *testing.T) {
		build := func(width int) *modelImpl {
			return &modelImpl{
				activeView:        viewHistory,
				width:             width,
				height:            40,
				imgProtocol:       termimg.ProtocolSixel,
				imagesEnabled:     true,
				historyPoster:     "ART",
				historyPosterOpID: 3,
			}
		}
		// Mirrors showPosterPane: content width >= 100, i.e. terminal width
		// 108 with the 4-column gutters.
		if m := build(107); m.historyPosterVisible() || keyIncludes(m, "history") {
			t.Error("width 107 must hide the history poster")
		}
		if m := build(108); !m.historyPosterVisible() || !keyIncludes(m, "history") {
			t.Error("width 108 must show the history poster")
		}
	})
}
