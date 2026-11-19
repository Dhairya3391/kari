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
	"sort"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"

	"golang.org/x/sync/errgroup"
)

var (
	_ provider.Provider           = (*Client)(nil)
	_ provider.EpisodeSource      = (*Client)(nil)
	_ provider.FeatureSource      = (*Client)(nil)
	_ provider.ConfiguredProvider = (*Client)(nil)
	_ provider.AddonInfoSource    = (*Client)(nil)
	_ provider.StreamingProvider  = (*Client)(nil)
)

type manifest struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Types     []string        `json:"types"`
	Resources []resourceEntry `json:"resources"`
	Catalogs  []addonCatalog  `json:"catalogs"`
	Behavior  struct {
		ConfigurationRequired bool `json:"configurationRequired"`
	} `json:"behaviorHints"`
}

// resourceEntry is one declared addon resource. Manifests encode resources
// either as a bare name or as an object naming the types it applies to.
type resourceEntry struct {
	Name       string   `json:"name"`
	Types      []string `json:"types"`
	IDPrefixes []string `json:"idPrefixes"`
}

// UnmarshalJSON accepts both the string and object forms of a Stremio resource.
func (r *resourceEntry) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		r.Name = name
		return nil
	}
	type plainResource resourceEntry
	var entry plainResource
	if err := json.Unmarshal(data, &entry); err != nil {
		return err
	}
	*r = resourceEntry(entry)
	return nil
}

type addonCatalog struct {
	ID             string         `json:"id"`
	Type           string         `json:"type"`
	Name           string         `json:"name"`
	Extra          []catalogExtra `json:"extra"`
	ExtraSupported []string       `json:"extraSupported"`
}

type catalogExtra struct {
	Name       string `json:"name"`
	IsRequired bool   `json:"isRequired"`
}

type catalogResponse struct {
	Metas []addonMeta `json:"metas"`
}

type addonMeta struct {
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	Name        string       `json:"name"`
	ReleaseInfo string       `json:"releaseInfo"`
	Year        string       `json:"year"`
	Poster      string       `json:"poster"`
	Description string       `json:"description"`
	Genres      []string     `json:"genres"`
	Runtime     string       `json:"runtime"`
	Background  string       `json:"background"`
	IMDBRating  string       `json:"imdbRating"`
	FirstAired  string       `json:"firstAired"`
	Trailer     string       `json:"trailer"`
	Released    string       `json:"released"`
	Videos      []addonVideo `json:"videos"`
}

type addonMetaResponse struct {
	Meta addonMeta `json:"meta"`
}

type addonVideo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	Released  string `json:"released"`
	Overview  string `json:"overview"`
	Thumbnail string `json:"thumbnail"`
}

type streamResponse struct {
	Streams []streamItem `json:"streams"`
}

type streamItem struct {
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	URL         string           `json:"url"`
	ExternalURL string           `json:"externalUrl"`
	YTID        string           `json:"ytId"`
	InfoHash    string           `json:"infoHash"`
	Subtitles   []streamSubtitle `json:"subtitles"`
	Description string           `json:"description"`
	Thumbnail   string           `json:"thumbnail"`
	Behavior    streamBehavior   `json:"behaviorHints"`
	ClearKey    string           `json:"clearkey"`
}

type streamSubtitle struct {
	URL   string `json:"url"`
	Lang  string `json:"lang"`
	Label string `json:"label"`
}

type streamBehavior struct {
	Headers      map[string]string `json:"headers"`
	ProxyHeaders proxyHeaders      `json:"proxyHeaders"`
	NotWebReady  bool              `json:"notWebReady"`
	Filename     string            `json:"filename"`
	VideoHash    string            `json:"videoHash"`
	VideoSize    int64             `json:"videoSize"`
	BingeGroup   string            `json:"bingeGroup"`
}

type proxyHeaders struct {
	Request map[string]string `json:"request"`
}

// Client accesses a configured Stremio stream addon using its manifest URL.
type Client struct {
	base         *streambase.Base
	httpClient   *http.Client
	manifest     manifest
	keyPool      *tmdb.KeyPool
	endpoint     string
	query        string
	name         string
	displayName  string
	digest       [32]byte
	modes        []provider.Mode
	hasSubtitles bool
	info         provider.AddonInfo
}

