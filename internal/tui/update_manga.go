package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/provider"
	"kari/internal/termimg"
)

// mangaPageTimeout bounds a single page fetch+render worker.
const mangaPageTimeout = 45 * time.Second

// readerPrefetch counts how many pages ahead of the current one are
// rendered in the background, so page turns usually hit a ready string.
const readerPrefetch = 2

// maxAutoAdvance bounds automatic skips over removed-upload chapters:
// enough to walk past a run of dead scanlations, never an endless hunt.
const maxAutoAdvance = 3

// isMangaProvider reports whether the named provider serves paged
// manga/comics (rather than video). The TUI branches on this capability,
// never on provider names or modes.
func (m *modelImpl) isMangaProvider(name string) bool {
	p, ok := m.registry.ProviderByName(name)
	if !ok {
		return false
	}
	_, ok = p.(provider.MangaSource)
	return ok
}

// selectMangaSeries starts the chapter listing for a manga/comic title.
func (m *modelImpl) selectMangaSeries(idx int) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.seriesResults) {
		m.setStatus(statusError, "Series selection out of range")
		return m, nil
	}
	if !m.guardLoad() {
		return m, nil
	}
	m.selectedSeries = &m.seriesResults[idx]
	m.searchIndex = idx
	m.chapters = nil
	m.selectedChapter = nil
	m.chapterIndex = 0
	m.autoAdvance = 0
	m.fallbackTried = false
	m.pages = nil
	m.clearReader()
	m.loading = true
	m.loadingText = "Loading chapters..."
	m.setStatus(statusInfo, "")
	opID := m.newOpID()
	m.chaptersOpID = opID
	m.pushView(viewChapters)
	return m, tea.Batch(m.spinner.Tick, m.chaptersCmd(opID, *m.selectedSeries))
}

// chaptersCmd fetches the English chapter listing for the series.
func (m *modelImpl) chaptersCmd(opID int, series provider.SearchResult) tea.Cmd {
	return func() tea.Msg {
		if m.mangaService == nil {
			return chaptersDoneMsg{opID: opID, err: context.Canceled}
		}
		results, err := m.mangaService.FetchChapters(m.appCtx, series)
		return chaptersDoneMsg{results: results, opID: opID, err: err}
	}
}

// onChaptersDone installs the chapter listing or reports the failure.
func (m *modelImpl) onChaptersDone(msg chaptersDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.chaptersOpID {
		return m, nil
	}
	m.chaptersOpID = 0
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		return m, m.setStatusTimed(statusError, "Chapters failed: "+msg.err.Error())
	}
	m.chapters = msg.results
	m.chapterList.SetItems(chaptersToItems(m.chapters))
	m.chapterList.Select(0)
	// A history resume stashed its chapter/page before the listing
	// arrived: jump straight in, keeping the saved page. A renamed or
	// removed chapter just leaves the list for manual picking.
	if m.pendingMangaResume != nil {
		target := m.pendingMangaResume
		m.pendingMangaResume = nil
		if idx, ok := mangaChapterIndex(m.chapters, target.number); ok {
			mdl, cmd := m.selectChapter(idx)
			if target.page > 1 {
				m.readerResumePage = target.page - 1
			}
			return mdl, cmd
		}
		return m, m.setStatusTimed(statusInfo, "Saved chapter not found — pick one to continue")
	}
	return m, nil
}

// selectedChapterIndex resolves the chapter list cursor to a result index.
func (m *modelImpl) selectedChapterIndex() int {
	if item, ok := m.chapterList.SelectedItem().(rowItem); ok {
		return item.index
	}
	return m.chapterList.Index()
}

// mangaChapterIndex locates a chapter by its catalog number. Empty
// numbers never match.
func mangaChapterIndex(chapters []provider.MangaChapter, number string) (int, bool) {
	number = strings.TrimSpace(number)
	if number == "" {
		return 0, false
	}
	for i, ch := range chapters {
		if strings.TrimSpace(ch.Number) == number {
			return i, true
		}
	}
	return 0, false
}

