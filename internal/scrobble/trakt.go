package scrobble

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/util"
)

// log scopes every line from this package.
var traktLog = logging.With("component", "scrobble.trakt")

// TraktToken is the persisted OAuth token for the Trakt device-auth flow.
type TraktToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// TraktClient scrobbles episode/movie progress to trakt.tv. Tokens are
// persisted under ~/.config/kari and refreshed automatically.
type TraktClient struct {
	clientID     string
	clientSecret string
	token        *TraktToken
	tokenPath    string
	httpClient   *http.Client
	// apiBase overrides the REST endpoint (tests point it at an httptest
	// server); empty selects config.TraktAPIBase.
	apiBase string
}

// NewTraktClient constructs a client, loading any persisted token from disk.
func NewTraktClient(clientID, clientSecret string) *TraktClient {
	tokenPath := resolveTokenPath("trakt.json", "trakt_token.json")

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
	return util.AtomicWriteFile(c.tokenPath, data, 0600)
}

// Revoke revokes the stored token server-side (best effort) and deletes it
// from disk.
func (c *TraktClient) Revoke() error {
	c.token = nil
	if err := os.Remove(c.tokenPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsAuthenticated reports whether a usable token is stored.
func (c *TraktClient) IsAuthenticated() bool {
	return c.token != nil && c.token.ExpiresAt.After(time.Now())
}

// StartDeviceAuth begins the device-code flow, returning the code the user
// enters at verificationURL plus polling parameters.
func (c *TraktClient) StartDeviceAuth(ctx context.Context) (userCode, verificationURL, deviceCode string, interval, expiresIn int, err error) {
	body := map[string]string{"client_id": c.clientID}
	data, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, "POST", c.endpoint()+"/oauth/device/code", bytes.NewBuffer(data))
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

// PollDeviceAuth polls until the user completes authorization or the code
// expires, persisting the exchanged token on success.
func (c *TraktClient) PollDeviceAuth(ctx context.Context, deviceCode string, interval, expiresIn int) error {
	if interval <= 0 {
		interval = 5
	}
	deadline := time.Duration(expiresIn)*time.Second + 30*time.Second
	if expiresIn <= 0 {
		deadline = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("trakt device auth timed out")
			}
			return ctx.Err()
		case <-ticker.C:
			body := map[string]string{
				"code":          deviceCode,
				"client_id":     c.clientID,
				"client_secret": c.clientSecret,
			}
			data, _ := json.Marshal(body)

			req, _ := http.NewRequestWithContext(ctx, "POST", c.endpoint()+"/oauth/device/token", bytes.NewBuffer(data))
			req.Header.Set("Content-Type", "application/json")

			resp, err := c.httpClient.Do(req)
			if err != nil {
				continue
			}

			if resp.StatusCode == 200 {
				var res struct {
					AccessToken  string `json:"access_token"`
					RefreshToken string `json:"refresh_token"`
					ExpiresIn    int    `json:"expires_in"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&res); err == nil {
					resp.Body.Close()
					c.token = &TraktToken{
						AccessToken:  res.AccessToken,
						RefreshToken: res.RefreshToken,
						ExpiresAt:    time.Now().Add(time.Duration(res.ExpiresIn) * time.Second),
					}
					return c.saveToken()
				}
				resp.Body.Close()
				continue
			}

			switch resp.StatusCode {
			case http.StatusBadRequest, http.StatusTooManyRequests:
				resp.Body.Close()
				continue
			case http.StatusNotFound, http.StatusConflict, http.StatusGone:
				resp.Body.Close()
				return fmt.Errorf("trakt auth failed: device code invalid or expired (%d)", resp.StatusCode)
			default:
				resp.Body.Close()
				return fmt.Errorf("trakt auth failed: %d", resp.StatusCode)
			}
		}
	}
}

// RefreshIfNeeded exchanges a soon-to-expire refresh token for a new access
// token; it is a no-op when the current token is still fresh.
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

	req, _ := http.NewRequestWithContext(ctx, "POST", c.endpoint()+"/oauth/token", bytes.NewBuffer(data))
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

// ScrobbleEpisode reports watch progress for an episode; progress is 0..1.
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

	traktLog.Debug("scrobbling episode", "series", media.SeriesTitle, "season", media.SeasonNumber, "episode", media.EpisodeNumber, "progressPct", progressPct)

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

// ScrobbleMovie reports watch progress for a movie; progress is 0..1.
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

	traktLog.Debug("scrobbling movie", "title", media.SeriesTitle, "progressPct", progressPct)

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

type traktHistoryMovie struct {
	Title     string       `json:"title,omitempty"`
	IDs       traktSyncIDs `json:"ids,omitempty"`
	WatchedAt string       `json:"watched_at"`
}

type traktHistoryShow struct {
	Title   string               `json:"title,omitempty"`
	IDs     traktSyncIDs         `json:"ids,omitempty"`
	Seasons []traktHistorySeason `json:"seasons"`
	season  map[int]int
}

type traktHistorySeason struct {
	Number   int                   `json:"number"`
	Episodes []traktHistoryEpisode `json:"episodes"`
	episode  map[int]struct{}
}

type traktHistoryEpisode struct {
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at"`
}

type traktSyncIDs struct {
	TMDB int `json:"tmdb,omitempty"`
}

type traktHistoryPayload struct {
	Movies []traktHistoryMovie `json:"movies,omitempty"`
	Shows  []traktHistoryShow  `json:"shows,omitempty"`
}

// SyncWatchedHistory adds a batch of completed local entries through Trakt's
// history endpoint, which accepts multiple events in one rate-limited write.
func (c *TraktClient) SyncWatchedHistory(ctx context.Context, items []WatchedItem) (int, error) {
	if !c.IsAuthenticated() {
		return 0, fmt.Errorf("trakt not connected")
	}
	const batchSize = 100
	var synced int
	var lastRequest time.Time
	for start := 0; start < len(items); start += batchSize {
		if !lastRequest.IsZero() {
			if err := waitContext(ctx, time.Until(lastRequest.Add(1100*time.Millisecond))); err != nil {
				return synced, err
			}
		}
		end := min(start+batchSize, len(items))
		payload := traktHistoryBatch(items[start:end])
		if len(payload.Movies) == 0 && len(payload.Shows) == 0 {
			continue
		}
		lastRequest = time.Now()
		if err := c.postHistoryBatch(ctx, payload); err != nil {
			return synced, err
		}
		synced += len(items[start:end])
	}
	return synced, nil
}

func traktHistoryBatch(items []WatchedItem) traktHistoryPayload {
	payload := traktHistoryPayload{}
	showIndexes := make(map[string]int)
	for _, item := range items {
		watchedAt := item.WatchedAt
		if watchedAt.IsZero() {
			watchedAt = time.Now()
		}
		stamp := watchedAt.UTC().Format(time.RFC3339)
		if item.MediaType == provider.MediaTypeMovie {
			payload.Movies = append(payload.Movies, traktHistoryMovie{
				Title: item.Title, IDs: traktSyncIDs{TMDB: item.TMDBID}, WatchedAt: stamp,
			})
			continue
		}
		if item.Episode <= 0 || item.Mode == string(provider.ModeLive) || item.Mode == string(provider.ModeManga) {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(item.Title))
		if item.TMDBID > 0 {
			key = strconv.Itoa(item.TMDBID)
		}
		showIndex, ok := showIndexes[key]
		if !ok {
			showIndex = len(payload.Shows)
			showIndexes[key] = showIndex
			payload.Shows = append(payload.Shows, traktHistoryShow{
				Title: item.Title, IDs: traktSyncIDs{TMDB: item.TMDBID}, season: make(map[int]int),
			})
		}
		show := &payload.Shows[showIndex]
		seasonIndex, ok := show.season[item.Season]
		if !ok {
			seasonIndex = len(show.Seasons)
			show.season[item.Season] = seasonIndex
			show.Seasons = append(show.Seasons, traktHistorySeason{
				Number: item.Season, episode: make(map[int]struct{}),
			})
		}
		season := &show.Seasons[seasonIndex]
		if _, duplicate := season.episode[item.Episode]; duplicate {
			continue
		}
		season.episode[item.Episode] = struct{}{}
		season.Episodes = append(season.Episodes, traktHistoryEpisode{Number: item.Episode, WatchedAt: stamp})
	}
	for i := range payload.Shows {
		for j := range payload.Shows[i].Seasons {
			payload.Shows[i].Seasons[j].episode = nil
		}
		payload.Shows[i].season = nil
	}
	return payload
}

func (c *TraktClient) postHistoryBatch(ctx context.Context, payload traktHistoryPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode trakt history: %w", err)
	}
	backoff := time.Second
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint()+"/sync/history", bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("create trakt history request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.token.AccessToken)
		req.Header.Set("trakt-api-version", "2")
		req.Header.Set("trakt-api-key", c.clientID)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("send trakt history: %w", err)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close()
			return nil
		}
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(resp.Body)
		retryDelay := time.Duration(0)
		if resp.StatusCode == http.StatusTooManyRequests {
			retryDelay = traktRetryAfter(resp.Header.Get("Retry-After"))
		}
		status, retryHeader := resp.StatusCode, resp.Header.Get("Retry-After")
		resp.Body.Close()
		if status != http.StatusTooManyRequests || attempt == 4 {
			return fmt.Errorf("trakt history sync error: %d: %s", status, strings.TrimSpace(errBody.String()))
		}
		if retryDelay <= 0 {
			retryDelay = backoff
			backoff = min(backoff*2, 30*time.Second)
		}
		traktLog.Warn("history sync rate limited; waiting before retry",
			"retry_after", retryHeader, "delay", retryDelay)
		if err := waitContext(ctx, retryDelay); err != nil {
			return err
		}
	}
	return fmt.Errorf("trakt history sync retries exhausted")
}

func traktRetryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil {
		return time.Until(until)
	}
	return 0
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *TraktClient) doScrobble(ctx context.Context, payload interface{}) error {
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", c.endpoint()+"/scrobble/stop", bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token.AccessToken)
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", c.clientID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		traktLog.Error("scrobble request failed", "err", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(resp.Body)
		traktLog.Error("scrobble failed", "status", resp.StatusCode, "body", errBody.String())
		return fmt.Errorf("trakt scrobble error: %d", resp.StatusCode)
	}
	traktLog.Info("scrobble succeeded")
	return nil
}
