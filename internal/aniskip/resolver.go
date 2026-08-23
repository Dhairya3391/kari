package aniskip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"kari/internal/config"
)

type graphqlQuery struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

type graphqlResponse struct {
	Data struct {
		Media struct {
			IDMal int `json:"idMal"`
		} `json:"Media"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// GetMALID resolves an anime series title to its MyAnimeList ID via the
// AniList GraphQL search endpoint, so aniskip lookups can run on titles
// providers already know.
func (c *Client) GetMALID(ctx context.Context, title string) (int, error) {
	query := `
	query ($search: String) {
		Media (search: $search, type: ANIME) {
			idMal
		}
	}
	`

	reqBody := graphqlQuery{
		Query: query,
		Variables: map[string]interface{}{
			"search": title,
		},
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return 0, fmt.Errorf("anilist marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.AniListAPIBase, bytes.NewBuffer(data))
	if err != nil {
		return 0, fmt.Errorf("anilist request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("anilist fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("anilist api returned status: %d", resp.StatusCode)
	}

	var resData graphqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&resData); err != nil {
		return 0, fmt.Errorf("anilist decode: %w", err)
	}

	if len(resData.Errors) > 0 {
		return 0, fmt.Errorf("anilist graphql error: %s", resData.Errors[0].Message)
	}

	if resData.Data.Media.IDMal == 0 {
		return 0, fmt.Errorf("mal id not found for title: %s", title)
	}

	return resData.Data.Media.IDMal, nil
}
