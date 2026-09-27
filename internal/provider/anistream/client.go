// Package anistream implements provider.Provider for anime via the
// Anistream catalogue (https://anistream.one, REST at pp.animex.one with
// fallback api.anistream.one).
//
//   - Search and episode listings reuse the shared AniList index: catalogue
//     IDs are AniList IDs, so no site-specific search is needed.
//   - ResolveSource maps an AniList ID to the catalogue entry, lists the
//     episode's servers, and resolves every server's direct HLS streams.
//     Each stream carries a verified or explicitly soft subtitle declaration,
//     which the shared ranking prefers hard-first.
//   - yuki-style token URLs need server-side playlist rewriting (poisoned
//     segments); kari hosts no proxy, so those streams are dropped and the
//     remaining servers cover the episode.
//
// Every returned URL is directly playable by mpv; playback fallback across
// ranked sources replaces the reference implementation's resolve-time mpv
// probing.
package anistream

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
	"kari/internal/tmdb"
)

// Upstream bases backing direct resolution, tried in order.
var apiBases = []string{
	"https://pp.animex.one",
	"https://api.anistream.one",
}

const (
	siteBase    = "https://anistream.one"
	siteReferer = "https://anistream.one/"
	catalogTTL  = 3600 // seconds
)

// catalogEntry maps an AniList ID to the catalogue's internal id.
type catalogEntry struct {
	catalogID string
	cachedAt  time.Time
}

// Client implements provider.Provider for Anistream anime.
type Client struct {
	http    *http.Client
	gqlBase string
	bases   []string

	mu       sync.Mutex
	catalogs map[string]catalogEntry
}

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "Anistream" }

// Name implements provider.Provider.
func (c *Client) Name() string { return "anistream" }

// Modes implements provider.Provider, registering Anistream for anime.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 2},
	}
}

// RequiresEpisodeListForMovies implements provider.MovieEpisodeFlow. Anime
// resolves via per-episode catalogue entries, so anime movies query
// FetchEpisodes first.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Features implements provider.FeatureSource. Anime supports sub/dub tracks.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeAnime {
		return provider.Features{}
	}
	return provider.Features{AudioSelection: true}
}

// NewClient constructs the Anistream provider with the shared HTTP client.
func NewClient() (*Client, error) {
	return NewClientWithBases(nil, "", nil)
}

// NewClientWithBases constructs the provider against custom endpoints.
// Empty values select production defaults; tests point all three at one
// httptest server (GraphQL at /, REST under /rest/api/).
func NewClientWithBases(keyPool *tmdb.KeyPool, gqlBase string, bases []string) (*Client, error) {
	_ = keyPool
	if gqlBase == "" {
		gqlBase = "https://graphql.animex.one/graphql"
	}
	if len(bases) == 0 {
		bases = apiBases
	}
	return &Client{
		http:     httpclient.New(),
		gqlBase:  strings.TrimRight(gqlBase, "/"),
		bases:    bases,
		catalogs: make(map[string]catalogEntry),
	}, nil
}

// Search queries the shared AniList GraphQL index; catalogue IDs are
// AniList IDs, so no site-specific search exists.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query)
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	results, err := anilist.Search(ctx, c.http, query)
	if err != nil {
		return nil, fmt.Errorf("anistream search: %w", err)
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

// FetchEpisodes builds sub/dub episode entries from the AniList episode
// count, mirroring the other anime providers.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	anID, err := strconv.Atoi(strings.TrimSpace(mediaID))
	if err != nil || anID <= 0 {
		return nil, provider.ErrNoEpisodes
	}
	m, err := anilist.FetchMedia(ctx, c.http, anID)
	if err != nil {
		return nil, fmt.Errorf("anistream episodes: %w", err)
	}
	count := m.EpisodeCount()
	if count < 1 {
		count = 1
	}
	eps := make([]provider.Episode, 0, count*2)
	for i := 1; i <= count; i++ {
		epTitle := fmt.Sprintf("Episode %d", i)
		eps = append(eps,
			provider.Episode{
				Title:   epTitle,
				ID:      fmt.Sprintf("watch/anistream/%s/sub/%d", mediaID, i),
				Episode: i,
				Season:  1,
				Audio:   "sub",
			},
			provider.Episode{
				Title:   epTitle,
				ID:      fmt.Sprintf("watch/anistream/%s/dub/%d", mediaID, i),
				Episode: i,
				Season:  1,
				Audio:   "dub",
			},
		)
	}
	return eps, nil
}

// ResolveSource resolves one episode to ranked direct streams across every
// server the catalogue lists for its audio track.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episodeID", episode.ID)

	h := kit.ParseHandle(mediaID, episode.ID, episode.Audio, episode.Episode, "anistream")
	if !h.Valid() {
		return nil, fmt.Errorf("anistream resolve: invalid episode parameters")
	}
	category := strings.ToLower(strings.TrimSpace(h.Category))
	if category != "dub" {
		category = "sub"
	}

	catalogID, err := c.catalogID(ctx, h.AniListID)
	if err != nil {
		return nil, fmt.Errorf("anistream resolve: %w", err)
	}

	servers, err := c.servers(ctx, catalogID, h.Number, category)
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		// The catalogue answered but lists no backends for this track:
		// verified absence, not a transport failure.
		return nil, fmt.Errorf("anistream resolve: no %s servers: %w", category, provider.ErrAudioUnavailable)
	}

	type result struct {
		sources []provider.MediaSource
	}
	results := make([]result, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func(i int, srv server) {
			defer wg.Done()
			results[i] = result{sources: c.resolveServer(ctx, catalogID, h.Number, category, srv)}
		}(i, srv)
	}
	wg.Wait()

	var sources []provider.MediaSource
	for _, r := range results {
		sources = append(sources, r.sources...)
	}

	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	sources = provider.FilterDirectSources(sources)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	logging.Debug("resolve source done via direct", "provider", c.Name(), "count", len(sources))
	return sources, nil
}

var (
	_ provider.Provider         = (*Client)(nil)
	_ provider.Presenter        = (*Client)(nil)
	_ provider.FeatureSource    = (*Client)(nil)
	_ provider.MovieEpisodeFlow = (*Client)(nil)
)
