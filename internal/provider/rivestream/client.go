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
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

// log scopes every line from this package with its identity.
var rsLog = logging.With("provider", "rivestream")

const (
	riveStreamAPI = config.RiveStreamAPIBase
	riveStreamUA  = config.DesktopUserAgent
)

// Client implements provider.Provider against the RiveStream API.
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

// NewClient constructs the RiveStream provider over the shared TMDB search
// base.
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

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "RiveStream" }

// Name implements Provider.
func (c *Client) Name() string { return "rivestream" }

// Modes implements Provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
	}
}

// audioLanguages is the set of audio-track languages rivestream's internal
// providers tag streams with (e.g. citadel's "720p | English",
// hindicast's Hindi audio).
var audioLanguages = []provider.AudioLanguage{
	{Code: "English", Display: "English"},
	{Code: "Hindi", Display: "Hindi"},
}

// AudioLanguages implements provider.AudioLanguagesSource.
func (c *Client) AudioLanguages() []provider.AudioLanguage { return audioLanguages }

// Search delegates to the shared TMDB-keyed base.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes delegates to the shared TMDB-keyed base.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

// ResolveSource fetches stream candidates for a TMDB id, deduplicating
// URLs and extracting per-source audio language from quality labels.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(mediaID)
		if err != nil {
			return nil, fmt.Errorf("invalid media ID: %w", err)
		}
	}
	mediaType := provider.MediaTypeMovie
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = provider.MediaTypeTV
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
		quality, audioLang := normalizeQualityLanguage(quality)
		source := provider.MediaSource{
			URL:       s.URL,
			Quality:   quality,
			Type:      s.Format,
			UserAgent: riveStreamUA,
			Subtitles: nil,
		}
		if audioLang != "" {
			source.Language = audioLang
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// languageFromQuality extracts the audio language from rivestream's
// "720p | English" quality format; empty when the field has no "| language"
// suffix.
func languageFromQuality(quality string) string {
	if i := strings.LastIndex(quality, "|"); i >= 0 {
		return strings.TrimSpace(quality[i+1:])
	}
	return ""
}

// normalizeQualityLanguage rewrites rivestream's "720p | <language>" quality
// so the suffix is always the English display name, and returns that name as
// the second value (empty when there is no language suffix). Guards against
// upstream native-script or coded text leaking into UI labels.
func normalizeQualityLanguage(quality string) (string, string) {
	audio := languageFromQuality(quality)
	if audio == "" {
		return quality, ""
	}
	display := lang.Name(audio)
	base := strings.TrimSpace(quality[:strings.LastIndex(quality, "|")])
	return base + " | " + display, display
}

// qualityLabel renders the rivestream quality field, which is inconsistently
// typed across its internal providers: some return a string ("720p | English",
// "HLS", raw CDN labels like "dcloud"/"ipcloud") while others return a bare
// integer (flowcast/hindicast: 480, 720). Accept both instead of letting the
// strict string decode fail the whole response.
func qualityLabel(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return ""
		}
		return s
	}
	var n json.Number
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return ""
	}
	return n.String()
}

func (c *Client) fetchSources(ctx context.Context, tmdbID int, mediaType string, season, episode int) (*riveStreamResponse, error) {
	rsLog.Debug("fetch start", "tmdbID", tmdbID, "mediaType", mediaType, "season", season, "episode", episode)

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
		logging.Debug("request failed", "provider", c.Name(), "err", err)
		return nil, fmt.Errorf("rivestream fetch sources: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		logging.Debug("api", "provider", c.Name(), "status", resp.StatusCode)
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: req.URL.String()}
	}

	var result riveStreamResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logging.Debug("decode failed", "provider", c.Name(), "err", err)
		return nil, fmt.Errorf("rivestream fetch sources: decode response: %w", err)
	}
	if len(result.Sources) == 0 {
		logging.Debug("no sources", "provider", c.Name(), "tmdbID", tmdbID)
		return nil, provider.ErrNoSources
	}

	logging.Debug("fetch success", "provider", c.Name(), "sources", len(result.Sources), "subs", len(result.Subtitles))
	return &result, nil
}

var (
	_ provider.Provider            = (*Client)(nil)
	_ provider.Presenter           = (*Client)(nil)
	_ provider.AudioLanguagesSource = (*Client)(nil)
)
