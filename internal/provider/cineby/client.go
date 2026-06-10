package cineby

import (
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
	cinebyReferer = config.CinebyReferer
	cinebyUA      = config.DesktopUserAgent
)

type Client struct {
	base       *streambase.Base
	httpClient *http.Client
}

type CinebySource struct {
	URL     string `json:"url"`
	Quality string `json:"quality"`
}

type CinebySubtitle struct {
	URL      string `json:"url"`
	Language string `json:"lang"`
	Name     string `json:"name"`
}

func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(cinebyUA),
	}, nil
}

func (c *Client) Name() string { return "cineby" }

func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeMovies, Priority: 1}, {Name: provider.ModeTV, Priority: 1}}
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
	result, subs, err := c.FetchCinebySources(ctx, tmdbID, mediaType, episode.Season, episode.Episode)
	if err != nil {
		return nil, err
	}

	best := pickBestSource(result)
	if best == nil {
		return nil, provider.ErrNoSources
	}

	ms := provider.MediaSource{
		URL:       best.URL,
		Quality:   "[CINEBY] Source 1",
		Referer:   cinebyReferer,
		UserAgent: cinebyUA,
	}
	for _, sub := range subs {
		if sub.Language == "en" {
			ms.Subtitles = append(ms.Subtitles, sub.URL)
		}
	}
	return []provider.MediaSource{ms}, nil
}

func (c *Client) FetchCinebySources(ctx context.Context, tmdbID int, mediaType string, season, episode int) ([]CinebySource, []CinebySubtitle, error) {
	logging.Debugf("cineby fetch start tmdbID=%d media_type=%q S%dE%d", tmdbID, mediaType, season, episode)
	mt := "movie"
	if mediaType == "tv" {
		mt = "tv"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, config.CinebyAPIBase, nil)
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

	req.Header.Set("User-Agent", cinebyUA)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logging.Errorf("cineby request failed err=%v", err)
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		logging.Warnf("cineby API returned status %d", resp.StatusCode)
		return nil, nil, &provider.HTTPError{Code: resp.StatusCode, URL: req.URL.String()}
	}

	var result struct {
		Sources   []CinebySource   `json:"sources"`
		Subtitles []CinebySubtitle `json:"subtitles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logging.Errorf("cineby parse failure err=%v", err)
		return nil, nil, err
	}
	if len(result.Sources) == 0 {
		logging.Warnf("cineby returned no sources tmdbID=%d", tmdbID)
		return nil, nil, provider.ErrNoSources
	}

	logging.Debugf("cineby fetch success sources=%d subs=%d", len(result.Sources), len(result.Subtitles))
	return result.Sources, result.Subtitles, nil
}

func pickBestSource(sources []CinebySource) *CinebySource {
	if len(sources) == 0 {
		return nil
	}
	best := &sources[0]
	bestVal := parseQuality(best.Quality)
	for i := 1; i < len(sources); i++ {
		if v := parseQuality(sources[i].Quality); v > bestVal {
			best = &sources[i]
			bestVal = v
		}
	}
	return best
}

func parseQuality(q string) int {
	q = strings.TrimSpace(strings.ToLower(q))
	q = strings.TrimSuffix(q, "p")
	n, err := strconv.Atoi(q)
	if err != nil {
		return 0
	}
	return n
}

var _ provider.Provider = (*Client)(nil)
