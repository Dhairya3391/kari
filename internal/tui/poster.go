package tui

import (
	"context"
	"fmt"
	"image"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/calendar"
	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/termimg"

	_ "golang.org/x/image/webp"
)

const (
	searchPosterMaxCols = 26
	searchPosterMaxRows = 16
	// previewPosterMaxCols keeps the reserved artwork slot compact: a wide
	// empty column made the metadata look stranded far to the right.
	previewPosterMaxCols = 24
	previewPosterMaxRows = 18
	historyPosterMaxCols = 26
	historyPosterMaxRows = 16
	// The calendar cover sits in the blank rows beneath the detail text. Its
	// width is capped so it never crowds the metadata pane, and its height is
	// derived from the space left after the fixed metadata block.
	calendarPosterMaxCols = 24
	calendarPosterMaxRows = 14
	// detailsPosterMaxRows matches the preview poster height so the metadata
	// beneath it starts below the full-size poster area, not halfway through it.
	detailsPosterMaxRows = 18
	// livePosterMaxCols and livePosterRows size the live guide's "On now"
	// artwork. The inspector reserves exactly this many rows, so an image
	// placed out-of-band by Kitty/Sixel/iTerm2 can never spill onto the list.
	livePosterMaxCols = 22
	livePosterRows    = 9
	// posterGutter is the fixed gap between a poster slot and its text pane.
	// Two cells keep the panes visually connected without crowding the text.
	posterGutter = 2
	// posterInset nudges artwork a couple of cells off the slot's left edge so
	// it is neither flush with the frame nor centered in a wide empty column.
	posterInset = 2
	// previewPosterInset places the preview artwork flush with its slot. The
	// image is the full slot width, so any inset would spill it into the gutter
	// and over the metadata; flush also puts it two cells further left.
	previewPosterInset = 0

	posterFetchTimeout = 15 * time.Second

	// Stable per-slot image ids. Kept distinct (and never reused for
	// anything else) so Cleanup can target exactly one slot's placement
	// — e.g. clearing the search-screen poster when navigating to a screen
	// that doesn't show one, without touching the preview-screen poster.
	// Only the Kitty protocol needs these; the rest ignore them.
	kittySearchImageID   uint32 = 1
	kittyPreviewImageID  uint32 = 2
	kittyHistoryImageID  uint32 = 4
	kittyCalendarImageID uint32 = 5
	kittyLiveImageID     uint32 = 6
)

// posterSlotRows returns the fixed poster height for a terminal height. It
// depends only on dimensions, never on whether artwork has loaded, so the slot
// keeps its size from the first frame to the last.
func posterSlotRows(height, maxRows int) int {
	rows := height / 2
	if rows > maxRows {
		rows = maxRows
	}
	if rows < 6 {
		rows = 6
	}
	return rows
}

// detailsPosterRows caps the inspector poster so the text beneath it always
// fits inside the frame body. Without this cap a tall poster made the details
// pane taller than RenderFrame's body budget and its text was clipped away.
func detailsPosterRows(height int) int {
	rows := posterSlotRows(height, detailsPosterMaxRows)
	maxRows := max(4, height-18)
	if rows > maxRows {
		rows = maxRows
	}
	return rows
}

const (
	// calendarDetailsMetaRows is the fixed height of the text above the
	// calendar cover. Every metadata variant fits inside it, so the artwork
	// always starts on the same row regardless of the selected release.
	calendarDetailsMetaRows = 20
	// calendarSynopsisRows is the fixed synopsis height inside the metadata
	// block, so a description with embedded newlines cannot move the cover.
	calendarSynopsisRows = 3
	// calendarPosterMinRows keeps the cover from becoming an unreadable sliver
	// on short terminals; below it the calendar detail pane shows no cover.
	calendarPosterMinRows = 4
)

// calendarDetailBodyHeight returns the height of the calendar detail pane for
// a screen-body height. RenderCalendarScreen reserves two rows for its nav
// line and the blank separator beneath it.
func calendarDetailBodyHeight(height int) int {
	return max(5, height-2)
}

// calendarPosterRows is the fixed cover height for a calendar detail pane.
// Both the renderer and the fetch command use it so the reserved slot and the
// encoded image always agree on geometry.
func calendarPosterRows(detailHeight int) int {
	rows := detailHeight - calendarDetailsMetaRows
	if rows > calendarPosterMaxRows {
		rows = calendarPosterMaxRows
	}
	if rows < 0 {
		rows = 0
	}
	return rows
}

