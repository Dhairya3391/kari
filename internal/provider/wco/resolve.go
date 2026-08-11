package wco

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"net/url"
	"strings"

	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/provider"
)

func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debugf("wco ResolveSource start: mediaID=%q episode=%v", mediaID, episode)
	episodeURL := episode.ID
	logging.Debugf("wco ResolveSource: fetching episode page %q", episodeURL)
	html, err := c.fetchEpisodePage(ctx, episodeURL)
	if err != nil {
		logging.Errorf("wco ResolveSource: fetchEpisodePage failed: %v", err)
		return nil, err
	}
	logging.Debugf("wco ResolveSource: decoding obfuscated iframe")
	decoded := c.decodeObfuscatedIframeHTML(html)
	if decoded == "" {
		logging.Debugf("wco ResolveSource: decodeObfuscatedIframeHTML returned empty, using raw html")
		decoded = html
	}
	logging.Debugf("wco ResolveSource: extracting iframe src")
	iframeURL := c.extractIframeSrcFromHTML(decoded)
	if iframeURL == "" {
		logging.Errorf("wco ResolveSource: could not locate embed iframe URL")
		return nil, fmt.Errorf("could not locate embed iframe URL")
	}
	logging.Debugf("wco ResolveSource: fetching embed page %q", iframeURL)
	embedHTML, err := c.fetchEmbedPage(ctx, iframeURL)
	if err != nil {
		logging.Errorf("wco ResolveSource: fetchEmbedPage failed: %v", err)
		return nil, err
	}
	var directSources []provider.MediaSource
	for _, directURL := range extractDirectMediaURLs(decoded + "\n" + embedHTML) {
		referer := iframeURL
		if mediaSourceType(directURL) == "hls" {
			directSources = appendUniqueMediaSource(directSources, wcoMediaSource(directURL, referer, c.CookieHeader(), "HLS"))
		}
	}
	for _, directURL := range extractDirectMediaURLs(decoded + "\n" + embedHTML) {
		if mediaSourceType(directURL) != "hls" {
			directSources = appendUniqueMediaSource(directSources, wcoMediaSource(directURL, iframeURL, c.CookieHeader(), "HD"))
		}
	}

	logging.Debugf("wco ResolveSource: finding getvidlink URL")
	getvidlinkURL := c.findGetvidlinkURL(embedHTML, iframeURL)
	if getvidlinkURL == "" {
		logging.Errorf("wco ResolveSource: could not derive getvidlink URL")
		if len(directSources) > 0 {
			return directSources, nil
		}
		return nil, fmt.Errorf("could not derive getvidlink URL")
	}
	logging.Debugf("wco ResolveSource: calling getvidlink %q", getvidlinkURL)
	payload, err := c.callGetvidlink(ctx, getvidlinkURL, iframeURL)
	if err != nil {
		logging.Errorf("wco ResolveSource: callGetvidlink failed: %v", err)
		if len(directSources) > 0 {
			return directSources, nil
		}
		return nil, err
	}
	logging.Debugf("wco ResolveSource: parsed payload: %v", payload)
	server, ok := payload["server"].(string)
	if !ok {
		logging.Warnf("wco ResolveSource: payload server field is not a string: %T", payload["server"])
	}
	if server == "" {
		server, ok = payload["cdn"].(string)
		if !ok {
			logging.Warnf("wco ResolveSource: payload cdn field is not a string: %T", payload["cdn"])
		}
	}

	if server == "" {
		logging.Errorf("wco ResolveSource: missing server in getvidlink response")
		if len(directSources) > 0 {
			return directSources, nil
		}
		return nil, fmt.Errorf("missing server in getvidlink response")
	}

	// Resolve all available quality tokens
	sources := directSources
	qualityKeys := []string{"fhd", "hd", "sd", "enc"}
	// Also look for numeric quality keys like "1080", "720"
	for k := range payload {
		if strings.Contains(k, "1080") || strings.Contains(k, "720") || strings.Contains(k, "480") {
			found := false
			for _, qk := range qualityKeys {
				if qk == k {
					found = true
					break
				}
			}
			if !found {
				qualityKeys = append(qualityKeys, k)
			}
		}
	}

	for _, key := range qualityKeys {
		token, ok := payload[key].(string)
		if !ok || token == "" {
			continue
		}

		logging.Debugf("wco ResolveSource: resolving quality %q with token %q", key, token)
		mediaURL, err := c.resolveFinalMediaURL(ctx, server, token)
		if err != nil {
			logging.Warnf("wco ResolveSource: resolveFinalMediaURL failed for %q: %v", key, err)
			continue
		}

		// The getvid hop only serves browser user agents and dead mappings
		// return 404, so probe the mapped URL and keep it only if it serves video.
		playable, certain := c.probeMediaURL(ctx, mediaURL)
		switch {
		case certain && !playable:
			logging.Warnf("wco ResolveSource: dropping quality %q (media URL not playable)", key)
			continue
		case !certain:
			logging.Debugf("wco ResolveSource: could not verify quality %q, keeping URL", key)
		}

		label := strings.ToUpper(key)
		if key == "enc" {
			label = "HD" // Default
		}

		finalReferer := "https://www.wcostream.com/"
		sources = appendUniqueMediaSource(sources, wcoMediaSource(mediaURL, finalReferer, c.CookieHeader(), label))
	}

	if len(sources) > 0 {
		return sources, nil
	}

	return nil, fmt.Errorf("no media sources found")
}

