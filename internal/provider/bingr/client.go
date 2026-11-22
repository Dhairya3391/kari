// Package bingr implements provider.Provider for Bingr (https://bingr.st),
// a movie, television and cartoon catalog.
//
// Playback comes from the sealed stream payload embedded in each
// https://bingr.one/watch page. Anime is served by Miruro, which already
// carries the same MegaPlay lanes, so Bingr no longer declares that mode.
package bingr

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

var (
	_ provider.Provider             = (*Client)(nil)
	_ provider.Presenter            = (*Client)(nil)
	_ provider.AudioLanguagesSource = (*Client)(nil)
	_ provider.StreamingProvider    = (*Client)(nil)
)

// providerID is the canonical registry handle for Bingr.
const providerID = "bingr"

var bingrAudioLanguages = []provider.AudioLanguage{
	{Code: "en", Display: "English"},
	{Code: "ja", Display: "Japanese"},
	{Code: "hi", Display: "Hindi"},
	{Code: "es", Display: "Spanish"},
	{Code: "fr", Display: "French"},
	{Code: "de", Display: "German"},
	{Code: "it", Display: "Italian"},
	{Code: "pt", Display: "Portuguese"},
	{Code: "ru", Display: "Russian"},
	{Code: "ar", Display: "Arabic"},
	{Code: "id", Display: "Indonesian"},
	{Code: "tl", Display: "Tagalog"},
	{Code: "ko", Display: "Korean"},
	{Code: "zh", Display: "Chinese"},
}

// Client implements provider.Provider against the Bingr streaming platform.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	siteBase   string
}

// NewClient constructs the Bingr provider over the shared TMDB search base
// pointing to production endpoints.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithURLs(keyPool, config.BingrSiteBase)
}

// NewClientWithURLs constructs the provider against a custom site origin so
// tests can mock watch pages without live network requests.
func NewClientWithURLs(keyPool *tmdb.KeyPool, siteBase string) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("bingr streambase: %w", err)
	}
	siteBase = strings.TrimRight(strings.TrimSpace(siteBase), "/")
	if siteBase == "" {
		siteBase = config.BingrSiteBase
	}
	return &Client{
		base:       base,
		httpClient: httpclient.New(),
		siteBase:   siteBase,
	}, nil
}

// Alias implements provider.Presenter with the user-facing display name.
func (c *Client) Alias() string { return "Bingr" }

// Name implements provider.Provider with the canonical identifier.
func (c *Client) Name() string { return providerID }

// Modes implements provider.Provider, declaring support for movies, TV series
// and cartoons.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
	}
}

// AudioLanguages implements provider.AudioLanguagesSource with the catalog's
// common audio tracks.
func (c *Client) AudioLanguages() []provider.AudioLanguage {
	return bingrAudioLanguages
}

// Search resolves titles through the shared TMDB index.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query, "mode", mode)
	if strings.TrimSpace(query) == "" {
		return nil, provider.ErrNoResults
	}
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes returns TMDB season/episode trees for movies, TV and cartoons.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

// ResolveSource resolves playable media sources, collecting all candidates into a single slice.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	ch := make(chan []provider.MediaSource, 16)
	var all []provider.MediaSource
	done := make(chan struct{})

	go func() {
		defer close(done)
		for batch := range ch {
			all = append(all, batch...)
		}
	}()

	err := c.ResolveStream(ctx, mediaID, episode, ch)
	close(ch)
	<-done

	sources := provider.FilterDirectSources(all)
	if len(sources) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

// ResolveStream implements provider.StreamingProvider, pushing playable source batches
// to updates as individual backend shards and resolvers deliver them.
func (c *Client) ResolveStream(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	logging.Debug("resolve stream start", "provider", c.Name(), "mediaID", mediaID, "episode", episode.Episode)
	return c.resolveDirect(ctx, mediaID, episode, updates)
}
