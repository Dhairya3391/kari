package vidnest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

const (
	vidNestAPI = config.VidNestAPIBase
)

type Client struct {
	base       *streambase.Base
	httpClient *http.Client
}

type VidNestStream struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type VidNestResponse struct {
	Sources []string        `json:"sources"`
	Streams []VidNestStream `json:"streams"`
}

func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:       base,
		httpClient: httpclient.New(),
	}, nil
}

func (c *Client) Name() string { return "vidnest" }

func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 1},
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

	u, err := url.Parse(vidNestAPI)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("tmdb", strconv.Itoa(tmdbID))
	q.Set("type", mediaType)
	if mediaType == "tv" {
		q.Set("s", strconv.Itoa(episode.Season))
		q.Set("e", strconv.Itoa(episode.Episode))
	}
	u.RawQuery = q.Encode()

	logging.Debugf("vidnest resolve start url=%q", u.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u.String()}
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res VidNestResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if len(res.Streams) == 0 {
		return nil, provider.ErrNoSources
	}

	out := make([]provider.MediaSource, 0, len(res.Streams))
	for i, s := range res.Streams {
		src := provider.MediaSource{
			URL:     s.URL,
			Quality: fmt.Sprintf("[VIDNEST] Source %d", i+1),
		}

		if val, ok := s.Headers["Referer"]; ok {
			src.Referer = val
		}
		if val, ok := s.Headers["User-Agent"]; ok {
			src.UserAgent = val
		}

		out = append(out, src)
	}

	return out, nil
}

var _ provider.Provider = (*Client)(nil)
