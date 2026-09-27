// Package moovie implements provider.Provider for movies, TV, and
// cartoons via the Moovie upstream network (https://moovie.fun, API at
// https://hahaevilcraft.site), ported from temp/moovie/server.mjs.
//
//   - Search and episode listings reuse the shared TMDB-keyed streambase.
//   - ResolveSource fans out to every server-side upstream provider over
//     SSE, falling back to the REST search endpoint, and maps streams,
//     dubbed-audio tracks, and subtitles into kari sources.
//   - URLs that need header injection play through kari's native mpv
//     headers instead of a proxy; kari hosts no proxy server.
//   - The reference implementation mpv-probes every URL at resolve time;
//     kari skips that (playback fallback across ranked sources covers it)
//     so resolution stays fast.
//
// Every returned URL is directly playable by mpv.
package moovie

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

// streamAPIBase is the upstream source service backing direct resolution.
const streamAPIBase = "https://hahaevilcraft.site"

// Client implements provider.Provider against the Moovie catalog.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	apiBase    string

	mu            sync.Mutex
	providerCache []upstreamProvider
	providerExp   int64 // unix millis
}

// upstreamProvider is one server-side scraping backend.
type upstreamProvider struct {
	id       string
	name     string
	priority int
}

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "Moovie" }

// Name implements provider.Provider.
func (c *Client) Name() string { return "moovie" }

// Modes implements provider.Provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
	}
}

// Search delegates to the shared TMDB-keyed base.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes delegates to the shared TMDB-keyed base.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

// NewClient constructs the Moovie provider over the shared TMDB search base.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithBaseURL(keyPool, streamAPIBase)
}

// NewClientWithBaseURL constructs the provider against a custom source
// service base URL (used in tests).
func NewClientWithBaseURL(keyPool *tmdb.KeyPool, apiBase string) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:       base,
		httpClient: httpclient.New(),
		apiBase:    strings.TrimRight(apiBase, "/"),
	}, nil
}

// ResolveSource resolves playable sources for a TMDB id directly from the
// upstream provider list, then merges the scraped results best-first.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return nil, fmt.Errorf("invalid media ID: %w", err)
		}
	}

	mediaType := "movie"
	season, epNum := 1, 1
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = "tv"
		if episode.Season > 0 {
			season = episode.Season
		}
		if episode.Episode > 0 {
			epNum = episode.Episode
		}
	}

	merged, err := c.fetchSourcesDirect(ctx, mediaType, tmdbID, season, epNum)
	if err != nil {
		return nil, err
	}
	return mapMerged(merged), nil
}

// mapMerged converts merged shard results into media sources: dedupe by
// URL, direct CDN addresses with per-stream headers for mpv, dubbed audio
// rows as language-tagged sources.
func mapMerged(m *mergedResults) []provider.MediaSource {
	subs := make([]provider.SubtitleOption, 0, len(m.subtitles))
	for _, subtitle := range m.subtitles {
		subs = append(subs, provider.SubtitleOption{
			URL:      subtitle.url,
			Language: subtitle.lang,
			Referer:  subtitle.referer,
		})
	}
	sources := make([]provider.MediaSource, 0, len(m.sources)+len(m.audio))
	seen := make(map[string]struct{}, len(m.sources)+len(m.audio))
	for _, s := range m.sources {
		if _, ok := seen[s.url]; ok {
			continue
		}
		seen[s.url] = struct{}{}
		sources = append(sources, provider.MediaSource{
			URL:       s.url,
			Quality:   s.quality,
			Type:      s.typ,
			Referer:   s.referer,
			UserAgent: config.DesktopUserAgent,
			Subtitles: subs,
		})
	}
	for _, a := range m.audio {
		if _, ok := seen[a.url]; ok {
			continue
		}
		seen[a.url] = struct{}{}
		quality := "Auto"
		if a.lang != "" && a.lang != "en" {
			quality = fmt.Sprintf("Auto (%s)", lang.Name(a.lang))
		}
		sources = append(sources, provider.MediaSource{
			URL:       a.url,
			Quality:   quality,
			Type:      a.typ,
			Referer:   a.referer,
			UserAgent: config.DesktopUserAgent,
			Language:  a.lang,
			Subtitles: subs,
		})
	}
	return sources
}

var (
	_ provider.Provider  = (*Client)(nil)
	_ provider.Presenter = (*Client)(nil)
)
