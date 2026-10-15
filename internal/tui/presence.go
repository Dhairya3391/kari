package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/model"
	"kari/internal/poster"
	"kari/internal/presence"
	"kari/internal/provider"
)

// presenceArtworkTimeout bounds the background poster-URL lookup used for
// Discord Rich Presence so a slow catalog lookup can't pile up goroutines.
const presenceArtworkTimeout = 8 * time.Second

// nowPlayingFor builds the Rich Presence payload for media without any
// network work, using a provider-stamped cover when one is already known.
// It reports false when presence is disabled or unavailable. It must run on
// the Update goroutine: it reads model state that playback goroutines must
// not touch.
func (m *modelImpl) nowPlayingFor(media model.ResolvedMedia) (presence.NowPlaying, bool) {
	if m.presence == nil || !m.discordPresence {
		return presence.NowPlaying{}, false
	}
	return presence.NowPlaying{
		Media:     media,
		ImageURL:  m.knownArtworkURL(),
		AniListID: anilistIDFor(m.appMode, m.selectedSeries),
	}, true
}

// publishPresence sends a now-playing card to Discord. When no artwork URL is
// known yet it resolves one in the background and refreshes the card, so
// playback never waits on a poster lookup.
func (m *modelImpl) publishPresence(np presence.NowPlaying) {
	if m.presence == nil {
		return
	}
	session := m.presenceSession.Add(1)
	m.presence.SetNowPlaying(np)
	if np.ImageURL != "" || m.posterClient == nil {
		return
	}
	posterClient := m.posterClient
	appCtx := m.appCtx
	go func() {
		url, err := artworkURL(appCtx, posterClient, np.Media)
		if err != nil || url == "" {
			return
		}
		// Drop the refresh when playback moved on or stopped meanwhile, so a
		// late poster lookup can't revive a cleared card.
		if m.presenceSession.Load() != session {
			return
		}
		np.ImageURL = url
		m.presence.SetNowPlaying(np)
	}()
}

// clearPresence removes the Rich Presence card, e.g. when playback stops or
// the viewer leaves the manga reader.
func (m *modelImpl) clearPresence() {
	if m.presence == nil {
		return
	}
	m.presenceSession.Add(1)
	m.presence.Clear()
}

// toggleDiscordPresence flips the Discord Rich Presence setting, applies it
// immediately, and persists it. Enabling it mid-playback publishes the
// current media right away rather than waiting for the next pause/seek.
func (m *modelImpl) toggleDiscordPresence() (tea.Model, tea.Cmd) {
	m.discordPresence = !m.discordPresence
	m.applyPresenceToggle()
	m.saveSettings()
	if m.discordPresence {
		if m.playOpID != 0 && m.resolved != nil {
			if np, ok := m.nowPlayingFor(*m.resolved); ok {
				m.publishPresence(np)
			}
		}
		return m, m.setStatusTimed(statusInfo, "Discord presence on — your activity shows on your profile while watching")
	}
	return m, m.setStatusTimed(statusInfo, "Discord presence off")
}

// applyPresenceToggle reconciles runtime state with the toggle: turning it
// off removes any card immediately.
func (m *modelImpl) applyPresenceToggle() {
	if !m.discordPresence {
		m.clearPresence()
	}
}

// reportReading publishes a manga chapter as Discord Rich Presence. The
// chapter context rides on a minimal ResolvedMedia so the presence layer can
// label it "Reading" without a second code path. Safe to call on the Update
// goroutine: manga covers are provider-stamped, so no network lookup runs.
func (m *modelImpl) reportReading(chapter provider.MangaChapter) {
	if m.presence == nil || !m.discordPresence {
		return
	}
	series := m.searchQuery
	if m.selectedSeries != nil && strings.TrimSpace(m.selectedSeries.Title) != "" {
		series = m.selectedSeries.Title
	}
	label := strings.TrimSpace(chapter.Number)
	if label == "" {
		label = strings.TrimSpace(chapter.Title)
	}
	media := model.ResolvedMedia{
		SeriesTitle:  series,
		EpisodeTitle: label,
		MediaType:    provider.MediaTypeManga,
	}
	m.presenceSession.Add(1)
	m.presence.SetNowPlaying(presence.NowPlaying{
		Media:    media,
		ImageURL: m.knownArtworkURL(),
	})
}

// knownArtworkURL returns an already-available artwork URL without a network
// call: provider-stamped covers (manga/anime) ride on the selected series.
func (m *modelImpl) knownArtworkURL() string {
	if m.selectedSeries == nil {
		return ""
	}
	return strings.TrimSpace(m.selectedSeries.CoverURL)
}

// artworkURL resolves the poster URL for media via the poster client's
// catalog lookup (TMDB/AniList).
func artworkURL(ctx context.Context, posterClient *poster.Client, media model.ResolvedMedia) (string, error) {
	if posterClient == nil {
		return "", nil
	}
	title := strings.TrimSpace(media.SeriesTitle)
	if title == "" {
		title = strings.TrimSpace(media.EpisodeTitle)
	}
	if media.TMDBID == 0 && title == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, presenceArtworkTimeout)
	defer cancel()
	return posterClient.ArtworkURL(ctx, media.TMDBID, media.MediaType, title)
}
