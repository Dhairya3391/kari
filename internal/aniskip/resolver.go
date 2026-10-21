package aniskip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"kari/internal/config"
)

type graphqlQuery struct {
	Query     string           `json:"query"`
	Variables graphqlVariables `json:"variables"`
}

type graphqlVariables struct {
	Search string `json:"search,omitempty"`
	ID     int    `json:"id,omitempty"`
}

type graphqlResponse struct {
	Data struct {
		Media struct {
			ID    int `json:"id"`
			IDMal int `json:"idMal"`
			Title struct {
				English string `json:"english"`
				Romaji  string `json:"romaji"`
				Native  string `json:"native"`
			} `json:"title"`
		} `json:"Media"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// GetIDs resolves an anime series title to its AniList ID and MyAnimeList ID
// in one request. Either may be zero if the API does not return it.
func (c *Client) GetIDs(ctx context.Context, title string) (anilistID int, malID int, err error) {
	query := `
	query ($search: String) {
		Media (search: $search, type: ANIME) {
			id
			idMal
			title { english romaji native }
		}
	}
	`
	resData, err := c.queryIDs(ctx, query, graphqlVariables{Search: title})
	if err != nil {
		return 0, 0, err
	}
	cleanTitle := normalizeTitle(title)
	if cleanTitle == "" || (normalizeTitle(resData.Data.Media.Title.English) != cleanTitle &&
		normalizeTitle(resData.Data.Media.Title.Romaji) != cleanTitle &&
		normalizeTitle(resData.Data.Media.Title.Native) != cleanTitle) {
		return 0, 0, nil
	}
	return resData.Data.Media.ID, resData.Data.Media.IDMal, nil
}

// GetIDsByAniListID resolves the MyAnimeList ID for an exact AniList ID.
func (c *Client) GetIDsByAniListID(ctx context.Context, anilistID int) (int, int, error) {
	if anilistID <= 0 {
		return 0, 0, nil
	}
	query := `
	query ($id: Int) {
		Media (id: $id, type: ANIME) {
			id
			idMal
		}
	}
	`
	resData, err := c.queryIDs(ctx, query, graphqlVariables{ID: anilistID})
	if err != nil {
		return 0, 0, err
	}
	return resData.Data.Media.ID, resData.Data.Media.IDMal, nil
}

func (c *Client) queryIDs(ctx context.Context, query string, variables graphqlVariables) (graphqlResponse, error) {
	var resData graphqlResponse
	reqBody := graphqlQuery{Query: query, Variables: variables}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return resData, fmt.Errorf("anilist marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.AniListAPIBase, bytes.NewBuffer(data))
	if err != nil {
		return resData, fmt.Errorf("anilist request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://anilist.co")
	req.Header.Set("Referer", "https://anilist.co/")
	req.Header.Set("User-Agent", config.DesktopUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return resData, fmt.Errorf("anilist fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return resData, fmt.Errorf("anilist api returned status: %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(&resData); err != nil {
		return resData, fmt.Errorf("anilist decode: %w", err)
	}

	if len(resData.Errors) > 0 {
		return resData, fmt.Errorf("anilist graphql error: %s", resData.Errors[0].Message)
	}

	return resData, nil
}

func normalizeTitle(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(title)), " "))
}

// GetMALID resolves an anime series title to its MyAnimeList ID. It wraps
// GetIDs for callers that only need the MAL ID.
func (c *Client) GetMALID(ctx context.Context, title string) (int, error) {
	_, malID, err := c.GetIDs(ctx, title)
	if err != nil {
		return 0, err
	}
	if malID == 0 {
		return 0, fmt.Errorf("mal id not found for title: %s", title)
	}
	return malID, nil
}
