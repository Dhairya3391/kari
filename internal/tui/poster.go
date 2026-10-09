package tui

import (
	"context"
	"fmt"
	"image"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/termimg"
)

const (
	searchPosterMaxCols  = 26
	searchPosterMaxRows  = 16
	previewPosterMaxCols = 36
	previewPosterMaxRows = 18
	historyPosterMaxCols = 26
	historyPosterMaxRows = 16

	posterFetchTimeout = 8 * time.Second

	// Stable per-slot image ids. Kept distinct (and never reused for
	// anything else) so Cleanup can target exactly one slot's placement
	// — e.g. clearing the search-screen poster when navigating to a screen
	// that doesn't show one, without touching the preview-screen poster.
	// Only the Kitty protocol needs these; the rest ignore them.
	kittySearchImageID  uint32 = 1
	kittyPreviewImageID uint32 = 2
	kittyHistoryImageID uint32 = 4
)

// clearSearchPoster resets the search poster slot's state, e.g. when the
// result list is cleared or the mode is switched — otherwise the last
// poster shown just keeps sitting there (as leftover text, or for Kitty a
// leftover placement, since nothing renders in its place to prompt cleanup)
// even though it no longer corresponds to anything on screen. Bumping the
// opID also discards any fetch still in flight for the old results.
func (m *modelImpl) clearSearchPoster() {
	if m.searchPosterCancel != nil {
		m.searchPosterCancel()
		m.searchPosterCancel = nil
	}
	m.searchPosterOpID++
	m.searchPoster = ""
	m.searchPosterUnavailable = false
}

// triggerSearchPoster starts fetching (or serves from cache) the poster for
// the search result at idx, to be shown in the search screen's right panel.
func (m *modelImpl) triggerSearchPoster(idx int) tea.Cmd {
	m.clearSearchPoster()
	if idx < 0 || idx >= len(m.seriesResults) {
		return nil
	}
	result := m.seriesResults[idx]
	return m.fetchPosterCmd(posterSlotSearch, m.searchPosterOpID, result.TMDBID, result.MediaType, result.Title, result.CoverURL, result.CoverReferer, searchPosterMaxCols, searchPosterMaxRows, kittySearchImageID)
}

// clearPreviewPoster resets the preview poster slot's state — call this
// anywhere m.resolved is reset to nil, so a stale poster (and stale
// overview/genres) from whatever was resolved before doesn't keep showing
// while the new one loads (or if the new one never gets one).
func (m *modelImpl) clearPreviewPoster() {
	if m.previewPosterCancel != nil {
		m.previewPosterCancel()
		m.previewPosterCancel = nil
	}
	m.previewPosterOpID++
	m.previewPoster = ""
	m.previewPosterUnavailable = false
	m.previewOverview = ""
	m.previewGenres = nil
	m.previewRating = ""
}

// prefetchPreviewPoster warms the poster caches from the selected series
// at resolve start, before the first source snapshot arrives. The later
// triggerPreviewPoster owns slot lifecycle (it bumps the opID, so a slow
// prefetch never paints stale art), but a fast prefetch — almost always
// a memory/disk cache hit — shows artwork one round-trip sooner, and a
// slow one still warms the caches the trigger reads.
func (m *modelImpl) prefetchPreviewPoster() tea.Cmd {
	if m.selectedSeries == nil {
		return nil
	}
	s := *m.selectedSeries
	return m.fetchPosterCmd(posterSlotPreview, m.previewPosterOpID, s.TMDBID, s.MediaType, s.Title, s.CoverURL, s.CoverReferer, previewPosterMaxCols, previewPosterMaxRows, kittyPreviewImageID)
}

// triggerPreviewPoster starts fetching (or serves from cache) the poster for
// the currently resolved media, shown on the preview screen.
func (m *modelImpl) triggerPreviewPoster() tea.Cmd {
	if m.resolved == nil {
		return nil
	}
	if m.previewPoster != "" {
		return nil
	}
	m.clearPreviewPoster()
	coverURL := ""
	coverReferer := ""
	title := m.resolved.SeriesTitle
	tmdbID := m.resolved.TMDBID
	mediaType := m.resolved.MediaType
	if m.selectedSeries != nil {
		coverURL = m.selectedSeries.CoverURL
		coverReferer = m.selectedSeries.CoverReferer
		if title == "" {
			title = m.selectedSeries.Title
		}
		if tmdbID == 0 {
			tmdbID = m.selectedSeries.TMDBID
		}
		if mediaType == "" {
			mediaType = m.selectedSeries.MediaType
		}
	}
	return m.fetchPosterCmd(posterSlotPreview, m.previewPosterOpID, tmdbID, mediaType, title, coverURL, coverReferer, previewPosterMaxCols, previewPosterMaxRows, kittyPreviewImageID)
}

// clearHistoryPoster resets the history poster slot's state, e.g. when
// the selection moves or the list rebuilds — otherwise the previous
// title's poster lingers next to the new selection. Bumping the opID
// also discards any fetch still in flight for the old row.
func (m *modelImpl) clearHistoryPoster() {
	if m.historyPosterCancel != nil {
		m.historyPosterCancel()
		m.historyPosterCancel = nil
	}
	m.historyPosterOpID++
	m.historyPoster = ""
	m.historyPosterUnavailable = false
}

