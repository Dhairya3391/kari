package vidking

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

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

type VidKingSource struct {
	URL string `json:"u"`
}

type VidKingSubtitle struct {
	URL      string `json:"url"`
	Language string `json:"lang"`
	Name     string `json:"label"`
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
	result, subs, err := c.FetchVidKingSources(ctx, tmdbID, mediaType, episode.Season, episode.Episode)
	if err != nil {
		return nil, err
	}
	sources := make([]provider.MediaSource, 0, len(result))
	for i, src := range result {
		ms := provider.MediaSource{
			URL:       src.URL,
			Quality:   fmt.Sprintf("[VIDKING] Source %d", i+1),
			Referer:   vidKingReferer,
			UserAgent: vidKingUA,
		}
		for _, sub := range subs {
			if sub.Language == "en" {
				ms.Subtitles = append(ms.Subtitles, sub.URL)
			}
		}
		sources = append(sources, ms)
	}
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

func (c *Client) FetchVidKingSources(ctx context.Context, tmdbID int, mediaType string, season, episode int) ([]VidKingSource, []VidKingSubtitle, error) {
	logging.Debugf("vidking fetch start tmdbID=%d media_type=%q S%dE%d", tmdbID, mediaType, season, episode)
	mt := "movie"
	if mediaType == "tv" {
		mt = "tv"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, vidKingAPI, nil)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		logging.Warnf("vidking API returned status %d", resp.StatusCode)
		return nil, nil, &provider.HTTPError{Code: resp.StatusCode, URL: req.URL.String()}
	}

	var result struct {
		Sources   []string          `json:"sources"`
		Subtitles []VidKingSubtitle `json:"subtitles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logging.Errorf("vidking parse failure err=%v", err)
		return nil, nil, err
	}
	if len(result.Sources) == 0 {
		logging.Warnf("vidking returned no sources tmdbID=%d", tmdbID)
		return nil, nil, provider.ErrNoSources
	}

	sources := make([]VidKingSource, len(result.Sources))
	for i, u := range result.Sources {
		sources[i] = VidKingSource{URL: u}
	}

	var subtitles []VidKingSubtitle
	if len(result.Subtitles) > 0 {
		subtitles = result.Subtitles
	}

	logging.Debugf("vidking fetch success sources=%d subs=%d", len(sources), len(subtitles))
	return sources, subtitles, nil
}

var _ provider.Provider = (*Client)(nil)
