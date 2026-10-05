// Package miruro implements provider.Provider for anime via the Miruro
// site (SvelteKit, no public API).
//
// All three stages ride the site's own __data.json endpoints, the same
// payloads its frontend consumes:
//
//   - Search hits /search/__data.json?q=..., whose items carry the
//     Miruro show id plus external_ids (AniList first). Result IDs stay
//     AniList-keyed like every other anime provider, so cross-provider
//     resolution, history matching, and search dedup keep working.
//   - FetchEpisodes resolves the AniList ID to a (showID, slug) pair via
//     one exact-match search, then reads the full episode list from the
//     watch chunk (id, number, title, filler). Sub and dub entries are
//     emitted per episode; resolution verifies the track exists.
//   - ResolveSource reads the watch chunk's tracks (sub/ssub/dub) with
//     per-server streams, headers, and subtitles. Stream URLs are signed
//     and short-lived, so they are fetched fresh on every resolve.
//
// SvelteKit serializes these payloads with devalue (index-pointer arrays);
// devalue.go holds the minimal hydrator. Every returned URL is a direct
// CDN address playable by mpv.
package miruro

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

const (
	// providerID is the stable registry identifier and episode-handle segment.
	providerID = "miruro"
	// placeholderSlug stands in for the canonical slug on the first
	// watch request; the server answers with a redirect naming it.
	placeholderSlug = "x"
)

// Client implements provider.Provider for Miruro anime.
type Client struct {
	http    *http.Client
	base    string
	gqlBase string

	mu sync.Mutex
	// byAniList maps AniList ID to its Miruro show coordinates.
	byAniList map[string]showRef
	// slugs maps Miruro show ID to its canonical watch slug.
	slugs map[string]string
}

// showRef carries everything needed to build a watch URL.
type showRef struct {
	showID string
	slug   string
}

// Alias implements provider.Presenter, providing the human-readable display name.
func (c *Client) Alias() string { return "Miruro" }

// Name implements provider.Provider, returning the stable registry identifier.
func (c *Client) Name() string { return providerID }

// Modes implements provider.Provider, registering Miruro for anime.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 2},
	}
}

// RequiresEpisodeListForMovies implements provider.MovieEpisodeFlow. Miruro
// resolves playback via per-episode watch URLs that only the episode
// listing (plus show resolution) can build, so anime movies must query
// FetchEpisodes first.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Features implements provider.FeatureSource. Anime media supports separate sub/dub audio tracks.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeAnime {
		return provider.Features{}
	}
	return provider.Features{AudioSelection: true}
}

// NewClient constructs the Miruro provider with the shared HTTP client.
func NewClient() (*Client, error) {
	return NewClientWithBases(config.MiruroBase, config.AniListAPIBase)
}

// NewClientWithBases constructs the Miruro provider against custom site and
// AniList GraphQL endpoints. Tests point both at one httptest server (site
// __data.json under /search and /watch, GraphQL under /graphql).
func NewClientWithBases(siteBase, gqlBase string) (*Client, error) {
	siteBase = strings.TrimRight(strings.TrimSpace(siteBase), "/")
	if siteBase == "" {
		return nil, fmt.Errorf("miruro: empty base URL")
	}
	if strings.TrimSpace(gqlBase) == "" {
		return nil, fmt.Errorf("miruro: empty GraphQL endpoint")
	}
	return &Client{
		http:      httpclient.New(),
		base:      siteBase,
		gqlBase:   strings.TrimRight(strings.TrimSpace(gqlBase), "/"),
		byAniList: make(map[string]showRef),
		slugs:     make(map[string]string),
	}, nil
}

// Search queries Miruro's search endpoint. Result IDs are AniList IDs so
// downstream layers (dedup, history, scrobble, cross-provider resolve)
// treat Miruro titles exactly like other anime providers' results.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query)
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("miruro search: empty query")
	}
	body, err := c.get(ctx, c.base+"/search/__data.json?q="+queryEscape(query))
	if err != nil {
		return nil, err
	}
	items, err := searchItems(body)
	if err != nil {
		return nil, err
	}
	results := make([]provider.SearchResult, 0, len(items))
	for _, it := range items {
		anilistID := firstExternalID(it.externalIDs, "anilist")
		if anilistID == "" {
			continue
		}
		title := pickTitle(it.titleEng, it.titleRomaji, it.titleNative)
		if title == "" {
			continue
		}
		mediaType := provider.MediaTypeAnime
		if strings.EqualFold(it.format, "MOVIE") {
			mediaType = provider.MediaTypeMovie
		}
		year := ""
		if it.seasonYear > 0 {
			year = strconv.Itoa(it.seasonYear)
		}
		results = append(results, provider.SearchResult{
			Title:     title,
			ID:        anilistID,
			Type:      provider.ModeAnime,
			Year:      year,
			MediaType: mediaType,
			TMDBID:    firstExternalIDInt(it.externalIDs, "tmdb_tv"),
			CoverURL:  it.coverURL,
		})
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	logging.Debug("search done", "provider", c.Name(), "results", len(results))
	return results, nil
}

