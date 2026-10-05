package skipdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
)

// SkipTimes holds interval boundaries (in seconds) for each skippable zone.
// A value of -1 indicates the zone is absent for this episode or movie.
type SkipTimes struct {
	OpStart      float64
	OpEnd        float64
	EdStart      float64
	EdEnd        float64
	RecapStart   float64
	RecapEnd     float64
	PreviewStart float64
	PreviewEnd   float64
}

// Client queries the SkipDB open API (https://skipdb.tv) for crowdsourced
// intro, recap, outro, and preview timestamps by IMDb ID.
type Client struct {
	http    *http.Client
	apiKey  string
	apiBase string
}

// NewClient constructs a SkipDB client with the given HTTP client and API key.
// Passing empty strings falls back to the bundled defaults.
func NewClient(httpClient *http.Client, apiKey string) *Client {
	if httpClient == nil {
		httpClient = httpclient.NewWithTimeout(10 * time.Second)
	}
	if strings.TrimSpace(apiKey) == "" {
		apiKey = config.DefaultSkipDBAPIKey
	}
	return &Client{
		http:    httpClient,
		apiKey:  strings.TrimSpace(apiKey),
		apiBase: config.SkipDBAPIBase,
	}
}

type segmentData struct {
	StartMS    float64 `json:"start_ms"`
	EndMS      float64 `json:"end_ms"`
	Match      string  `json:"match"`
	Adjusted   bool    `json:"adjusted"`
	Confidence float64 `json:"confidence"`
}

type segmentsResponse struct {
	IMDbID   string `json:"imdb_id"`
	Season   *int   `json:"season"`
	Episode  *int   `json:"episode"`
	Segments struct {
		Intro   *segmentData `json:"intro"`
		Recap   *segmentData `json:"recap"`
		Outro   *segmentData `json:"outro"`
		Preview *segmentData `json:"preview"`
	} `json:"segments"`
	IntroLengthEstimateMS *float64 `json:"intro_length_estimate_ms"`
}

func parseSegment(s *segmentData) (float64, float64) {
	if s == nil {
		return -1, -1
	}
	// Sentinels: (0,0) explicitly denotes confirmed absent in SkipDB specification.
	if s.StartMS == 0 && s.EndMS == 0 {
		return -1, -1
	}
	if s.EndMS <= s.StartMS {
		return -1, -1
	}
	return s.StartMS / 1000.0, s.EndMS / 1000.0
}

// GetSegments queries SkipDB for segment intervals by IMDb ID.
// For movies, season and episode query parameters are omitted.
func (c *Client) GetSegments(ctx context.Context, imdbID string, season, episode int, isMovie bool) (*SkipTimes, error) {
	cleanIMDb := strings.TrimSpace(imdbID)
	if cleanIMDb == "" {
		return nil, nil
	}

	params := url.Values{}
	params.Set("imdb_id", cleanIMDb)
	if !isMovie && season > 0 && episode > 0 {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(episode))
	}

	endpoint := fmt.Sprintf("%s/api/segments?%s", strings.TrimRight(c.apiBase, "/"), params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("skipdb request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("skipdb fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("skipdb api status: %d", resp.StatusCode)
	}

	var data segmentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("skipdb decode: %w", err)
	}

	times := &SkipTimes{
		OpStart:      -1,
		OpEnd:        -1,
		EdStart:      -1,
		EdEnd:        -1,
		RecapStart:   -1,
		RecapEnd:     -1,
		PreviewStart: -1,
		PreviewEnd:   -1,
	}

	times.OpStart, times.OpEnd = parseSegment(data.Segments.Intro)
	times.RecapStart, times.RecapEnd = parseSegment(data.Segments.Recap)
	times.EdStart, times.EdEnd = parseSegment(data.Segments.Outro)
	times.PreviewStart, times.PreviewEnd = parseSegment(data.Segments.Preview)

	if times.OpStart < 0 && times.EdStart < 0 && times.RecapStart < 0 && times.PreviewStart < 0 {
		return nil, nil
	}

	return times, nil
}
