package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
)

// mangaTestModel builds a minimal model for chapter/reader render tests.
func mangaTestModel() *modelImpl {
	return &modelImpl{
		registry:     &provider.Registry{},
		width:        100,
		height:       30,
		chapterList:  list.New(nil, list.NewDefaultDelegate(), 80, 16),
		readerRender: make(map[int]string),
	}
}

// TestChaptersScreenOrdering pins the stacked layout: title first,
// chapter rows before the Story synopsis.
func TestChaptersScreenOrdering(t *testing.T) {
	m := mangaTestModel()
	m.selectedSeries = &provider.SearchResult{
		Title: "Kingdom", Provider: "mangadex",
		Overview: "Historical epic.",
		Genres:   []string{"Historical", "Action"},
	}
	m.chapters = []provider.MangaChapter{
		{ID: "c1", Number: "1", Title: "Start"},
		{ID: "c2", Number: "2", Title: "Next"},
	}
	m.chapterList.SetItems(chaptersToItems(m.chapters))

	out := RenderChaptersScreen(m.collectChaptersData(100, 24, ResolveAccent(model.KindManga, "Auto")))
	titleIdx := strings.Index(out, "Kingdom")
	chIdx := strings.Index(out, "Ch 1")
	storyIdx := strings.Index(out, "Story")
	if titleIdx < 0 || chIdx < 0 || storyIdx < 0 {
		t.Fatalf("missing sections:\n%s", out)
	}
	if !(titleIdx < chIdx && chIdx < storyIdx) {
		t.Errorf("wrong section order: title=%d chapters=%d story=%d", titleIdx, chIdx, storyIdx)
	}
	if strings.Contains(out, "\x1b_G") || strings.Contains(out, "\x1bP0;1q") {
		t.Error("chapters screen must not contain image escapes")
	}
}

// TestReaderFullscreenLines pins the fullscreen contract: exactly
// m.height lines, all chrome on opaque black.
func TestReaderFullscreenLines(t *testing.T) {
	m := mangaTestModel()
	m.selectedSeries = &provider.SearchResult{Title: "Kingdom"}
	m.selectedChapter = &provider.MangaChapter{Number: "1", Title: "Start"}
	m.pages = []provider.MangaPage{{URL: "https://cdn.example/p1.jpg"}}

	out := m.renderReaderFullscreen()
	if got := strings.Count(out, "\n") + 1; got != m.height {
		t.Errorf("reader = %d lines, want %d", got, m.height)
	}
	if !strings.Contains(out, "Page 1/1") {
		t.Error("reader footer missing page indicator")
	}
	if !strings.Contains(out, "esc back") {
		t.Error("reader footer missing controls hint")
	}
}

// TestReaderFullscreenPageLines pins the page box: a cached render with
// exactly boxH lines keeps the total at m.height.
func TestReaderFullscreenPageLines(t *testing.T) {
	m := mangaTestModel()
	m.selectedSeries = &provider.SearchResult{Title: "Kingdom"}
	m.selectedChapter = &provider.MangaChapter{Number: "1"}
	m.pages = []provider.MangaPage{{URL: "u"}}
	boxH := m.height - readerChromeRows
	lines := make([]string, boxH)
	for i := range lines {
		lines[i] = strings.Repeat("x", m.width)
	}
	m.readerRender[0] = strings.Join(lines, "\n")

	out := m.renderReaderFullscreen()
	if got := strings.Count(out, "\n") + 1; got != m.height {
		t.Errorf("reader with page = %d lines, want %d", got, m.height)
	}
}

// TestSaveMangaProgressRoundtrip pins the history mapping: one entry
// per series keyed by title, chapter number in EpisodeTitle, page and
// page count in Position/Duration.
func TestSaveMangaProgressRoundtrip(t *testing.T) {
	store, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	// Saves run on a background goroutine: Close before TempDir cleanup
	// or the async write races the directory removal (flaky unlinkat).
	defer store.Close()
	m := mangaTestModel()
	m.historyStore = store
	m.selectedSeries = &provider.SearchResult{Title: "One Piece", Provider: "weebcentral"}
	m.selectedChapter = &provider.MangaChapter{ID: "c5", Number: "5", Provider: "weebcentral"}

	m.saveMangaProgress(7, 24)

	entry, ok := store.Get(history.EntryKey{Title: "One Piece", Mode: "manga", MediaType: "manga"})
	if !ok {
		t.Fatalf("no history entry recorded")
	}
	if entry.EpisodeTitle != "5" || entry.PositionSecs != 7 || entry.DurationSecs != 24 {
		t.Errorf("entry = %+v, want chapter 5 page 7/24", entry)
	}
	if entry.Mode != string(provider.ModeManga) || entry.MediaType != provider.MediaTypeManga {
		t.Errorf("entry typing = %+v", entry)
	}
}

