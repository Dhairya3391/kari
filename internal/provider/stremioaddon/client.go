// Package stremioaddon implements user-configured Stremio-compatible stream addons.
package stremioaddon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"

	"golang.org/x/sync/errgroup"
)

var (
	_ provider.Provider           = (*Client)(nil)
	_ provider.EpisodeSource      = (*Client)(nil)
	_ provider.FeatureSource      = (*Client)(nil)
	_ provider.ConfiguredProvider = (*Client)(nil)
)

type manifest struct {
	Name      string            `json:"name"`
	Types     []string          `json:"types"`
	Resources []json.RawMessage `json:"resources"`
	Catalogs  []addonCatalog    `json:"catalogs"`
	Behavior  struct {
		ConfigurationRequired bool `json:"configurationRequired"`
	} `json:"behaviorHints"`
}

type addonCatalog struct {
	ID             string         `json:"id"`
	Type           string         `json:"type"`
	Name           string         `json:"name"`
	Extra          []catalogExtra `json:"extra"`
	ExtraSupported []string       `json:"extraSupported"`
}

type catalogExtra struct {
	Name string `json:"name"`
}

type catalogResponse struct {
	Metas []addonMeta `json:"metas"`
}

type addonMeta struct {
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	Name        string       `json:"name"`
	ReleaseInfo string       `json:"releaseInfo"`
	Poster      string       `json:"poster"`
	Description string       `json:"description"`
	Genres      []string     `json:"genres"`
	Videos      []addonVideo `json:"videos"`
}

type addonMetaResponse struct {
	Meta addonMeta `json:"meta"`
}

type addonVideo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
}

type streamResponse struct {
	Streams []streamItem `json:"streams"`
}

type streamItem struct {
	Name     string         `json:"name"`
	Title    string         `json:"title"`
	URL      string         `json:"url"`
	Behavior streamBehavior `json:"behaviorHints"`
}

type streamBehavior struct {
	Headers      map[string]string `json:"headers"`
	ProxyHeaders proxyHeaders      `json:"proxyHeaders"`
}

type proxyHeaders struct {
	Request map[string]string `json:"request"`
}

// Client accesses a configured Stremio stream addon using its manifest URL.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	manifest   manifest
	keyPool    *tmdb.KeyPool
	endpoint   string
	query      string
	name       string
	digest     [32]byte
	modes      []provider.Mode
}

// NewClient fetches and validates an addon manifest before exposing its stream capabilities.
func NewClient(ctx context.Context, manifestURL string, keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithHTTP(ctx, manifestURL, keyPool, httpclient.New())
}

// NewClientWithHTTP constructs a client with an injected HTTP client for offline testing.
func NewClientWithHTTP(ctx context.Context, manifestURL string, keyPool *tmdb.KeyPool, hc *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(manifestURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid addon manifest URL")
	}
	if hc == nil {
		return nil, fmt.Errorf("http client is required")
	}
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("create addon catalog base: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create manifest request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("fetch addon manifest: %w", ctxErr)
		}
		return nil, fmt.Errorf("fetch addon manifest: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch addon manifest: %w", &provider.HTTPError{Code: resp.StatusCode, URL: u.Scheme + "://" + u.Host})
	}
	var info manifest
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode addon manifest: %w", err)
	}
	if info.Name == "" {
		return nil, fmt.Errorf("addon manifest has no name")
	}
	if info.Behavior.ConfigurationRequired {
		return nil, fmt.Errorf("addon requires configuration; paste its configured manifest URL")
	}
	if !supportsStreams(info) {
		return nil, fmt.Errorf("addon manifest does not declare stream support")
	}
	modes := manifestModes(info)
	if len(modes) == 0 {
		return nil, fmt.Errorf("addon manifest declares no supported catalog or stream types")
	}
	endpoint := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(strings.ToLower(u.Path), "/manifest.json") {
		endpoint = strings.TrimSuffix(endpoint, "/manifest.json")
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(manifestURL)))
	return &Client{base: base, httpClient: hc, manifest: info, keyPool: keyPool, endpoint: strings.TrimSuffix(u.Scheme+"://"+u.Host+endpoint, "/"), query: u.RawQuery, name: "addon-" + hex.EncodeToString(sum[:6]), digest: sum, modes: modes}, nil
}

