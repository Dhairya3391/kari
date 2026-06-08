package scrobble

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/model"
)

type TraktToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type TraktClient struct {
	clientID     string
	clientSecret string
	token        *TraktToken
	tokenPath    string
	httpClient   *http.Client
}

func NewTraktClient(clientID, clientSecret string) *TraktClient {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	tokenPath := filepath.Join(home, ".config", "kari", "trakt_token.json")

	c := &TraktClient{
		clientID:     clientID,
		clientSecret: clientSecret,
		tokenPath:    tokenPath,
		httpClient:   httpclient.NewWithTimeout(10 * time.Second),
	}

	c.loadToken()
	return c
}

func (c *TraktClient) loadToken() {
	if _, err := os.Stat(c.tokenPath); err == nil {
		data, err := os.ReadFile(c.tokenPath)
		if err == nil {
			var token TraktToken
			if err := json.Unmarshal(data, &token); err == nil {
				c.token = &token
			}
		}
	}
}

func (c *TraktClient) saveToken() error {
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

func (c *TraktClient) Revoke() error {
	c.token = nil
	return os.Remove(c.tokenPath)
}

func (c *TraktClient) IsAuthenticated() bool {
	return c.token != nil && c.token.ExpiresAt.After(time.Now())
}

func (c *TraktClient) StartDeviceAuth(ctx context.Context) (userCode, verificationURL, deviceCode string, interval, expiresIn int, err error) {
	body := map[string]string{"client_id": c.clientID}
	data, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, "POST", config.TraktAPIBase+"/oauth/device/code", bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", "", 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", "", "", 0, 0, fmt.Errorf("trakt api error: %d", resp.StatusCode)
	}

	var res struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", "", "", 0, 0, err
	}

	return res.UserCode, res.VerificationURL, res.DeviceCode, res.Interval, res.ExpiresIn, nil
}

func (c *TraktClient) PollDeviceAuth(ctx context.Context, deviceCode string, interval int) error {
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			body := map[string]string{
				"code":          deviceCode,
				"client_id":     c.clientID,
				"client_secret": c.clientSecret,
			}
			data, _ := json.Marshal(body)

			req, _ := http.NewRequestWithContext(ctx, "POST", config.TraktAPIBase+"/oauth/device/token", bytes.NewBuffer(data))
			req.Header.Set("Content-Type", "application/json")

			resp, err := c.httpClient.Do(req)
			if err != nil {
				continue
			}
			defer resp.Body.Close()

			if resp.StatusCode == 200 {
				var res struct {
					AccessToken  string `json:"access_token"`
					RefreshToken string `json:"refresh_token"`
					ExpiresIn    int    `json:"expires_in"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&res); err == nil {
					c.token = &TraktToken{
						AccessToken:  res.AccessToken,
						RefreshToken: res.RefreshToken,
						ExpiresAt:    time.Now().Add(time.Duration(res.ExpiresIn) * time.Second),
					}
					return c.saveToken()
				}
			} else if resp.StatusCode == 400 || resp.StatusCode == 404 || resp.StatusCode == 409 || resp.StatusCode == 410 {
				// pending, slow_down, expired, etc.
				continue
			} else {
				return fmt.Errorf("trakt auth failed: %d", resp.StatusCode)
			}
		}
	}
}

func (c *TraktClient) RefreshIfNeeded(ctx context.Context) error {
	if c.token == nil || c.token.RefreshToken == "" {
		return nil
	}
	if c.token.ExpiresAt.After(time.Now().Add(7 * 24 * time.Hour)) {
		return nil
	}

	body := map[string]string{
		"refresh_token": c.token.RefreshToken,
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"grant_type":    "refresh_token",
	}
	data, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, "POST", config.TraktAPIBase+"/oauth/token", bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("trakt refresh error: %d", resp.StatusCode)
	}

	var res struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return err
	}

	c.token = &TraktToken{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(res.ExpiresIn) * time.Second),
	}
	return c.saveToken()
}

func (c *TraktClient) ScrobbleEpisode(ctx context.Context, media model.ResolvedMedia, progress float64) error {
	if !c.IsAuthenticated() {
		return nil
	}

	progressPct := progress * 100
	if progressPct < 0 {
		progressPct = 0
	}
	if progressPct > 100 {
		progressPct = 100
	}

	logging.Debugf("trakt: scrobbling episode %q S%02dE%02d at %.1f%%", media.SeriesTitle, media.SeasonNumber, media.EpisodeNumber, progressPct)

	payload := map[string]interface{}{
		"episode": map[string]interface{}{
			"season": media.SeasonNumber,
			"number": media.EpisodeNumber,
		},
		"show": map[string]interface{}{
			"title": media.SeriesTitle,
		},
		"progress": progressPct,
	}

	if media.TMDBID > 0 {
		payload["show"].(map[string]interface{})["ids"] = map[string]int{"tmdb": media.TMDBID}
	}

	return c.doScrobble(ctx, payload)
}

func (c *TraktClient) ScrobbleMovie(ctx context.Context, media model.ResolvedMedia, progress float64) error {
	if !c.IsAuthenticated() {
		return nil
	}

	progressPct := progress * 100
	if progressPct < 0 {
		progressPct = 0
	}
	if progressPct > 100 {
		progressPct = 100
	}

	logging.Debugf("trakt: scrobbling movie %q at %.1f%%", media.SeriesTitle, progressPct)

	payload := map[string]interface{}{
		"movie": map[string]interface{}{
			"title": media.SeriesTitle,
		},
		"progress": progressPct,
	}

	if media.TMDBID > 0 {
		payload["movie"].(map[string]interface{})["ids"] = map[string]int{"tmdb": media.TMDBID}
	}

	return c.doScrobble(ctx, payload)
}

func (c *TraktClient) doScrobble(ctx context.Context, payload interface{}) error {
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", config.TraktAPIBase+"/scrobble/stop", bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token.AccessToken)
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", c.clientID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logging.Errorf("trakt scrobble request failed: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(resp.Body)
		logging.Errorf("trakt scrobble failed: status=%d body=%q", resp.StatusCode, errBody.String())
		return fmt.Errorf("trakt scrobble error: %d", resp.StatusCode)
	}
	logging.Infof("trakt: scrobble successful")
	return nil
}
