package timstreams

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
)

const maxPlayerPageBytes = 1 << 20

// ResolveSource fetches current stream lanes and turns their ticket URLs into
// signed HLS playlist URLs immediately before playback.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	handle := mediaID
	if !strings.HasPrefix(handle, handlePrefix) {
		handle = episode.ID
	}
	kind, slug, ok := parseHandle(handle)
	if !ok {
		return nil, fmt.Errorf("timstreams resolve: invalid live handle %q: %w", handle, provider.ErrNoSources)
	}
	var item event
	switch kind {
	case "event":
		var response watchResponse
		endpoint := c.apiBase + "/watch/" + url.PathEscape(slug)
		if err := c.getJSON(ctx, endpoint, &response); err != nil {
			return nil, fmt.Errorf("timstreams fetch event: %w", err)
		}
		item = response.Item
	case "channel":
		var catalog channelCatalog
		if err := c.getJSON(ctx, c.apiBase+"/channels", &catalog); err != nil {
			return nil, fmt.Errorf("timstreams fetch channels: %w", err)
		}
		for _, channel := range catalog.Channels {
			if channel.URL == slug {
				item = channel
				break
			}
		}
	default:
		return nil, fmt.Errorf("timstreams resolve: unsupported handle type %q: %w", kind, provider.ErrNoSources)
	}
	if item.VIP {
		return nil, fmt.Errorf("timstreams resolve: item requires VIP access: %w", provider.ErrAuthRequired)
	}

	type laneResult struct {
		source provider.MediaSource
		err    error
	}
	resolved := make([]laneResult, len(item.Streams))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, lane := range item.Streams {
		if lane.VIP || !c.validShortLink(lane.URL) {
			continue
		}
		wg.Add(1)
		go func(index int, streamLane stream) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				resolved[index].err = ctx.Err()
				return
			}
			defer func() { <-sem }()
			source, err := c.resolveLane(ctx, streamLane)
			resolved[index] = laneResult{source: source, err: err}
		}(i, lane)
	}
	wg.Wait()

	sources := make([]provider.MediaSource, 0, len(resolved))
	for i, result := range resolved {
		if result.err != nil {
			timStreamsLog.Debug("stream lane resolution failed", "lane", item.Streams[i].Name, "err", result.err)
			continue
		}
		if result.source.URL != "" {
			sources = append(sources, result.source)
		}
	}
	if len(sources) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