// NewClient fetches and validates an addon manifest before exposing its stream capabilities.
func NewClient(ctx context.Context, manifestURL string, keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithHTTP(ctx, manifestURL, keyPool, httpclient.New())
}

// NewClientWithHTTP constructs a client with an injected HTTP client for testing.
func NewClientWithHTTP(ctx context.Context, manifestURL string, keyPool *tmdb.KeyPool, hc *http.Client) (*Client, error) {
	normalized, err := normalizeManifestURL(manifestURL)
	if err != nil {
		return nil, err
	}
	if hc == nil {
		return nil, fmt.Errorf("http client is required")
	}
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("create addon catalog base: %w", err)
	}
	info, err := fetchManifest(ctx, hc, normalized)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(normalized)
	// Identity intentionally uses the original string: persisted disabled
	// provider keys and configured aliases are derived from it.
	sum := sha256.Sum256([]byte(strings.TrimSpace(manifestURL)))
	identity := "addon-" + hex.EncodeToString(sum[:6])
	if info.Behavior.ConfigurationRequired {
		logging.Debug("stremio addon declares configurationRequired", "host", u.Host)
	}
	displayName := strings.TrimSpace(info.Name)
	if displayName == "" {
		displayName = strings.TrimSpace(info.ID)
	}
	if displayName == "" {
		displayName = u.Host
	}
	if displayName == "" {
		displayName = identity
	}
	modes := manifestModes(info)
	endpoint := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(strings.ToLower(endpoint), "/"+manifestSuffix) {
		endpoint = endpoint[:len(endpoint)-len(manifestSuffix)-1]
	}
	return &Client{
		base: base, httpClient: hc, manifest: info, keyPool: keyPool,
		endpoint: strings.TrimSuffix(u.Scheme+"://"+u.Host+endpoint, "/"),
		query:    u.RawQuery, name: identity, displayName: displayName,
		digest: sum, modes: modes, hasSubtitles: supportsResource(info, "subtitles"),
		info: addonInfo(info, modes),
	}, nil
}

// Probe checks whether an addon can resolve a catalog item to a playable
// stream. Adding an addon no longer requires a successful probe.
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
					episodes, err := c.fetchCatalogEpisodesForType(ctx, catalog.Type, meta.ID)
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

// Alias returns the addon's display name, falling back to its manifest ID or host.
func (c *Client) Alias() string { return c.displayName }

// AddonInfo reports manifest capabilities for the settings screen.
func (c *Client) AddonInfo() provider.AddonInfo { return c.info }

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
					// Some addons advertise search but ignore the query and
					// return their whole catalog. Always filter locally so a
					// non-filtering upstream cannot bury the real matches.
					if meta.ID == "" || meta.Name == "" || (query != "" && !metaMatchesQuery(meta, query)) {
						continue
					}
					// Addons often group items under a custom catalog type
					// ("HdHub") while each meta declares the real Stremio
					// type. Streams are keyed by the meta type, not the
					// catalog type, so carry that in the resolution handle.
					kind := strings.TrimSpace(meta.Type)
					if kind == "" {
						kind = catalog.Type
					}
					resultsByCatalog[index] = append(resultsByCatalog[index], provider.SearchResult{
						Title:     meta.Name,
						ID:        c.Name() + "/" + kind + "/" + meta.ID,
						Type:      provider.ModeStremio,
						Year:      metaYear(meta),
						MediaType: catalogMediaType(kind),
						CoverURL:  meta.Poster,
						Overview:  meta.Description,
						Genres:    append([]string(nil), meta.Genres...),
						TMDBID:    metaTMDBID(meta.ID),
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

// metaYear reads the release year from either Stremio spelling. Bridge addons
// publish "year" while catalog addons tend to use "releaseInfo".
func metaYear(meta addonMeta) string {
	if year := strings.TrimSpace(meta.ReleaseInfo); year != "" {
		return year
	}
	return strings.TrimSpace(meta.Year)
}

// metaTMDBID parses a TMDB id from the "tmdb:<n>" form Stremio metas use.
func metaTMDBID(id string) int {
	const prefix = "tmdb:"
	raw := strings.TrimSpace(id)
	if len(raw) < len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw[len(prefix):]))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func (c *Client) fetchCatalog(ctx context.Context, catalog addonCatalog, query string) ([]addonMeta, error) {
	extras := make(map[string]string)
	if query != "" && catalogSupportsExtra(catalog, "search") {
		extras["search"] = query
	}
	// Required pagination extras cannot be inferred from a text query;
	// supplying their first page keeps those catalogs browsable. "skip" is a
	// 0-based offset, while "page" is a 1-based page number.
	for _, pagination := range []struct{ name, value string }{
		{name: "skip", value: "0"},
		{name: "page", value: "1"},
	} {
		if requiredCatalogExtra(catalog, pagination.name) {
			extras[pagination.name] = pagination.value
		}
	}
	endpoint, err := c.catalogURLWithExtras("catalog", catalog.Type, catalog.ID, extras)
	if err != nil {
		return nil, fmt.Errorf("build addon catalog URL: %w", err)
	}
	var response catalogResponse
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
		return nil, err
	}
	return response.Metas, nil
}