// Probe verifies that an addon can resolve a catalog item to a playable stream
// before Kari stores its manifest URL.
func (c *Client) Probe(ctx context.Context) error {
	if len(c.manifest.Catalogs) > 0 {
		var firstErr error
		for _, catalog := range c.manifest.Catalogs {
			if !supportedCatalogType(catalog.Type) || catalog.ID == "" {
				continue
			}
			metas, err := c.fetchCatalog(ctx, catalog, "")
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			for _, meta := range metas[:min(len(metas), maxAddonProbeItems)] {
				if strings.TrimSpace(meta.ID) == "" {
					continue
				}
				itemIDs := []string{meta.ID}
				if catalog.Type == "series" {
					episodes, err := c.fetchCatalogEpisodes(ctx, meta.ID)
					if err != nil {
						if firstErr == nil {
							firstErr = err
						}
						continue
					}
					itemIDs = make([]string, 0, min(len(episodes), maxAddonProbeItems))
					for _, episode := range episodes[:min(len(episodes), maxAddonProbeItems)] {
						itemIDs = append(itemIDs, episode.ID)
					}
				}
				for _, itemID := range itemIDs {
					if _, err := c.resolveAddonStreams(ctx, catalog.Type, itemID); err == nil {
						return nil
					} else if firstErr == nil {
						firstErr = err
					}
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if firstErr != nil {
			return fmt.Errorf("addon catalog stream check failed: %w", firstErr)
		}
		return fmt.Errorf("addon catalogs returned no playable streams for their first items")
	}
	// Stream-only addons have no catalog item to test, so use stable movie or
	// series identifiers and require at least one direct URL Kari can play.
	checks := []struct {
		id      string
		season  int
		episode int
	}{
		{id: "tt0133093"},
		{id: "tt0903747", season: 1, episode: 1},
	}
	for _, check := range checks {
		if check.season == 0 && !supportsMode(c.modes, provider.ModeMovies) {
			continue
		}
		if check.season > 0 && !supportsMode(c.modes, provider.ModeTV) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := c.ResolveSource(ctx, check.id, provider.Episode{Season: check.season, Episode: check.episode})
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("addon returned no playable streams for Kari's movie or series checks")
}

const maxAddonProbeItems = 3

func supportsMode(modes []provider.Mode, target provider.ContentType) bool {
	for _, mode := range modes {
		if mode.Name == target {
			return true
		}
	}
	return false
}

// Name returns a stable identity derived from the configured manifest URL.
func (c *Client) Name() string { return c.name }

// MatchesConfiguration reports whether value is this addon's manifest URL.
func (c *Client) MatchesConfiguration(value string) bool {
	return sha256.Sum256([]byte(strings.TrimSpace(value))) == c.digest
}

// Modes returns catalog-browsing and movie or series modes declared by the addon.
func (c *Client) Modes() []provider.Mode { return append([]provider.Mode(nil), c.modes...) }

// Alias returns the display name declared by the addon manifest.
func (c *Client) Alias() string { return c.manifest.Name }

// Features declares empty-query catalog browsing for addons with a Stremio mode.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode == provider.ModeStremio {
		return provider.Features{AllowEmptyQuery: true, SearchPlaceholder: "Search Stremio addon catalogs…"}
	}
	return provider.Features{}
}

// Search uses addon catalogs for Stremio mode and Kari's TMDB catalog otherwise.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode == provider.ModeStremio {
		return c.searchCatalogs(ctx, query)
	}
	return c.base.Search(ctx, query, mode)
}