// resumeMangaSeries enters the chapters screen for a history-resumed
// manga title, stashing the saved chapter/page for onChaptersDone. It
// mirrors selectMangaSeries without a result-list index.
func (m *modelImpl) resumeMangaSeries(entry history.Entry, series provider.SearchResult) (tea.Model, tea.Cmd) {
	if !m.guardLoad() {
		return m, nil
	}
	m.selectedSeries = &series
	m.chapters = nil
	m.selectedChapter = nil
	m.chapterIndex = 0
	m.autoAdvance = 0
	m.fallbackTried = false
	m.pages = nil
	m.clearReader()
	m.pendingMangaResume = &mangaResume{
		number: strings.TrimSpace(entry.EpisodeTitle),
		page:   int(entry.PositionSecs),
	}
	m.loading = true
	m.loadingText = "Loading chapters..."
	m.setStatus(statusInfo, "")
	opID := m.newOpID()
	m.chaptersOpID = opID
	m.pushView(viewChapters)
	return m, tea.Batch(m.spinner.Tick, m.chaptersCmd(opID, series))
}

// selectChapter fetches a chapter's pages and enters the reader.
func (m *modelImpl) selectChapter(idx int) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.chapters) {
		m.setStatus(statusError, "Chapter selection out of range")
		return m, nil
	}
	if !m.guardLoad() {
		return m, nil
	}
	return m.startChapterLoad(idx)
}

// startChapterLoad fetches pages without the user guard: completion
// chains (dead-upload auto-advance) arrive mid-load by design.
func (m *modelImpl) startChapterLoad(idx int) (tea.Model, tea.Cmd) {
	m.selectedChapter = &m.chapters[idx]
	m.chapterIndex = idx
	m.pages = nil
	m.clearReader()
	m.loading = true
	m.loadingText = "Loading pages..."
	m.setStatus(statusInfo, "")
	opID := m.newOpID()
	m.pagesOpID = opID
	return m, tea.Batch(m.spinner.Tick, m.pagesCmd(opID, *m.selectedChapter))
}

// pagesCmd resolves a chapter's page-image URLs.
func (m *modelImpl) pagesCmd(opID int, chapter provider.MangaChapter) tea.Cmd {
	return func() tea.Msg {
		if m.mangaService == nil {
			return pagesDoneMsg{opID: opID, err: context.Canceled}
		}
		pages, err := m.mangaService.FetchPages(m.appCtx, chapter)
		return pagesDoneMsg{chapter: chapter, pages: pages, opID: opID, err: err}
	}
}

// onPagesDone enters the reader on page one and kicks off rendering
// plus prefetch of the following pages.
func (m *modelImpl) onPagesDone(msg pagesDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.pagesOpID {
		return m, nil
	}
	m.pagesOpID = 0
	if msg.err != nil {
		m.loading = false
		m.loadingText = ""
		// Removed uploads read as "unavailable — try another chapter",
		// not a raw HTTP status. Dead runs auto-advance to the next
		// chapter (bounded), so one removed upload doesn't end the
		// session on titles mixing live and dead scanlations.
		if errors.Is(msg.err, provider.ErrNoSources) {
			if m.autoAdvance < maxAutoAdvance && m.chapterIndex+1 < len(m.chapters) {
				m.autoAdvance++
				skipped := ""
				if m.selectedChapter != nil {
					skipped = m.selectedChapter.DisplayLabel()
				}
				m.setStatus(statusInfo, fmt.Sprintf("%s unavailable — trying next…", skipped))
				return m.startChapterLoad(m.chapterIndex + 1)
			}
			// Same-provider options exhausted: look for the same chapter
			// readable on any other source before giving up.
			if !m.fallbackTried && m.selectedSeries != nil && m.selectedChapter != nil {
				m.fallbackTried = true
				m.loading = true
				m.loadingText = "Trying other sources…"
				opID := m.newOpID()
				m.pagesOpID = opID
				return m, tea.Batch(m.spinner.Tick, m.fallbackCmd(opID, *m.selectedSeries, *m.selectedChapter))
			}
			return m, m.setStatusTimed(statusWarn, "No readable upload on any source — try another chapter")
		}
		return m, m.setStatusTimed(statusError, "Pages failed: "+msg.err.Error())
	}
	m.autoAdvance = 0
	if len(msg.pages) == 0 {
		m.loading = false
		m.loadingText = ""
		return m, m.setStatusTimed(statusWarn, "No pages in this chapter")
	}
	m.pages = msg.pages
	m.pushView(viewReader)
	// A mid-read re-resolution lands back where the reader was; stale
	// renders from the dead URLs are dropped and in-flight renders for
	// them discarded via the bumped op ID.
	m.readerRender = make(map[int]string)
	m.readerOpID = m.newOpID()
	target := 0
	if m.readerResumePage >= 0 && m.readerResumePage < len(m.pages) {
		target = m.readerResumePage
	}
	m.readerResumePage = -1
	return m, m.gotoReaderPage(target)
}

