// Package a11 implements provider.Provider for the 111477 streaming service,
// resolving ultra-high-definition 4K BluRay Remuxes and multi-bitrate streams
// directly from Cloudflare Worker endpoints without helper proxies.
package a11

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
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

var a11Log = logging.With("provider", "a11")

var (
	reResolution = regexp.MustCompile(`(?i)\b(2160p|4k|1440p|1080p|720p|480p)\b`)
	reFileSize   = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?\s*(?:gb|mb))\b`)
	reRemux      = regexp.MustCompile(`(?i)\bremux\b`)
	reIMAX       = regexp.MustCompile(`(?i)\bimax\b`)
)

const (
	defaultServiceOrigin = "https://st.111477.xyz"
	defaultConfigBase    = "https://a.111477.xyz"
)

// Client implements provider.Provider against the 111477 Stremio addon protocol.
type Client struct {
	base          *streambase.Base
	keyPool       *tmdb.KeyPool
	httpClient    *http.Client
	serviceOrigin string
	configBase    string
	tmdbBase      string
}

type streamResponse struct {
	Streams []streamItem `json:"streams"`
}

type streamItem struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// NewClient constructs the 111477 provider using the shared TMDB search base.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithURLs(keyPool, defaultServiceOrigin, defaultConfigBase, httpclient.New())
}

// NewClientWithURLs constructs the 111477 provider with custom service endpoints
// and HTTP client for integration and offline unit testing.
func NewClientWithURLs(keyPool *tmdb.KeyPool, serviceOrigin, configBase string, hc *http.Client) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("a11 streambase: %w", err)
	}
	if hc == nil {
		hc = httpclient.New()
	}
	return &Client{
		base:          base,
		keyPool:       keyPool,
		httpClient:    hc,
		serviceOrigin: strings.TrimRight(serviceOrigin, "/"),
		configBase:    strings.TrimRight(configBase, "/"),
		tmdbBase:      config.TMDBAPIBase,
	}, nil
}

// SetTMDBBaseURL overrides the TMDB API endpoint for offline unit testing.
func (c *Client) SetTMDBBaseURL(endpoint string) {
	c.tmdbBase = strings.TrimRight(endpoint, "/")
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "111477" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "a11" }

// Modes implements provider.Provider, declaring movie and TV catalog support.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 10},
		{Name: provider.ModeTV, Priority: 10},
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

// ResolveSource queries 111477 for all available 4K and HD streams.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	ch := make(chan []provider.MediaSource, 8)
	errCh := make(chan error, 1)

	go func() {
		defer close(ch)
		errCh <- c.ResolveStream(ctx, mediaID, episode, ch)
	}()

	var all []provider.MediaSource
	for batch := range ch {
		all = append(all, batch...)
	}

	if err := <-errCh; err != nil && len(all) == 0 {
		return nil, err
	}
	if len(all) == 0 {
		return nil, provider.ErrNoSources
	}
	return all, nil
}

// ResolveStream implements provider.StreamingProvider, pushing playable 4K Remux
// and HD streams as they are discovered.
func (c *Client) ResolveStream(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	tmdbID, isTv, season, epNum, err := parseCoords(mediaID, episode)
	if err != nil {
		return err
	}

	imdbID := c.resolveIMDBID(ctx, tmdbID, isTv)
	targetIDs := make([]string, 0, 2)
	if imdbID != "" && strings.HasPrefix(imdbID, "tt") {
		targetIDs = append(targetIDs, imdbID)
	}
	targetIDs = append(targetIDs, fmt.Sprintf("tmdb:%d", tmdbID))

	tmdbKey, _ := c.keyPool.NextKey()
	addonBase := c.generateManifestBaseURL(tmdbKey)

	var emitted []provider.MediaSource
	seenURLs := make(map[string]struct{})

	for _, targetID := range targetIDs {
		var endpoint string
		if isTv {
			endpoint = fmt.Sprintf("%s/stream/series/%s:%d:%d.json", addonBase, targetID, season, epNum)
		} else {
			endpoint = fmt.Sprintf("%s/stream/movie/%s.json", addonBase, targetID)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", config.DesktopUserAgent)
		req.Header.Set("Accept", "application/json, text/plain, */*")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			a11Log.Debug("stream request failed", "targetID", targetID, "err", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var payload streamResponse
		decErr := json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if decErr != nil {
			continue
		}

		if len(payload.Streams) == 0 {
			continue
		}

		var batch []provider.MediaSource
		for _, item := range payload.Streams {
			cleanURL := strings.TrimSpace(item.URL)
			if cleanURL == "" || !strings.HasPrefix(cleanURL, "http") {
				continue
			}
			if _, exists := seenURLs[cleanURL]; exists {
				continue
			}
			seenURLs[cleanURL] = struct{}{}
			src := toMediaSource(cleanURL, item)
			batch = append(batch, src)
			emitted = append(emitted, src)
		}

		if len(batch) > 0 {
			if updates != nil {
				select {
				case updates <- batch:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			// If target matched streams, don't fallback to subsequent target IDs
			break
		}
	}

	if len(emitted) == 0 {
		return provider.ErrNoSources
	}
	return nil
}

func (c *Client) generateManifestBaseURL(tmdbKey string) string {
	rawCfg := fmt.Sprintf("%s/::sort=file-desc::limit=5", c.configBase)
	if tmdbKey != "" {
		rawCfg += fmt.Sprintf("::tmdb=%s", tmdbKey)
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(rawCfg))
	return fmt.Sprintf("%s/config/%s", c.serviceOrigin, encoded)
}

func (c *Client) resolveIMDBID(ctx context.Context, tmdbID int, isTv bool) string {
	key, err := c.keyPool.NextKey()
	if err != nil || key == "" {
		return ""
	}
	mediaType := "movie"
	if isTv {
		mediaType = "tv"
	}
	tmdbBase := c.tmdbBase
	if tmdbBase == "" {
		tmdbBase = config.TMDBAPIBase
	}
	lookupURL := fmt.Sprintf("%s/%s/%d?api_key=%s&append_to_response=external_ids", tmdbBase, mediaType, tmdbID, url.QueryEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			c.keyPool.MarkFailed(key)
		}
		return ""
	}
	var res struct {
		IMDBID      string `json:"imdb_id"`
		ExternalIDs struct {
			IMDBID string `json:"imdb_id"`
		} `json:"external_ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return ""
	}

	if res.IMDBID != "" {
		return res.IMDBID
	}
	return res.ExternalIDs.IMDBID
}

