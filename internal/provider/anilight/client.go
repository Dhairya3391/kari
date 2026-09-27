package anilight

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
)

// directAPIBase is the AniLight site API backing direct resolution.
const directAPIBase = "https://api.anilight.live/api"

// Client implements provider.Provider by resolving streams directly: the
// AniLight site API supplies slugs, watch data, and per-provider sources,
// Megaplay embeds supply AES-encrypted streams decrypted in crypto.go, and
// AniList supplies the search index plus the episode-count fallback. No
// intermediary API or proxy is involved; every returned URL is a direct
// address playable by mpv.
type Client struct {
	http    *http.Client
	apiBase string
	gqlBase string
}

// Alias implements provider.Presenter, providing the human-readable display name.
func (c *Client) Alias() string { return "AniLight" }

// Name implements provider.Provider, returning the stable registry identifier.
func (c *Client) Name() string {
	return "anilight"
}

// Modes implements provider.Provider, registering AniLight as an anime provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 3},
	}
}

// RequiresEpisodeListForMovies implements provider.MovieEpisodeFlow. AniLight
// resolves playback via per-episode endpoints, so anime movies must query
// FetchEpisodes first.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Features implements provider.FeatureSource. Anime media supports separate sub/dub audio tracks.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeAnime {
		return provider.Features{}
	}
	return provider.Features{AudioSelection: true}
}

// NewClient constructs the AniLight provider with the shared HTTP client.
func NewClient() (*Client, error) {
	return &Client{
		http:    httpclient.New(),
		apiBase: directAPIBase,
		gqlBase: config.AniListAPIBase,
	}, nil
}

// NewClientWithBaseURL constructs the AniLight provider against a custom
// base URL. In tests a single httptest server stands in for both the
// AniLight site API and the AniList GraphQL endpoint, so both bases point
// at it.
func NewClientWithBaseURL(baseURL string) (*Client, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		http:    httpclient.New(),
		apiBase: baseURL,
		gqlBase: baseURL,
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
		return nil, fmt.Errorf("anilight search: %w", err)
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	logging.Debug("search done via anilist", "provider", c.Name(), "results", len(results))
	return results, nil
}

// FetchAvailableEpisodes lists only tracks backed by AniLight watch embeds.
func (c *Client) FetchAvailableEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	if slug, err := c.resolveSlug(ctx, mediaID); err == nil && slug != "" {
		if watch, wErr := c.fetchWatchData(ctx, slug); wErr == nil && watch != nil {
			if eps := episodesFromWatch(mediaID, watch); len(eps) > 0 {
				return eps, nil
			}
		}
	}
	return nil, provider.ErrNoEpisodes
}

// FetchEpisodes lists sub/dub episodes for an AniList media ID from the
// AniLight watch data, falling back to an AniList count when the site has no
// catalog entry.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	logging.Debug("fetch episodes", "provider", c.Name(), "mediaID", mediaID)

	if eps, err := c.FetchAvailableEpisodes(ctx, series); err == nil {
		return eps, nil
	} else {
		logging.Debug("direct watch data unavailable; falling back to anilist count", "provider", c.Name(), "mediaID", mediaID)
	}

	anID, err := atoiPositive(mediaID)
	if err != nil {
		return nil, provider.ErrNoEpisodes
	}
	m, err := anilist.FetchMediaWithEndpoint(ctx, c.http, anID, c.gqlBase)
	if err != nil {
		return nil, fmt.Errorf("anilight episodes: %w", err)
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
				ID:      fmt.Sprintf("watch/anilight/%s/sub/%d", mediaID, i),
				Episode: i,
				Season:  1,
				Audio:   "sub",
			},
			provider.Episode{
				Title:   epTitle,
				ID:      fmt.Sprintf("watch/anilight/%s/dub/%d", mediaID, i),
				Episode: i,
				Season:  1,
				Audio:   "dub",
			},
		)
	}
	sortEpisodes(eps)
	return eps, nil
}

// episodesFromWatch converts AniLight watch episodes into sub/dub entries.
// An audio track is emitted only when the episode carries an embed URL for
// it, so mpv never receives a track the backend cannot serve.
func episodesFromWatch(mediaID string, watch *anilightWatchResp) []provider.Episode {
	eps := make([]provider.Episode, 0, len(watch.Episodes)*2)
	for _, e := range watch.Episodes {
		if e.Number <= 0 {
			continue
		}
		title := cleanAniLightText(e.Title)
		if title == "" {
			title = fmt.Sprintf("Episode %d", e.Number)
		}
		if u := strings.TrimSpace(e.EmbedURL["sub"]); u != "" {
			eps = append(eps, provider.Episode{
				Title:   title,
				ID:      fmt.Sprintf("watch/anilight/%s/sub/%d", mediaID, e.Number),
				Episode: e.Number,
				Season:  1,
				Audio:   "sub",
			})
		}
		if u := strings.TrimSpace(e.EmbedURL["dub"]); u != "" {
			eps = append(eps, provider.Episode{
				Title:   title,
				ID:      fmt.Sprintf("watch/anilight/%s/dub/%d", mediaID, e.Number),
				Episode: e.Number,
				Season:  1,
				Audio:   "dub",
			})
		}
	}
	sortEpisodes(eps)
	return eps
}

func sortEpisodes(eps []provider.Episode) {
	slices.SortFunc(eps, func(a, b provider.Episode) int {
		if a.Episode != b.Episode {
			return a.Episode - b.Episode
		}
		// Sub first: the default audio track users expect.
		if a.Audio != b.Audio {
			if a.Audio == "sub" {
				return -1
			}
			if b.Audio == "sub" {
				return 1
			}
			return strings.Compare(a.Audio, b.Audio)
		}
		return 0
	})
}

// ResolveSource resolves one episode to direct stream sources via the
// AniLight site API and Megaplay embeds.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episodeID", episode.ID)

	h := kit.ParseHandle(mediaID, episode.ID, episode.Audio, episode.Episode, "anilight")
	if !h.Valid() {
		return nil, fmt.Errorf("anilight resolve: invalid episode parameters")
	}
	sources, err := c.resolveDirectStreams(ctx, h.AniListID, h.Category, h.Number)
	if err != nil {
		return nil, fmt.Errorf("anilight resolve: %w", err)
	}
	// Defense in depth: only direct CDN addresses reach mpv.
	sources = provider.FilterDirectSources(sources)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	logging.Debug("resolve source done via direct streams", "provider", c.Name(), "count", len(sources))
	return sources, nil
}

func cleanAniLightText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

// atoiPositive parses a numeric media ID, rejecting zero and negatives so
// slug-shaped IDs never reach the AniList fallback.
func atoiPositive(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid media ID %q", s)
	}
	return n, nil
}

var (
	_ provider.Provider                  = (*Client)(nil)
	_ provider.Presenter                 = (*Client)(nil)
	_ provider.FeatureSource             = (*Client)(nil)
	_ provider.MovieEpisodeFlow          = (*Client)(nil)
	_ provider.EpisodeAvailabilitySource = (*Client)(nil)
)
