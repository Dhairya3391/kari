package moviebox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

const (
	movieboxAPI = config.MovieboxAPIBase
	movieboxUA  = config.DesktopUserAgent
)

type Client struct {
	base       *streambase.Base
	httpClient *http.Client
}

type movieboxSubtitle struct {
	URL  string `json:"url"`
	Lang string `json:"lang"`
	Name string `json:"name"`
}

type movieboxSourceItem struct {
	URL     string `json:"url"`
	Quality string `json:"quality"`
}

type movieboxMeta struct {
	Title     string   `json:"title"`
	Year      int      `json:"year"`
	TMDBID    int      `json:"tmdb_id"`
	SubjectID string   `json:"moviebox_subject_id"`
	Languages []string `json:"languages"`
}

type movieboxResponse struct {
	Sources   map[string][]movieboxSourceItem `json:"sources"`
	Subtitles []movieboxSubtitle              `json:"subtitles"`
	Meta      movieboxMeta                    `json:"meta"`
}

func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(movieboxUA),
	}, nil
}

func (c *Client) Name() string { return "moviebox" }

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

	result, err := c.fetchMovieBoxSources(ctx, tmdbID, mediaType, episode.Season, episode.Episode)
	if err != nil {
		return nil, err
	}

	var sources []provider.MediaSource
	sortedLangs := make([]string, 0, len(result.Sources))
	for lang := range result.Sources {
		sortedLangs = append(sortedLangs, lang)
	}
	sort.Strings(sortedLangs)

	var englishSubs []string
	for _, sub := range result.Subtitles {
		if sub.Lang == "en" {
			englishSubs = append(englishSubs, sub.URL)
		}
	}

	for _, lang := range sortedLangs {
		items := result.Sources[lang]
		for _, item := range items {
			label := fmt.Sprintf("[MOVIEBOX] %s %s", item.Quality, lang)
			ms := provider.MediaSource{
				URL:       item.URL,
				Quality:   label,
				Referer:   "",
				UserAgent: movieboxUA,
				Language:  lang,
				Subtitles: englishSubs,
			}
			sources = append(sources, ms)
		}
	}

	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}

	return sources, nil
}

func (c *Client) fetchMovieBoxSources(ctx context.Context, tmdbID int, mediaType string, season, episode int) (*movieboxResponse, error) {
	logging.Debugf("moviebox fetch start tmdbID=%d type=%q S%dE%d", tmdbID, mediaType, season, episode)

	u, err := url.Parse(movieboxAPI)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("tmdb", strconv.Itoa(tmdbID))
	q.Set("type", mediaType)
	if season > 0 {
		q.Set("s", strconv.Itoa(season))
	}
	if episode > 0 {
		q.Set("e", strconv.Itoa(episode))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", movieboxUA)

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

	var result movieboxResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

var _ provider.Provider = (*Client)(nil)
