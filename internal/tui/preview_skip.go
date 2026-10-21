package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/model"
	"kari/internal/provider"
)

func (m *modelImpl) triggerPreviewSkipInfo() tea.Cmd {
	if m.resolved == nil {
		return nil
	}
	return m.triggerPreviewSkipInfoFor(*m.resolved)
}

func (m *modelImpl) triggerPreviewSkipInfoFor(media model.ResolvedMedia) tea.Cmd {
	if m.players == nil {
		return nil
	}
	key := fmt.Sprintf("%d:%d:%d:%s:%s:%s",
		media.TMDBID, media.SeasonNumber, media.EpisodeNumber,
		media.MediaType+":"+media.SeriesTitle, media.SeriesURL, media.EpisodeURL+":"+media.EpisodeTitle)
	if key == m.previewSkipKey {
		return nil
	}
	m.previewSkipKey = key
	m.skipInfoOpID++
	opID := m.skipInfoOpID
	m.previewSkipInfo = "checking..."
	players := m.players
	return func() tea.Msg {
		return previewSkipInfoMsg{opID: opID, info: players.ResolveSkipInfo(media)}
	}
}

func previewSkipMedia(series provider.SearchResult, episode provider.Episode, mode provider.ContentType) model.ResolvedMedia {
	return model.ResolvedMedia{
		SeriesTitle:   series.Title,
		SeriesURL:     series.ID,
		EpisodeTitle:  episode.Title,
		EpisodeURL:    episode.ID,
		MediaType:     series.MediaType,
		ContentMode:   mode,
		Year:          series.Year,
		TMDBID:        series.TMDBID,
		SeasonNumber:  episode.Season,
		EpisodeNumber: episode.Episode,
	}
}

func (m *modelImpl) resetPreviewSkipInfo() {
	m.previewSkipInfo = ""
	m.previewSkipKey = ""
	m.skipInfoOpID++
}

func (m *modelImpl) onPreviewSkipInfo(msg previewSkipInfoMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.skipInfoOpID {
		return m, nil
	}
	m.previewSkipInfo = msg.info.String()
	return m, nil
}
