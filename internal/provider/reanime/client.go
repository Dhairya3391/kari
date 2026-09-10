package reanime

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
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
)

// Client implements provider.Provider against the Re:ANIME streaming API.
type Client struct {
	http    *http.Client
	baseURL string
}

// Alias implements provider.Presenter, providing the human-readable display name.
func (c *Client) Alias() string { return "Re:ANIME" }

// Name implements provider.Provider, returning the stable registry identifier.
func (c *Client) Name() string {
	return "reanime"
}

// Modes implements provider.Provider, registering Re:ANIME as an anime provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 2},
	}
}

// RequiresEpisodeListForMovies implements provider.MovieEpisodeFlow.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Features implements provider.FeatureSource. Anime media supports separate sub/dub audio tracks.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeAnime {
		return provider.Features{}
	}
	return provider.Features{AudioSelection: true}
}

// NewClient constructs the Re:ANIME provider with the shared HTTP client.
func NewClient() (*Client, error) {
	return NewClientWithBaseURL(config.ReAnimeAPIBase)
}

// NewClientWithBaseURL constructs the Re:ANIME provider against a custom base URL.
func NewClientWithBaseURL(baseURL string) (*Client, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		http:    httpclient.New(),
		baseURL: baseURL,
	}, nil
}

// Search queries Re:ANIME's AniList-backed search index.
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
		return nil, fmt.Errorf("reanime search: build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reanime search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u.String()}
	}

	var sr searchResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("reanime search: decode response: %w", err)
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
			yearStr = strconv.Itoa(r.Year)
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