func calendarPosterFits(detailHeight int) bool {
	return calendarPosterRows(detailHeight) >= calendarPosterMinRows
}

// previewDetailsKey identifies one title's plot/genre/score cache entry.
func previewDetailsKey(tmdbID int, mediaType string, title string) string {
	return fmt.Sprintf("%d|%s|%s", tmdbID, mediaType, strings.ToLower(strings.TrimSpace(title)))
}

// cachedPreviewDetails returns the last fetched details for the selected
// series. Navigation clears the live fields, but the cache lets the screen
// repaint them immediately instead of showing an empty panel while the async
// fetch runs again.
func (m *modelImpl) cachedPreviewDetails() (string, []string, string) {
	if m.selectedSeries == nil || m.previewDetailsCache == nil {
		return "", nil, ""
	}
	key := previewDetailsKey(m.selectedSeries.TMDBID, m.selectedSeries.MediaType, m.selectedSeries.Title)
	details, ok := m.previewDetailsCache.Get(key)
	if !ok {
		return "", nil, ""
	}
	return details.overview, details.genres, details.rating
}

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
	// The live guide shares the results-selection lifecycle, so its artwork
	// slot must be released whenever the catalog poster slot is released.
	m.clearLivePoster()
}

// triggerSearchPoster starts fetching (or serves from cache) the poster for
// the search result at idx, to be shown in the search screen's right panel.
func (m *modelImpl) triggerSearchPoster(idx int) tea.Cmd {
	// Live shares the results list widget but renders its artwork in a
	// dedicated, correctly-sized inspector slot instead of above the list.
	if m.appMode == provider.ModeLive {
		return m.triggerLivePoster(idx)
	}
	m.clearSearchPoster()
	if idx < 0 || idx >= len(m.seriesResults) {
		return nil
	}
	result := m.seriesResults[idx]
	return m.fetchPosterCmd(posterSlotSearch, m.searchPosterOpID, result.TMDBID, result.MediaType, result.Title, result.CoverURL, result.CoverReferer, searchPosterMaxCols, searchPosterMaxRows, kittySearchImageID)
}

// clearLivePoster resets the live guide's artwork slot. Bumping the opID also
// discards a fetch still in flight for a previously selected channel.
func (m *modelImpl) clearLivePoster() {
	if m.livePosterCancel != nil {
		m.livePosterCancel()
		m.livePosterCancel = nil
	}
	m.livePosterOpID++
	m.livePoster = ""
	m.livePosterUnavailable = false
}

// triggerLivePoster fetches the artwork for the selected live channel shown in
// the guide's "On now" inspector.
func (m *modelImpl) triggerLivePoster(idx int) tea.Cmd {
	m.clearLivePoster()
	if idx < 0 || idx >= len(m.seriesResults) {
		return nil
	}
	result := m.seriesResults[idx]
	return m.fetchPosterCmd(
		posterSlotLive,
		m.livePosterOpID,
		result.TMDBID,
		result.MediaType,
		result.Title,
		result.CoverURL,
		result.CoverReferer,
		livePosterMaxCols,
		livePosterRows,
		kittyLiveImageID,
	)
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
	hasNewTMDB := m.resolved.TMDBID != 0 && (m.selectedSeries == nil || m.selectedSeries.TMDBID == 0)
	if m.previewPosterCancel != nil && !hasNewTMDB {
		return nil
	}
	m.clearPreviewPoster()
	coverURL := ""
	coverReferer := ""
	title := ""
	if m.selectedSeries != nil {
		coverURL = m.selectedSeries.CoverURL
		coverReferer = m.selectedSeries.CoverReferer
		title = m.selectedSeries.Title
	}
	if title == "" {
		title = m.resolved.SeriesTitle
	}
	tmdbID := m.resolved.TMDBID
	if tmdbID == 0 && m.selectedSeries != nil {
		tmdbID = m.selectedSeries.TMDBID
	}
	mediaType := m.resolved.MediaType
	if mediaType == "" && m.selectedSeries != nil {
		mediaType = m.selectedSeries.MediaType
	}
	return m.fetchPosterCmd(posterSlotPreview, m.previewPosterOpID, tmdbID, mediaType, title, coverURL, coverReferer, previewPosterMaxCols, previewPosterMaxRows, kittyPreviewImageID)
}