func (c *Client) searchCatalogs(ctx context.Context, query string) ([]provider.SearchResult, error) {
	query = strings.TrimSpace(query)
	resultsByCatalog := make([][]provider.SearchResult, len(c.manifest.Catalogs))
	errorsByCatalog := make([]error, len(c.manifest.Catalogs))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for index, catalog := range c.manifest.Catalogs {
		if !supportedCatalogType(catalog.Type) || strings.TrimSpace(catalog.ID) == "" {
			continue
		}
		group.Go(func() error {
			metas, fetchErr := c.fetchCatalog(groupCtx, catalog, query)
			if fetchErr != nil {
				errorsByCatalog[index] = fetchErr
			} else {
				for _, meta := range metas {
					if meta.ID == "" || meta.Name == "" || (query != "" && !catalogSupportsSearch(catalog) && !metaMatchesQuery(meta, query)) {
						continue
					}
					mediaType := provider.MediaTypeMovie
					if catalog.Type == "series" {
						mediaType = provider.MediaTypeTV
					}
					resultsByCatalog[index] = append(resultsByCatalog[index], provider.SearchResult{
						Title:     meta.Name,
						ID:        c.Name() + "/" + catalog.Type + "/" + meta.ID,
						Type:      provider.ModeStremio,
						Year:      meta.ReleaseInfo,
						MediaType: mediaType,
						CoverURL:  meta.Poster,
						Overview:  meta.Description,
						Genres:    append([]string(nil), meta.Genres...),
					})
				}
			}
			return nil
		})
	}
	_ = group.Wait()
	var results []provider.SearchResult
	var firstErr error
	for index, rows := range resultsByCatalog {
		results = append(results, rows...)
		if firstErr == nil {
			firstErr = errorsByCatalog[index]
		}
	}
	if len(results) > 0 {
		return results, nil
	}
	if firstErr != nil {
		return nil, fmt.Errorf("search addon catalogs: %w", firstErr)
	}
	return nil, provider.ErrNoResults
}

func (c *Client) fetchCatalog(ctx context.Context, catalog addonCatalog, query string) ([]addonMeta, error) {
	endpoint, err := c.catalogURL("catalog", catalog.Type, catalog.ID)
	if err != nil {
		return nil, fmt.Errorf("build addon catalog URL: %w", err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse addon catalog URL: %w", err)
	}
	if catalogSupportsSearch(catalog) && query != "" {
		// Stremio encodes catalog extras in the path; the query string is
		// reserved for addon configuration such as API tokens.
		parsed.Path = strings.TrimSuffix(parsed.Path, ".json") + "/" + url.PathEscape("search="+query) + ".json"
	}
	var response catalogResponse
	if err := c.getJSON(ctx, parsed.String(), &response); err != nil {
		return nil, err
	}
	return response.Metas, nil
}

func (c *Client) fetchCatalogEpisodes(ctx context.Context, itemID string) ([]provider.Episode, error) {
	endpoint, err := c.catalogURL("meta", "series", itemID)
	if err != nil {
		return nil, fmt.Errorf("build addon metadata URL: %w", err)
	}
	var response addonMetaResponse
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
		return nil, fmt.Errorf("fetch addon metadata: %w", err)
	}
	episodes := make([]provider.Episode, 0, len(response.Meta.Videos))
	for _, video := range response.Meta.Videos {
		if strings.TrimSpace(video.ID) == "" {
			continue
		}
		episodes = append(episodes, provider.Episode{
			ID:      video.ID,
			Title:   video.Title,
			Season:  video.Season,
			Episode: video.Episode,
		})
	}
	if len(episodes) == 0 {
		return nil, provider.ErrNoEpisodes
	}
	return episodes, nil
}

func (c *Client) catalogURL(resource, mediaType, id string) (string, error) {
	endpoint, err := url.Parse(c.endpoint + "/" + resource + "/" + mediaType + "/" + url.PathEscape(id) + ".json")
	if err != nil {
		return "", err
	}
	endpoint.RawQuery = c.query
	return endpoint.String(), nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create addon request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("request addon resource: %w", ctxErr)
		}
		return fmt.Errorf("request addon resource failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("request addon resource: %w", &provider.HTTPError{Code: resp.StatusCode, URL: c.endpoint})
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode addon resource: %w", err)
	}
	return nil
}

