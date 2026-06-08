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
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u.String()}
	}

	var sr searchResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
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
	return results, nil
}

func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	logging.Debugf("miruro fetch episodes mediaID=%q", mediaID)
	u := fmt.Sprintf("%s/episodes/%s", apiURL, mediaID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	var er []episodeResp
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
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

	logging.Debugf("miruro fetch episodes done count=%d", len(eps))
	return eps, nil
}

func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debugf("miruro resolve source mediaID=%q episodeID=%q", mediaID, episode.ID)
	u, err := url.Parse(apiURL + "/link")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("id", episode.ID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
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
		return nil, err
	}

	var lr linkResp
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, err
	}

	subtitleURLs := make([]string, 0, len(lr.Subtitles))
	for _, sub := range lr.Subtitles {
		if sub.File != "" {
			subtitleURLs = append(subtitleURLs, sub.File)
		}
	}

	streams := append([]linkStream(nil), lr.Streams...)
	sort.SliceStable(streams, func(i, j int) bool {
		score := func(idx int) int {
			s := streams[idx]
			server := strings.ToLower(cleanMiruroText(s.Server))
			if server == "yt-mp4" {
				return 100
			}
			if strings.Contains(strings.ToLower(cleanMiruroText(s.Quality)), "1080") {
				return 90
			}
			if strings.EqualFold(cleanMiruroText(s.Type), "mp4") {
				return 80
			}
			if server == "fm-hls" {
				return 70
			}
			if s.Default {
				return 1
			}
			return 0
		}
		si := score(i)
		sj := score(j)
		if si != sj {
			return si > sj
		}
		if streams[i].Default != streams[j].Default {
			return streams[i].Default
		}
		if streams[i].Priority != streams[j].Priority {
			return streams[i].Priority < streams[j].Priority
		}
		return miruroStreamKey(streams[i]) < miruroStreamKey(streams[j])
	})

	seen := make(map[string]struct{}, len(streams))
	sources := make([]provider.MediaSource, 0, len(streams))
	for i, raw := range streams {
		s := normalizeMiruroStream(raw)
		key := miruroStreamKey(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		referer := s.Referer
		if referer == "" {
			referer = config.MiruroOrigin
		}

		sources = append(sources, provider.MediaSource{
			URL:       s.URL,
			Quality:   fmt.Sprintf("[MIRURO] Source %d", i+1),
			Referer:   referer,
			Type:      s.Type,
			UserAgent: config.DesktopUserAgent, // Use desktop UA for better compatibility
			Subtitles: subtitleURLs,
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
	return s
}

func miruroStreamKey(s linkStream) string {
	return strings.Join([]string{
		cleanMiruroText(s.URL),
		strings.ToLower(cleanMiruroText(s.Server)),
		strings.ToLower(cleanMiruroText(s.Type)),
		strings.ToLower(cleanMiruroText(s.Quality)),
		cleanMiruroText(s.Referer),
	}, "|")
}

func cleanMiruroText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

var _ provider.Provider = (*Client)(nil)
