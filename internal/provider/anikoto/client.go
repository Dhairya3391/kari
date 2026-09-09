package anikoto

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
)

// Client implements provider.Provider against the Anikoto streaming API.
type Client struct {
	http    *http.Client
	baseURL string
}

// Alias implements provider.Presenter, providing the human-readable display name.
func (c *Client) Alias() string { return "Anikoto" }

// Name implements provider.Provider, returning the stable registry identifier.
func (c *Client) Name() string {
	return "anikoto"
}

// Modes implements provider.Provider, registering Anikoto as the primary anime provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 1},
	}
}

// RequiresEpisodeListForMovies implements provider.MovieEpisodeFlow. Anikoto
// resolves playback via per-episode route IDs (e.g. watch/anikoto/{id}/sub/1)
// that only the episode listing endpoint produces, so anime movies must query
// FetchEpisodes first.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Features implements provider.FeatureSource. Anime media supports separate sub/dub audio tracks.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeAnime {
		return provider.Features{}
	}
	return provider.Features{AudioSelection: true}
}

// NewClient constructs the Anikoto provider with the shared HTTP client.
func NewClient() (*Client, error) {
	return NewClientWithBaseURL(config.AnikotoAPIBase)
}

// NewClientWithBaseURL constructs the Anikoto provider against a custom base URL (useful in tests).
func NewClientWithBaseURL(baseURL string) (*Client, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		http:    httpclient.New(),
		baseURL: baseURL,
	}, nil
}

// Search queries Anikoto's AniList-backed search index. Movies report MediaTypeMovie
// so the TUI renders movie badges while still routing through the episode flow.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	u, err := url.Parse(c.baseURL + "/search")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("anikoto search: build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anikoto search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u.String()}
	}

	var sr searchResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("anikoto search: decode response: %w", err)
	}
	results := make([]provider.SearchResult, 0, len(sr.Results))
	for _, r := range sr.Results {
		mediaType := provider.MediaTypeAnime
		if strings.EqualFold(r.Format, "MOVIE") {
			mediaType = provider.MediaTypeMovie
		}
		idStr := r.ID.String()
		if idStr == "" {
			continue
		}
		yearStr := ""
		if r.Year > 0 {
			yearStr = fmt.Sprintf("%d", r.Year)
		}
		results = append(results, provider.SearchResult{
			Title:     r.Name,
			ID:        idStr,
			Type:      provider.ModeAnime,
			Year:      yearStr,
			MediaType: mediaType,
		})
	}
	logging.Debug("search done", "provider", c.Name(), "results", len(results))
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

// FetchEpisodes lists episodes for an AniList media ID; fractional or
// zero-numbered entries are skipped.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	logging.Debug("fetch episodes", "provider", c.Name(), "mediaID", mediaID)
	u := fmt.Sprintf("%s/episodes/%s", c.baseURL, mediaID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("anikoto episodes: build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anikoto episodes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	var er []episodeResp
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("anikoto episodes: decode response: %w", err)
	}
	eps := make([]provider.Episode, 0, len(er))
	for _, e := range er {
		if e.Number <= 0 || math.Trunc(e.Number) != e.Number {
			continue
		}
		eps = append(eps, provider.Episode{
			Title:   e.Title,
			ID:      e.ID,
			Episode: int(e.Number),
			Season:  1,
			Audio:   strings.ToLower(e.Category),
			Filler:  e.Filler,
		})
	}

	if len(eps) == 0 {
		return nil, provider.ErrNoEpisodes
	}

	sort.Slice(eps, func(i, j int) bool {
		if eps[i].Season != eps[j].Season {
			return eps[i].Season < eps[j].Season
		}
		return eps[i].Episode < eps[j].Episode
	})

	logging.Debug("fetch episodes done", "provider", c.Name(), "count", len(eps))
	return eps, nil
}

