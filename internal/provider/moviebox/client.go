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
var mbLog = logging.With("provider", "moviebox")

const (
	movieboxAPI     = config.MovieboxAPIBase
	movieboxUA      = config.DesktopUserAgent
	movieboxMediaUA = "curl/8.7.1"
)

// Client implements provider.Provider against the MovieBox API.
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

// NewClient constructs the MovieBox provider over the shared TMDB search
// base.
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

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "MovieBox" }

// Name implements Provider.
func (c *Client) Name() string { return "moviebox" }

// Modes implements Provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
	}
}

// audioLanguages is the full set of audio-track languages the MovieBox API
// can return on MediaSources. Derived from testing across 17 movies and 12
// TV series.
var audioLanguages = []provider.AudioLanguage{
	{Code: "Original", Display: "Original"},
	{Code: "English", Display: "English"},
	{Code: "English sub", Display: "English sub"},
	{Code: "Bengali", Display: "Bengali"},
	{Code: "esla", Display: "Spanish (LatAm)"},
	{Code: "Hindi", Display: "Hindi"},
	{Code: "Kannada", Display: "Kannada"},
	{Code: "Malayalam", Display: "Malayalam"},
	{Code: "ptbr", Display: "Portuguese (BR)"},
	{Code: "Tamil", Display: "Tamil"},
	{Code: "Telugu", Display: "Telugu"},
}

// AudioLanguages implements provider.AudioLanguagesSource.
func (c *Client) AudioLanguages() []provider.AudioLanguage { return audioLanguages }

// movieboxDisplay resolves a MovieBox language code to its English display
// name: the provider's own table first (codes like "esla"/"ptbr" are
// MovieBox-private), then the shared language mapper as a fallback so an
// unexpected new code still renders readably instead of leaking raw.
func movieboxDisplay(code string) string {
	for _, l := range audioLanguages {
		if strings.EqualFold(l.Code, code) {
			return l.Display
		}
	}
	return lang.Name(code)
}

// Search delegates to the shared TMDB-keyed base.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes delegates to the shared TMDB-keyed base.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

// ResolveSource maps one TMDB id/season/episode to MovieBox sources,
// emitting one source per (language, quality) pair with attached subtitles.
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

	result, err := c.fetchMovieBoxSources(ctx, tmdbID, mediaType, episode.Season, episode.Episode)
	if err != nil {
		return nil, err
	}

	sortedLangs := make([]string, 0, len(result.Sources))
	for lang := range result.Sources {
		sortedLangs = append(sortedLangs, lang)
	}
	sort.Strings(sortedLangs)

	var subs []provider.SubtitleOption
	for _, sub := range result.Subtitles {
		subs = append(subs, provider.SubtitleOption{URL: sub.URL, Language: sub.Lang})
	}

	sources := buildSources(result.Sources, sortedLangs, subs)

	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}

	return sources, nil
}

// buildSources emits one MediaSource per (language, quality) pair. Language
// carries the raw API code — the stable AudioLanguage.Code that settings
// persist and match against; the display name stays UI-only in Quality.
func buildSources(sourcesByLang map[string][]movieboxSourceItem, langs []string, subs []provider.SubtitleOption) []provider.MediaSource {
	sources := make([]provider.MediaSource, 0)
	for _, code := range langs {
		items := sourcesByLang[code]
		display := movieboxDisplay(code)
		for _, item := range items {
			ms := provider.MediaSource{
				URL:       item.URL,
				Quality:   fmt.Sprintf("%s %s", item.Quality, display),
				UserAgent: movieboxMediaUA,
				Language:  code,
				Subtitles: subs,
			}
			sources = append(sources, ms)
		}
	}
	return sources
}

func (c *Client) fetchMovieBoxSources(ctx context.Context, tmdbID int, mediaType string, season, episode int) (*movieboxResponse, error) {
	mbLog.Debug("fetch start", "tmdbID", tmdbID, "mediaType", mediaType, "season", season, "episode", episode)

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
		return nil, fmt.Errorf("moviebox fetch sources: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u.String()}
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("moviebox fetch sources: read body: %w", err)
	}

	var result movieboxResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("moviebox fetch sources: decode response: %w", err)
	}

	return &result, nil
}

var (
	_ provider.Provider            = (*Client)(nil)
	_ provider.Presenter           = (*Client)(nil)
	_ provider.AudioLanguagesSource = (*Client)(nil)
)
