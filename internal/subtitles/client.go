package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/util"
)

// log scopes every line from this package.
var osLog = logging.With("component", "subtitles.opensubtitles")

const (
	apiBase   = config.OpenSubtitlesAPI
	userAgent = config.OpenSubtitlesUA
)

// Client talks to the OpenSubtitles REST API (login, search, download
// links).
type Client struct {
	apiKey   string
	username string
	password string

	token       string
	tokenExpiry time.Time
	tokenMu     sync.Mutex
	http        *http.Client
}

// NewClient constructs an OpenSubtitles client from API credentials.
func NewClient(apiKey, username, password string) *Client {
	return &Client{
		apiKey:   apiKey,
		username: username,
		password: password,
		http:     httpclient.NewWithUserAgent(userAgent),
	}
}

// Configured reports whether credentials were provided.
func (c *Client) Configured() bool {
	return c.apiKey != "" && c.username != "" && c.password != ""
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token  string `json:"token"`
	Status int    `json:"status"`
}

type tokenCache struct {
	Token  string    `json:"token"`
	Expiry time.Time `json:"expiry"`
}

func (c *Client) ensureToken(ctx context.Context) error {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		return nil
	}

	// Try loading from disk
	if cachePath, err := resolveTokenCachePath(); err == nil {
		if data, err := os.ReadFile(cachePath); err == nil {
			var tc tokenCache
			if err := json.Unmarshal(data, &tc); err == nil {
				if time.Now().Before(tc.Expiry) {
					c.token = tc.Token
					c.tokenExpiry = tc.Expiry
					osLog.Debug("token cache loaded from disk", "expiresAt", tc.Expiry)
					return nil
				}
				osLog.Debug("token cache expired", "expiredAt", tc.Expiry)
			} else {
				osLog.Debug("token cache unmarshal failed", "err", err)
			}
		} else if !os.IsNotExist(err) {
			osLog.Debug("token cache read failed", "err", err)
		}
	}

	return c.login(ctx)
}

func resolveTokenCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "kari")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "os_token.json"), nil
}

func (c *Client) downloadFile(ctx context.Context, fileURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles download request creation failed: %w", err)
	}
	c.setHeaders(req, true)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	return raw, err
}

func (c *Client) login(ctx context.Context) error {
	osLog.Debug("login start", "username", c.username)
	body, err := json.Marshal(loginRequest{Username: c.username, Password: c.password})
	if err != nil {
		return fmt.Errorf("opensubtitles login marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+"/login", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("opensubtitles login request: %w", err)
	}
	c.setHeaders(req, false)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("opensubtitles login: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("opensubtitles login: status %d: %s", resp.StatusCode, string(raw))
	}

	var lr loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return fmt.Errorf("opensubtitles login decode: %w", err)
	}
	if lr.Token == "" {
		return fmt.Errorf("opensubtitles login: empty token")
	}

	c.token = lr.Token
	c.tokenExpiry = time.Now().Add(23 * time.Hour) // Slightly less than 24h to be safe

	// Save to disk
	if cachePath, err := resolveTokenCachePath(); err == nil {
		tc := tokenCache{Token: c.token, Expiry: c.tokenExpiry}
		if data, err := json.Marshal(tc); err == nil {
			if err := util.AtomicWriteFile(cachePath, data, 0o600); err != nil {
				osLog.Warn("token cache write failed", "err", err)
				return fmt.Errorf("opensubtitles write token cache: %w", err)
			}
		}
	}

	osLog.Info("login successful", "user", c.username)
	return nil
}

func (c *Client) setHeaders(req *http.Request, auth bool) {
	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if auth && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}