// clearHistoryPoster cancels stale artwork so it cannot appear beside a new selection.
func (m *modelImpl) clearHistoryPoster() {
	if m.historyPosterCancel != nil {
		m.historyPosterCancel()
		m.historyPosterCancel = nil
	}
	m.historyPosterOpID++
	m.historyPoster = ""
	m.historyPosterUnavailable = false
	m.historyPosterDrawn = false
}

// triggerHistoryPoster loads artwork for the selected history title.
func (m *modelImpl) triggerHistoryPoster() tea.Cmd {
	m.clearHistoryPoster()
	item, ok := m.historyList.SelectedItem().(rowItem)
	if !ok {
		return nil
	}
	for _, group := range m.historyGroups {
		if group.Key.String() != item.key {
			continue
		}
		entry := group.ContinueEntry
		return m.fetchPosterCmd(
			posterSlotHistory,
			m.historyPosterOpID,
			entry.TMDBID,
			group.MediaType,
			group.Title,
			"",
			"",
			historyPosterMaxCols,
			historyPosterMaxRows,
			kittyHistoryImageID,
		)
	}
	return nil
}

// clearCalendarPoster resets the calendar slot so a fast cursor move cannot
// leave the previous cover sitting in the fixed slot. Bumping the opID also
// discards any fetch still in flight for the old release.
func (m *modelImpl) clearCalendarPoster() {
	if m.calendarPosterCancel != nil {
		m.calendarPosterCancel()
		m.calendarPosterCancel = nil
	}
	m.calendarPosterOpID++
	m.calendarPoster = ""
	m.calendarPosterUnavailable = false
}

// triggerCalendarPoster fetches the cover for the selected calendar release.
// Provider-stamped CoverURLs (AniList extraLarge, TMDB w500) resolve directly;
// releases without one fall back to the poster client's catalog lookup.
func (m *modelImpl) triggerCalendarPoster(item calendar.ReleaseItem) tea.Cmd {
	m.clearCalendarPoster()
	rel := item.Release
	referer := ""
	if rel.Mode == provider.ModeAnime {
		referer = config.AniListReferer
	}
	bodyHeight := calendarDetailBodyHeight(ComputeDims(m.width, m.height).BodyHeight)
	return m.fetchPosterCmd(
		posterSlotCalendar,
		m.calendarPosterOpID,
		rel.TMDBID,
		string(rel.Mode),
		rel.Title,
		rel.CoverURL,
		referer,
		calendarPosterMaxCols,
		calendarPosterRows(bodyHeight),
		kittyCalendarImageID,
	)
}

// triggerPreviewDetails starts fetching the plot overview/genres for the
// currently resolved media, shown next to the poster. It's independent of
// triggerPreviewPoster (own network call, own failure mode) so a missing or
// failed poster doesn't prevent showing a description, and vice versa.
func (m *modelImpl) triggerPreviewDetails() tea.Cmd {
	if m.posterClient == nil {
		return nil
	}
	opID := m.previewPosterOpID
	tmdbID, mediaType, title := 0, "", ""
	if m.resolved != nil {
		tmdbID, mediaType, title = m.resolved.TMDBID, m.resolved.MediaType, m.resolved.SeriesTitle
	} else if m.selectedSeries != nil {
		tmdbID, mediaType, title = m.selectedSeries.TMDBID, m.selectedSeries.MediaType, m.selectedSeries.Title
	}
	if tmdbID == 0 && strings.TrimSpace(title) == "" {
		return nil
	}
	key := previewDetailsKey(tmdbID, mediaType, title)
	if m.previewDetailsCache != nil {
		if cached, ok := m.previewDetailsCache.Get(key); ok {
			m.previewOverview, m.previewGenres, m.previewRating = cached.overview, cached.genres, cached.rating
		}
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.appCtx, posterFetchTimeout)
		defer cancel()

		details, err := m.posterClient.FetchDetails(ctx, tmdbID, mediaType, title)
		if err != nil {
			logging.Debug("preview details fetch failed", "tmdb_id", tmdbID, "title", title, "err", err)
			return previewDetailsMsg{opID: opID, key: key, err: err}
		}
		return previewDetailsMsg{opID: opID, key: key, overview: details.Overview, genres: details.Genres, rating: details.Rating}
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
	case posterSlotCalendar:
		parentCtx, m.calendarPosterCancel = context.WithCancel(appCtx)
	case posterSlotLive:
		parentCtx, m.livePosterCancel = context.WithCancel(appCtx)
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
