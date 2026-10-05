// Package anicine implements provider.Provider for AniCine
// (https://anicine.xyz) across anime, movies, TV, and cartoons.
//
//   - Search reuses the shared indexes: AniList GraphQL for anime, the
//     TMDB-keyed streambase for movies/TV/cartoons. No site scraping.
//   - Anime resolves via AniList MAL IDs straight into Megaplay embeds
//     (https://megaplay.buzz/stream/mal/{id}/{ep}/{sub|dub}), decrypted
//     with the shared kit megaplay helpers — the same flow anicine's own
//     anicine-1 server uses.
//   - Movies/TV resolve through the CinePro worker backing anicine's
//     players: a Bearer token plus per-title endpoints returning direct
//     (worker-proxied) HLS URLs that mpv plays natively.
//
// Every returned URL is directly playable by mpv; kari hosts no proxy.
package anicine

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

// Site and infrastructure bases backing direct resolution.
const (
	// cineproBase is the worker serving anicine's players and its
	// token/sources API.
	cineproBase = "https://aniwish.dekhovo.workers.dev"
	// megaplayBase serves the anime embeds anicine addresses by MAL id.
	megaplayBase = "https://megaplay.buzz"
)

// Client implements provider.Provider for AniCine.
type Client struct {
	base       *streambase.Base
	http       *http.Client
	keyPool    *tmdb.KeyPool
	workerBase string
	gqlBase    string
	embedBase  string

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "AniCine" }

// Name implements provider.Provider.
func (c *Client) Name() string { return "anicine" }

// Modes implements provider.Provider, registering AniCine as the primary
// source for anime, movies, TV, and cartoons.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 1},
		{Name: provider.ModeMovies, Priority: 1},
		{Name: provider.ModeTV, Priority: 1},
		{Name: provider.ModeCartoon, Priority: 1},
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

// NewClient constructs the AniCine provider over the shared TMDB search base.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	c, err := NewClientWithBaseURL(keyPool, cineproBase)
	if err != nil {
		return nil, err
	}
	c.gqlBase = config.AniListAPIBase
	c.embedBase = megaplayBase
	return c, nil
}

// NewClientWithBaseURL constructs the provider against a custom worker base
// URL. In tests a single httptest server stands in for both the worker and
// the AniList GraphQL endpoint, so both bases point at it.
func NewClientWithBaseURL(keyPool *tmdb.KeyPool, workerBase string) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(workerBase, "/")
	return &Client{
		base:       base,
		http:       httpclient.New(),
		keyPool:    keyPool,
		workerBase: baseURL,
		gqlBase:    baseURL,
		embedBase:  baseURL,
	}, nil
}

// Search queries AniList for anime and the shared TMDB-keyed base for
// movies, TV, and cartoons.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query, "mode", mode)
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	if mode == provider.ModeAnime {
		results, err := anilist.SearchWithEndpoint(ctx, c.http, query, c.gqlBase)
		if err != nil {
			return nil, fmt.Errorf("anicine search: %w", err)
		}
		if len(results) == 0 {
			return nil, provider.ErrNoResults
		}
		return results, nil
	}
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes lists episodes: AniList-counted sub/dub entries for anime,
// delegated TMDB listings otherwise. Anime results always carry Type
// ModeAnime (shared anilist search); MediaTypeAnime backs callers that
// leave Type unset.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if series.Type == provider.ModeAnime || series.MediaType == provider.MediaTypeAnime {
		return c.fetchAnimeEpisodes(ctx, series)
	}
	return c.base.FetchEpisodes(ctx, series)
}

// fetchAnimeEpisodes builds sub/dub episode entries from the AniList
// episode count, mirroring the anilight fallback shape.
func (c *Client) fetchAnimeEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
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

// ResolveSource resolves one episode or TMDB title to direct stream sources.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episodeID", episode.ID)

	// Anime episodes carry watch/anicine route IDs; everything else is a
	// TMDB id resolved through the worker.
	if h := kit.ParseHandle(mediaID, episode.ID, episode.Audio, episode.Episode, "anicine"); h.Valid() && isAnimeRoute(episode) {
		return c.resolveAnime(ctx, h.AniListID, h.Category, h.Number)
	}
	return c.resolveTMDB(ctx, mediaID, episode)
}

// isAnimeRoute reports whether this resolution belongs to the anime flow:
// an explicit watch/anicine episode id, or dub/sub episode audio (only
// anime episodes carry an Audio tag; TMDB-backed episodes leave it blank).
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