func (c *Client) fetchCatalogEpisodesForType(ctx context.Context, kind, itemID string) ([]provider.Episode, error) {
	endpoint, err := c.catalogURL("meta", kind, itemID)
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
	return c.catalogURLWithExtras(resource, mediaType, id, nil)
}

// catalogURLWithExtras builds a Stremio resource URL with optional extras
// encoded as a single path segment in the canonical key=value&key=value form.
// The path is assembled as text: passing an already-escaped segment through
// url.URL.String() escapes the percent signs a second time.
func (c *Client) catalogURLWithExtras(resource, mediaType, id string, extras map[string]string) (string, error) {
	endpoint := c.endpoint + "/" + url.PathEscape(resource) + "/" +
		url.PathEscape(mediaType) + "/" + url.PathEscape(id) + ".json"
	if len(extras) > 0 {
		keys := make([]string, 0, len(extras))
		for key := range extras {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, escapeStremioValue(key)+"="+escapeStremioValue(extras[key]))
		}
		endpoint = strings.TrimSuffix(endpoint, ".json") + "/" + strings.Join(parts, "&") + ".json"
	}
	if c.query != "" {
		endpoint += "?" + c.query
	}
	return endpoint, nil
}

// escapeStremioValue percent-encodes a key or value in an extras segment.
// PathEscape leaves & and = intact, which would break the key=value&key=value
// framing, so those two characters are encoded explicitly.
func escapeStremioValue(value string) string {
	return strings.NewReplacer("&", "%26", "=", "%3D").Replace(url.PathEscape(value))
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
		return provider.WrapHint("stremioaddon", "request resource", provider.KindUpstream, "addon request failed", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return fmt.Errorf("request addon resource: %w", &provider.HTTPError{Code: resp.StatusCode, URL: c.endpoint})
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return fmt.Errorf("read addon resource: %w", err)
	}
	if err := json.Unmarshal(body, out); err != nil {
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

// supportedCatalogType reports whether Kari can browse and resolve a Stremio
// catalog type. Stremio types are free-form, so any non-empty type is allowed.
func supportedCatalogType(mediaType string) bool {
	return strings.TrimSpace(mediaType) != ""
}

// catalogMediaType maps a Stremio catalog type onto Kari's media-type
// vocabulary. Live rows resolve straight from the search result instead of
// opening an episode list, which is the shape live catalogs describe.
func catalogMediaType(catalogType string) string {
	switch catalogType {
	case "series":
		return provider.MediaTypeTV
	case "tv":
		return provider.MediaTypeLive
	case "anime":
		return provider.MediaTypeAnime
	default:
		return provider.MediaTypeMovie
	}
}

// catalogSupportsExtra reports whether a catalog declares an extra, including
// the legacy extraSupported form some addons still publish.
func catalogSupportsExtra(catalog addonCatalog, name string) bool {
	for _, extra := range catalog.Extra {
		if strings.EqualFold(extra.Name, name) {
			return true
		}
	}
	for _, extra := range catalog.ExtraSupported {
		if strings.EqualFold(extra, name) {
			return true
		}
	}
	return false
}

// requiredCatalogExtra reports whether an extra must be supplied for the
// catalog to accept a request.
func requiredCatalogExtra(catalog addonCatalog, name string) bool {
	for _, extra := range catalog.Extra {
		if strings.EqualFold(extra.Name, name) {
			return extra.IsRequired
		}
	}
	return false
}

func metaMatchesQuery(meta addonMeta, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	haystack := strings.ToLower(meta.Name + " " + meta.Description + " " + meta.ID)
	if strings.Contains(haystack, query) {
		return true
	}
	// Token matching keeps results whose title words appear in a different
	// order than the query while still dropping unrelated catalog rows.
	for _, word := range strings.Fields(query) {
		if !strings.Contains(haystack, word) {
			return false
		}
	}
	return true
}

// FetchEpisodes returns addon metadata episodes or delegates to Kari's TMDB catalog.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if kind, itemID, ok := c.parseStremioHandle(series.ID); ok {
		// Movies and live channels resolve straight from the search result;
		// every other Stremio type may carry an episodic meta resource.
		if kind == "movie" || kind == "tv" {
			return []provider.Episode{{ID: itemID, Title: series.Title}}, nil
		}
		if supportsResourceFor(c.manifest, "meta", itemID) {
			if episodes, err := c.fetchCatalogEpisodesForType(ctx, kind, itemID); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				logging.Debug("stremio meta episodes unavailable; using single item",
					"kind", kind, "id", itemID, "err", err)
			} else if len(episodes) > 0 {
				return episodes, nil
			}
		}
		return []provider.Episode{{ID: itemID, Title: series.Title}}, nil
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
	resolvedID := id
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
			resolvedID = targetID
			break
		}
	}
	sources := mediaSources(items)
	c.mergeSubtitles(ctx, kind, resolvedID, items, sources)
	if len(sources) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("resolve addon streams: %w", err)
		}
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

