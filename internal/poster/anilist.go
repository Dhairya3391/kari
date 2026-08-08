package poster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"kari/internal/config"
)

type anilistMedia struct {
	CoverImage struct {
		Large  string `json:"large"`
		Medium string `json:"medium"`
	} `json:"coverImage"`
	Description  string   `json:"description"`
	Genres       []string `json:"genres"`
	AverageScore int      `json:"averageScore"`
}

// fetchAnilistMedia looks a title up on AniList's public GraphQL API. Unlike
// internal/scrobble's AniList client, this call is anonymous (no OAuth
// token) so posters and descriptions work even for users who never linked
// AniList. Cover art and the description/genres shown on screen are both
// derived from this one query.
func (c *Client) fetchAnilistMedia(ctx context.Context, title string) (anilistMedia, error) {
	if title == "" {
		return anilistMedia{}, fmt.Errorf("poster: anilist lookup requires a title")
	}

	query := `
	query ($search: String) {
		Media (search: $search, type: ANIME) {
			coverImage {
				large
				medium
			}
			description(asHtml: false)
			genres
			averageScore
		}
	}
	`
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"search": title},
	})
	if err != nil {
		return anilistMedia{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.AniListAPIBase, bytes.NewReader(body))
	if err != nil {
		return anilistMedia{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return anilistMedia{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return anilistMedia{}, fmt.Errorf("poster: anilist status %d", resp.StatusCode)
	}

	var res struct {
		Data struct {
			Media anilistMedia `json:"Media"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return anilistMedia{}, err
	}
	return res.Data.Media, nil
}

func (c *Client) anilistPosterURL(ctx context.Context, title string) (string, error) {
	media, err := c.fetchAnilistMedia(ctx, title)
	if err != nil {
		return "", err
	}
	if media.CoverImage.Large != "" {
		return media.CoverImage.Large, nil
	}
	return media.CoverImage.Medium, nil
}

func (c *Client) anilistDetailsInfo(ctx context.Context, title string) (Details, error) {
	media, err := c.fetchAnilistMedia(ctx, title)
	if err != nil {
		return Details{}, err
	}
	rating := ""
	if media.AverageScore > 0 {
		rating = fmt.Sprintf("★ %d%%", media.AverageScore)
	}
	return Details{Overview: cleanAnilistDescription(media.Description), Genres: media.Genres, Rating: rating}, nil
}

var htmlTagRE = regexp.MustCompile(`<[^>]*>`)

// cleanAnilistDescription strips the light HTML AniList still sometimes
// includes even with asHtml:false (e.g. <br>, <i> around source notes) so it
// reads as plain text in the terminal.
func cleanAnilistDescription(s string) string {
	s = htmlTagRE.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}
