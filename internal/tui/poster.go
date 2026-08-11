package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/logging"
	"kari/internal/termimg"
)

const (
	searchPosterMaxCols  = 26
	searchPosterMaxRows  = 16
	previewPosterMaxCols = 36
	previewPosterMaxRows = 18

	posterFetchTimeout = 8 * time.Second

	// Stable per-slot Kitty image ids. Kept distinct (and never reused for
	// anything else) so DeleteKitty can target exactly one slot's placement
	// — e.g. clearing the search-screen poster when navigating to a screen
	// that doesn't show one, without touching the preview-screen poster.
	kittySearchImageID  uint32 = 1
	kittyPreviewImageID uint32 = 2
)

// clearSearchPoster resets the search poster slot's state, e.g. when the
// result list is cleared or the mode is switched — otherwise the last
// poster shown just keeps sitting there (as leftover text, or for Kitty a
// leftover placement, since nothing renders in its place to prompt cleanup)
// even though it no longer corresponds to anything on screen. Bumping the
// opID also discards any fetch still in flight for the old results.
func (m *modelImpl) clearSearchPoster() {
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
	return m.fetchPosterCmd(posterSlotSearch, m.searchPosterOpID, result.TMDBID, result.MediaType, result.Title, searchPosterMaxCols, searchPosterMaxRows, kittySearchImageID)
}

// clearPreviewPoster resets the preview poster slot's state — call this
// anywhere m.resolved is reset to nil, so a stale poster (and stale
// overview/genres) from whatever was resolved before doesn't keep showing
// while the new one loads (or if the new one never gets one).
func (m *modelImpl) clearPreviewPoster() {
	m.previewPosterOpID++
	m.previewPoster = ""
	m.previewPosterUnavailable = false
	m.previewOverview = ""
	m.previewGenres = nil
	m.previewRating = ""
}

// triggerPreviewPoster starts fetching (or serves from cache) the poster for
// the currently resolved media, shown on the preview screen.
func (m *modelImpl) triggerPreviewPoster() tea.Cmd {
	if m.resolved == nil {
		return nil
	}
	m.clearPreviewPoster()
	return m.fetchPosterCmd(posterSlotPreview, m.previewPosterOpID, m.resolved.TMDBID, m.resolved.MediaType, m.resolved.SeriesTitle, previewPosterMaxCols, previewPosterMaxRows, kittyPreviewImageID)
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
			logging.Debugf("preview details fetch failed tmdb_id=%d title=%q err=%v", tmdbID, title, err)
			return previewDetailsMsg{opID: opID, err: err}
		}
		return previewDetailsMsg{opID: opID, overview: details.Overview, genres: details.Genres, rating: details.Rating}
	}
}

// setImagesEnabled flips the image-rendering setting and persists it.
// Turning images back on needs an explicit re-fetch for whatever's
// currently on screen: fetchPosterCmd short-circuits while disabled, so
// nothing else would prompt a poster to actually load until the user moves
// the selection.
func (m *modelImpl) setImagesEnabled(enabled bool) tea.Cmd {
	if m.imagesEnabled == enabled {
		return nil
	}
	m.imagesEnabled = enabled
	m.saveSettings()
	if enabled {
		m.setStatus(statusInfo, "Image rendering: Enabled")
	} else {
		m.setStatus(statusInfo, "Image rendering: Disabled")
	}
	if !enabled {
		return nil
	}

	var cmds []tea.Cmd
	if cmd := m.triggerSearchPoster(m.selectedSeriesIndex()); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if m.resolved != nil {
		if cmd := m.triggerPreviewPoster(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func (m *modelImpl) fetchPosterCmd(slot posterSlot, opID int, tmdbID int, mediaType, title string, maxCols, maxRows int, imageID uint32) tea.Cmd {
	if !m.imagesEnabled || m.posterClient == nil || m.imgProtocol == termimg.ProtocolNone {
		return nil
	}
	if tmdbID == 0 && strings.TrimSpace(title) == "" {
		return nil
	}

	cacheKey := fmt.Sprintf("%d|%s|%s|%d|%dx%d", tmdbID, mediaType, title, m.imgProtocol, maxCols, maxRows)
	if cached, ok := m.posterCache.Get(cacheKey); ok {
		return func() tea.Msg {
			return posterLoadedMsg{slot: slot, opID: opID, rendered: cached}
		}
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.appCtx, posterFetchTimeout)
		defer cancel()

		img, err := m.posterClient.FetchImage(ctx, tmdbID, mediaType, title)
		if err != nil {
			logging.Debugf("poster fetch failed tmdb_id=%d title=%q err=%v", tmdbID, title, err)
			return posterLoadedMsg{slot: slot, opID: opID, err: err}
		}

		rendered, err := termimg.RenderFit(img, m.imgProtocol, maxCols, maxRows, imageID)
		if err != nil {
			logging.Debugf("poster render failed tmdb_id=%d title=%q err=%v", tmdbID, title, err)
			return posterLoadedMsg{slot: slot, opID: opID, err: err}
		}

		m.posterCache.Set(cacheKey, rendered)
		return posterLoadedMsg{slot: slot, opID: opID, rendered: rendered}
	}
}