// FetchEpisodes implements provider.Provider by listing the full episode
// roster from the watch payload, emitting sub and dub entries per
// episode. Dub availability is verified per episode at resolve time.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	anilistID := strings.TrimSpace(series.ID)
	logging.Debug("fetch episodes", "provider", c.Name(), "mediaID", anilistID)
	if anilistID == "" {
		return nil, provider.ErrNoEpisodes
	}
	ref, err := c.resolveShow(ctx, anilistID, series.Title, "")
	if err != nil {
		return nil, err
	}
	eps, err := c.watchEpisodes(ctx, ref, 1)
	if err != nil {
		return nil, err
	}
	seasonNum := kit.ParseSeason(series.Title)
	if seasonNum <= 0 {
		seasonNum = 1
	}
	out := make([]provider.Episode, 0, len(eps)*2)
	for _, ep := range eps {
		title := strings.TrimSpace(ep.title)
		if title == "" {
			title = fmt.Sprintf("Episode %d", ep.number)
		}
		showID := ref.showID
		out = append(out,
			provider.Episode{
				Title:   title,
				ID:      fmt.Sprintf("watch/%s/%s/%s/%d", providerID, showID, provider.AudioSub, ep.number),
				Episode: ep.number,
				Season:  seasonNum,
				Audio:   provider.AudioSub,
				Filler:  ep.filler,
			},
			provider.Episode{
				Title:   title,
				ID:      fmt.Sprintf("watch/%s/%s/%s/%d", providerID, showID, provider.AudioDub, ep.number),
				Episode: ep.number,
				Season:  seasonNum,
				Audio:   provider.AudioDub,
				Filler:  ep.filler,
			},
		)
	}
	if len(out) == 0 {
		return nil, provider.ErrNoEpisodes
	}
	logging.Debug("fetch episodes done", "provider", c.Name(), "count", len(out))
	return out, nil
}

// ResolveSource implements provider.Provider by reading the episode's
// watch chunk and returning its ranked direct streams.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episodeID", episode.ID)
	h := kit.ParseHandle(mediaID, episode.ID, episode.Audio, episode.Episode, providerID)
	if !h.Valid() {
		return nil, fmt.Errorf("miruro resolve: invalid episode parameters")
	}
	category := strings.ToLower(strings.TrimSpace(h.Category))
	if category != provider.AudioDub {
		category = provider.AudioSub
	}
	// Own episode handles carry the Miruro show ID in the ID segment;
	// cross-provider episodes resolve through the AniList ID instead.
	var showID string
	if parts := strings.Split(strings.TrimSpace(episode.ID), "/"); len(parts) >= 5 && parts[0] == "watch" && parts[1] == providerID {
		showID = strings.TrimSpace(parts[2])
	}
	anilistID := strings.TrimSpace(mediaID)
	if showID == "" {
		anilistID = strings.TrimSpace(h.AniListID)
	}
	ref, err := c.resolveShow(ctx, anilistID, "", showID)
	if err != nil {
		return nil, err
	}
	tracks, order, err := c.watchTracks(ctx, ref, h.Number)
	if err != nil {
		return nil, err
	}
	sources := rankStreams(tracks, order, category)
	// Defense in depth: only direct CDN addresses reach mpv.
	sources = provider.FilterDirectSources(sources)
	if len(sources) == 0 {
		if category == provider.AudioDub && len(tracks) > 0 {
			return nil, fmt.Errorf("miruro resolve: no dub streams: %w", provider.ErrAudioUnavailable)
		}
		return nil, provider.ErrNoSources
	}
	logging.Debug("resolve source done", "provider", c.Name(), "count", len(sources))
	return sources, nil
}

var (
	_ provider.Provider         = (*Client)(nil)
	_ provider.Presenter        = (*Client)(nil)
	_ provider.FeatureSource    = (*Client)(nil)
	_ provider.MovieEpisodeFlow = (*Client)(nil)
)