// fallbackCmd searches every manga source for the same chapter
// readable elsewhere.
func (m *modelImpl) fallbackCmd(opID int, series provider.SearchResult, chapter provider.MangaChapter) tea.Cmd {
	return func() tea.Msg {
		if m.mangaService == nil {
			return fallbackDoneMsg{opID: opID, err: context.Canceled}
		}
		readable, err := m.mangaService.ResolveReadableChapter(m.appCtx, series, chapter)
		return fallbackDoneMsg{readable: readable, opID: opID, err: err}
	}
}

// onFallbackDone installs a cross-source chapter (new series context,
// listing, and pages) and enters the reader, or reports that no source
// can serve it.
func (m *modelImpl) onFallbackDone(msg fallbackDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.pagesOpID {
		return m, nil
	}
	m.pagesOpID = 0
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		return m, m.setStatusTimed(statusWarn, "No readable upload on any source — try another chapter")
	}
	m.selectedSeries = &msg.readable.Series
	m.chapters = msg.readable.Chapters
	m.chapterList.SetItems(chaptersToItems(m.chapters))
	m.selectedChapter = &msg.readable.Chapters[msg.readable.Index]
	m.chapterIndex = msg.readable.Index
	m.chapterList.Select(msg.readable.Index)
	m.pages = msg.readable.Pages
	m.setStatus(statusSuccess, fmt.Sprintf("Reading via %s", m.registry.DisplayName(msg.readable.Series.Provider)))
	m.pushView(viewReader)
	return m, m.gotoReaderPage(0)
}

// clearReader drops rendered pages and bumps the reader op ID, which
// discards any render still in flight for the previous chapter/size.
func (m *modelImpl) clearReader() {
	m.readerOpID = m.newOpID()
	m.readerPage = 0
	m.readerRender = make(map[int]string)
	m.readerCols = 0
	m.readerRows = 0
	m.readerUnavailable = false
	m.readerRelisted = false
	m.readerResumePage = -1
}

// readerDims derives the page-image box at full terminal size: the
// reader bypasses the app chrome, so the box spans the whole width with
// only its own header/footer rows reserved.
func (m *modelImpl) readerDims() (cols, rows int) {
	return max(20, m.width), max(5, m.height-readerChromeRows)
}

// gotoReaderPage moves to page idx, rendering it (and prefetching ahead)
// when not already cached.
func (m *modelImpl) gotoReaderPage(idx int) tea.Cmd {
	if len(m.pages) == 0 {
		return nil
	}
	// Without a renderable protocol (or with images off) no page can ever
	// render — say so once instead of spinning on "Loading page…".
	// Terminals with no graphics protocol still get quadrant-block art
	// via effectiveImgProtocol, so this only fires when images are off.
	if m.effectiveImgProtocol() == termimg.ProtocolNone || !m.imagesEnabled {
		m.loading = false
		m.loadingText = ""
		m.readerUnavailable = true
		return m.setStatusTimed(statusWarn, "Image display unavailable — enable images or use a graphics-capable terminal")
	}
	idx = min(max(0, idx), len(m.pages)-1)
	m.readerPage = idx
	m.readerUnavailable = false
	cols, rows := m.readerDims()
	if cols != m.readerCols || rows != m.readerRows {
		m.readerRender = make(map[int]string)
		m.readerCols, m.readerRows = cols, rows
	}
	var cmds []tea.Cmd
	for i := idx; i <= min(idx+readerPrefetch, len(m.pages)-1); i++ {
		if _, ok := m.readerRender[i]; !ok {
			cmds = append(cmds, m.readerPageCmd(m.readerOpID, i, cols, rows, m.width, m.height))
		}
	}
	if len(cmds) == 0 {
		m.loading = false
		m.loadingText = ""
		return nil
	}
	m.loading = true
	m.loadingText = "Loading page..."
	return tea.Batch(append(cmds, m.spinner.Tick)...)
}

