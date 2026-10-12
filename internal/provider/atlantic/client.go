// Package atlantic implements provider.Provider for Atlantic (https://atlantic.st),
// a multi-backend streaming platform for movies, TV series, and animation.
//
// Catalog search and episode listings reuse the shared TMDB-keyed streambase.
// Playback resolution fans out concurrently to multiple upstream shards
// (Helios, Moscow, Planets, Athena, Vespera), decrypting AES-GCM payloads
// for HLS stream manifests and pulling multi-language subtitles from Granite
// and Natsuki.
//
// Streams require Atlantic referer and origin headers and lavf HLS demuxer
// settings for extensionless playlist segments, all passed cleanly to mpv
// via MediaSource headers and ExtraArgs.
package atlantic

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

var (
	_ provider.Provider          = (*Client)(nil)
	_ provider.Presenter         = (*Client)(nil)
	_ provider.StreamingProvider = (*Client)(nil)
)

// Client implements provider.Provider against the Atlantic streaming network.
type Client struct {
	base        *streambase.Base
	httpClient  *http.Client
	siteBase    string
	streamBase  string
	athenaBase  string
	subsBase    string
	graniteBase string
}

// NewClient constructs the Atlantic provider over the shared TMDB search base
// with production API endpoints.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithURLs(
		keyPool,
		config.AtlanticBase,
		config.AtlanticStreamBase,
		config.AtlanticAthenaBase,
		config.AtlanticSubsBase,
		config.AtlanticGraniteBase,
	)
}

// NewClientWithURLs constructs the Atlantic provider with custom upstream endpoints,
// allowing tests to mock resolution servers and CDNs without network access.
func NewClientWithURLs(
	keyPool *tmdb.KeyPool,
	siteBase, streamBase, athenaBase, subsBase, graniteBase string,
) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("atlantic streambase: %w", err)
	}
	return &Client{
		base:        base,
		httpClient:  httpclient.New(),
		siteBase:    strings.TrimRight(siteBase, "/"),
		streamBase:  strings.TrimRight(streamBase, "/"),
		athenaBase:  strings.TrimRight(athenaBase, "/"),
		subsBase:    strings.TrimRight(subsBase, "/"),
		graniteBase: strings.TrimRight(graniteBase, "/"),
	}, nil
}

// Alias implements provider.Presenter with the display brand name.
func (c *Client) Alias() string { return "Atlantic" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "atlantic" }

// Modes implements provider.Provider, declaring support for movies, TV series,
// and cartoon/animation modes.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
	}
}

// Search delegates to the shared TMDB-backed streambase.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes delegates to the shared TMDB-backed streambase.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

// ResolveSource resolves all available sources for a title into a single slice,
// accumulating results across all upstream backends.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID, mediaType, season, epNum, err := parseEpisodeCoords(mediaID, episode)
	if err != nil {
		return nil, err
	}

	ch := make(chan []provider.MediaSource, 8)
	var all []provider.MediaSource
	done := make(chan struct{})

	go func() {
		defer close(done)
		for batch := range ch {
			all = append(all, batch...)
		}
	}()

	subs, err := c.fanoutResolve(ctx, tmdbID, mediaType, season, epNum, ch)
	close(ch)
	<-done

	if len(all) > 0 && len(subs) > 0 {
		for i := range all {
			if len(all[i].Subtitles) == 0 {
				all[i].Subtitles = subs
			}
		}
	}

	if err != nil && len(all) == 0 {
		return nil, err
	}
	if len(all) == 0 {
		return nil, provider.ErrNoSources
	}
	return provider.FilterDirectSources(all), nil
}

// ResolveStream implements provider.StreamingProvider, fanning out concurrently
// across all Atlantic upstream shards and delivering sources as they resolve.
func (c *Client) ResolveStream(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	tmdbID, mediaType, season, epNum, err := parseEpisodeCoords(mediaID, episode)
	if err != nil {
		return err
	}

	_, err = c.fanoutResolve(ctx, tmdbID, mediaType, season, epNum, updates)
	return err
}

func parseEpisodeCoords(mediaID string, episode provider.Episode) (int, string, int, int, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %w", err)
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
