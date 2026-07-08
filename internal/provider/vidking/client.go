package vidking

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	vidKingAPI     = config.VidKingAPIBase
	vidKingReferer = config.VidKingReferer
	vidKingUA      = config.DesktopUserAgent
)

type Client struct {
	base       *streambase.Base
	httpClient *http.Client
}

type VidKingSubtitle struct {
	URL      string `json:"url"`
	Language string `json:"lang"`
	Name     string `json:"label"`
}

type vidKingResponse struct {
	Sources   []string          `json:"sources"`
	Subtitles []VidKingSubtitle `json:"subtitles"`
}

func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(vidKingUA),
	}, nil
}

func (c *Client) Name() string { return "vidking" }

func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeMovies, Priority: 2}, {Name: provider.ModeTV, Priority: 1}}
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
	resp, err := c.FetchVidKingSources(ctx, tmdbID, mediaType, episode.Season, episode.Episode)
	if err != nil {
		return nil, err
	}
	if len(resp.Sources) == 0 {
		return nil, provider.ErrNoSources
	}

	sources := make([]provider.MediaSource, 0, len(resp.Sources))
	for i, u := range resp.Sources {
		q := qualityFromPath(u)
		if q == "" {
			q = fmt.Sprintf("Source %d", i+1)
		}
		ms := provider.MediaSource{
			URL:       u,
			Quality:   fmt.Sprintf("[VIDKING] %s", q),
			Referer:   vidKingReferer,
			UserAgent: vidKingUA,
		}
		for _, sub := range resp.Subtitles {
			if sub.Language == "en" {
				ms.Subtitles = append(ms.Subtitles, sub.URL)
			}
		}
		sources = append(sources, ms)
	}
	return sources, nil
}

func qualityFromPath(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	mediaURL := parsed.Query().Get("url")
	if mediaURL == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(mediaURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.TrimSuffix(decoded, "/"), "/")
	if len(parts) >= 2 {
		candidate := parts[len(parts)-2]
		if candidate == "4k" || strings.HasSuffix(candidate, "p") {
			return candidate
		}
	}
	return ""
}

func (c *Client) FetchVidKingSources(ctx context.Context, tmdbID int, mediaType string, season, episode int) (*vidKingResponse, error) {
	logging.Debugf("vidking fetch start tmdbID=%d media_type=%q S%dE%d", tmdbID, mediaType, season, episode)
	mt := "movie"
	if mediaType == "tv" {
		mt = "tv"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, vidKingAPI, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("tmdb", strconv.Itoa(tmdbID))
	q.Set("type", mt)
	if season > 0 {
		q.Set("s", strconv.Itoa(season))
		q.Set("season", strconv.Itoa(season))
	}
	if episode > 0 {
		q.Set("e", strconv.Itoa(episode))
		q.Set("episode", strconv.Itoa(episode))
	}
	req.URL.RawQuery = q.Encode()

	req.Header.Set("User-Agent", vidKingUA)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logging.Errorf("vidking request failed err=%v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		logging.Warnf("vidking API returned status %d", resp.StatusCode)
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: req.URL.String()}
	}

	var result vidKingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logging.Errorf("vidking parse failure err=%v", err)
		return nil, err
	}
	if len(result.Sources) == 0 {
		logging.Warnf("vidking returned no sources tmdbID=%d", tmdbID)
		return nil, provider.ErrNoSources
	}

	logging.Debugf("vidking fetch success sources=%d subs=%d", len(result.Sources), len(result.Subtitles))
	return &result, nil
}

var _ provider.Provider = (*Client)(nil)
