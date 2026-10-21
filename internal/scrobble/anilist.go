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
	"kari/internal/util"
)

// log scopes every line from this package.
var aniLog = logging.With("component", "scrobble.anilist")

// AniListToken is the persisted OAuth token for the AniList auth-code flow.
type AniListToken struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// MediaListStatus represents the viewing status of an anime entry on AniList.
type MediaListStatus string

const (
	// StatusCurrent indicates an anime is currently being watched.
	StatusCurrent MediaListStatus = "CURRENT"
	// StatusPlanning indicates an anime is planned to be watched.
	StatusPlanning MediaListStatus = "PLANNING"
	// StatusCompleted indicates an anime has been completed.
	StatusCompleted MediaListStatus = "COMPLETED"
	// StatusDropped indicates an anime has been dropped.
	StatusDropped MediaListStatus = "DROPPED"
	// StatusPaused indicates an anime is on hold/paused.
	StatusPaused MediaListStatus = "PAUSED"
	// StatusRepeating indicates an anime is currently being rewatched.
	StatusRepeating MediaListStatus = "REPEATING"
)

// MediaListEntry contains the user's tracking entry details for an anime on AniList.
type MediaListEntry struct {
	ID       int             `json:"id"`
	Status   MediaListStatus `json:"status"`
	Progress int             `json:"progress"`
	Repeat   int             `json:"repeat"`
}

// AniListClient updates anime watch progress via the AniList GraphQL API.
// Tokens are persisted under ~/.config/kari.
type AniListClient struct {
	clientID     string
	clientSecret string
	token        *AniListToken
	tokenPath    string
	httpClient   *http.Client
	// apiBase overrides the GraphQL endpoint (tests point it at an
	// httptest server); empty selects config.AniListAPIBase.
	apiBase string
}

// resolveTokenPath resolves the token path under ~/.config/kari/tokens/<name>,
// migrating from ~/.config/kari/<legacyName> if present.
func resolveTokenPath(name, legacyName string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	baseDir := filepath.Join(home, ".config", "kari")
	newDir := filepath.Join(baseDir, "tokens")
	newPath := filepath.Join(newDir, name)
	legacyPath := filepath.Join(baseDir, legacyName)

	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}
	if _, err := os.Stat(legacyPath); err == nil {
		_ = os.MkdirAll(newDir, 0o755)
		if err := os.Rename(legacyPath, newPath); err == nil {
			return newPath
		}
		return legacyPath
	}
	return newPath
}

// NewAniListClient constructs a client, loading any persisted token from disk.
func NewAniListClient(clientID, clientSecret string) *AniListClient {
	tokenPath := resolveTokenPath("anilist.json", "anilist_token.json")

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
	return util.AtomicWriteFile(c.tokenPath, data, 0600)
}