func (c *Client) parseStremioHandle(raw string) (kind, itemID string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(raw), "/", 3)
	if len(parts) != 3 || parts[0] != c.Name() || !supportedCatalogType(parts[1]) || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func supportedCatalogType(mediaType string) bool {
	return mediaType == "movie" || mediaType == "series"
}

func catalogSupportsSearch(catalog addonCatalog) bool {
	for _, extra := range catalog.Extra {
		if extra.Name == "search" {
			return true
		}
	}
	for _, extra := range catalog.ExtraSupported {
		if extra == "search" {
			return true
		}
	}
	return false
}

func metaMatchesQuery(meta addonMeta, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	return strings.Contains(strings.ToLower(meta.Name), query) || strings.Contains(strings.ToLower(meta.Description), query)
}

// FetchEpisodes returns addon metadata episodes or delegates to Kari's TMDB catalog.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if kind, itemID, ok := c.parseStremioHandle(series.ID); ok {
		if kind == "movie" {
			return []provider.Episode{{ID: itemID, Title: series.Title}}, nil
		}
		return c.fetchCatalogEpisodes(ctx, itemID)
	}
	return c.base.FetchEpisodes(ctx, series)
}

// ResolveSource requests addon streams using a catalog item or TMDB ID.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	if kind, itemID, ok := c.parseStremioHandle(mediaID); ok {
		streamID := strings.TrimSpace(episode.ID)
		if streamID == "" || streamID == mediaID {
			streamID = itemID
		}
		return c.resolveAddonStreams(ctx, kind, streamID)
	}
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		tmdbID, _ = strconv.Atoi(strings.TrimSpace(mediaID))
	}
	id := strconv.Itoa(tmdbID)
	if episode.TMDBID <= 0 {
		id = strings.TrimSpace(mediaID)
	}
	kind := "movie"
	season, episodeNumber := episode.Season, episode.Episode
	if episode.Season > 0 || episode.Episode > 0 {
		kind = "series"
		season, episodeNumber = max(1, season), max(1, episodeNumber)
	}
	ids := []string{id}
	if tmdbID > 0 {
		if imdbID := c.lookupIMDBID(ctx, tmdbID, kind == "series"); imdbID != "" {
			ids = append([]string{imdbID}, ids...)
		}
		ids = append(ids, "tmdb:"+strconv.Itoa(tmdbID))
	}
	var items []streamItem
	for _, targetID := range ids {
		if kind == "series" {
			targetID += ":" + strconv.Itoa(season) + ":" + strconv.Itoa(episodeNumber)
		}
		endpoint := c.endpoint + "/stream/" + kind + "/" + url.PathEscape(targetID) + ".json"
		if c.query != "" {
			endpoint += "?" + c.query
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("create addon stream request: %w", err)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("fetch addon streams: %w", ctxErr)
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}
		var payload streamResponse
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err == nil && len(payload.Streams) > 0 {
			items = payload.Streams
			break
		}
	}
	sources := mediaSources(items)
	if len(sources) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("resolve addon streams: %w", err)
		}
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

func (c *Client) resolveAddonStreams(ctx context.Context, kind, itemID string) ([]provider.MediaSource, error) {
	endpoint, err := c.catalogURL("stream", kind, itemID)
	if err != nil {
		return nil, fmt.Errorf("build addon stream URL: %w", err)
	}
	var response streamResponse
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
		return nil, fmt.Errorf("fetch addon streams: %w", err)
	}
	sources := mediaSources(response.Streams)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

