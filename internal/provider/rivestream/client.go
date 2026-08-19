package rivestream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

const (
	riveStreamAPI = config.RiveStreamAPIBase
	riveStreamUA  = config.DesktopUserAgent
)

type Client struct {
	base       *streambase.Base
	httpClient *http.Client
}

type riveStreamSourceItem struct {
	URL     string          `json:"url"`
	Quality json.RawMessage `json:"quality"`
	Format  string          `json:"format"`
}

type riveStreamSubtitle struct {
	URL  string `json:"url"`
	Lang string `json:"lang"`
	Name string `json:"name"`
}

type riveStreamResponse struct {
	Sources   []riveStreamSourceItem `json:"sources"`
	Subtitles []riveStreamSubtitle   `json:"subtitles"`
}

func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(riveStreamUA),
	}, nil
}

func (c *Client) Name() string { return "rivestream" }

func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
	}
}

func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return c.base.Search(ctx, query, mode)
}

func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(mediaID)
		if err != nil {
			return nil, fmt.Errorf("invalid media ID: %w", err)
		}
	}
	mediaType := "movie"
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = "tv"
	}
	resp, err := c.fetchSources(ctx, tmdbID, mediaType, episode.Season, episode.Episode)
	if err != nil {
		return nil, err
	}
	if len(resp.Sources) == 0 {
		return nil, provider.ErrNoSources
	}

	sources := make([]provider.MediaSource, 0, len(resp.Sources))
	seen := make(map[string]struct{}, len(resp.Sources))
	for _, s := range resp.Sources {
		if s.URL == "" {
			continue
		}
		if _, ok := seen[s.URL]; ok {
			continue
		}
		seen[s.URL] = struct{}{}
		quality := qualityLabel(s.Quality)
		if quality == "" {
			quality = "Auto"
		}
		name := riveProviderName(quality)
		if name == "" {
			name = "RIVESTREAM"
		}
		sources = append(sources, provider.MediaSource{
			URL:       s.URL,
			Quality:   fmt.Sprintf("[%s] %s", strings.ToUpper(name), quality),
			Type:      s.Format,
			Language:  languageFromQuality(quality),
			UserAgent: riveStreamUA,
			Subtitles: nil,
		})
	}
	return sources, nil
}

func riveProviderName(quality string) string {
	q := strings.ToLower(strings.TrimSpace(quality))
	switch {
	case strings.Contains(q, "tcloud") || strings.Contains(q, "dcloud") || strings.Contains(q, "ipcloud"):
		return "primevids" // CDN labels, no resolution
	case strings.Contains(q, "|"):
		return "citadel" // "720p | English" — resolution + audio language
	case q == "hls":
		return "apex" // bare "HLS"
	case isBareInteger(q):
		return "flowcast" // "480", "720" — bare integers (also hindicast, indistinguishable)
	default:
		return ""
	}
}

func isBareInteger(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func languageFromQuality(quality string) string {
	if i := strings.Index(quality, "|"); i >= 0 {
		return strings.TrimSpace(quality[i+1:])
	}
	return ""
}

// qualityLabel renders the rivestream quality field, which is inconsistently
// typed across its internal providers: some return a string ("720p | English",
// "HLS", raw CDN labels like "dcloud"/"ipcloud") while others return a bare
// integer (flowcast/hindicast: 480, 720). Accept both instead of letting the
// strict string decode fail the whole response.
func qualityLabel(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return ""
	}
	return n.String()
}

func (c *Client) fetchSources(ctx context.Context, tmdbID int, mediaType string, season, episode int) (*riveStreamResponse, error) {
	logging.Debugf("rivestream fetch start tmdbID=%d media_type=%q S%dE%d", tmdbID, mediaType, season, episode)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, riveStreamAPI, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("type", mediaType)
	q.Set("id", strconv.Itoa(tmdbID))
	if season > 0 {
		q.Set("season", strconv.Itoa(season))
	}
	if episode > 0 {
		q.Set("episode", strconv.Itoa(episode))
	}
	req.URL.RawQuery = q.Encode()

	req.Header.Set("User-Agent", riveStreamUA)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logging.Errorf("rivestream request failed err=%v", err)
		return nil, fmt.Errorf("rivestream fetch sources: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		logging.Warnf("rivestream API returned status %d", resp.StatusCode)
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: req.URL.String()}
	}

	var result riveStreamResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logging.Errorf("rivestream parse failure err=%v", err)
		return nil, fmt.Errorf("rivestream fetch sources: decode response: %w", err)
	}
	if len(result.Sources) == 0 {
		logging.Warnf("rivestream returned no sources tmdbID=%d", tmdbID)
		return nil, provider.ErrNoSources
	}

	logging.Debugf("rivestream fetch success sources=%d subs=%d", len(result.Sources), len(result.Subtitles))
	return &result, nil
}

var _ provider.Provider = (*Client)(nil)
