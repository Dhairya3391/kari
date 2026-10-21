// Package anivault implements provider.Provider for anime via AniVault's
// scraper API.
//
// AniVault's site (https://anivault.tv) sits behind a Cloudflare challenge
// for non-browser clients, so this provider talks to the public API its own
// frontend consumes. Search is AniList-keyed, and episode/stream routes take
// the same AniList ID, which keeps cross-provider resolution and history
// matching consistent with the other anime providers.
//
// AniVault playback is Anikoto-backed and soft-subbed; AnimeHeaven supplies
// hard-subbed MP4s and fills catalog gaps. Stream URLs are signed and
// short-lived, so they are resolved fresh on every playback request. Only
// direct CDN addresses are returned — the API's local proxy routes are
// unwrapped, with their upstream referer kept because the CDNs reject
// requests without it.
package anivault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

const (
	// providerID is the stable registry identifier.
	providerID = "anivault"
	// sourceAniVault is the API's AniVault catalog + Anikoto playback source.
	sourceAniVault = "anivault"
	// sourceAnimeHeaven is the fallback source for titles Anikoto lacks.
	sourceAnimeHeaven = "animeheaven"
)

// Client implements provider.Provider for AniVault anime.
type Client struct {
	http *http.Client
	base string
}

// playbackSource is one episode-list source and whether it carries dubs.
type playbackSource struct {
	name string
	dub  bool
}

// Alias implements provider.Presenter, providing the human-readable display
// name.
func (c *Client) Alias() string { return "AniVault" }

// Name implements provider.Provider, returning the stable registry identifier.
func (c *Client) Name() string { return providerID }

// Modes implements provider.Provider, registering AniVault for anime.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 2},
	}
}

// RequiresEpisodeListForMovies implements provider.MovieEpisodeFlow. AniVault
// resolves playback through per-episode watch routes, so anime movies must
// query FetchEpisodes first.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Features implements provider.FeatureSource. Anime media supports separate
// sub/dub audio tracks.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeAnime {
		return provider.Features{}
	}
	return provider.Features{AudioSelection: true}
}

// NewClient constructs the AniVault provider with the shared HTTP client.
func NewClient() (*Client, error) {
	return NewClientWithBase(config.AniVaultAPIBase)
}

// NewClientWithBase constructs the AniVault provider against a custom API
// base URL. Tests point it at one httptest server.
func NewClientWithBase(baseURL string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("anivault: empty API base URL")
	}
	return &Client{
		http: httpclient.New(),
		base: baseURL,
	}, nil
}

// Search queries AniVault's AniList-backed search endpoint. Result IDs stay
// AniList-keyed so search dedup, history, and cross-provider resolution treat
// AniVault titles like every other anime provider's results.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query)
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("anivault search: empty query")
	}
	body, err := c.get(ctx, "/api/search?q="+url.QueryEscape(strings.TrimSpace(query)))
	if err != nil {
		return nil, fmt.Errorf("anivault search: %w", err)
	}
	var payload searchResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("anivault search: decode response: %w", err)
	}
	results := make([]provider.SearchResult, 0, len(payload.Results))
	for _, item := range payload.Results {
		if item.ID <= 0 {
			continue
		}
		title := kit.Clean(item.Title)
		if title == "" {
			continue
		}
		mediaType := provider.MediaTypeAnime
		if strings.EqualFold(kit.Clean(item.Format), "MOVIE") {
			mediaType = provider.MediaTypeMovie
		}
		results = append(results, provider.SearchResult{
			Title:     title,
			ID:        strconv.Itoa(item.ID),
			Type:      provider.ModeAnime,
			MediaType: mediaType,
			CoverURL:  strings.TrimSpace(item.CoverImage),
		})
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	logging.Debug("search done", "provider", c.Name(), "results", len(results))
	return results, nil
}

// FetchEpisodes lists episodes from AniVault's catalog source, falling back
// to AnimeHeaven when the title is absent. Every AniVault episode gets a sub
// and dub handle; dub availability is verified at resolve time. AnimeHeaven
// only carries subs.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	anilistID := strings.TrimSpace(series.ID)
	logging.Debug("fetch episodes", "provider", c.Name(), "mediaID", anilistID)
	if _, err := strconv.Atoi(anilistID); err != nil || anilistID == "" {
		return nil, fmt.Errorf("anivault episodes: invalid AniList id %q: %w", anilistID, provider.ErrNoEpisodes)
	}
	var lastErr error
	for _, source := range []playbackSource{
		{name: sourceAniVault, dub: true},
		{name: sourceAnimeHeaven},
	} {
		entries, err := c.fetchEpisodeList(ctx, anilistID, source.name)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		eps := buildEpisodes(series, anilistID, source, entries)
		if len(eps) > 0 {
			logging.Debug("fetch episodes done", "provider", c.Name(), "count", len(eps), "source", source.name)
			return eps, nil
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("anivault episodes: %w", lastErr)
	}
	return nil, provider.ErrNoEpisodes
}