// ResolveStream implements provider.StreamingProvider. The addon exposes a
// single stream endpoint per item, so the resolved batch is published as soon
// as it is available rather than after further enrichment.
func (c *Client) ResolveStream(
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

func (c *Client) resolveAddonStreams(ctx context.Context, kind, itemID string) ([]provider.MediaSource, error) {
	if !supportsResourceFor(c.manifest, "stream", itemID) {
		return nil, provider.ErrNoSources
	}
	endpoint, err := c.catalogURL("stream", kind, itemID)
	if err != nil {
		return nil, fmt.Errorf("build addon stream URL: %w", err)
	}
	var response streamResponse
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
		return nil, fmt.Errorf("fetch addon streams: %w", err)
	}
	sources := mediaSources(response.Streams)
	c.mergeSubtitles(ctx, kind, itemID, response.Streams, sources)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

// subtitleKey identifies the addon subtitle lookup tied to one playback URL.
type subtitleKey struct {
	filename string
	hash     string
}

// mergeSubtitles augments each source with tracks for its own filename and
// video hash. A failed subtitle lookup is diagnostic only and must never fail
// resolution.
func (c *Client) mergeSubtitles(ctx context.Context, kind, itemID string, items []streamItem, sources []provider.MediaSource) {
	if !c.hasSubtitles || len(sources) == 0 {
		return
	}
	type lookup struct {
		indices  []int
		filename string
		hash     string
		tracks   []provider.SubtitleOption
	}
	lookups := make(map[subtitleKey]*lookup)
	for index := range sources {
		hash := ""
		for _, item := range items {
			if strings.TrimSpace(item.URL) == sources[index].URL {
				hash = item.Behavior.VideoHash
				break
			}
		}
		key := subtitleKey{filename: subtitleFilename(sources[index].URL), hash: hash}
		entry := lookups[key]
		if entry == nil {
			entry = &lookup{filename: key.filename, hash: key.hash}
			lookups[key] = entry
		}
		entry.indices = append(entry.indices, index)
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for _, entry := range lookups {
		group.Go(func() error {
			entry.tracks = c.fetchStreamSubtitles(groupCtx, kind, itemID, entry.filename, entry.hash)
			return nil
		})
	}
	_ = group.Wait()
	for _, entry := range lookups {
		for _, index := range entry.indices {
			sources[index].Subtitles = appendUniqueSubtitles(sources[index].Subtitles, entry.tracks)
		}
	}
}

// subtitleFilename extracts the filename Stremio addons match subtitles against.
func subtitleFilename(videoURL string) string {
	raw := strings.TrimSpace(videoURL)
	if raw == "" {
		return ""
	}
	return path.Base(strings.SplitN(raw, "?", 2)[0])
}

// fetchStreamSubtitles asks the addon for tracks matching one playback filename.
// It returns an empty slice on any failure because subtitles are optional.
func (c *Client) fetchStreamSubtitles(ctx context.Context, kind, itemID, filename, videoHash string) []provider.SubtitleOption {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return nil
	}
	extra := "filename=" + filename
	if hash := strings.TrimSpace(videoHash); hash != "" {
		extra += "&videoHash=" + hash
	}
	endpoint := c.endpoint + "/subtitles/" + url.PathEscape(kind) + "/" + url.PathEscape(itemID) + "/" + url.PathEscape(extra) + ".json"
	if c.query != "" {
		endpoint += "?" + c.query
	}
	var payload struct {
		Subtitles []streamSubtitle `json:"subtitles"`
	}
	if err := c.getJSON(ctx, endpoint, &payload); err != nil {
		return nil
	}
	var options []provider.SubtitleOption
	seen := make(map[string]bool, len(payload.Subtitles))
	for _, sub := range payload.Subtitles {
		raw := strings.TrimSpace(sub.URL)
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		options = append(options, provider.SubtitleOption{
			URL: raw, Language: lang.Normalize(sub.Lang), Referer: c.endpoint,
		})
	}
	return options
}

func appendUniqueSubtitles(existing, extra []provider.SubtitleOption) []provider.SubtitleOption {
	if len(extra) == 0 {
		return existing
	}
	seen := make(map[string]bool, len(existing)+len(extra))
	for _, sub := range existing {
		seen[sub.URL] = true
	}
	for _, sub := range extra {
		if sub.URL == "" || seen[sub.URL] {
			continue
		}
		seen[sub.URL] = true
		existing = append(existing, sub)
	}
	return existing
}

// streamQuality derives a short quality label from the fields bridge addons
// actually populate. Their title is a multi-line advertisement, so the file
// name, binge group and resolution token are more reliable sources.
func streamQuality(item streamItem) (string, int) {
	title := strings.TrimSpace(item.Title)
	name := strings.TrimSpace(item.Name)
	// Short single-line titles are genuine quality labels (e.g. "1080p WEB");
	// bridge addons instead publish multi-line advertising copy.
	if title != "" && !strings.Contains(title, "\n") && len(title) <= 40 {
		height, _ := kit.NormalizeQuality(title)
		return title, height
	}
	// Explicit resolution tokens are more reliable than a generic stream name.
	for _, candidate := range []string{item.Behavior.Filename, item.Behavior.BingeGroup, title, name} {
		if height := findResolution(candidate); height > 0 {
			return strconv.Itoa(height) + "p", height
		}
	}
	if name != "" && !strings.Contains(name, "\n") && len(name) <= 40 {
		height, _ := kit.NormalizeQuality(name)
		return name, height
	}
	return "Auto", 0
}

// findResolution extracts the first plausible <number>p token in value.
func findResolution(value string) int {
	lower := strings.ToLower(value)
	for index := 0; index < len(lower); index++ {
		if lower[index] != 'p' {
			continue
		}
		end := index
		start := index
		for start > 0 && lower[start-1] >= '0' && lower[start-1] <= '9' {
			start--
		}
		if start == end {
			continue
		}
		height, err := strconv.Atoi(lower[start:end])
		if err == nil && height >= 240 && height <= 4320 {
			return height
		}
	}
	return 0
}

func mediaSources(items []streamItem) []provider.MediaSource {
	var sources []provider.MediaSource
	for _, item := range items {
		streamURL := strings.TrimSpace(item.URL)
		if streamURL == "" || (!provider.IsDirectURL(streamURL) && !isRemoteAddonProxyURL(streamURL)) {
			// Kari plays streams itself without yt-dlp or a torrent client;
			// externalUrl/ytId/infoHash-only rows would be dead entries.
			if item.ExternalURL != "" || item.YTID != "" || item.InfoHash != "" {
				// Values can carry addon credentials; record presence only.
				logging.Debug("skipping stremio stream without a direct playable URL",
					"hasExternalURL", item.ExternalURL != "",
					"hasYTID", item.YTID != "",
					"hasInfoHash", item.InfoHash != "")
			}
			continue
		}
		quality, height := streamQuality(item)
		source := provider.MediaSource{
			URL: streamURL, Quality: quality, Type: sourceType(streamURL),
			Height: height, FallbackOnly: strings.TrimSpace(item.ClearKey) != "",
		}
		headers := make(map[string]string, len(item.Behavior.Headers)+len(item.Behavior.ProxyHeaders.Request))
		for key, value := range item.Behavior.Headers {
			headers[key] = value
		}
		for key, value := range item.Behavior.ProxyHeaders.Request {
			headers[key] = value
		}
		extraHeaders := make(map[string]string)
		for key, value := range headers {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "referer":
				source.Referer = value
			case "user-agent":
				source.UserAgent = value
			case "cookie":
				source.CookieHeader = value
			default:
				if name := http.CanonicalHeaderKey(strings.TrimSpace(key)); name != "" {
					extraHeaders[name] = value
				}
			}
		}
		if len(extraHeaders) > 0 {
			source.ExtraHeaders = extraHeaders
		}
		source.Subtitles = subtitleOptions(item.Subtitles, source.Referer)
		sources = append(sources, source)
	}
	return sources
}

