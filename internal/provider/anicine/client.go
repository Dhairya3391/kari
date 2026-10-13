// Package anicine implements provider.Provider for AniCine
// (https://anicine.xyz) for anime.
//
//   - Search reuses the shared index: AniList GraphQL. No site scraping.
//   - Anime resolves via AniList MAL IDs straight into Megaplay embeds
//     (https://megaplay.buzz/stream/mal/{id}/{ep}/{sub|dub}), decrypted
//     with the shared kit megaplay helpers — the same flow anicine's own
//     anicine-1 server uses.
//
// Every returned URL is directly playable by mpv; kari hosts no proxy.
package anicine

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
)

const (
	// megaplayBase serves the anime embeds anicine addresses by MAL id.
	megaplayBase = "https://megaplay.buzz"
)

// Client implements provider.Provider for AniCine.
type Client struct {
	http      *http.Client
	gqlBase   string
	embedBase string
}

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "AniCine" }

// Name implements provider.Provider.
func (c *Client) Name() string { return "anicine" }

// Modes implements provider.Provider, registering AniCine for anime.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 1},
	}
}

// RequiresEpisodeListForMovies implements provider.MovieEpisodeFlow. Anime
// resolves via per-episode embeds, so anime movies query FetchEpisodes first.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Features implements provider.FeatureSource. Anime supports sub/dub tracks.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeAnime {
		return provider.Features{}
	}
	return provider.Features{AudioSelection: true}
}

// NewClient constructs the AniCine provider.
func NewClient() (*Client, error) {
	return &Client{
		http:      httpclient.New(),
		gqlBase:   config.AniListAPIBase,
		embedBase: megaplayBase,
	}, nil
}

// NewClientWithBaseURL constructs the provider against a custom base URL.
// In tests a single httptest server stands in for both the AniList GraphQL
// endpoint and the Megaplay embeds, so both bases point at it.
func NewClientWithBaseURL(baseURL string) (*Client, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		http:      httpclient.New(),
		gqlBase:   baseURL,
		embedBase: baseURL,
	}, nil
}

// Search queries AniList for anime.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query, "mode", mode)
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	results, err := anilist.SearchWithEndpoint(ctx, c.http, query, c.gqlBase)
	if err != nil {
		return nil, fmt.Errorf("anicine search: %w", err)
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

// FetchEpisodes lists sub/dub episode entries for an anime series,
// synthesizing sub/dub entries from AniList episode counts.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	anID, err := strconv.Atoi(strings.TrimSpace(mediaID))
	if err != nil || anID <= 0 {
		return nil, provider.ErrNoEpisodes
	}
	m, err := anilist.FetchMediaWithEndpoint(ctx, c.http, anID, c.gqlBase)
	if err != nil {
		return nil, fmt.Errorf("anicine episodes: %w", err)
	}
	count := m.EpisodeCount()
	if count < 1 {
		count = 1
	}
	seasonNum := kit.ParseSeason(series.Title)
	if seasonNum <= 0 {
		seasonNum = 1
	}
	eps := make([]provider.Episode, 0, count*2)
	for i := 1; i <= count; i++ {
		epTitle := fmt.Sprintf("Episode %d", i)
		eps = append(eps,
			provider.Episode{
				Title:   epTitle,
				ID:      fmt.Sprintf("watch/anicine/%s/sub/%d", mediaID, i),
				Episode: i,
				Season:  seasonNum,
				Audio:   "sub",
			},
			provider.Episode{
				Title:   epTitle,
				ID:      fmt.Sprintf("watch/anicine/%s/dub/%d", mediaID, i),
				Episode: i,
				Season:  seasonNum,
				Audio:   "dub",
			},
		)
	}
	return eps, nil
}

// ResolveSource resolves one anime episode to its direct stream source.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episodeID", episode.ID)

	// Anime episodes carry watch/anicine route IDs; everything else cannot
	// resolve through this provider.
	if h := kit.ParseHandle(mediaID, episode.ID, episode.Audio, episode.Episode, "anicine"); h.Valid() && isAnimeRoute(episode) {
		return c.resolveAnime(ctx, h.AniListID, h.Category, h.Number)
	}
	return nil, fmt.Errorf("anicine resolve: invalid episode parameters")
}

// isAnimeRoute reports whether this resolution belongs to the anime flow:
// an explicit watch/anicine episode id, or dub/sub episode audio (only
// anime episodes carry an Audio tag).
func isAnimeRoute(episode provider.Episode) bool {
	if strings.HasPrefix(strings.TrimSpace(episode.ID), "watch/anicine/") {
		return true
	}
	return episode.Audio == "sub" || episode.Audio == "dub"
}

var (
	_ provider.Provider         = (*Client)(nil)
	_ provider.Presenter        = (*Client)(nil)
	_ provider.FeatureSource    = (*Client)(nil)
	_ provider.MovieEpisodeFlow = (*Client)(nil)
)
