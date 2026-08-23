package miruro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
)

const (
	apiURL = config.MiruroAPIBase
)

type Client struct {
	http *http.Client
}

func (c *Client) Name() string {
	return "miruro"
}

func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 1},
	}
}

func NewClient() (*Client, error) {
	return &Client{
		http: httpclient.New(),
	}, nil
}

func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debugf("miruro search start query=%q", query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	u, err := url.Parse(apiURL + "/search")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("miruro search: build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("miruro search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u.String()}
	}

	var sr searchResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("miruro search: decode response: %w", err)
	}
	results := make([]provider.SearchResult, 0, len(sr.Results))
	for _, r := range sr.Results {
		mediaType := "anime"
		if strings.EqualFold(r.Format, "MOVIE") {
			mediaType = "movie"
		}
		results = append(results, provider.SearchResult{
			Title:     r.Name,
			ID:        strconv.Itoa(r.ID),
			Type:      provider.ModeAnime,
			Year:      strconv.Itoa(r.Year),
			MediaType: mediaType,
		})
	}
	logging.Debugf("miruro search done results=%d", len(results))
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	logging.Debugf("miruro fetch episodes mediaID=%q", mediaID)
	u := fmt.Sprintf("%s/episodes/%s", apiURL, mediaID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("miruro episodes: build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("miruro episodes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	var er []episodeResp
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("miruro episodes: decode response: %w", err)
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

	logging.Debugf("miruro fetch episodes done count=%d", len(eps))
	return eps, nil
}

func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debugf("miruro resolve source mediaID=%q episodeID=%q", mediaID, episode.ID)
	u, err := url.Parse(apiURL + "/link")
	if err != nil {
		return nil, fmt.Errorf("miruro resolve: build url: %w", err)
	}
	q := u.Query()
	q.Set("id", episode.ID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("miruro resolve: build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("miruro resolve: %w", err)
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
		return nil, fmt.Errorf("miruro resolve: read body: %w", err)
	}

	var lr linkResp
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("miruro resolve: decode response: %w", err)
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
			q := strings.ToLower(cleanMiruroText(s.Quality))
			t := strings.ToLower(cleanMiruroText(s.Type))

			if strings.Contains(q, "1080") {
				sc += 100
			} else if strings.Contains(q, "720") {
				sc += 70
			} else if strings.Contains(q, "480") {
				sc += 40
			} else if strings.Contains(q, "360") {
				sc += 20
			} else if t == "hls" || strings.Contains(q, "auto") {
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
		return miruroStreamKey(streams[i]) < miruroStreamKey(streams[j])
	})

	seen := make(map[string]struct{}, len(streams))
	sources := make([]provider.MediaSource, 0, len(streams))
	for _, raw := range streams {
		s := normalizeMiruroStream(raw)
		key := miruroStreamKey(s)
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
			referer = config.MiruroOrigin
		}
		if userAgent == "" {
			userAgent = config.DesktopUserAgent
		}

		quality := s.Quality
		if quality == "" || strings.EqualFold(quality, "auto") {
			if strings.EqualFold(s.Type, "hls") || strings.EqualFold(quality, "auto") {
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
			Quality:   fmt.Sprintf("[MIRURO] %s", quality),
			Referer:   referer,
			Type:      s.Type,
			UserAgent: userAgent,
			ExtraArgs: extraArgs,
			Subtitles: subtitleOptions,
		})
	}
	logging.Debugf("miruro resolve source done count=%d", len(sources))
	return sources, nil
}

func normalizeMiruroStream(s linkStream) linkStream {
	s.URL = cleanMiruroText(s.URL)
	s.Type = cleanMiruroText(s.Type)
	s.Quality = cleanMiruroText(s.Quality)
	s.Referer = cleanMiruroText(s.Referer)
	s.Server = cleanMiruroText(s.Server)
	s.Provider = cleanMiruroText(s.Provider)
	return s
}

func miruroStreamKey(s linkStream) string {
	return strings.Join([]string{
		cleanMiruroText(s.URL),
		strings.ToLower(cleanMiruroText(s.Server)),
		strings.ToLower(cleanMiruroText(s.Provider)),
		strings.ToLower(cleanMiruroText(s.Type)),
		strings.ToLower(cleanMiruroText(s.Quality)),
		cleanMiruroText(s.Referer),
	}, "|")
}
func cleanMiruroText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}


var _ provider.Provider = (*Client)(nil)