// ResolveSource resolves one episode handle to direct stream sources. The
// handle names the source that listed the episode; requests without one fall
// back to AniVault. Sub resolutions return both Anikoto's soft-subbed HLS and
// AnimeHeaven's hard-subbed MP4 when available; dub failures map to
// ErrAudioUnavailable so callers can hide the track.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episodeID", episode.ID)
	h := kit.ParseHandle(mediaID, episode.ID, episode.Audio, episode.Episode, "")
	if !h.Valid() {
		return nil, fmt.Errorf("anivault resolve: invalid episode parameters")
	}
	category := strings.ToLower(strings.TrimSpace(h.Category))
	if category != provider.AudioDub {
		category = provider.AudioSub
	}
	origin := episodeSource(episode.ID)
	// AniVault-listed subs can come from either Anikoto's soft-subbed HLS or
	// AnimeHeaven's burned-in MP4s. Resolve both so the hard-sub stream is
	// selectable even when the soft source succeeds.
	if category == provider.AudioSub && origin != sourceAnimeHeaven {
		return c.resolveSubSources(ctx, h.AniListID, h.Number)
	}
	var lastErr error
	for _, source := range candidateSources(origin, category) {
		watch, err := c.fetchWatch(ctx, source, h.AniListID, h.Number, category)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		sources := provider.FilterDirectSources(watchSources(watch))
		if len(sources) > 0 {
			logging.Debug("resolve source done", "provider", c.Name(), "count", len(sources), "source", source)
			return sources, nil
		}
		lastErr = fmt.Errorf("%s: %w", source, provider.ErrNoSources)
	}
	if category == provider.AudioDub &&
		(lastErr == nil || errors.Is(lastErr, provider.ErrNotFound) || errors.Is(lastErr, provider.ErrNoSources)) {
		return nil, fmt.Errorf("anivault resolve: no dub stream: %w", provider.ErrAudioUnavailable)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("anivault resolve: %w", errors.Join(provider.ErrNoSources, lastErr))
	}
	return nil, provider.ErrNoSources
}

