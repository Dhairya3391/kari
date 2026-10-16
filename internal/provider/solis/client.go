// Package solis implements provider.Provider against the hosted Solis
// streaming resolution API, resolving multi-backend video streams and subtitles
// without performing direct DOM scraping on client machines.
package solis

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

var (
	_ provider.Provider          = (*Client)(nil)
	_ provider.Presenter         = (*Client)(nil)
	_ provider.StreamingProvider = (*Client)(nil)
)

var defaultLavfDemuxerArgs = []string{
	"--demuxer=lavf",
	"--demuxer-lavf-format=hls",
	"--demuxer-lavf-o=extension_picky=0",
}

// Client implements provider.Provider against the hosted Solis resolution service.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	apiBase    string
}

// NewClient constructs the Solis provider over the shared TMDB search base
// pointing to the production resolution API.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithURL(keyPool, config.SolisAPIBase)
}

// NewClientWithURL constructs the Solis provider with a custom API endpoint,
// enabling integration tests to mock the resolution backend without network calls.
func NewClientWithURL(keyPool *tmdb.KeyPool, apiBase string) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("solis streambase: %w", err)
	}
	return &Client{
		base:       base,
		httpClient: httpclient.New(),
		apiBase:    strings.TrimRight(apiBase, "/"),
	}, nil
}

// Alias implements provider.Presenter with the user-facing display name.
func (c *Client) Alias() string { return "Solis" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "solis" }

// Modes implements provider.Provider, declaring support for movies, TV series,
// and cartoon/animation modes.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
	}
}

// Search delegates title discovery to the shared TMDB catalog index.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes delegates season and episode indexing to the shared TMDB catalog.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

// ResolveSource queries the Solis resolution API for all playable sources in one batch.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID, mediaType, season, epNum, err := parseEpisodeCoords(mediaID, episode)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("id", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	if mediaType == provider.MediaTypeTV {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(epNum))
	}

	endpoint := fmt.Sprintf("%s/api/resolve?%s", c.apiBase, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create solis resolve request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute solis resolve request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}

	var data apiResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode solis resolve response: %w", err)
	}
	if data.Error != "" {
		return nil, fmt.Errorf("solis api: %s", data.Error)
	}

	subs := toSubtitleOptions(data.Subtitles)
	var sources []provider.MediaSource
	for _, s := range data.Sources {
		src := toMediaSource(s, subs)
		if provider.IsDirectURL(src.URL) {
			sources = append(sources, src)
		}
	}

	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return provider.FilterDirectSources(sources), nil
}

// ResolveStream implements provider.StreamingProvider, consuming progressive SSE
// events from the Solis API and pushing resolved sources to the updates channel.
func (c *Client) ResolveStream(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	tmdbID, mediaType, season, epNum, err := parseEpisodeCoords(mediaID, episode)
	if err != nil {
		return err
	}

	params := url.Values{}
	params.Set("id", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	if mediaType == provider.MediaTypeTV {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(epNum))
	}
	params.Set("stream", "true")

	endpoint := fmt.Sprintf("%s/api/resolve?%s", c.apiBase, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create solis stream request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logging.Debug("solis streaming request failed; falling back to batch resolve", "err", err)
		return c.fallbackStream(ctx, mediaID, episode, updates)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logging.Debug("solis streaming returned non-200; falling back to batch resolve", "status", resp.StatusCode)
		return c.fallbackStream(ctx, mediaID, episode, updates)
	}

	var emitted []provider.MediaSource
	var currentSubs []provider.SubtitleOption
	seenURLs := make(map[string]struct{})
	delivered := 0

	scanner := bufio.NewScanner(resp.Body)
	var currentEvent string

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		switch currentEvent {
		case "source":
			var s apiSource
			if err := json.Unmarshal([]byte(payload), &s); err != nil {
				continue
			}
			cleanURL := strings.TrimSpace(s.URL)
			if !provider.IsDirectURL(cleanURL) {
				continue
			}
			if _, ok := seenURLs[cleanURL]; ok {
				continue
			}
			seenURLs[cleanURL] = struct{}{}

			src := toMediaSource(s, currentSubs)
			emitted = append(emitted, src)
			delivered++

			select {
			case updates <- []provider.MediaSource{src}:
			case <-ctx.Done():
				return ctx.Err()
			}

		case "subtitles":
			var apiSubs []apiSubtitle
			if err := json.Unmarshal([]byte(payload), &apiSubs); err != nil {
				continue
			}
			currentSubs = append(currentSubs, toSubtitleOptions(apiSubs)...)

			if len(emitted) > 0 {
				updated := make([]provider.MediaSource, len(emitted))
				for i, src := range emitted {
					src.Subtitles = currentSubs
					updated[i] = src
				}
				select {
				case updates <- updated:
				case <-ctx.Done():
					return ctx.Err()
				}
			}

		case "done":
			if delivered == 0 {
				return provider.ErrNoSources
			}
			return nil

		case "error":
			var errPayload struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal([]byte(payload), &errPayload)
			if delivered == 0 {
				if errPayload.Message != "" {
					return errors.New(errPayload.Message)
				}
				return provider.ErrNoSources
			}
			return nil
		}
	}

	if err := scanner.Err(); err != nil && delivered == 0 {
		return fmt.Errorf("solis stream scan: %w", err)
	}
	if delivered == 0 {
		return provider.ErrNoSources
	}
	return nil
}

