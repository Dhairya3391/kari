// Package movysx implements provider.Provider for the Movy.sx catalog
// (https://www.movy.sx). Search and episode listings come from the shared
// TMDB-keyed streambase; source resolution talks to the source
// infrastructure directly (seed + city shards + stream-cipher decrypt in
// direct.go, ported from temp/movy/server.mjs). Every returned URL is a
// direct CDN address playable by mpv.
package movysx

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

// movyReferer must be sent by the player when streaming resolver URLs.
const movyReferer = config.MovyReferer

// Client implements provider.Provider against the Movy.sx catalog.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	keyPool    *tmdb.KeyPool
	streamBase string
	tmdbBase   string
	seedCache  sync.Map // key: int tmdbID → seedEntry
	langFilter map[string]bool
}

// NewClient constructs the Movy.sx provider over the shared TMDB search base.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithLanguageFilter(keyPool, nil)
}

// NewClientWithLanguageFilter constructs Movy.sx with an explicit language filter.
func NewClientWithLanguageFilter(keyPool *tmdb.KeyPool, langFilter map[string]bool) (*Client, error) {
	return NewClientWithBaseURLAndLanguageFilter(keyPool, defaultStreamBase, langFilter)
}

// NewClientWithBaseURL constructs the provider against a custom source
// service base URL (used in tests).
func NewClientWithBaseURL(keyPool *tmdb.KeyPool, apiBase string) (*Client, error) {
	return NewClientWithBaseURLAndLanguageFilter(keyPool, apiBase, nil)
}

// NewClientWithBaseURLAndLanguageFilter constructs the provider against a custom
// source service base URL and explicit language filter.
func NewClientWithBaseURLAndLanguageFilter(keyPool *tmdb.KeyPool, apiBase string, langFilter map[string]bool) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:       base,
		httpClient: httpclient.New(),
		keyPool:    keyPool,
		streamBase: strings.TrimRight(apiBase, "/"),
		tmdbBase:   config.TMDBAPIBase,
		langFilter: langFilter,
	}, nil
}

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "Movy.sx" }

// Name implements provider.Provider.
func (c *Client) Name() string { return "movysx" }

// Modes implements provider.Provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
	}
}

