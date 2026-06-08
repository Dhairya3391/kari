package scrobble

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/model"
)

type AniListToken struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type AniListClient struct {
	clientID     string
	clientSecret string
	token        *AniListToken
	tokenPath    string
	httpClient   *http.Client
}

func NewAniListClient(clientID, clientSecret string) *AniListClient {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	tokenPath := filepath.Join(home, ".config", "kari", "anilist_token.json")

	c := &AniListClient{
		clientID:     clientID,
		clientSecret: clientSecret,
		tokenPath:    tokenPath,
		httpClient:   httpclient.NewWithTimeout(10 * time.Second),
	}

	c.loadToken()
	return c
}

func (c *AniListClient) loadToken() {
	if _, err := os.Stat(c.tokenPath); err == nil {
		data, err := os.ReadFile(c.tokenPath)
		if err == nil {
			var token AniListToken
			if err := json.Unmarshal(data, &token); err == nil {
				c.token = &token
			}
		}
	}
}

func (c *AniListClient) saveToken() error {
	if c.token == nil {
		return nil
	}
	data, err := json.MarshalIndent(c.token, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(c.tokenPath), 0755)
	return os.WriteFile(c.tokenPath, data, 0644)
}

func (c *AniListClient) Revoke() error {
	c.token = nil
	return os.Remove(c.tokenPath)
}

func (c *AniListClient) IsAuthenticated() bool {
	return c.token != nil && (c.token.ExpiresAt.IsZero() || c.token.ExpiresAt.After(time.Now()))
}

func (c *AniListClient) AuthURL() string {
	return fmt.Sprintf("%s/api/v2/oauth/authorize?client_id=%s&response_type=token",
		config.AniListAuthBase, c.clientID)
}

func (c *AniListClient) ExchangeCode(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)

	// If the user pasted the entire URL, try to extract the access_token
	if strings.Contains(token, "access_token=") {
		parts := strings.Split(token, "access_token=")
		if len(parts) > 1 {
			token = strings.Split(parts[1], "&")[0]
		}
	}

	if token == "" {
		return fmt.Errorf("token cannot be empty")
	}

	c.token = &AniListToken{
		AccessToken: token,
		ExpiresAt:   time.Time{}, // Tokens from implicit grant are long-lived
	}
	return c.saveToken()
}

func (c *AniListClient) UpdateProgress(ctx context.Context, media model.ResolvedMedia) error {
	if !c.IsAuthenticated() {
		return fmt.Errorf("anilist client not authenticated")
	}

	logging.Debugf("anilist: updating progress for %q (ep %d)", media.SeriesTitle, media.EpisodeNumber)

	// Always search by title because TMDB IDs are not AniList IDs
	mediaID, err := c.searchMediaID(ctx, media.SeriesTitle)
	if err != nil {
		return fmt.Errorf("failed to find anilist media id: %w", err)
	}

	logging.Debugf("anilist: found media id %d for %q", mediaID, media.SeriesTitle)

	query := `
	mutation ($mediaId: Int, $progress: Int) {
		SaveMediaListEntry (mediaId: $mediaId, progress: $progress, status: CURRENT) {
			id
			progress
		}
	}
	`
	vars := map[string]interface{}{
		"mediaId":  mediaID,
		"progress": media.EpisodeNumber,
	}

	if err := c.doGraphQL(ctx, query, vars, nil); err != nil {
		return fmt.Errorf("failed to save progress to anilist: %w", err)
	}

	return nil
}

func (c *AniListClient) searchMediaID(ctx context.Context, title string) (int, error) {
	query := `
	query ($search: String) {
		Media (search: $search, type: ANIME) {
			id
			title {
				romaji
				english
			}
		}
	}
	`
	vars := map[string]interface{}{"search": title}
	var res struct {
		Data struct {
			Media struct {
				ID int `json:"id"`
			} `json:"Media"`
		} `json:"data"`
	}

	err := c.doGraphQL(ctx, query, vars, &res)
	if err != nil {
		return 0, err
	}
	if res.Data.Media.ID == 0 {
		return 0, fmt.Errorf("media not found on anilist")
	}
	return res.Data.Media.ID, nil
}

func (c *AniListClient) doGraphQL(ctx context.Context, query string, vars map[string]interface{}, out interface{}) error {
	body := map[string]interface{}{
		"query":     query,
		"variables": vars,
	}
	data, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, "POST", config.AniListAPIBase, bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token.AccessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, errBody.String())
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return err
		}
	}
	return nil
}
