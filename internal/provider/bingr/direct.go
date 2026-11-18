package bingr

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"kari/internal/provider"
)

// resolveDirect resolves a movie, TV, or cartoon through the sealed source
// payload that Bingr embeds in its watch page. The old shard API on
// api.bingr.st now answers HTTP 410 and no longer yields streams.
func (c *Client) resolveDirect(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	tmdbID, mediaType, season, epNum, err := parseEpisodeCoords(mediaID, episode)
	if err != nil {
		return err
	}

	sources, err := c.fetchSealedSources(ctx, mediaType, tmdbID, season, epNum)
	if err != nil {
		return err
	}
	if updates != nil {
		select {
		case updates <- sources:
		case <-ctx.Done():
		}
	}
	return nil
}

func parseEpisodeCoords(mediaID string, episode provider.Episode) (int, string, int, int, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID %q: %w", mediaID, err)
		}
		if tmdbID <= 0 {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %q", mediaID)
		}
	}

	mediaType := provider.MediaTypeMovie
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = provider.MediaTypeTV
	}
	season := episode.Season
	if mediaType == provider.MediaTypeTV && season <= 0 {
		season = 1
	}
	epNum := episode.Episode
	if mediaType == provider.MediaTypeTV && epNum <= 0 {
		epNum = 1
	}
	return tmdbID, mediaType, season, epNum, nil
}