func (c *Client) fetchEmbedPage(ctx context.Context, iframeURL string) (string, error) {
	logging.Debugf("wco fetchEmbedPage url=%q", iframeURL)
	resp, err := c.doRequest(ctx, http.MethodGet, iframeURL, map[string]string{"Referer": config.BaseURL + "/"}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: iframeURL}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *Client) callGetvidlink(ctx context.Context, getvidlinkURL, referer string) (map[string]any, error) {
	logging.Debugf("wco callGetvidlink url=%q referer=%q", getvidlinkURL, referer)
	headers := map[string]string{
		"Accept":           "application/json, text/javascript, */*; q=0.01",
		"X-Requested-With": "XMLHttpRequest",
		"Referer":          referer,
		"Origin":           config.EmbedOrigin,
	}
	resp, err := c.doRequest(ctx, http.MethodGet, getvidlinkURL, headers, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: getvidlinkURL}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (c *Client) resolveFinalMediaURL(ctx context.Context, server, token string) (string, error) {
	target := strings.TrimRight(server, "/") + "/getvid?evid=" + url.QueryEscape(token) + "&json"
	headers := map[string]string{
		"Referer":          "https://www.wcostream.com/",
		"X-Requested-With": "XMLHttpRequest",
		"Accept":           "*/*",
	}
	resp, err := c.doRequest(ctx, http.MethodGet, target, headers, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(raw))

	// Check if it's a JSON object
	var mediaObj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(text), &mediaObj); err == nil && mediaObj.URL != "" {
		return mediaObj.URL, nil
	}

	// Otherwise try parsing as a JSON string
	var mediaStr string
	if err := json.Unmarshal([]byte(text), &mediaStr); err == nil && mediaStr != "" {
		return mediaStr, nil
	}

	// Fallback cleanup
	fallback := strings.ReplaceAll(strings.Trim(text, `"`), `\/`, `/`)
	return fallback, nil
}

// probeMediaURL issues a small Range request to confirm the URL actually
// serves video before emitting it as a playable source. certain is false when
// the request failed at the network level, in which case the caller should
// keep the URL rather than drop it.
func (c *Client) probeMediaURL(ctx context.Context, mediaURL string) (playable bool, certain bool) {
	headers := map[string]string{
		"Range":            "bytes=0-0",
		"Referer":          "https://www.wcostream.com/",
		"X-Requested-With": "XMLHttpRequest",
		"Accept":           "video/*, */*",
	}
	resp, err := c.doRequest(ctx, http.MethodGet, mediaURL, headers, nil)
	if err != nil {
		logging.Debugf("wco probeMediaURL request failed for %q: %v", mediaURL, err)
		return false, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return false, true
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "video/") || strings.Contains(ct, "mp4") ||
		strings.Contains(ct, "mpegurl") || strings.Contains(ct, "octet-stream") {
		return true, true
	}
	return false, true
}
