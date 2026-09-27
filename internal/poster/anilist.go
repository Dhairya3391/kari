package poster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"kari/internal/config"
)

type anilistMedia struct {
	CoverImage struct {
		ExtraLarge string `json:"extraLarge"`
		Large      string `json:"large"`
		Medium     string `json:"medium"`
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
	title = strings.TrimSpace(title)
	if title == "" {
		return anilistMedia{}, fmt.Errorf("poster: anilist lookup requires a title")
	}

	query := `
	query ($id: Int, $search: String) {
		Media (id: $id, search: $search, type: ANIME) {
			coverImage {
				extraLarge
				large
				medium
			}
			description(asHtml: false)
			genres
			averageScore
		}
	}
	`
	vars := map[string]any{}
	if id, err := strconv.Atoi(title); err == nil && id > 0 {
		vars["id"] = id
	} else {
		vars["search"] = title
	}

	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": vars,
	})
	if err != nil {
		return anilistMedia{}, err
	}

	endpoint := config.AniListAPIBase
	if c.anilistURL != "" {
		endpoint = c.anilistURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return anilistMedia{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://anilist.co")
	req.Header.Set("Referer", "https://anilist.co/")
	req.Header.Set("User-Agent", config.DesktopUserAgent)

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
	// Largest first: pixel-level terminals (kitty/sixel) show the
	// difference; the disk/memory caches make the bigger download once.
	if media.CoverImage.ExtraLarge != "" {
		return media.CoverImage.ExtraLarge, nil
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
		rating = fmt.Sprintf("%d%%", media.AverageScore)
	}
	return Details{Overview: cleanAnilistDescription(media.Description), Genres: media.Genres, Rating: rating}, nil
}

var htmlTagRE = regexp.MustCompile(`<[^>]*>`)

// episodeTitleRE splits AniList streaming episode titles of the form
// "Episode 12 - The Real Title" into number and title. Entries without
// a title part (bare "Episode 12") carry no information and are skipped.
var episodeTitleRE = regexp.MustCompile(`(?i)^\s*ep(?:isode)?s?\.?\s*(\d+)\s*[-:–—]\s*(.+?)\s*$`)

// FetchEpisodeTitles returns real episode titles for an anime by AniList
// ID, keyed by episode number. Some streaming providers only send
// placeholders ("Episode 1"); AniList's streamingEpisodes carry the
// descriptive part ("Episode 1 - The Journey's End"). Only entries with
// an actual title are returned — the caller decides which rows to patch.
func (c *Client) FetchEpisodeTitles(ctx context.Context, anilistID int) (map[int]string, error) {
	if anilistID <= 0 {
		return nil, fmt.Errorf("poster: episode titles require an anilist id")
	}

	query := `
	query ($id: Int) {
		Media (id: $id, type: ANIME) {
			streamingEpisodes {
				title
			}
		}
	}
	`
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"id": anilistID},
	})
	if err != nil {
		return nil, err
	}

	endpoint := config.AniListAPIBase
	if c.anilistURL != "" {
		endpoint = c.anilistURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://anilist.co")
	req.Header.Set("Referer", "https://anilist.co/")
	req.Header.Set("User-Agent", config.DesktopUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poster: anilist episode titles status %d", resp.StatusCode)
	}

	var res struct {
		Data struct {
			Media struct {
				StreamingEpisodes []struct {
					Title string `json:"title"`
				} `json:"streamingEpisodes"`
			} `json:"Media"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	out := make(map[int]string)
	for _, ep := range res.Data.Media.StreamingEpisodes {
		m := episodeTitleRE.FindStringSubmatch(ep.Title)
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil || num <= 0 {
			continue
		}
		if _, seen := out[num]; seen {
			continue
		}
		out[num] = m[2]
	}
	return out, nil
}

// cleanAnilistDescription strips the light HTML AniList still sometimes
// includes even with asHtml:false (e.g. <br>, <i> around source notes) so it
// reads as plain text in the terminal.
func cleanAnilistDescription(s string) string {
	s = htmlTagRE.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}