// ResolveSource resolves one episode ID to ranked stream sources,
// extracting referer/UA from headers and mpv args and attaching subtitles.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episodeID", episode.ID)
	u, err := url.Parse(c.baseURL + "/link")
	if err != nil {
		return nil, fmt.Errorf("anikoto resolve: build url: %w", err)
	}
	q := u.Query()
	q.Set("id", episode.ID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("anikoto resolve: build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anikoto resolve: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusBadRequest:
			return nil, fmt.Errorf("invalid episode ID format")
		case http.StatusNotFound:
			return nil, provider.ErrNotFound
		case http.StatusServiceUnavailable:
			return nil, fmt.Errorf("streaming provider is temporarily down")
		default:
			return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u.String()}
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anikoto resolve: read body: %w", err)
	}

	var lr linkResp
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("anikoto resolve: decode response: %w", err)
	}

	subtitleOptions := make([]provider.SubtitleOption, 0, len(lr.Subtitles))
	seenSubs := make(map[string]struct{}, len(lr.Subtitles))
	for _, sub := range lr.Subtitles {
		file := strings.TrimSpace(sub.File)
		if file == "" {
			continue
		}
		if _, ok := seenSubs[file]; ok {
			continue
		}
		seenSubs[file] = struct{}{}
		lang := strings.TrimSpace(sub.Language)
		if lang == "" {
			lang = strings.TrimSpace(sub.Label)
		}
		if lang == "" {
			lang = "en"
		}
		subtitleOptions = append(subtitleOptions, provider.SubtitleOption{
			URL:      file,
			Language: lang,
		})
	}

	streams := append([]linkStream(nil), lr.Streams...)
	sort.SliceStable(streams, func(i, j int) bool {
		if streams[i].Priority != streams[j].Priority {
			return streams[i].Priority < streams[j].Priority
		}
		if streams[i].Verified != streams[j].Verified {
			return streams[i].Verified
		}
		score := func(s linkStream) int {
			sc := 0
			q := strings.ToLower(cleanAnikotoText(s.Quality))
			t := strings.ToLower(cleanAnikotoText(s.Type))

			if strings.Contains(q, "1080") {
				sc += 100
			} else if strings.Contains(q, "720") {
				sc += 70
			} else if strings.Contains(q, "480") {
				sc += 40
			} else if strings.Contains(q, "360") {
				sc += 20
			} else if t == provider.SourceTypeHLS || strings.Contains(q, "auto") {
				sc += 80
			} else if t == "mp4" {
				sc += 50
			}

			if s.Default {
				sc += 5
			}
			return sc
		}
		si := score(streams[i])
		sj := score(streams[j])
		if si != sj {
			return si > sj
		}
		if streams[i].Default != streams[j].Default {
			return streams[i].Default
		}
		return anikotoStreamKey(streams[i]) < anikotoStreamKey(streams[j])
	})

	seen := make(map[string]struct{}, len(streams))
	sources := make([]provider.MediaSource, 0, len(streams))
	for _, raw := range streams {
		s := normalizeAnikotoStream(raw)
		key := anikotoStreamKey(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		referer := s.Referer
		if referer == "" {
			if s.Headers != nil && s.Headers["Referer"] != "" {
				referer = s.Headers["Referer"]
			} else if s.HTTPHeaders != nil && s.HTTPHeaders["Referer"] != "" {
				referer = s.HTTPHeaders["Referer"]
			}
		}

		userAgent := ""
		if s.Headers != nil && s.Headers["User-Agent"] != "" {
			userAgent = s.Headers["User-Agent"]
		} else if s.HTTPHeaders != nil && s.HTTPHeaders["User-Agent"] != "" {
			userAgent = s.HTTPHeaders["User-Agent"]
		}

		var extraArgs []string
		if s.MPV != nil {
			for _, arg := range s.MPV.Args {
				arg = strings.TrimSpace(arg)
				if strings.HasPrefix(arg, "--referrer=") {
					if referer == "" {
						referer = strings.TrimPrefix(arg, "--referrer=")
					}
				} else if strings.HasPrefix(arg, "--user-agent=") {
					if userAgent == "" {
						userAgent = strings.TrimPrefix(arg, "--user-agent=")
					}
				} else if arg != "" {
					extraArgs = append(extraArgs, arg)
				}
			}
		}

		if referer == "" {
			referer = config.AnikotoReferer
		}
		if userAgent == "" {
			userAgent = config.DesktopUserAgent
		}

		quality := s.Quality
		if quality == "" || strings.EqualFold(quality, "auto") {
			if strings.EqualFold(s.Type, provider.SourceTypeHLS) || strings.EqualFold(quality, "auto") {
				quality = "Auto"
			} else if strings.EqualFold(s.Type, "embed") {
				quality = "Embed"
			} else {
				quality = "Direct"
			}
		}
		serverOrProvider := s.Server
		if serverOrProvider == "" {
			serverOrProvider = s.Provider
		}
		if serverOrProvider != "" {
			quality = fmt.Sprintf("%s (%s)", quality, serverOrProvider)
		}

		sources = append(sources, provider.MediaSource{
			URL:       s.URL,
			Quality:   quality,
			Referer:   referer,
			Type:      s.Type,
			UserAgent: userAgent,
			ExtraArgs: extraArgs,
			Subtitles: subtitleOptions,
		})
	}
	logging.Debug("resolve source done", "provider", c.Name(), "count", len(sources))
	return sources, nil
}

func normalizeAnikotoStream(s linkStream) linkStream {
	s.URL = cleanAnikotoText(s.URL)
	s.Type = cleanAnikotoText(s.Type)
	s.Quality = cleanAnikotoText(s.Quality)
	s.Referer = cleanAnikotoText(s.Referer)
	s.Server = cleanAnikotoText(s.Server)
	s.Provider = cleanAnikotoText(s.Provider)
	return s
}

func anikotoStreamKey(s linkStream) string {
	return strings.Join([]string{
		cleanAnikotoText(s.URL),
		strings.ToLower(cleanAnikotoText(s.Server)),
		strings.ToLower(cleanAnikotoText(s.Provider)),
		strings.ToLower(cleanAnikotoText(s.Type)),
		strings.ToLower(cleanAnikotoText(s.Quality)),
		cleanAnikotoText(s.Referer),
	}, "|")
}

func cleanAnikotoText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

var (
	_ provider.Provider         = (*Client)(nil)
	_ provider.Presenter        = (*Client)(nil)
	_ provider.FeatureSource    = (*Client)(nil)
	_ provider.MovieEpisodeFlow = (*Client)(nil)
)
