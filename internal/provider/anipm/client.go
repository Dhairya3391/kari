package anipm

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
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// Client implements provider.Provider for the standalone Ani.pm site (https://ani.pm).
// It queries Ani.pm's backend directly for anime search, episode listings,
// and playback sessions resolved via the Settlar embed player without intermediary proxies.
type Client struct {
	http      *http.Client
	siteBase  string
	embedBase string
}

// Alias implements provider.Presenter, returning the human-readable display name.
func (c *Client) Alias() string { return "Ani.pm" }

// Name implements provider.Provider, returning the stable registry identifier.
func (c *Client) Name() string { return "anipm" }

// Modes implements provider.Provider, registering Ani.pm for anime streaming.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 1},
	}
}

// RequiresEpisodeListForMovies implements provider.MovieEpisodeFlow. Ani.pm
// indexes episodes by number and uses episode-level playback bootstrapping.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Features implements provider.FeatureSource.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeAnime {
		return provider.Features{}
	}
	return provider.Features{AudioSelection: true}
}

// NewClient constructs an Ani.pm provider with the shared HTTP client.
func NewClient() (*Client, error) {
	return &Client{
		http:      httpclient.New(),
		siteBase:  config.AniPMBase,
		embedBase: config.AniPMEmbedBase,
	}, nil
}

// NewClientWithBaseURL constructs an Ani.pm provider with explicit base URLs for tests.
func NewClientWithBaseURL(siteBase, embedBase string) *Client {
	return &Client{
		http:      httpclient.New(),
		siteBase:  strings.TrimRight(siteBase, "/"),
		embedBase: strings.TrimRight(embedBase, "/"),
	}
}

func (c *Client) newRequest(ctx context.Context, method, targetURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.siteBase+"/")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

type searchResponse struct {
	Items []searchItem `json:"items"`
}

type searchItem struct {
	ID           int      `json:"id"`
	Title        string   `json:"title"`
	Native       string   `json:"native"`
	Year         int      `json:"year"`
	Type         string   `json:"type"`
	Score        float64  `json:"score"`
	Poster       string   `json:"poster"`
	EpisodeCount int      `json:"episodeCount"`
	SubCount     int      `json:"subCount"`
	DubCount     int      `json:"dubCount"`
	Synopsis     string   `json:"synopsis"`
	Genres       []string `json:"genres"`
	AniListID    any      `json:"anilistId"`
	MALID        int      `json:"malId"`
	RouteID      string   `json:"routeId"`
}

// Search queries Ani.pm's search endpoint.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	searchURL := fmt.Sprintf("%s/api/anime/search?q=%s", c.siteBase, url.QueryEscape(query))
	req, err := c.newRequest(ctx, http.MethodGet, searchURL)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anipm search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: searchURL}
	}

	var data searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("anipm search decode: %w", err)
	}

	if len(data.Items) == 0 {
		return nil, provider.ErrNoResults
	}

	results := make([]provider.SearchResult, 0, len(data.Items))
	for _, it := range data.Items {
		aniID := parseAniListID(it.AniListID)
		mediaType := provider.MediaTypeAnime
		if strings.EqualFold(it.Type, "movie") {
			mediaType = provider.MediaTypeMovie
		}

		resID := strconv.Itoa(it.ID)
		if aniID > 0 {
			resID = strconv.Itoa(aniID)
		}

		results = append(results, provider.SearchResult{
			ID:        resID,
			Title:     it.Title,
			Type:      mode,
			Year:      strconv.Itoa(it.Year),
			Provider:  c.Name(),
			CoverURL:  it.Poster,
			Overview:  it.Synopsis,
			Genres:    it.Genres,
			MediaType: mediaType,
		})
	}

	return results, nil
}

type seriesResponse struct {
	ID        int           `json:"id"`
	Title     string        `json:"title"`
	Episodes  []episodeItem `json:"episodes"`
	SettlarID int           `json:"settlarId"`
}

