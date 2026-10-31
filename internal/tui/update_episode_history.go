package tui

import (
	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
	"time"
)

func (m *modelImpl) episodeHistoryKey(ep provider.Episode) history.EntryKey {
	seriesTitle, mediaType := "", ""
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
		mediaType = m.selectedSeries.MediaType
	}
	if seriesTitle == "" && m.resolved != nil {
		seriesTitle = m.resolved.SeriesTitle
	}
	if mediaType == "" && m.resolved != nil {
		mediaType = m.resolved.MediaType
	}
	if mediaType == "" {
		mediaType = string(m.appMode)
	}
	return history.EntryKey{
		Title:     seriesTitle,
		Mode:      string(m.appMode),
		MediaType: mediaType,
		Season:    ep.Season,
		Episode:   ep.Episode,
	}
}

func (m *modelImpl) isEpisodeComplete(ep provider.Episode) bool {
	if m.historyStore == nil {
		return false
	}
	entry, ok := m.historyStore.Get(m.episodeHistoryKey(ep))
	return ok && (entry.Complete || entry.PercentComplete >= 0.95 || (entry.DurationSecs > 0 && (entry.PositionSecs/entry.DurationSecs) >= 0.95))
}

func (m *modelImpl) markEpisodeComplete(ep provider.Episode) error {
	if m.historyStore == nil {
		return nil
	}
	key := m.episodeHistoryKey(ep)
	existing, found := m.historyStore.Get(key)

	seriesTitle, mediaType := "", ""
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
		mediaType = m.selectedSeries.MediaType
	}
	if seriesTitle == "" && m.resolved != nil {
		seriesTitle = m.resolved.SeriesTitle
	}
	if mediaType == "" && m.resolved != nil {
		mediaType = m.resolved.MediaType
	}
	if mediaType == "" {
		mediaType = string(m.appMode)
	}

	tmdbID := 0
	if m.selectedSeries != nil && m.selectedSeries.TMDBID != 0 {
		tmdbID = m.selectedSeries.TMDBID
	} else if ep.TMDBID != 0 {
		tmdbID = ep.TMDBID
	} else if m.resolved != nil {
		tmdbID = m.resolved.TMDBID
	}

	epTitle := ep.Title
	if epTitle == "" && m.resolved != nil {
		epTitle = m.resolved.EpisodeTitle
	}

	entry := history.Entry{
		Key:                  key,
		Title:                seriesTitle,
		EpisodeTitle:         epTitle,
		Season:               ep.Season,
		Episode:              ep.Episode,
		WatchedAt:            time.Now(),
		PercentComplete:      1.0,
		Complete:             true,
		LastScrobbledPercent: 1.0,
		Mode:                 string(m.appMode),
		MediaType:            mediaType,
		TMDBID:               tmdbID,
		AniListID:            anilistIDFor(m.appMode, m.selectedSeries),
		AudioMode:            m.audioMode,
	}

	if found {
		entry.DurationSecs = existing.DurationSecs
		if existing.DurationSecs > 0 {
			entry.PositionSecs = existing.DurationSecs
		}
		if existing.EpisodeTitle != "" && entry.EpisodeTitle == "" {
			entry.EpisodeTitle = existing.EpisodeTitle
		}
	}

	if err := m.historyStore.Upsert(entry); err != nil {
		return err
	}
	m.invalidateEpisodeHistoryIndex()

	media := &model.ResolvedMedia{
		SeriesTitle:   seriesTitle,
		EpisodeTitle:  entry.EpisodeTitle,
		MediaType:     mediaType,
		AniListID:     entry.AniListID,
		SeasonNumber:  ep.Season,
		EpisodeNumber: ep.Episode,
		TMDBID:        tmdbID,
	}
	m.triggerScrobble(entry, media)
	return nil
}

func (m *modelImpl) unmarkEpisode(ep provider.Episode) error {
	if m.historyStore == nil {
		return nil
	}
	key := m.episodeHistoryKey(ep)
	err := m.historyStore.Delete(key)
	if err == nil {
		m.invalidateEpisodeHistoryIndex()
	}
	return err
}

func (m *modelImpl) toggleEpisodeComplete(ep provider.Episode) (bool, error) {
	if m.isEpisodeComplete(ep) {
		if err := m.unmarkEpisode(ep); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := m.markEpisodeComplete(ep); err != nil {
		return false, err
	}
	return true, nil
}