func parseHandle(handle string) (kind, slug string, ok bool) {
	if !strings.HasPrefix(handle, handlePrefix) {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(handle, handlePrefix), ":", 2)
	if len(parts) != 2 || parts[1] == "" || strings.Contains(parts[1], "/") {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (c *Client) resolveLane(ctx context.Context, lane stream) (provider.MediaSource, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lane.URL, nil)
	if err != nil {
		return provider.MediaSource{}, fmt.Errorf("create player request: %w", err)
	}
	req.Header.Set("Referer", config.TimStreamsBase+"/")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return provider.MediaSource{}, fmt.Errorf("fetch player page: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return provider.MediaSource{}, &provider.HTTPError{Code: resp.StatusCode, URL: lane.URL}
	}
	if resp.Request == nil || !sameHost(resp.Request.URL, c.shortBase) {
		resp.Body.Close()
		return provider.MediaSource{}, fmt.Errorf("player page redirected outside TimStreams short-link host: %w", provider.ErrUpstreamChanged)
	}
	page, err := httpclient.ReadCapped(resp)
	if err != nil {
		return provider.MediaSource{}, fmt.Errorf("read player page: %w", err)
	}
	if len(page) > maxPlayerPageBytes {
		return provider.MediaSource{}, fmt.Errorf("player page exceeds %d bytes: %w", maxPlayerPageBytes, provider.ErrUpstreamChanged)
	}
	ticketURL, err := extractTicketURL(string(page))
	if err != nil {
		return provider.MediaSource{}, err
	}
	signedURL, err := c.fetchSignedURL(ctx, ticketURL, resp.Request.URL.String())
	if err != nil {
		return provider.MediaSource{}, err
	}
	return provider.MediaSource{
		URL:       signedURL,
		Quality:   "Auto",
		Backend:   lane.Name,
		Type:      provider.SourceTypeHLS,
		Referer:   config.TimStreamsBase + "/",
		UserAgent: config.DesktopUserAgent,
	}, nil
}

func sameHost(candidate *url.URL, baseURL string) bool {
	base, err := url.Parse(baseURL)
	return err == nil && candidate != nil && candidate.Scheme == "https" && strings.EqualFold(candidate.Host, base.Host)
}

func extractTicketURL(page string) (string, error) {
	pattern := `var\s+\w+\s*=\s*\[([0-9,\s]+)\]\s*,\s*\w+\s*=\s*(\d+)\s*,\s*\w+\s*=\s*(\d+)\s*,`
	decoderPattern, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("compile player decoder matcher: %w", err)
	}
	match := decoderPattern.FindStringSubmatch(page)
	if len(match) != 4 {
		return "", fmt.Errorf("player page decoder not found: %w", provider.ErrUpstreamChanged)
	}
	key, err := strconv.Atoi(match[2])
	if err != nil {
		return "", fmt.Errorf("parse player decoder key: %w", provider.ErrUpstreamChanged)
	}
	offset, err := strconv.Atoi(match[3])
	if err != nil {
		return "", fmt.Errorf("parse player decoder offset: %w", provider.ErrUpstreamChanged)
	}
	var decoded strings.Builder
	for _, raw := range strings.Split(match[1], ",") {
		value, parseErr := strconv.Atoi(strings.TrimSpace(raw))
		if parseErr != nil || value < 0 || value > 255 {
			return "", fmt.Errorf("invalid player decoder value: %w", provider.ErrUpstreamChanged)
		}
		decoded.WriteByte(byte(((value ^ key) - offset + 256) & 0xff))
	}
	ticketMatch := regexp.MustCompile(`loadStreamUrl\("(https://[^" ]+)"`).FindStringSubmatch(decoded.String())
	if len(ticketMatch) != 2 {
		return "", fmt.Errorf("playback ticket not found: %w", provider.ErrUpstreamChanged)
	}
	u, err := url.Parse(ticketMatch[1])
	if err != nil || u.Scheme != "https" || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".sytes.net") {
		return "", fmt.Errorf("invalid playback ticket URL: %w", provider.ErrUpstreamChanged)
	}
	return u.String(), nil
}

func (c *Client) fetchSignedURL(ctx context.Context, ticketURL, referer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ticketURL, nil)
	if err != nil {
		return "", fmt.Errorf("create ticket request: %w", err)
	}
	req.Header.Set("Referer", referer)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch playback ticket: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: ticketURL}
	}
	blob, err := httpclient.ReadCapped(resp)
	if err != nil {
		return "", fmt.Errorf("read playback ticket: %w", err)
	}
	parsed, _ := url.Parse(ticketURL)
	token := path.Base(parsed.Path)
	if len(blob) < 2 || token == "" || token == "." {
		return "", fmt.Errorf("invalid playback ticket payload: %w", provider.ErrUpstreamChanged)
	}
	key := []byte(token)
	decoded := make([]byte, len(blob)-1)
	for i := 1; i < len(blob); i++ {
		decoded[i-1] = blob[i] ^ key[(i-1)%len(key)] ^ byte((int(blob[0])+i-1)&0xff)
	}
	signedURL := string(decoded)
	if !provider.IsDirectURL(signedURL) {
		return "", fmt.Errorf("ticket did not contain a direct stream URL: %w", provider.ErrUpstreamChanged)
	}
	streamURL, err := url.Parse(signedURL)
	if err != nil || streamURL.Scheme != "https" || !strings.HasSuffix(strings.ToLower(streamURL.Path), ".m3u8") {
		return "", fmt.Errorf("ticket did not contain an HLS playlist: %w", provider.ErrUpstreamChanged)
	}
	return streamURL.String(), nil
}