type episodeItem struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Sub         bool   `json:"sub"`
	Dub         bool   `json:"dub"`
	RouteID     string `json:"routeId"`
}

// FetchAvailableEpisodes lists all episodes for an anime series, noting sub/dub availability.
func (c *Client) FetchAvailableEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	seriesID := strings.TrimSpace(series.ID)
	if seriesID == "" {
		return nil, fmt.Errorf("empty series ID")
	}

	endpoint := fmt.Sprintf("%s/api/anime/ani/%s", c.siteBase, seriesID)
	req, err := c.newRequest(ctx, http.MethodGet, endpoint)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anipm episodes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Try by internal series ID if ani/{id} misses
		endpoint = fmt.Sprintf("%s/api/anime/series/%s", c.siteBase, seriesID)
		req2, err2 := c.newRequest(ctx, http.MethodGet, endpoint)
		if err2 == nil {
			resp2, err3 := c.http.Do(req2)
			if err3 == nil && resp2.StatusCode == http.StatusOK {
				defer resp2.Body.Close()
				resp = resp2
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}

	var data seriesResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("anipm episodes decode: %w", err)
	}

	if len(data.Episodes) == 0 {
		return nil, provider.ErrNoEpisodes
	}

	episodes := make([]provider.Episode, 0, len(data.Episodes))
	for _, ep := range data.Episodes {
		if ep.Sub {
			epHandle := fmt.Sprintf("watch/anipm/%s/sub/%d", seriesID, ep.Number)
			episodes = append(episodes, provider.Episode{
				ID:      epHandle,
				Title:   ep.Title,
				Episode: ep.Number,
				Season:  1,
				Audio:   provider.AudioSub,
			})
		}
		if ep.Dub {
			epHandle := fmt.Sprintf("watch/anipm/%s/dub/%d", seriesID, ep.Number)
			episodes = append(episodes, provider.Episode{
				ID:      epHandle,
				Title:   ep.Title,
				Episode: ep.Number,
				Season:  1,
				Audio:   provider.AudioDub,
			})
		}
	}

	return episodes, nil
}

// FetchEpisodes implements provider.Provider.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.FetchAvailableEpisodes(ctx, series)
}

type bootstrapResponse struct {
	SettlarSelection  string `json:"settlarSelection"`
	EffectiveLanguage string `json:"effectiveLanguage"`
}

type sessionResponse struct {
	EmbedURL string `json:"embedUrl"`
}

type playbackSession struct {
	Source    string             `json:"source"`
	Title     string             `json:"title"`
	AudioLang string             `json:"audioLang"`
	Subtitles []playbackSubtitle `json:"subtitles"`
}

type playbackSubtitle struct {
	URL     string `json:"url"`
	Label   string `json:"label"`
	SrcLang string `json:"srclang"`
	Default bool   `json:"default"`
}