// Revoke is accepted for parity with TraktClient but is a no-op: AniList's
// OAuth spec has no programmatic revoke endpoint, so tokens are simply
// discarded locally.
func (c *AniListClient) Revoke() error {
	c.token = nil
	if err := os.Remove(c.tokenPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsAuthenticated reports whether an unexpired token is stored.
func (c *AniListClient) IsAuthenticated() bool {
	return c.token != nil && (c.token.ExpiresAt.IsZero() || c.token.ExpiresAt.After(time.Now()))
}

// AuthURL returns the browser URL the user visits to obtain an auth code.
func (c *AniListClient) AuthURL() string {
	return fmt.Sprintf("%s/api/v2/oauth/authorize?client_id=%s&response_type=token",
		config.AniListAuthBase, c.clientID)
}

// ExchangeCode swaps the user-pasted auth code for a token and persists it.
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

// UpdateProgress advances the matching AniList list entry to the resolved
// media's episode number, searching by series title.
func (c *AniListClient) UpdateProgress(ctx context.Context, media model.ResolvedMedia) error {
	if !c.IsAuthenticated() {
		return fmt.Errorf("anilist client not authenticated")
	}

	aniLog.Debug("updating progress", "series", media.SeriesTitle, "episode", media.EpisodeNumber)

	// Always search by title because TMDB IDs are not AniList IDs
	mediaID, err := c.searchMediaID(ctx, media.SeriesTitle)
	if err != nil {
		return fmt.Errorf("failed to find anilist media id: %w", err)
	}

	aniLog.Debug("media resolved", "mediaID", mediaID, "series", media.SeriesTitle)

	query := `
	mutation ($mediaId: Int, $progress: Int) {
		SaveMediaListEntry (mediaId: $mediaId, progress: $progress) {
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

// GetMediaListEntry retrieves the authenticated user's list entry and total episodes for an anime.
// mediaID can be 0 if searching by title. If the anime is not in the user's list, entry is nil.
func (c *AniListClient) GetMediaListEntry(ctx context.Context, mediaID int, title string) (*MediaListEntry, int, error) {
	if !c.IsAuthenticated() {
		return nil, 0, fmt.Errorf("anilist client not authenticated")
	}

	query := `
	query ($id: Int, $search: String) {
		Media (id: $id, search: $search, type: ANIME) {
			id
			episodes
			mediaListEntry {
				id
				status
				progress
				repeat
			}
		}
	}
	`
	vars := make(map[string]interface{})
	if mediaID > 0 {
		vars["id"] = mediaID
	} else if title != "" {
		vars["search"] = title
	} else {
		return nil, 0, fmt.Errorf("media id or title required")
	}

	var res struct {
		Data struct {
			Media *struct {
				ID             int             `json:"id"`
				Episodes       *int            `json:"episodes"`
				MediaListEntry *MediaListEntry `json:"mediaListEntry"`
			} `json:"Media"`
		} `json:"data"`
	}

	if err := c.doGraphQL(ctx, query, vars, &res); err != nil {
		return nil, 0, err
	}
	if res.Data.Media == nil || res.Data.Media.ID == 0 {
		return nil, 0, fmt.Errorf("media not found on anilist")
	}

	totalEpisodes := 0
	if res.Data.Media.Episodes != nil {
		totalEpisodes = *res.Data.Media.Episodes
	}
	return res.Data.Media.MediaListEntry, totalEpisodes, nil
}

// UpdateMediaListStatus updates or sets the status, progress, and repeat count for an anime on AniList.
// If mediaID is <= 0, title is resolved via searchMediaID. progress and repeat are optional (nil to omit).
func (c *AniListClient) UpdateMediaListStatus(ctx context.Context, mediaID int, title string, status MediaListStatus, progress *int, repeat *int) error {
	if !c.IsAuthenticated() {
		return fmt.Errorf("anilist client not authenticated")
	}

	if mediaID <= 0 {
		var err error
		mediaID, err = c.searchMediaID(ctx, title)
		if err != nil {
			return fmt.Errorf("failed to find anilist media id: %w", err)
		}
	}

	query := `
	mutation ($mediaId: Int, $status: MediaListStatus, $progress: Int, $repeat: Int) {
		SaveMediaListEntry (mediaId: $mediaId, status: $status, progress: $progress, repeat: $repeat) {
			id
			status
			progress
			repeat
		}
	}
	`
	vars := map[string]interface{}{
		"mediaId": mediaID,
		"status":  status,
	}
	if progress != nil {
		vars["progress"] = *progress
	}
	if repeat != nil {
		vars["repeat"] = *repeat
	}

	if err := c.doGraphQL(ctx, query, vars, nil); err != nil {
		return fmt.Errorf("failed to update anilist status: %w", err)
	}

	return nil
}

// DeleteMediaListEntry removes a media list entry from the user's AniList library by entry ID.
func (c *AniListClient) DeleteMediaListEntry(ctx context.Context, entryID int) error {
	if !c.IsAuthenticated() {
		return fmt.Errorf("anilist client not authenticated")
	}

	query := `
	mutation ($id: Int) {
		DeleteMediaListEntry (id: $id) {
			deleted
		}
	}
	`
	vars := map[string]interface{}{
		"id": entryID,
	}

	if err := c.doGraphQL(ctx, query, vars, nil); err != nil {
		return fmt.Errorf("failed to delete anilist media list entry: %w", err)
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

	req, _ := http.NewRequestWithContext(ctx, "POST", c.endpoint(), bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://anilist.co")
	req.Header.Set("Referer", "https://anilist.co/")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	if c.token != nil && c.token.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.token.AccessToken)
	}

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