func mediaSources(items []streamItem) []provider.MediaSource {
	var sources []provider.MediaSource
	for _, item := range items {
		streamURL := strings.TrimSpace(item.URL)
		if streamURL == "" || (!provider.IsDirectURL(streamURL) && !isRemoteAddonProxyURL(streamURL)) {
			continue
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = strings.TrimSpace(item.Name)
		}
		if title == "" {
			title = "Auto"
		}
		source := provider.MediaSource{URL: streamURL, Quality: title, Type: sourceType(streamURL)}
		headers := make(map[string]string, len(item.Behavior.Headers)+len(item.Behavior.ProxyHeaders.Request))
		for key, value := range item.Behavior.Headers {
			headers[key] = value
		}
		for key, value := range item.Behavior.ProxyHeaders.Request {
			headers[key] = value
		}
		for key, value := range headers {
			switch strings.ToLower(key) {
			case "referer":
				source.Referer = value
			case "user-agent":
				source.UserAgent = value
			case "cookie":
				source.CookieHeader = value
			}
		}
		sources = append(sources, source)
	}
	return sources
}

func isRemoteAddonProxyURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.Contains(strings.ToLower(parsed.Path), "/proxy/") {
		return false
	}
	// Hosted addons may proxy a source at their own public endpoint. Kari can
	// fetch that URL directly; only local/helper proxies are unsafe to expose.
	parsed.Path = "/"
	parsed.RawPath = ""
	return provider.IsDirectURL(parsed.String())
}

func (c *Client) lookupIMDBID(ctx context.Context, tmdbID int, isSeries bool) string {
	key, err := c.keyPool.NextKey()
	if err != nil || key == "" {
		return ""
	}
	kind := provider.MediaTypeMovie
	if isSeries {
		kind = provider.MediaTypeTV
	}
	endpoint := fmt.Sprintf("%s/%s/%d?api_key=%s&append_to_response=external_ids", config.TMDBAPIBase, kind, tmdbID, url.QueryEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		c.keyPool.MarkFailed(key)
	}
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var result struct {
		IMDBID      string `json:"imdb_id"`
		ExternalIDs struct {
			IMDBID string `json:"imdb_id"`
		} `json:"external_ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}
	if result.IMDBID != "" {
		return result.IMDBID
	}
	return result.ExternalIDs.IMDBID
}

func supportsStreams(info manifest) bool {
	for _, raw := range info.Resources {
		var name string
		if json.Unmarshal(raw, &name) == nil && name == "stream" {
			return true
		}
		var resource struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &resource) == nil && resource.Name == "stream" {
			return true
		}
	}
	return false
}

func manifestModes(info manifest) []provider.Mode {
	hasMovie, hasSeries := false, false
	for _, raw := range info.Types {
		hasMovie = hasMovie || raw == "movie"
		hasSeries = hasSeries || raw == "series"
	}
	for _, catalog := range info.Catalogs {
		hasMovie = hasMovie || catalog.Type == "movie"
		hasSeries = hasSeries || catalog.Type == "series"
	}
	var modes []provider.Mode
	if hasMovie {
		modes = append(modes, provider.Mode{Name: provider.ModeMovies, Priority: 40})
	}
	if hasSeries {
		modes = append(modes, provider.Mode{Name: provider.ModeTV, Priority: 40})
	}
	if len(info.Catalogs) > 0 && supportsCatalogs(info) {
		modes = append(modes, provider.Mode{Name: provider.ModeStremio, Priority: 10})
	}
	return modes
}

func supportsCatalogs(info manifest) bool {
	for _, raw := range info.Resources {
		var name string
		if json.Unmarshal(raw, &name) == nil && name == "catalog" {
			return true
		}
		var resource struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &resource) == nil && resource.Name == "catalog" {
			return true
		}
	}
	return false
}

func sourceType(raw string) string {
	switch strings.ToLower(path.Ext(strings.SplitN(raw, "?", 2)[0])) {
	case ".m3u8":
		return provider.SourceTypeHLS
	case ".mp4":
		return provider.SourceTypeMP4
	default:
		return ""
	}
}
