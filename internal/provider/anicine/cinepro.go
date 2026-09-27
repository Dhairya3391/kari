package anicine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// cineproSourcesResp is the worker's per-title payload: direct stream URLs
// plus subtitle tracks. Provider carries the serving backend's identity.
type cineproSourcesResp struct {
	Sources   []cineproSource   `json:"sources"`
	Subtitles []cineproSubtitle `json:"subtitles"`
}

type cineproSource struct {
	URL      string         `json:"url"`
	Type     string         `json:"type"`
	Quality  string         `json:"quality"`
	Provider cineproBackend `json:"provider"`
}

type cineproBackend struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cineproSubtitle struct {
	URL    string `json:"url"`
	Label  string `json:"label"`
	Format string `json:"format"`
}

type cineproTokenResp struct {
	Token     string `json:"token"`
	Exp       int64  `json:"exp"`
	ExpiresAt string `json:"expiresAt"`
}

// cineproError captures the worker's error shapes:
// {"error":{"code","message"}} or {"error":"..."}.
type cineproError struct {
	Error json.RawMessage `json:"error"`
}

func (e *cineproError) message() string {
	var obj struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(e.Error, &obj); err == nil && obj.Message != "" {
		return obj.Code + ": " + obj.Message
	}
	var text string
	if err := json.Unmarshal(e.Error, &text); err == nil {
		return text
	}
	return strings.TrimSpace(string(e.Error))
}

// bearerToken returns a cached worker token, refreshing past expiry.
func (c *Client) bearerToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	cached, exp := c.token, c.tokenExp
	c.mu.Unlock()
	if cached != "" && time.Until(exp) > time.Minute {
		return cached, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.workerBase+"/v1/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("anicine token: %w", err)
	}
	body, err := httpclient.ReadCapped(resp)
	_ = resp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("anicine token: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: req.URL.String()}
	}

	var tr cineproTokenResp
	if err := json.Unmarshal(body, &tr); err != nil || tr.Token == "" {
		return "", fmt.Errorf("anicine token: decode response")
	}
	exp = time.Now().Add(30 * time.Minute)
	if tr.Exp > 0 {
		// exp is epoch millis when large, seconds otherwise.
		epoch := tr.Exp
		if epoch > 1e12 {
			exp = time.UnixMilli(epoch)
		} else {
			exp = time.Unix(epoch, 0)
		}
	}
	c.mu.Lock()
	c.token, c.tokenExp = tr.Token, exp
	c.mu.Unlock()
	return tr.Token, nil
}

// cineproGet GETs one worker endpoint with a fresh Bearer token, retrying
// once on auth rejection.
func (c *Client) cineproGet(ctx context.Context, path string) ([]byte, error) {
	do := func(token string) ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.workerBase+path, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("User-Agent", config.DesktopUserAgent)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, 0, err
		}
		body, err := httpclient.ReadCapped(resp)
		_ = resp.Body.Close()
		if err != nil {
			return nil, resp.StatusCode, err
		}
		return body, resp.StatusCode, nil
	}

	token, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	body, code, err := do(token)
	if err != nil {
		return nil, fmt.Errorf("anicine worker: %w", err)
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
		if token, err = c.bearerToken(ctx); err != nil {
			return nil, err
		}
		body, code, err = do(token)
		if err != nil {
			return nil, fmt.Errorf("anicine worker: %w", err)
		}
	}
	if code != http.StatusOK {
		if msg := workerErrorMessage(body); msg != "" {
			return nil, fmt.Errorf("anicine worker: %s", msg)
		}
		return nil, &provider.HTTPError{Code: code, URL: c.workerBase + path}
	}
	if msg := workerErrorMessage(body); msg != "" {
		return nil, fmt.Errorf("anicine worker: %s", msg)
	}
	return body, nil
}

// workerErrorMessage extracts the worker's error payload, if any.
func workerErrorMessage(body []byte) string {
	var ce cineproError
	if err := json.Unmarshal(body, &ce); err != nil || len(ce.Error) == 0 {
		return ""
	}
	if strings.TrimSpace(string(ce.Error)) == "null" {
		return ""
	}
	return ce.message()
}

// resolveTMDB resolves a TMDB title (movie, episode, or cartoon) through
// the worker into direct stream sources.
func (c *Client) resolveTMDB(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil || tmdbID <= 0 {
			return nil, fmt.Errorf("invalid media ID: %w", err)
		}
	}

	var path string
	if episode.Season > 0 || episode.Episode > 0 {
		season := max(episode.Season, 1)
		path = fmt.Sprintf("/v1/tv/%d/seasons/%d/episodes/%d", tmdbID, season, max(episode.Episode, 1))
	} else {
		path = fmt.Sprintf("/v1/movies/%d", tmdbID)
	}

	body, err := c.cineproGet(ctx, path)
	if err != nil {
		return nil, err
	}
	var out cineproSourcesResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("anicine worker: decode response: %w", err)
	}

	subs := make([]provider.SubtitleOption, 0, len(out.Subtitles))
	for _, sub := range out.Subtitles {
		file := strings.TrimSpace(sub.URL)
		if file == "" {
			continue
		}
		language := lang.Normalize(strings.TrimSpace(sub.Label))
		if language == "" {
			language = "en"
		}
		subs = append(subs, provider.SubtitleOption{URL: file, Language: language})
	}

	sources := make([]provider.MediaSource, 0, len(out.Sources))
	seen := make(map[string]struct{}, len(out.Sources))
	for _, raw := range out.Sources {
		streamURL := strings.TrimSpace(raw.URL)
		if streamURL == "" {
			continue
		}
		if _, ok := seen[streamURL]; ok {
			continue
		}
		seen[streamURL] = struct{}{}

		streamType := strings.ToLower(strings.TrimSpace(raw.Type))
		if streamType == "" {
			streamType = provider.SourceTypeHLS
		}
		quality := kit.QualityLabel(kit.Clean(raw.Quality), streamType)
		if name := strings.TrimSpace(raw.Provider.Name); name != "" {
			quality = kit.TagQuality(quality, name, "anicine")
		}
		sources = append(sources, provider.MediaSource{
			URL:       streamURL,
			Quality:   quality,
			Type:      streamType,
			UserAgent: config.DesktopUserAgent,
			Subtitles: subs,
		})
	}
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	sources = provider.FilterDirectSources(sources)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	logging.Debug("resolve source done via worker", "provider", c.Name(), "count", len(sources))
	return sources, nil
}