func parseCoords(mediaID string, episode provider.Episode) (int, bool, int, int, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return 0, false, 0, 0, fmt.Errorf("invalid tmdb media id %q: %w", mediaID, err)
		}
	}
	isTv := episode.Season > 0 || episode.Episode > 0
	season := episode.Season
	epNum := episode.Episode
	if isTv {
		if season <= 0 {
			season = 1
		}
		if epNum <= 0 {
			epNum = 1
		}
	}
	return tmdbID, isTv, season, epNum, nil
}

func toMediaSource(cleanURL string, item streamItem) provider.MediaSource {
	title := item.Title
	quality := extractQuality(title)
	srcType := provider.SourceTypeMP4
	if strings.Contains(cleanURL, ".m3u8") {
		srcType = provider.SourceTypeHLS
	}

	return provider.MediaSource{
		URL:       cleanURL,
		Quality:   quality,
		Type:      srcType,
		UserAgent: config.DesktopUserAgent,
	}
}

func extractQuality(title string) string {
	res := reResolution.FindString(title)
	if res == "" {
		res = "2160p"
	} else {
		res = strings.ToLower(res)
	}

	size := reFileSize.FindString(title)
	isRemux := reRemux.MatchString(title)
	isIMAX := reIMAX.MatchString(title)

	tag := "Direct"
	if isRemux {
		tag = "Remux"
	} else if isIMAX {
		tag = "IMAX"
	} else if strings.Contains(strings.ToLower(title), "bluray") {
		tag = "BluRay"
	}

	if size != "" {
		tag = fmt.Sprintf("%s %s", tag, strings.ToUpper(size))
	}

	return fmt.Sprintf("%s [%s]", strings.ToUpper(res), tag)
}