// FetchEpisodes lists episodes for an AniList media ID.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	logging.Debug("fetch episodes", "provider", c.Name(), "mediaID", mediaID)
	u := fmt.Sprintf("%s/episodes/%s", c.baseURL, url.PathEscape(mediaID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("reanime episodes: build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reanime episodes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	var er []episodeItem
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("reanime episodes: decode response: %w", err)
	}

	seenEpisodes := make(map[string]struct{}, len(er))
	eps := make([]provider.Episode, 0, len(er))
	for _, e := range er {
		if e.Number <= 0 || math.Trunc(e.Number) != e.Number {
			continue
		}
		cat := strings.ToLower(e.Category)
		if cat == "" {
			cat = "sub"
		}
		key := fmt.Sprintf("%d:%s", int(e.Number), cat)
		if _, ok := seenEpisodes[key]; ok {
			continue
		}
		seenEpisodes[key] = struct{}{}

		eps = append(eps, provider.Episode{
			Title:   e.Title,
			ID:      e.ID,
			Episode: int(e.Number),
			Season:  1,
			Audio:   cat,
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
		if eps[i].Episode != eps[j].Episode {
			return eps[i].Episode < eps[j].Episode
		}
		if eps[i].Audio != eps[j].Audio {
			return eps[i].Audio == "sub"
		}
		return false
	})

	logging.Debug("fetch episodes done", "provider", c.Name(), "count", len(eps))
	return eps, nil
}

// ResolveSource resolves one episode to direct/HLS FlixCloud streams and subtitles.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episodeID", episode.ID)

	anilistID, category, epNum := parseEpisodeHandle(mediaID, episode)
	if anilistID == "" || epNum <= 0 {
		return nil, fmt.Errorf("reanime resolve: invalid episode parameters")
	}

	servers := []string{"hd1", "hd2"}
	var lastErr error

	for _, srv := range servers {
		u := fmt.Sprintf("%s/watch/%s/%s/%s/%d", c.baseURL, srv, url.PathEscape(anilistID), category, epNum)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("reanime resolve: build request: %w", err)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("reanime resolve: %w", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				lastErr = provider.ErrNotFound
			} else {
				lastErr = &provider.HTTPError{Code: resp.StatusCode, URL: u}
			}
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("reanime resolve: read body: %w", err)
			continue
		}

		var wr watchResp
		if err := json.Unmarshal(body, &wr); err != nil {
			lastErr = fmt.Errorf("reanime resolve: decode response: %w", err)
			continue
		}

		if len(wr.Streams) == 0 {
			continue
		}

		subtitleOptions := make([]provider.SubtitleOption, 0, len(wr.Subtitles))
		seenSubs := make(map[string]struct{}, len(wr.Subtitles))
		for _, sub := range wr.Subtitles {
			file := strings.TrimSpace(sub.URL)
			if file == "" {
				file = strings.TrimSpace(sub.File)
			}
			if file == "" {
				continue
			}
			if strings.EqualFold(sub.Kind, "thumbnails") || strings.EqualFold(sub.Language, "thumbnails") || strings.EqualFold(sub.Lang, "thumbnails") {
				continue
			}
			if _, ok := seenSubs[file]; ok {
				continue
			}
			seenSubs[file] = struct{}{}

			langTag := strings.TrimSpace(sub.Language)
			if langTag == "" {
				langTag = strings.TrimSpace(sub.Lang)
			}
			if langTag == "" {
				langTag = strings.TrimSpace(sub.Label)
			}
			if langTag == "" {
				langTag = "en"
			}
			normLang := lang.Normalize(langTag)

			subtitleOptions = append(subtitleOptions, provider.SubtitleOption{
				URL:      file,
				Language: normLang,
			})
		}

		sources := make([]provider.MediaSource, 0, len(wr.Streams))
		seenStreamURLs := make(map[string]struct{}, len(wr.Streams))

		for _, raw := range wr.Streams {
			streamURL := strings.TrimSpace(raw.URL)
			if streamURL == "" {
				continue
			}
			if _, ok := seenStreamURLs[streamURL]; ok {
				continue
			}
			seenStreamURLs[streamURL] = struct{}{}

			referer := raw.Referer
			if referer == "" {
				if raw.Headers != nil && raw.Headers["Referer"] != "" {
					referer = raw.Headers["Referer"]
				} else if raw.HTTPHeaders != nil && raw.HTTPHeaders["Referer"] != "" {
					referer = raw.HTTPHeaders["Referer"]
				}
			}

			userAgent := ""
			if raw.Headers != nil && raw.Headers["User-Agent"] != "" {
				userAgent = raw.Headers["User-Agent"]
			} else if raw.HTTPHeaders != nil && raw.HTTPHeaders["User-Agent"] != "" {
				userAgent = raw.HTTPHeaders["User-Agent"]
			}

			var extraArgs []string
			if raw.MPV != nil {
				for _, arg := range raw.MPV.Args {
					arg = strings.Trim(strings.TrimSpace(arg), `"`)
					if strings.HasPrefix(arg, "--http-header-fields=") {
						extraArgs = append(extraArgs, arg)
					} else if strings.HasPrefix(arg, "--referrer=") {
						if referer == "" {
							referer = strings.TrimPrefix(arg, "--referrer=")
						}
					} else if strings.HasPrefix(arg, "--user-agent=") {
						if userAgent == "" {
							userAgent = strings.TrimPrefix(arg, "--user-agent=")
						}
					} else if arg != "" && !strings.HasPrefix(arg, "http://") && !strings.HasPrefix(arg, "https://") {
						extraArgs = append(extraArgs, arg)
					}
				}
			}

			if referer == "" {
				referer = config.ReAnimeReferer
			}
			if userAgent == "" {
				userAgent = config.DesktopUserAgent
			}

			quality := cleanReAnimeText(raw.Quality)
			if quality == "" || strings.EqualFold(quality, "auto") {
				quality = "Auto"
			}

			serverOrProvider := raw.Server
			if serverOrProvider == "" {
				serverOrProvider = raw.Provider
			}
			if serverOrProvider != "" {
				quality = fmt.Sprintf("%s (%s)", quality, serverOrProvider)
			}

			streamType := raw.Type
			if streamType == "" {
				streamType = provider.SourceTypeHLS
			}

			sources = append(sources, provider.MediaSource{
				URL:       streamURL,
				Quality:   quality,
				Referer:   referer,
				Type:      streamType,
				UserAgent: userAgent,
				ExtraArgs: extraArgs,
				Subtitles: subtitleOptions,
			})
		}

		if len(sources) > 0 {
			logging.Debug("resolve source done", "provider", c.Name(), "count", len(sources))
			return sources, nil
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, provider.ErrNoSources
}

func parseEpisodeHandle(mediaID string, episode provider.Episode) (anilistID string, category string, epNum int) {
	category = strings.ToLower(strings.TrimSpace(episode.Audio))
	if category == "" {
		category = "sub"
	}
	epNum = episode.Episode
	anilistID = strings.TrimSpace(mediaID)

	if strings.Contains(episode.ID, "/") {
		parts := strings.Split(episode.ID, "/")
		if len(parts) >= 5 && parts[0] == "watch" {
			anilistID = parts[2]
			category = parts[3]
			if n, err := strconv.Atoi(parts[4]); err == nil && n > 0 {
				epNum = n
			}
		} else if len(parts) == 4 && parts[0] == "watch" {
			anilistID = parts[1]
			category = parts[2]
			if n, err := strconv.Atoi(parts[3]); err == nil && n > 0 {
				epNum = n
			}
		} else if len(parts) == 3 {
			anilistID = parts[0]
			category = parts[1]
			if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 {
				epNum = n
			}
		}
	}
	return anilistID, category, epNum
}

func cleanReAnimeText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
