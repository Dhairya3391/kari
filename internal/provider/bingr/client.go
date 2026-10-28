// Package bingr implements provider.Provider for Bingr (https://bingr.one),
// a multi-format media catalog providing movies, television series,
// animation/cartoons, and anime.
//
// and cartoons, and the AniList index for anime. Stream resolution routes
// through Bingr's backend shard cluster (Aphelion, Polaris, Bastion, Moscow,
// Novo) for movies, television, and TMDB-mapped anime, augmented by HiAnime /
// MegaPlay for AniList-keyed subbed and dubbed anime streams.
package bingr

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

var (
	_ provider.Provider             = (*Client)(nil)
	_ provider.Presenter            = (*Client)(nil)
	_ provider.FeatureSource        = (*Client)(nil)
	_ provider.AudioLanguagesSource = (*Client)(nil)
	_ provider.StreamingProvider    = (*Client)(nil)
)

const (
	// providerID is the canonical registry handle for Bingr.
	providerID = "bingr"
)

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
	base        *streambase.Base
	httpClient  *http.Client
	apiBase     string
	filmuBase   string
	anilistBase string
	langFilter  map[string]bool

	tokenMu        sync.Mutex
	token          string
	tokenExp       time.Time
	metaCache      sync.Map
	animeMetaCache sync.Map
}

// NewClient constructs the Bingr provider over the shared TMDB search base
// pointing to production endpoints.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithLanguageFilter(keyPool, nil)
}

// NewClientWithLanguageFilter constructs the Bingr provider with an explicit audio language filter.
func NewClientWithLanguageFilter(keyPool *tmdb.KeyPool, langFilter map[string]bool) (*Client, error) {
	return NewClientWithURLs(keyPool, config.BingrAPIBase, config.FilmuAPIBase, config.AniListAPIBase, langFilter)
}

// NewClientWithURLs constructs the provider against custom endpoints so tests
// can mock external backends without live network requests.
func NewClientWithURLs(keyPool *tmdb.KeyPool, apiBase, filmuBase, anilistBase string, langFilter map[string]bool) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("bingr streambase: %w", err)
	}
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		apiBase = config.BingrAPIBase
	}
	filmuBase = strings.TrimRight(strings.TrimSpace(filmuBase), "/")
	if filmuBase == "" {
		filmuBase = config.FilmuAPIBase
	}
	anilistBase = strings.TrimRight(strings.TrimSpace(anilistBase), "/")
	if anilistBase == "" {
		anilistBase = config.AniListAPIBase
	}
	return &Client{
		base:        base,
		httpClient:  httpclient.New(),
		apiBase:     apiBase,
		filmuBase:   filmuBase,
		anilistBase: anilistBase,
		langFilter:  langFilter,
	}, nil
}

// Alias implements provider.Presenter with the user-facing display name.
func (c *Client) Alias() string { return "Bingr" }

// Name implements provider.Provider with the canonical identifier.
func (c *Client) Name() string { return providerID }

// Modes implements provider.Provider, declaring support for movies, TV series,
// cartoons, and anime.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
		{Name: provider.ModeAnime, Priority: 2},
	}
}

// Features implements provider.FeatureSource. Anime exposes separate sub and
// dub audio options.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode == provider.ModeAnime {
		return provider.Features{AudioSelection: true}
	}
	return provider.Features{}
}

// AudioLanguages implements provider.AudioLanguagesSource with the catalog's
// common audio tracks.
func (c *Client) AudioLanguages() []provider.AudioLanguage {
	return bingrAudioLanguages
}

// languageEnabled tests whether an audio language tag is permitted by the user's
// language filter.
func (c *Client) languageEnabled(tag string) bool {
	if c.langFilter == nil || tag == "" {
		return true
	}
	code := lang.Normalize(tag)
	display := lang.Name(code)
	for _, k := range []string{tag, code, display} {
		if k == "" {
			continue
		}
		if v, ok := c.langFilter[k]; ok {
			return v
		}
		if v, ok := c.langFilter[strings.ToLower(k)]; ok {
			return v
		}
	}
	return true
}

// Search resolves anime via AniList index and movies/TV/cartoon through TMDB.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query, "mode", mode)
	if strings.TrimSpace(query) == "" {
		return nil, provider.ErrNoResults
	}
	if mode == provider.ModeAnime {
		results, err := anilist.SearchWithEndpoint(ctx, c.httpClient, query, c.anilistBase)
		if err == nil && len(results) > 0 {
			return results, nil
		}
		// Fallback to Bingr's internal anime search endpoint if AniList is unreachable.
		fallback, fErr := c.searchAnimeBingr(ctx, query)
		if fErr == nil && len(fallback) > 0 {
			return fallback, nil
		}
		if err != nil {
			return nil, fmt.Errorf("bingr anime search: %w", err)
		}
		return nil, provider.ErrNoResults
	}
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes returns episode lists: AniList-backed sub/dub pairs for anime,
// or TMDB season/episode trees for movies, TV, and cartoons.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if isAnimeSeries(series) {
		return c.fetchAnimeEpisodes(ctx, series)
	}
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
	logging.Debug("resolve stream start", "provider", c.Name(), "mediaID", mediaID, "episode", episode.Episode, "audio", episode.Audio)
	if isAnimeEpisode(mediaID, episode) {
		return c.resolveAnime(ctx, mediaID, episode, updates)
	}
	err := c.resolveDirect(ctx, mediaID, episode, updates)
	if err != nil {
		if meta, aErr := c.fetchAnimeMeta(ctx, mediaID); aErr == nil && (meta.IDMal > 0 || meta.TMDBID > 0) {
			logging.Debug("falling back to anime resolution for AniList ID", "mediaID", mediaID)
			return c.resolveAnime(ctx, mediaID, episode, updates)
		}
	}
	return err
}

func isAnimeSeries(series provider.SearchResult) bool {
	return series.Type == provider.ModeAnime || series.MediaType == provider.MediaTypeAnime || series.AniListID > 0
}

func isAnimeEpisode(mediaID string, episode provider.Episode) bool {
	epID := strings.TrimSpace(episode.ID)
	mID := strings.TrimSpace(mediaID)
	if strings.HasPrefix(epID, "watch/") || strings.HasPrefix(mID, "watch/") {
		return true
	}
	if episode.Audio == provider.AudioSub || episode.Audio == provider.AudioDub {
		return true
	}
	if strings.HasPrefix(mID, "anilist:") || strings.HasPrefix(epID, "anilist:") {
		return true
	}
	if strings.HasPrefix(mID, "mal:") || strings.HasPrefix(epID, "mal:") {
		return true
	}
	return false
}
