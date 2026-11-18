package zlive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
)

// sessionTicket is the short-lived token issued by the site before a
// resolution request.
type sessionTicket struct {
	W string `json:"w"`
}

// resolveReply is the resolved stream URL returned by the site.
type resolveReply struct {
	Location string `json:"location"`
}

// ResolveSource resolves every playable lane the channel advertises. Each
// lane is resolved independently so one dead source does not hide the rest.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	handle := strings.TrimSpace(mediaID)
	if handle == "" {
		handle = episode.ID
	}
	id, ok := parseHandle(handle)
	if !ok {
		return nil, fmt.Errorf("zlive resolve: invalid channel handle %q: %w", handle, provider.ErrNoSources)
	}

	sources, err := c.sourcesForChannel(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("zlive resolve: %w", err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("zlive channel %q has no streams: %w", id, provider.ErrNoSources)
	}

	var resolved []provider.MediaSource
	for _, source := range sources {
		target, err := c.resolveLocation(ctx, source.Key)
		if err != nil {
			zliveLog.Debug("lane resolution failed", "channel", id, "source", source.Key, "err", err)
			continue
		}
		resolved = append(resolved, c.newSource(target, source.Label, source.Quality))
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("zlive channel %q: %w", id, provider.ErrNoSources)
	}
	return provider.FilterDirectSources(resolved), nil
}

// sourcesForChannel returns the lanes from the cached catalog, falling back
// to the handle as a bare source key when the catalog has no entry.
func (c *Client) sourcesForChannel(ctx context.Context, id string) ([]channelSource, error) {
	channels, err := c.directory(ctx)
	if err != nil {
		return nil, err
	}
	for _, ch := range channels {
		if ch.ID == id {
			return playableSources(ch), nil
		}
	}
	return []channelSource{{Key: id, Label: "zlive"}}, nil
}

// resolveLocation fetches a session ticket and resolves one source key into
// a direct HLS URL. Transient upstream resets are retried with bounds.
func (c *Client) resolveLocation(ctx context.Context, key string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < resolveAttempts; attempt++ {
		target, err := c.resolveOnce(ctx, key)
		if err == nil && target != "" {
			return target, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = provider.ErrNoSources
	}
	return "", lastErr
}

// resolveOnce performs one ticket fetch and signed resolution round trip.
func (c *Client) resolveOnce(ctx context.Context, key string) (string, error) {
	ticket, err := c.fetchTicket(ctx)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{
		"s": key,
		"u": time.Now().Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("zlive encode payload: %w", err)
	}
	envelope, err := signRequest(ticket, payload, signingDate(time.Now()))
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("zlive encode envelope: %w", err)
	}

	requestCtx, cancelRequest := context.WithTimeout(ctx, resolveRequestTimeout)
	defer cancelRequest()
	target := c.apiBase + "/resolve"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("zlive resolve request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", c.siteBase)
	req.Header.Set("Referer", c.siteBase+"/")
	// The upstream gate rejects browser-style requests without this header.
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("zlive resolve: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	raw, err := httpclient.ReadCapped(resp)
	if err != nil {
		return "", fmt.Errorf("zlive resolve read: %w", err)
	}
	var reply resolveReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return "", fmt.Errorf("zlive decode resolve reply: %w", err)
	}
	location := strings.TrimSpace(reply.Location)
	if location == "" || !isPlayableLocation(location) {
		return "", fmt.Errorf("zlive resolve returned no playable URL: %w", provider.ErrNoSources)
	}
	return location, nil
}

// fetchTicket requests the short-lived session token the envelope is signed
// with. The upstream resets idle connections occasionally, hence the retry in
// resolveLocation.
func (c *Client) fetchTicket(ctx context.Context) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	target := c.apiBase + "/session/ticket"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("zlive ticket request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", c.siteBase)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("zlive ticket: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	raw, err := httpclient.ReadCapped(resp)
	if err != nil {
		return "", fmt.Errorf("zlive ticket read: %w", err)
	}
	var ticket sessionTicket
	if err := json.Unmarshal(raw, &ticket); err != nil {
		return "", fmt.Errorf("zlive decode ticket: %w", err)
	}
	if strings.TrimSpace(ticket.W) == "" {
		return "", fmt.Errorf("zlive ticket is empty: %w", provider.ErrUpstreamChanged)
	}
	return ticket.W, nil
}

// newSource builds one playable HLS row. The resolved URL is short-lived and
// keyed to a single viewer request: probing it here would consume it before
// the live verifier or mpv opens it. Quality therefore comes from the catalog
// label only, and Height/Bitrate stay zero rather than claiming a probed tier.
func (c *Client) newSource(target, backend, catalogQuality string) provider.MediaSource {
	return provider.MediaSource{
		URL:       target,
		Quality:   qualityFromCatalog(catalogQuality),
		Backend:   backend,
		Type:      provider.SourceTypeHLS,
		Referer:   c.siteBase + "/",
		UserAgent: config.DesktopUserAgent,
		ExtraArgs: []string{"--http-header-fields=" + mpvHeaderFields()},
	}
}

// isPlayableLocation rejects the decoy URL the upstream returns for unsigned
// or expired requests. That fallback host never serves a real playlist.
func isPlayableLocation(location string) bool {
	if !strings.HasPrefix(strings.ToLower(location), "http://") &&
		!strings.HasPrefix(strings.ToLower(location), "https://") {
		return false
	}
	return !strings.Contains(strings.ToLower(location), "route.transcode.cfd")
}

// qualityFromCatalog maps the catalog's coarse quality label to a content
// label. It is only a fallback: a manifest with real variants wins.
func qualityFromCatalog(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "4K", "UHD":
		return "2160p"
	case "FHD", "1080P":
		return "1080p"
	case "HD", "720P":
		return "720p"
	case "SD":
		return "480p"
	default:
		return "Auto"
	}
}

// mediaHeaders are the browser fetch-metadata headers the delivery CDN
// requires on playlist and segment requests. mpv does not send them by
// default, so they are also carried in ExtraArgs for playback.
func mediaHeaders() map[string]string {
	return map[string]string{
		"Sec-Fetch-Site": "cross-site",
		"Sec-Fetch-Mode": "cors",
		"Accept":         "*/*",
	}
}

// mpvHeaderFields renders mediaHeaders in mpv's --http-header-fields format.
func mpvHeaderFields() string {
	fields := []string{
		"Sec-Fetch-Site: cross-site",
		"Sec-Fetch-Mode: cors",
	}
	return strings.Join(fields, ",")
}