// triggerHistoryPoster starts fetching the poster for the currently
// selected history row (headers resolve to nothing and just clear the
// slot), shown in the history screen's right panel.
func (m *modelImpl) triggerHistoryPoster() tea.Cmd {
	m.clearHistoryPoster()
	item, ok := m.historyList.SelectedItem().(rowItem)
	if !ok {
		return nil
	}
	var group *history.Group
	for i := range m.historyGroups {
		if m.historyGroups[i].Key.String() == item.key {
			group = &m.historyGroups[i]
			break
		}
	}
	if group == nil {
		return nil
	}
	entry := group.ContinueEntry
	return m.fetchPosterCmd(posterSlotHistory, m.historyPosterOpID, entry.TMDBID, group.MediaType, group.Title, "", "", historyPosterMaxCols, historyPosterMaxRows, kittyHistoryImageID)
}

// triggerPreviewDetails starts fetching the plot overview/genres for the
// currently resolved media, shown next to the poster. It's independent of
// triggerPreviewPoster (own network call, own failure mode) so a missing or
// failed poster doesn't prevent showing a description, and vice versa.
func (m *modelImpl) triggerPreviewDetails() tea.Cmd {
	if m.resolved == nil || m.posterClient == nil {
		return nil
	}
	opID := m.previewPosterOpID
	tmdbID := m.resolved.TMDBID
	mediaType := m.resolved.MediaType
	title := m.resolved.SeriesTitle

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.appCtx, posterFetchTimeout)
		defer cancel()

		details, err := m.posterClient.FetchDetails(ctx, tmdbID, mediaType, title)
		if err != nil {
			logging.Debug("preview details fetch failed", "tmdb_id", tmdbID, "title", title, "err", err)
			return previewDetailsMsg{opID: opID, err: err}
		}
		return previewDetailsMsg{opID: opID, overview: details.Overview, genres: details.Genres, rating: details.Rating}
	}
}

func (m *modelImpl) fetchPosterCmd(slot posterSlot, opID int, tmdbID int, mediaType, title, coverURL, coverReferer string, maxCols, maxRows int, imageID uint32) tea.Cmd {
	proto := m.effectiveImgProtocol()
	if !m.imagesEnabled || m.posterClient == nil || proto == termimg.ProtocolNone {
		return nil
	}
	if tmdbID == 0 && strings.TrimSpace(title) == "" && coverURL == "" {
		return nil
	}

	cacheKey := fmt.Sprintf("%d|%s|%s|%s|%s|%d|%dx%d", tmdbID, mediaType, title, coverURL, coverReferer, proto, maxCols, maxRows)
	if cached, ok := m.posterCache.Get(cacheKey); ok {
		return func() tea.Msg {
			return posterLoadedMsg{slot: slot, opID: opID, rendered: cached}
		}
	}
	// Capture the live terminal size for real cell measurement: encoding
	// at the 8x16 fallback size and letting the terminal upscale is what
	// made every poster render soft.
	termCols, termRows := m.width, m.height
	appCtx := m.appCtx
	if appCtx == nil {
		appCtx = context.Background()
	}
	var parentCtx context.Context
	switch slot {
	case posterSlotSearch:
		parentCtx, m.searchPosterCancel = context.WithCancel(appCtx)
	case posterSlotPreview:
		parentCtx, m.previewPosterCancel = context.WithCancel(appCtx)
	case posterSlotHistory:
		parentCtx, m.historyPosterCancel = context.WithCancel(appCtx)
	default:
		parentCtx = appCtx
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parentCtx, posterFetchTimeout)
		defer cancel()
		var img image.Image
		var err error
		if coverURL != "" {
			// Provider-stamped artwork (manga covers) resolves directly;
			// catalog titles fall through to TMDB/AniList as before.
			img, err = m.posterClient.FetchImageURL(ctx, coverURL, coverReferer)
		} else {
			img, err = m.posterClient.FetchImage(ctx, tmdbID, mediaType, title)
		}
		if err != nil {
			logging.Debug("poster fetch failed", "tmdb_id", tmdbID, "title", title, "cover", coverURL != "", "err", err)
			return posterLoadedMsg{slot: slot, opID: opID, err: err}
		}

		rendered, err := termimg.RenderFit(img, proto, maxCols, maxRows, imageID, termCols, termRows)
		if err != nil {
			logging.Debug("poster render failed", "tmdb_id", tmdbID, "title", title, "err", err)
			return posterLoadedMsg{slot: slot, opID: opID, err: err}
		}

		m.posterCache.Set(cacheKey, rendered)
		return posterLoadedMsg{slot: slot, opID: opID, rendered: rendered}
	}
}

// effectiveImgProtocol is the protocol posters and manga pages actually
// render with. Terminals with no graphics protocol (tmux, plain xterm)
// still get quadrant-block art instead of nothing: blocks are plain ANSI
// output and render anywhere the rest of the UI's colors already do.
// termimg itself is untouched — this only chooses its fallback input.
func (m *modelImpl) effectiveImgProtocol() termimg.Protocol {
	if m.imgProtocol == termimg.ProtocolNone {
		return termimg.ProtocolBlocks
	}
	return m.imgProtocol
}
