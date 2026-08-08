package vidking

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
	vidKingAPI     = config.VidKingAPIBase
	vidKingReferer = config.VidKingReferer
	vidKingUA      = config.DesktopUserAgent

	// preferredVidKingCDN is the host substring treated as the preferred CDN
	// for each quality level. Prefer the direct MP4 (mbph) over the HLS
	// playlist (moon): the MP4 is a single byte-range-requestable file with
	// stable playback and instant seeking, while HLS fetches per segment (on a
	// different CDN) and is prone to transient per-segment failures. Qualities
	// only served by the other CDN (e.g. 4K only on moon) are still kept.
	preferredVidKingCDN = "mbph"
)

type Client struct {
	base       *streambase.Base
	httpClient *http.Client
}

type vidKingSourceItem struct {
	URL     string `json:"url"`
	Quality string `json:"quality"`
}

type VidKingSubtitle struct {
	URL      string `json:"url"`
	Language string `json:"lang"`
	Name     string `json:"name"`
}

type vidKingResponse struct {
	Sources   []vidKingSourceItem `json:"sources"`
	Subtitles []VidKingSubtitle   `json:"subtitles"`
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
	resp, err := c.FetchVidKingSources(ctx, tmdbID, mediaType, episode.Season, episode.Episode)
	if err != nil {
		return nil, err
	}
	if len(resp.Sources) == 0 {
		return nil, provider.ErrNoSources
	}

	// Deduplicate by quality. The API returns the same rendition from two
	// CDNs (a direct MP4 and an HLS playlist). Pick one source per quality,
	// preferring the direct MP4 CDN and only keeping the other CDN for
	// qualities the preferred CDN lacks, so no quality (e.g. 4K-only HLS) is
	// dropped.
	type chosenQuality struct {
		item vidKingSourceItem
	}
	chosen := make(map[string]*chosenQuality, len(resp.Sources))
	order := make([]string, 0, len(resp.Sources))
	for _, s := range resp.Sources {
		q := s.Quality
		if q == "" {
			q = "unknown"
		}
		cur, exists := chosen[q]
		preferred := strings.Contains(s.URL, preferredVidKingCDN)
		if !exists {
			order = append(order, q)
		} else if !preferred && cur.item.URL != "" {
			continue
		}
		chosen[q] = &chosenQuality{item: s}
	}

	sources := make([]provider.MediaSource, 0, len(chosen))
	for _, q := range order {
		entry := chosen[q]
		ms := provider.MediaSource{
			URL:       entry.item.URL,
			Quality:   fmt.Sprintf("[VIDKING] %s", q),
			Referer:   vidKingReferer,
			UserAgent: vidKingUA,
		}
		for _, sub := range resp.Subtitles {
			ms.Subtitles = append(ms.Subtitles, provider.SubtitleOption{URL: sub.URL, Language: sub.Language})
		}
		sources = append(sources, ms)
	}
	return sources, nil
}

func (c *Client) FetchVidKingSources(ctx context.Context, tmdbID int, mediaType string, season, episode int) (*vidKingResponse, error) {
	logging.Debugf("vidking fetch start tmdbID=%d media_type=%q S%dE%d", tmdbID, mediaType, season, episode)
	mt := "movie"
	if mediaType == "tv" {
		mt = "tv"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, vidKingAPI, nil)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		logging.Warnf("vidking API returned status %d", resp.StatusCode)
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: req.URL.String()}
	}

	var result vidKingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logging.Errorf("vidking parse failure err=%v", err)
		return nil, err
	}
	if len(result.Sources) == 0 {
		logging.Warnf("vidking returned no sources tmdbID=%d", tmdbID)
		return nil, provider.ErrNoSources
	}

	logging.Debugf("vidking fetch success sources=%d subs=%d", len(result.Sources), len(result.Subtitles))
	return &result, nil
}

var _ provider.Provider = (*Client)(nil)