// resolveSubSources resolves Anikoto-backed HLS and AnimeHeaven's hard-subbed
// MP4s in parallel. Both are returned when available; a failure in either
// source only removes that source. Running them concurrently keeps the extra
// hard-sub option from adding another round trip to playback start.
func (c *Client) resolveSubSources(ctx context.Context, anilistID string, number int) ([]provider.MediaSource, error) {
	type subResult struct {
		source  string
		sources []provider.MediaSource
		err     error
	}

	resCh := make(chan subResult, 2)
	fetch := func(src string) {
		watch, err := c.fetchWatch(ctx, src, anilistID, number, provider.AudioSub)
		if err != nil {
			resCh <- subResult{source: src, err: err}
			return
		}
		resCh <- subResult{source: src, sources: provider.FilterDirectSources(watchSources(watch))}
	}

	go fetch(sourceAniVault)
	go fetch(sourceAnimeHeaven)

	var results []subResult

	// Wait for first arrival
	select {
	case r := <-resCh:
		results = append(results, r)
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// If the first arrival returned valid sources, give the second source up to 1500ms
	// grace so both variants are captured without stalling indefinitely. Otherwise, wait
	// with the full context deadline.
	if len(results[0].sources) > 0 {
		graceTimer := time.NewTimer(1500 * time.Millisecond)
		defer graceTimer.Stop()
		select {
		case r := <-resCh:
			results = append(results, r)
		case <-graceTimer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		select {
		case r := <-resCh:
			results = append(results, r)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var out []provider.MediaSource
	seen := make(map[string]struct{})
	var lastErr error
	for _, result := range results {
		if len(result.sources) == 0 {
			if result.err != nil {
				lastErr = result.err
			} else {
				lastErr = fmt.Errorf("%s: %w", result.source, provider.ErrNoSources)
			}
			continue
		}
		for _, source := range result.sources {
			key := source.TransportIdentity()
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, source)
		}
	}
	if len(out) > 0 {
		included := make([]string, 0, len(results))
		for _, result := range results {
			if len(result.sources) > 0 {
				included = append(included, result.source)
			}
		}
		logging.Debug("resolve sub sources done", "provider", c.Name(), "count", len(out), "sources", strings.Join(included, ","))
		return out, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("anivault resolve: %w", errors.Join(provider.ErrNoSources, lastErr))
	}
	return nil, provider.ErrNoSources
}

// fetchEpisodeList loads one source's episode roster.
func (c *Client) fetchEpisodeList(ctx context.Context, anilistID, source string) ([]apiEpisode, error) {
	path := fmt.Sprintf("/api/episodes?anilistId=%s&source=%s",
		url.QueryEscape(anilistID), url.QueryEscape(source))
	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var payload episodesResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode episodes: %w", err)
	}
	return payload.Episodes, nil
}

// fetchWatch resolves one episode's stream for a source.
func (c *Client) fetchWatch(ctx context.Context, source, anilistID string, episode int, category string) (watchResponse, error) {
	path := fmt.Sprintf("/api/watch/%s/%s/%d/%s",
		url.PathEscape(source), url.PathEscape(anilistID), episode, category)
	body, err := c.get(ctx, path)
	if err != nil {
		return watchResponse{}, err
	}
	var payload watchResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return watchResponse{}, fmt.Errorf("decode watch response: %w", err)
	}
	return payload, nil
}

// buildEpisodes maps one source's raw roster into provider episodes.
func buildEpisodes(series provider.SearchResult, anilistID string, source playbackSource, entries []apiEpisode) []provider.Episode {
	seasonNum := kit.ParseSeason(series.Title)
	if seasonNum <= 0 {
		seasonNum = 1
	}
	out := make([]provider.Episode, 0, len(entries)*2)
	for _, entry := range entries {
		number := int(entry.Num)
		// Watch routes take an integer episode number; fractional specials
		// cannot be addressed and would silently resolve the neighbouring
		// episode instead.
		if number <= 0 || entry.Num != float64(number) {
			continue
		}
		title := kit.Clean(entry.Title)
		if title == "" {
			title = fmt.Sprintf("Episode %d", number)
		}
		audios := []string{provider.AudioSub}
		if source.dub {
			audios = append(audios, provider.AudioDub)
		}
		for _, audio := range audios {
			out = append(out, provider.Episode{
				Title:   title,
				ID:      fmt.Sprintf("watch/%s/%s/%s/%d", source.name, anilistID, audio, number),
				Episode: number,
				Season:  seasonNum,
				Audio:   audio,
			})
		}
	}
	return out
}

// get fetches one API path and returns the body. 404 and 403 become the
// corresponding provider sentinels; other non-2xx statuses become HTTPError.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", redactPath(path), err)
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return body, nil
	}
	message := apiMessage(body)
	target := redactPath(path)
	switch resp.StatusCode {
	case http.StatusNotFound:
		if message != "" {
			return nil, fmt.Errorf("%s: %s: %w", target, message, provider.ErrNotFound)
		}
		return nil, fmt.Errorf("%s: %w", target, provider.ErrNotFound)
	case http.StatusForbidden:
		return nil, fmt.Errorf("%s: %w", target, provider.ErrBlocked)
	default:
		httpErr := &provider.HTTPError{Code: resp.StatusCode, URL: target}
		if message != "" {
			return nil, fmt.Errorf("%s: %s: %w", target, message, httpErr)
		}
		return nil, httpErr
	}
}

// apiMessage extracts the short upstream error string from an API error body.
func apiMessage(body []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	message := kit.Clean(payload.Error)
	if len(message) > 200 {
		message = message[:200]
	}
	return message
}

// redactPath strips query strings from API paths for errors and logs.
func redactPath(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}

// Search, episode, watch, and subtitle shapes returned by the AniVault API.
type (
	searchResponse struct {
		Results []apiSearchResult `json:"results"`
	}
	apiSearchResult struct {
		ID         int    `json:"id"`
		MALID      int    `json:"malId"`
		Title      string `json:"title"`
		CoverImage string `json:"coverImage"`
		Format     string `json:"format"`
	}
	episodesResponse struct {
		Episodes []apiEpisode `json:"episodes"`
	}
	apiEpisode struct {
		Num   float64 `json:"num"`
		Title string  `json:"title"`
	}
	watchResponse struct {
		Server       string        `json:"server"`
		Source       string        `json:"source"`
		EmbedURL     string        `json:"embedUrl"`
		M3U8         string        `json:"m3u8"`
		HLSProxyURL  string        `json:"hlsProxyUrl"`
		MP4          string        `json:"mp4"`
		MP4ProxyURL  string        `json:"mp4ProxyUrl"`
		StreamURL    string        `json:"streamUrl"`
		RawStreamURL string        `json:"rawStreamUrl"`
		IframeOnly   bool          `json:"iframeOnly"`
		Subtitles    []apiSubtitle `json:"subtitles"`
	}
	apiSubtitle struct {
		URL     string `json:"url"`
		Lang    string `json:"lang"`
		Default bool   `json:"default"`
	}
)

var (
	_ provider.Provider         = (*Client)(nil)
	_ provider.Presenter        = (*Client)(nil)
	_ provider.FeatureSource    = (*Client)(nil)
	_ provider.MovieEpisodeFlow = (*Client)(nil)
)
