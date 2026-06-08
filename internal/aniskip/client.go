package aniskip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type SkipTimes struct {
	OpStart float64
	OpEnd   float64
	EdStart float64
	EdEnd   float64
}

type Client struct {
	http *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	return &Client{http: httpClient}
}

type skipResponse struct {
	Found   bool `json:"found"`
	Results []struct {
		Interval struct {
			StartTime float64 `json:"start_time"`
			EndTime   float64 `json:"end_time"`
		} `json:"interval"`
		SkipType string `json:"skip_type"`
	} `json:"results"`
}

func (c *Client) GetSkipTimes(ctx context.Context, malID int, episode int) (*SkipTimes, error) {
	url := fmt.Sprintf("https://api.aniskip.com/v1/skip-times/%d/%d?types=op&types=ed", malID, episode)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("aniskip request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aniskip fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil // No skip times found, this is fine
		}
		return nil, fmt.Errorf("aniskip api returned status: %d", resp.StatusCode)
	}

	var data skipResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("aniskip decode: %w", err)
	}

	if !data.Found {
		return nil, nil
	}

	times := &SkipTimes{
		OpStart: -1, OpEnd: -1,
		EdStart: -1, EdEnd: -1,
	}

	for _, result := range data.Results {
		switch result.SkipType {
		case "op":
			times.OpStart = result.Interval.StartTime
			times.OpEnd = result.Interval.EndTime
		case "ed":
			times.EdStart = result.Interval.StartTime
			times.EdEnd = result.Interval.EndTime
		}
	}

	return times, nil
}