// ResolveSource resolves an episode to an HLS stream and subtitles via Ani.pm's Settlar embed service.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	h := kit.ParseHandle(mediaID, episode.ID, episode.Audio, episode.Episode, c.Name())
	if !h.Valid() {
		return nil, fmt.Errorf("anipm resolve: invalid episode handle %q", episode.ID)
	}

	channel := "sub"
	if strings.EqualFold(h.Category, provider.AudioDub) {
		channel = "dub"
	}

	// 1. Playback bootstrap
	bootURL := fmt.Sprintf("%s/api/anime/playback-bootstrap/anilist/%s?ep=%d&lang=%s",
		c.siteBase, h.AniListID, h.Number, channel)
	req, err := c.newRequest(ctx, http.MethodGet, bootURL)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anipm bootstrap: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: bootURL}
	}

	var boot bootstrapResponse
	if err := json.NewDecoder(resp.Body).Decode(&boot); err != nil {
		return nil, fmt.Errorf("anipm bootstrap decode: %w", err)
	}
	if strings.TrimSpace(boot.SettlarSelection) == "" {
		return nil, fmt.Errorf("anipm: missing settlar selection token: %w", provider.ErrNoSources)
	}

	// 2. Settlar session
	params := url.Values{}
	params.Set("selection", boot.SettlarSelection)
	params.Set("provider", "anipm")
	params.Set("ep", strconv.Itoa(h.Number))
	params.Set("channel", channel)
	params.Set("telemetry", "0")

	sessionURL := fmt.Sprintf("%s/api/anime/settlar/session?%s", c.siteBase, params.Encode())
	req, err = c.newRequest(ctx, http.MethodGet, sessionURL)
	if err != nil {
		return nil, err
	}

	resp, err = c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anipm session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: sessionURL}
	}

	var sess sessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return nil, fmt.Errorf("anipm session decode: %w", err)
	}
	if strings.TrimSpace(sess.EmbedURL) == "" {
		return nil, fmt.Errorf("anipm: empty embed url: %w", provider.ErrNoSources)
	}

	// 3. Resolve playback embed session
	parsedEmbed, err := url.Parse(sess.EmbedURL)
	if err != nil {
		return nil, fmt.Errorf("anipm parse embed url: %w", err)
	}
	tParam := parsedEmbed.Query().Get("t")
	if tParam == "" {
		return nil, fmt.Errorf("anipm: missing t param in embed url: %w", provider.ErrNoSources)
	}

	embedSessionURL := fmt.Sprintf("%s/api/embed/session?t=%s", c.embedBase, url.QueryEscape(tParam))
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, embedSessionURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", sess.EmbedURL)
	req.Header.Set("Accept", "application/json")

	resp, err = c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anipm embed session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: embedSessionURL}
	}

	var pb playbackSession
	if err := json.NewDecoder(resp.Body).Decode(&pb); err != nil {
		return nil, fmt.Errorf("anipm embed decode: %w", err)
	}
	if strings.TrimSpace(pb.Source) == "" {
		return nil, fmt.Errorf("anipm: empty stream source: %w", provider.ErrNoSources)
	}

	// 4. Map subtitles
	var subtitles []provider.SubtitleOption
	for _, sub := range pb.Subtitles {
		u := strings.TrimSpace(sub.URL)
		if u == "" {
			continue
		}
		subLang := lang.Normalize(sub.SrcLang)
		if subLang == "" {
			subLang = lang.Normalize(sub.Label)
		}
		if subLang == "" {
			subLang = "und"
		}
		subtitles = append(subtitles, provider.SubtitleOption{
			URL:      u,
			Language: subLang,
			Referer:  config.AniPMMediaReferer,
			Default:  sub.Default,
		})
	}

	audioCode := "ja"
	if strings.EqualFold(channel, "dub") {
		audioCode = "en"
	}
	if pb.AudioLang != "" {
		if norm := lang.Normalize(pb.AudioLang); norm != "" {
			audioCode = norm
		}
	}

	source := provider.MediaSource{
		URL:       pb.Source,
		Quality:   "1080p (Ani.pm)",
		Type:      provider.SourceTypeHLS,
		Referer:   config.AniPMMediaReferer,
		UserAgent: config.DesktopUserAgent,
		Language:  audioCode,
		Subtitles: subtitles,
	}

	logging.Debug("anipm resolved stream", "title", pb.Title, "audio", audioCode, "subs", len(subtitles))
	return []provider.MediaSource{source}, nil
}

func parseAniListID(val any) int {
	if val == nil {
		return 0
	}
	switch v := val.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

var (
	_ provider.Provider                  = (*Client)(nil)
	_ provider.Presenter                 = (*Client)(nil)
	_ provider.FeatureSource             = (*Client)(nil)
	_ provider.MovieEpisodeFlow          = (*Client)(nil)
	_ provider.EpisodeAvailabilitySource = (*Client)(nil)
)