// TestSaveMangaProgressNilStore pins the fire-and-forget contract: no
// history store, no panic, no-op.
func TestSaveMangaProgressNilStore(t *testing.T) {
	m := mangaTestModel()
	m.selectedSeries = &provider.SearchResult{Title: "T"}
	m.selectedChapter = &provider.MangaChapter{Number: "1"}
	m.saveMangaProgress(1, 10)
}

// TestHistoryEntryTagManga pins the "Ch N" tag shown beside manga
// history rows.
func TestHistoryEntryTagManga(t *testing.T) {
	entry := history.Entry{Mode: string(provider.ModeManga), EpisodeTitle: "1191"}
	if got := historyEntryTag(entry); got != "Ch 1191" {
		t.Errorf("tag = %q, want Ch 1191", got)
	}
}

// TestHistoryMangaResumeText pins the history row copy: page counts,
// never durations, for manga entries.
func TestHistoryMangaResumeText(t *testing.T) {
	groups := history.BuildGroups([]history.Entry{{
		Key:          history.EntryKey{Title: "One Piece", Mode: "manga", MediaType: "manga"},
		Title:        "One Piece",
		EpisodeTitle: "1191",
		PositionSecs: 5,
		DurationSecs: 24,
		Mode:         "manga",
		MediaType:    "manga",
		WatchedAt:    time.Now(),
	}})
	items := historyTabItems(groups, false)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 row", len(items))
	}
	row, _ := items[0].(rowItem)
	if !strings.Contains(row.desc, "p5/24") || !strings.Contains(row.desc, "Ch 1191") {
		t.Errorf("row desc = %q, want page resume + chapter tag", row.desc)
	}
	if !strings.Contains(row.title, "One Piece") {
		t.Errorf("row title = %q", row.title)
	}
}

// TestMangaChapterIndex pins resume matching by catalog number,
// including fractional chapters and the empty-number miss.
func TestMangaChapterIndex(t *testing.T) {
	chapters := []provider.MangaChapter{{Number: "1"}, {Number: "12.5"}, {Number: ""}}
	if idx, ok := mangaChapterIndex(chapters, "12.5"); !ok || idx != 1 {
		t.Errorf("fractional match = %d, %v", idx, ok)
	}
	if _, ok := mangaChapterIndex(chapters, ""); ok {
		t.Errorf("empty number must not match")
	}
	if _, ok := mangaChapterIndex(chapters, "999"); ok {
		t.Errorf("missing number must not match")
	}
}

// TestOnChaptersDoneConsumesResume pins the resume jump: with a stashed
// target the listing opens the saved chapter and preserves the page
// for onPagesDone, then clears the stash.
func TestOnChaptersDoneConsumesResume(t *testing.T) {
	m := mangaTestModel()
	m.chaptersOpID = 7
	m.pendingMangaResume = &mangaResume{number: "2", page: 3}
	m.selectedSeries = &provider.SearchResult{Title: "T", Provider: "stub"}

	_, _ = m.onChaptersDone(chaptersDoneMsg{
		opID: 7,
		results: []provider.MangaChapter{
			{ID: "c1", Number: "1"},
			{ID: "c2", Number: "2"},
		},
	})

	if m.selectedChapter == nil || m.selectedChapter.Number != "2" {
		t.Fatalf("selected = %+v, want chapter 2", m.selectedChapter)
	}
	if m.readerResumePage != 2 {
		t.Errorf("resume page = %d, want 2 (page 3, 0-based)", m.readerResumePage)
	}
	if m.pendingMangaResume != nil {
		t.Errorf("stash must clear after consuming")
	}
}

func TestSelectChapter_FilteredIndexRegression(t *testing.T) {
	m := mangaTestModel()
	m.chapters = []provider.MangaChapter{
		{ID: "ch-1", Number: "1", Title: "First"},
		{ID: "ch-2", Number: "2", Title: "Second"},
		{ID: "ch-3", Number: "3", Title: "Third"},
	}
	m.chapterList.SetItems(chaptersToItems(m.chapters))

	// Simulate user filtering for "Third": list contains only the filtered rowItem
	items := []list.Item{
		rowItem{index: 2, title: "Ch 3 — Third", key: "ch-3"},
	}
	m.chapterList.SetItems(items)
	m.chapterList.Select(0)

	idx := m.selectedChapterIndex()
	if idx != 2 {
		t.Fatalf("selectedChapterIndex() = %d, want 2 (mapped from rowItem.index)", idx)
	}

	_, _ = m.selectChapter(idx)
	if m.selectedChapter == nil || m.selectedChapter.ID != "ch-3" {
		t.Errorf("expected selectedChapter ch-3, got %+v", m.selectedChapter)
	}
}