// saveMangaProgress records the viewed page for resume as one entry
// per series: chapter numbers are fractional strings ("12.5"), so the
// number rides EpisodeTitle while Position/Duration carry page and
// page count. Fire-and-forget; a nil store means history is
// unavailable this session.
func (m *modelImpl) saveMangaProgress(page, total int) {
	if m.historyStore == nil || m.selectedSeries == nil || m.selectedChapter == nil || total <= 0 {
		return
	}
	_ = m.historyStore.Upsert(history.Entry{
		Key: history.EntryKey{
			Title:     m.selectedSeries.Title,
			Mode:      string(provider.ModeManga),
			MediaType: provider.MediaTypeManga,
		},
		Title:        m.selectedSeries.Title,
		EpisodeTitle: strings.TrimSpace(m.selectedChapter.Number),
		PositionSecs: float64(page),
		DurationSecs: float64(total),
		WatchedAt:    time.Now(),
		Mode:         string(provider.ModeManga),
		MediaType:    provider.MediaTypeManga,
	})
}

// readerPageCmd fetches, decodes, and terminal-renders one page.
// termCols/termRows are captured at dispatch so the render worker measures
// the real cell size instead of the 8x16 fallback.
func (m *modelImpl) readerPageCmd(opID, idx, cols, rows, termCols, termRows int) tea.Cmd {
	page := m.pages[idx]
	chapter := m.chapterIndex
	return func() tea.Msg {
		if m.mangaClient == nil || m.effectiveImgProtocol() == termimg.ProtocolNone {
			return readerPageMsg{chapter: chapter, index: idx, cols: cols, rows: rows, opID: opID, err: context.Canceled}
		}
		ctx, cancel := context.WithTimeout(m.appCtx, mangaPageTimeout)
		defer cancel()
		img, err := m.mangaClient.FetchImage(ctx, page)
		if err != nil {
			return readerPageMsg{chapter: chapter, index: idx, cols: cols, rows: rows, opID: opID, err: err}
		}
		rendered, err := termimg.RenderPage(img, m.effectiveImgProtocol(), cols, rows, kittyReaderImageID, termCols, termRows)
		return readerPageMsg{chapter: chapter, index: idx, cols: cols, rows: rows, render: rendered, opID: opID, err: err}
	}
}

// onReaderPage installs a rendered page, clearing the loading state once
// the current page arrives and prefetching further ahead from it.
func (m *modelImpl) onReaderPage(msg readerPageMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.readerOpID || msg.chapter != m.chapterIndex {
		return m, nil
	}
	if msg.cols != m.readerCols || msg.rows != m.readerRows {
		return m, nil
	}
	if msg.err != nil {
		if msg.index == m.readerPage {
			// Upload replaced mid-read: re-resolve the chapter's
			// pages once (bounded), keeping position. A repeated
			// failure flows into the chapter-level recovery in
			// onPagesDone (next chapter, then other sources).
			if errors.Is(msg.err, provider.ErrNoSources) && !m.readerRelisted && m.selectedChapter != nil {
				m.readerRelisted = true
				m.readerResumePage = m.readerPage
				m.loading = true
				m.loadingText = "Refreshing pages..."
				opID := m.newOpID()
				m.pagesOpID = opID
				return m, tea.Batch(m.spinner.Tick, m.pagesCmd(opID, *m.selectedChapter))
			}
			m.loading = false
			m.loadingText = ""
			m.readerUnavailable = true
			return m, m.setStatusTimed(statusError, "Page failed: "+msg.err.Error())
		}
		return m, nil
	}
	m.readerRender[msg.index] = msg.render
	if msg.index == m.readerPage {
		m.loading = false
		m.loadingText = ""
		// Only the viewed page counts as progress: prefetched renders
		// landing early must not bookmark pages the user never saw.
		m.saveMangaProgress(msg.index+1, len(m.pages))
	}
	// Keep the prefetch window full as pages land.
	next := msg.index + 1
	if next <= m.readerPage+readerPrefetch && next < len(m.pages) {
		if _, ok := m.readerRender[next]; !ok {
			return m, m.readerPageCmd(m.readerOpID, next, m.readerCols, m.readerRows, m.width, m.height)
		}
	}
	return m, nil
}

// stepReaderChapter moves to the adjacent chapter, staying on the first
// or last page edge when crossing the boundary. Explicit steps reset
// the auto-advance run.
func (m *modelImpl) stepReaderChapter(delta int) (tea.Model, tea.Cmd) {
	next := m.chapterIndex + delta
	if next < 0 || next >= len(m.chapters) {
		m.setStatus(statusWarn, "No further chapters")
		return m, nil
	}
	m.autoAdvance = 0
	m.fallbackTried = false
	return m.selectChapter(next)
}