// PreferredForPlayback marks Movy.sx as the repair priority for movie, TV,
// and cartoon playback.
func (c *Client) PreferredForPlayback(mode provider.ContentType) bool {
	switch mode {
	case provider.ModeMovies, provider.ModeTV, provider.ModeCartoon:
		return true
	default:
		return false
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

// ResolveSource resolves playable sources for a TMDB id directly: TMDB
// metadata plus the source seed are fetched concurrently, then every city
// shard is queried and the decrypted results merged best-first.
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
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = "tv"
	}

	// Metadata and seed are independent: fetch both at once.
	var meta *tmdbMeta
	var seed string
	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		m, err := c.fetchMeta(gCtx, tmdbID, mediaType)
		if err != nil {
			return err
		}
		meta = m
		return nil
	})
	g.Go(func() error {
		s, err := c.fetchSeed(gCtx, tmdbID)
		if err != nil {
			return err
		}
		seed = s
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("movysx resolve: %w", err)
	}
	title := meta.title()
	if title == "" {
		return nil, fmt.Errorf("movysx resolve: empty title: %w", provider.ErrNotFound)
	}
	merged, err := c.fetchSourcesDirect(ctx, mediaType, tmdbID, episode.Season, episode.Episode,
		title, meta.originalTitle(), meta.year(), meta.imdbID(), meta.NumberOfSeasons, seed)
	if err != nil {
		return nil, err
	}
	sources := provider.FilterDirectSources(c.mapMerged(merged))
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

// mapMerged converts merged shard results into media sources: dedupe by
// URL, direct CDN addresses with the Movy referer, dubbed audio rows as
// language-tagged sources.
func (c *Client) mapMerged(m *mergedResults) []provider.MediaSource {
	subs := make([]provider.SubtitleOption, 0, len(m.subtitles))
	for _, s := range m.subtitles {
		subs = append(subs, provider.SubtitleOption{
			URL:      s.url,
			Language: s.lang,
			Referer:  movyReferer,
		})
	}
	sources := make([]provider.MediaSource, 0, len(m.sources)+len(m.audio))
	seen := make(map[string]struct{}, len(m.sources)+len(m.audio))
	for _, s := range m.sources {
		if _, ok := seen[s.url]; ok {
			continue
		}
		seen[s.url] = struct{}{}
		if !c.languageEnabled("en") {
			continue
		}
		sources = append(sources, provider.MediaSource{
			URL:       s.url,
			Quality:   s.quality,
			Type:      streamType(s.url, s.typ),
			Referer:   movyReferer,
			UserAgent: config.DesktopUserAgent,
			Language:  "en",
			Subtitles: subs,
		})
	}
	for _, a := range m.audio {
		if _, ok := seen[a.url]; ok {
			continue
		}
		seen[a.url] = struct{}{}
		if !c.languageEnabled(a.lang) {
			continue
		}
		sources = append(sources, provider.MediaSource{
			URL:       a.url,
			Quality:   "Auto",
			Type:      streamType(a.url, a.typ),
			Referer:   movyReferer,
			UserAgent: config.DesktopUserAgent,
			Language:  a.lang,
			Subtitles: subs,
		})
	}
	return sources
}

// streamType classifies a resolver stream for the player/downloader
// layers. The declared upstream type wins when known (hls/mp4/dash);
// otherwise the container is inferred from the URL, and unknown
// extensionless URLs keep the declared value (usually "auto") instead
// of a wrong guess.
func streamType(rawURL, declared string) string {
	lowerDeclared := strings.ToLower(strings.TrimSpace(declared))
	switch lowerDeclared {
	case provider.SourceTypeHLS, provider.SourceTypeM3U8:
		return provider.SourceTypeHLS
	case provider.SourceTypeMP4:
		return provider.SourceTypeMP4
	case "dash":
		return "dash"
	}
	lowerURL := strings.ToLower(rawURL)
	switch {
	case strings.Contains(lowerURL, ".m3u8"):
		return provider.SourceTypeHLS
	case strings.Contains(lowerURL, ".mpd"):
		return "dash"
	case strings.Contains(lowerURL, ".mp4") ||
		strings.Contains(lowerURL, ".mkv") ||
		strings.Contains(lowerURL, ".webm") ||
		strings.Contains(lowerURL, ".avi") ||
		strings.Contains(lowerURL, ".mov"):
		return provider.SourceTypeMP4
	case lowerDeclared != "":
		return lowerDeclared
	default:
		return provider.SourceTypeMP4
	}
}

var movyAudioLanguages = []provider.AudioLanguage{
	{Code: "en", Display: "English"},
	{Code: "hi", Display: "Hindi"},
	{Code: "ta", Display: "Tamil"},
	{Code: "te", Display: "Telugu"},
	{Code: "es", Display: "Spanish"},
	{Code: "fr", Display: "French"},
	{Code: "pt", Display: "Portuguese"},
	{Code: "de", Display: "German"},
	{Code: "it", Display: "Italian"},
	{Code: "ar", Display: "Arabic"},
	{Code: "ru", Display: "Russian"},
	{Code: "ja", Display: "Japanese"},
	{Code: "ko", Display: "Korean"},
	{Code: "zh", Display: "Chinese"},
	{Code: "id", Display: "Indonesian"},
	{Code: "tr", Display: "Turkish"},
}

// AudioLanguages implements provider.AudioLanguagesSource.
func (c *Client) AudioLanguages() []provider.AudioLanguage {
	return movyAudioLanguages
}

func (c *Client) languageEnabled(tag string) bool {
	if c.langFilter == nil {
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

var (
	_ provider.Provider                 = (*Client)(nil)
	_ provider.PlaybackPreferenceSource = (*Client)(nil)
	_ provider.Presenter                = (*Client)(nil)
	_ provider.AudioLanguagesSource     = (*Client)(nil)
)