// subtitleOptions keeps only subtitles Kari can fetch itself and carries the
// stream's referer so hotlink-protected subtitle hosts keep working.
func subtitleOptions(subs []streamSubtitle, referer string) []provider.SubtitleOption {
	var options []provider.SubtitleOption
	for _, sub := range subs {
		raw := strings.TrimSpace(sub.URL)
		if raw == "" || (!provider.IsDirectURL(raw) && !isRemoteAddonProxyURL(raw)) {
			continue
		}
		options = append(options, provider.SubtitleOption{
			URL:      raw,
			Language: lang.Normalize(sub.Lang),
			Referer:  referer,
			Default:  strings.Contains(strings.ToLower(sub.Label), "default"),
		})
	}
	return options
}

func isRemoteAddonProxyURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.Contains(strings.ToLower(parsed.Path), "/proxy/") {
		return false
	}
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
	return supportsResource(info, "stream")
}

func supportsResource(info manifest, name string) bool {
	return supportsResourceFor(info, name, "")
}

// supportsResourceFor honors the ID-prefix constraint carried by an object-form
// manifest resource. Resource type lists are advisory: Stremio types are
// free-form and bridge addons commonly declare standard types while serving
// custom catalogs, so a type mismatch must not hide an otherwise valid stream.
func supportsResourceFor(info manifest, name, itemID string) bool {
	for _, resource := range info.Resources {
		if resource.Name != name {
			continue
		}
		if itemID != "" && len(resource.IDPrefixes) > 0 && !matchesAnyPrefix(itemID, resource.IDPrefixes) {
			continue
		}
		return true
	}
	return false
}

func matchesAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(value, prefix) {
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
	for _, resource := range info.Resources {
		for _, mediaType := range resource.Types {
			hasMovie = hasMovie || mediaType == "movie"
			hasSeries = hasSeries || mediaType == "series"
		}
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
	if len(modes) == 0 {
		modes = append(modes, provider.Mode{Name: provider.ModeStremio, Priority: 10})
	}
	return modes
}

func supportsCatalogs(info manifest) bool {
	return supportsResource(info, "catalog")
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
