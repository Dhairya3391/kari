package anikoto

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
)

// Client implements provider.Provider by scraping the Anikoto site directly:
// AniList supplies the search index and title candidates, anikototv.to
// supplies episode listings and server embeds, and Megaplay's getSources
// supplies AES-encrypted streams decrypted in crypto.go. No intermediary
// API or proxy is involved; every returned URL is a direct CDN address
// playable by mpv.
type Client struct {
	http     *http.Client
	siteBase string
	gqlBase  string
}

// Alias implements provider.Presenter, providing the human-readable display name.
func (c *Client) Alias() string { return "Anikoto" }

// Name implements provider.Provider, returning the stable registry identifier.
func (c *Client) Name() string {
	return "anikoto"
}

// Modes implements provider.Provider, registering Anikoto as the primary anime provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 2},
	}
}

// RequiresEpisodeListForMovies implements provider.MovieEpisodeFlow. Anikoto
// resolves playback via per-episode route IDs (e.g. watch/anikoto/{id}/sub/1)
// that only the episode listing produces, so anime movies must query
// FetchEpisodes first.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Features implements provider.FeatureSource. Anime media supports separate sub/dub audio tracks.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeAnime {
		return provider.Features{}
	}
	return provider.Features{AudioSelection: true}
}

// NewClient constructs the Anikoto provider with the shared HTTP client.
func NewClient() (*Client, error) {
	return &Client{
		http:     httpclient.New(),
		siteBase: config.AnikotoBase,
		gqlBase:  config.AniListAPIBase,
	}, nil
}

// NewClientWithBaseURL constructs the Anikoto provider against a custom base
// URL. In tests a single httptest server stands in for both the Anikoto
// site and the AniList GraphQL endpoint, so both bases point at it.
func NewClientWithBaseURL(baseURL string) (*Client, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		http:     httpclient.New(),
		siteBase: baseURL,
		gqlBase:  baseURL,
	}, nil
}

// Search queries the shared AniList GraphQL index. Movies report
// MediaTypeMovie so the TUI renders movie badges while still routing through
// the episode flow.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	results, err := anilist.SearchWithEndpoint(ctx, c.http, query, c.gqlBase)
	if err != nil {
		return nil, fmt.Errorf("anikoto search: %w", err)
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	logging.Debug("search done via anilist", "provider", c.Name(), "results", len(results))
	return results, nil
}

// FetchAvailableEpisodes lists only episodes exposed by Anikoto's audio
// tracks, including the sub/dub availability for each episode.
func (c *Client) FetchAvailableEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	logging.Debug("fetch episodes", "provider", c.Name(), "mediaID", mediaID)

	_, slug, err := c.resolveAnilistToAnikoto(ctx, mediaID)
	if err != nil {
		return nil, fmt.Errorf("anikoto episodes: resolve: %w", err)
	}
	if slug == "" {
		return nil, provider.ErrNoEpisodes
	}
	animeID := c.fetchAnimeIDFromWatchPage(ctx, slug)
	if animeID == "" {
		return nil, fmt.Errorf("anikoto episodes: no anime ID for %q: %w", slug, provider.ErrNoEpisodes)
	}
	eps, err := c.fetchEpisodesDirect(ctx, animeID, mediaID)
	if err != nil {
		return nil, fmt.Errorf("anikoto episodes: %w", err)
	}
	logging.Debug("fetch episodes done via direct", "provider", c.Name(), "count", len(eps))
	return eps, nil
}

// FetchEpisodes implements provider.Provider using Anikoto's verified
// per-track episode list.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.FetchAvailableEpisodes(ctx, series)
}

// ResolveSource resolves one episode ID to ranked stream sources via the
// Anikoto site: server list, embed page, Megaplay getSources, AES decrypt,
// CDN sign.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episodeID", episode.ID)

	// Strip the pipe-separated dataIDs suffix before parsing the handle,
	// but keep it: it lets resolution skip the episode re-fetch.
	cleanID := episode.ID
	dataIDs := ""
	if idx := strings.Index(cleanID, "|"); idx >= 0 {
		dataIDs = cleanID[idx+1:]
		cleanID = cleanID[:idx]
	}
	h := kit.ParseHandle(mediaID, cleanID, episode.Audio, episode.Episode, "anikoto")
	if !h.Valid() {
		return nil, fmt.Errorf("anikoto resolve: invalid episode parameters")
	}
	sources, err := c.resolveDirectStreams(ctx, h.AniListID, h.Category, h.Number, dataIDs)
	if err != nil {
		return nil, fmt.Errorf("anikoto resolve: %w", err)
	}
	// Defense in depth: only direct CDN addresses reach mpv.
	sources = provider.FilterDirectSources(sources)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	logging.Debug("resolve source done via direct", "provider", c.Name(), "count", len(sources))
	return sources, nil
}

var (
	_ provider.Provider                  = (*Client)(nil)
	_ provider.Presenter                 = (*Client)(nil)
	_ provider.FeatureSource             = (*Client)(nil)
	_ provider.MovieEpisodeFlow          = (*Client)(nil)
	_ provider.EpisodeAvailabilitySource = (*Client)(nil)
)