func (c *Client) fallbackStream(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	sources, err := c.ResolveSource(ctx, mediaID, episode)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return provider.ErrNoSources
	}
	select {
	case updates <- sources:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type apiSource struct {
	URL       string   `json:"url"`
	Quality   string   `json:"quality"`
	Type      string   `json:"type"`
	Referer   string   `json:"referer"`
	UserAgent string   `json:"userAgent"`
	ExtraArgs []string `json:"extraArgs"`
}

type apiSubtitle struct {
	URL      string `json:"url"`
	Language string `json:"language"`
}

type apiResolveResponse struct {
	Sources   []apiSource   `json:"sources"`
	Subtitles []apiSubtitle `json:"subtitles"`
	Error     string        `json:"error,omitempty"`
}

func toMediaSource(s apiSource, subs []provider.SubtitleOption) provider.MediaSource {
	src := provider.MediaSource{
		URL:       strings.TrimSpace(s.URL),
		Quality:   strings.TrimSpace(s.Quality),
		Type:      strings.TrimSpace(s.Type),
		Referer:   strings.TrimSpace(s.Referer),
		UserAgent: strings.TrimSpace(s.UserAgent),
		ExtraArgs: append([]string(nil), s.ExtraArgs...),
		Subtitles: subs,
	}
	if src.Type == "" {
		src.Type = provider.SourceTypeHLS
	}
	if src.UserAgent == "" {
		src.UserAgent = config.DesktopUserAgent
	}
	if len(src.ExtraArgs) == 0 {
		src.ExtraArgs = append([]string(nil), defaultLavfDemuxerArgs...)
	}
	return src
}

func toSubtitleOptions(apiSubs []apiSubtitle) []provider.SubtitleOption {
	var subs []provider.SubtitleOption
	seen := make(map[string]struct{})
	for _, sub := range apiSubs {
		u := strings.TrimSpace(sub.URL)
		if u == "" {
			continue
		}
		norm := lang.Normalize(sub.Language)
		if norm == "" {
			norm = "und"
		}
		key := u + ":" + norm
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		subs = append(subs, provider.SubtitleOption{
			URL:      u,
			Language: norm,
		})
	}
	return subs
}

func parseEpisodeCoords(mediaID string, episode provider.Episode) (int, string, int, int, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %w", err)
		}
		if tmdbID <= 0 {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %q", mediaID)
		}
	}

	mediaType := provider.MediaTypeMovie
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = provider.MediaTypeTV
	}
	season := episode.Season
	if mediaType == provider.MediaTypeTV && season <= 0 {
		season = 1
	}
	epNum := episode.Episode
	if mediaType == provider.MediaTypeTV && epNum <= 0 {
		epNum = 1
	}
	return tmdbID, mediaType, season, epNum, nil
}
