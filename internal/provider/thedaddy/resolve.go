package thedaddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"kari/internal/provider"
)

// playerProfile is one DaddyLive player page. Profiles are tried in order; the
// first lane that actually plays wins.
type playerProfile struct {
	id          string
	label       string
	refererMode refererMode
}

// refererMode says which Referer the CDN expects for playlist requests.
type refererMode int

const (
	// refererOmit leaves Referer off entirely (the casting and wideiptv
	// parents reject mismatched origins).
	refererOmit refererMode = iota
	// refererEmbed sends the embed page that produced the lane.
	refererEmbed
)

// playerProfiles is ordered by observed reliability: casting serves signed
// playlists that need no rewriting, while the premiumtv family mints
// short-lived edge tokens that only cover the master playlist.
var playerProfiles = []playerProfile{
	{id: "casting", label: "PLAYER 5", refererMode: refererOmit},
	{id: "cast", label: "PLAYER 2", refererMode: refererOmit},
	{id: "player", label: "PLAYER 6", refererMode: refererOmit},
	{id: "plus", label: "PLAYER 4", refererMode: refererOmit},
	{id: "stream", label: "PLAYER 1", refererMode: refererEmbed},
	{id: "watch", label: "PLAYER 3", refererMode: refererEmbed},
}

const (
	// resolveTimeout bounds one full player resolution including verification.
	resolveTimeout = 20 * time.Second
	// verifyTimeout bounds the playlist probe of a single lane.
	verifyTimeout = 12 * time.Second
	// embedDepth caps how many nested frames are followed per player.
	embedDepth = 4
)

var (
	rePageIframe       = regexp.MustCompile(`(?is)<iframe[^>]*class="[^"]*video[^"]*"[^>]*src="([^"]+)"`)
	rePageIframeBefore = regexp.MustCompile(`(?is)<iframe[^>]*src="([^"]+)"[^>]*class="[^"]*video[^"]*"`)
	reInnerIframe      = regexp.MustCompile(`(?i)<iframe[^>]+src="([^"]+)"`)
	rePlayerSource     = regexp.MustCompile(`(?is)src:\s*"(https?://[^"]+\.m3u8[^"]*)"`)
	reStreamURL        = regexp.MustCompile(`streamUrl:\s*"((?:\\/|[^"])+)"`)
	reDembedSRC        = regexp.MustCompile(`const\s+SRC\s*=\s*"(https?://[^"\s]+\.m3u8)"`)
	reDembedEDGE       = regexp.MustCompile(`const\s+EDGE\s*=\s*"(https?://[^"\s]+)"`)
	reDembedChannel    = regexp.MustCompile(`const\s+CHANNEL\s*=\s*"([^"\s]+)"`)
	reEconfig          = regexp.MustCompile(`window\._econfig\s*=\s*['"]([^'"]+)['"]`)
)

// lane is one candidate direct stream before verification.
type lane struct {
	url     string
	referer string
}

// resolveLanes turns an event or channel handle into verified direct lanes.
// Channels are tried in handle order; at most two are inspected so an event
// carried by many channels still resolves quickly.
func (c *Client) resolveLanes(ctx context.Context, channelIDs []int) ([]provider.MediaSource, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	var failures []string
	for attempt, channelID := range channelIDs {
		if attempt >= 2 {
			break
		}
		for _, profile := range playerProfiles {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("thedaddy resolve: %w", ctx.Err())
			default:
			}
			candidate, err := c.resolveProfile(ctx, channelID, profile)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%d/%s: %v", channelID, profile.id, err))
				continue
			}
			if err := c.verifyLane(ctx, candidate, profile); err != nil {
				failures = append(failures, fmt.Sprintf("%d/%s: %v", channelID, profile.id, err))
				continue
			}
			thedaddyLog.Debug("resolved lane", "channel", channelID, "player", profile.id)
			return []provider.MediaSource{mediaSourceFor(candidate, profile)}, nil
		}
	}
	for _, failure := range failures {
		thedaddyLog.Debug("player failed", "failure", failure)
	}
	return nil, fmt.Errorf("no playable player for %v: %w", channelIDs, provider.ErrNoSources)
}

// resolveProfile fetches one player page, follows its embeds and extracts a
// direct stream URL.
func (c *Client) resolveProfile(ctx context.Context, channelID int, profile playerProfile) (lane, error) {
	watch := fmt.Sprintf("%s/watch.php?id=%d", c.baseURL, channelID)
	pageURL := fmt.Sprintf("%s/%s/stream-%d.php", c.baseURL, profile.id, channelID)
	page, err := c.fetchText(ctx, pageURL, watch)
	if err != nil {
		return lane{}, fmt.Errorf("load player page: %w", err)
	}
	embedURL := firstNonEmpty(
		firstGroup(rePageIframe, page),
		firstGroup(rePageIframeBefore, page),
		resolveInnerFrame(page, pageURL),
	)
	if embedURL == "" {
		return lane{}, fmt.Errorf("no embed iframe: %w", provider.ErrUpstreamChanged)
	}

	html := ""
	referer := embedURL
	for depth := 0; depth < embedDepth; depth++ {
		body, fetchErr := c.fetchText(ctx, embedURL, referer)
		if fetchErr != nil {
			return lane{}, fmt.Errorf("load embed: %w", fetchErr)
		}
		html = body
		if candidate, ok := c.extractLane(ctx, html, embedURL); ok {
			return candidate, nil
		}
		next := resolveInnerFrame(html, embedURL)
		if next == "" || next == embedURL {
			break
		}
		referer = embedURL
		embedURL = next
	}
	return lane{}, fmt.Errorf("no playable stream in %s embed: %w", profile.id, provider.ErrNoSources)
}

// extractLane finds a direct stream URL in one embed page.
func (c *Client) extractLane(ctx context.Context, html, embedURL string) (lane, bool) {
	if source := firstGroup(rePlayerSource, html); source != "" {
		return lane{url: source, referer: embedURL}, true
	}
	if raw := firstGroup(reStreamURL, html); raw != "" {
		streamURL := strings.ReplaceAll(raw, `\/`, "/")
		if strings.HasPrefix(streamURL, "http") {
			return lane{url: streamURL, referer: embedURL}, true
		}
	}
	if payload := firstGroup(reEconfig, html); payload != "" {
		if config, err := decryptEconfig(payload); err == nil {
			if streamURL := config.playableURL(); strings.HasPrefix(streamURL, "http") {
				return lane{url: streamURL, referer: embedURL}, true
			}
		}
	}
	return c.extractDembedLane(ctx, html, embedURL)
}

// extractDembedLane handles the premiumtv player, whose edge CDN only serves a
// playlist to requests carrying a token minted for the embed origin.
func (c *Client) extractDembedLane(ctx context.Context, html, embedURL string) (lane, bool) {
	source := firstGroup(reDembedSRC, html)
	edge := firstGroup(reDembedEDGE, html)
	slug := firstGroup(reDembedChannel, html)
	if source == "" || edge == "" || slug == "" {
		return lane{}, false
	}
	sourceURL, err := url.Parse(source)
	if err != nil {
		return lane{}, false
	}
	edgeURL, err := url.Parse(edge)
	if err != nil || sourceURL.Hostname() != edgeURL.Hostname() {
		return lane{}, false
	}
	token := c.mintDembedToken(ctx, edge, slug, embedURL)
	if token == "" {
		return lane{}, false
	}
	playable := *sourceURL
	query := playable.Query()
	query.Set("t", token)
	playable.RawQuery = query.Encode()
	return lane{url: playable.String(), referer: embedURL}, true
}

// mintDembedToken asks the edge for a short-lived playlist token. A failure is
// expected on some networks; the caller then skips this player.
func (c *Client) mintDembedToken(ctx context.Context, edge, slug, referer string) string {
	parsed, err := url.Parse(referer)
	if err != nil {
		return ""
	}
	target := edge + "/api/playlist-token?slug=" + url.QueryEscape(slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
	req.Header.Set("Referer", referer)
	req.Header.Set("Accept", "application/json")

	mintCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req = req.WithContext(mintCtx)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Token)
}

// verifyLane probes the playlist and its first variant the same way playback
// will, so a lane that cannot actually start is never offered.
func (c *Client) verifyLane(ctx context.Context, candidate lane, profile playerProfile) error {
	source := mediaSourceFor(candidate, profile)
	probeCtx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	return provider.VerifyLiveStream(probeCtx, c.httpClient, source)
}

// mediaSourceFor maps a lane into Kari's source vocabulary.
func mediaSourceFor(candidate lane, profile playerProfile) provider.MediaSource {
	source := provider.MediaSource{
		URL:     candidate.url,
		Quality: "Auto",
		Backend: profile.label,
		Type:    provider.SourceTypeHLS,
	}
	if profile.refererMode == refererEmbed && candidate.referer != "" {
		source.Referer = candidate.referer
		source.SuppressOrigin = true
	}
	return source
}

// resolveInnerFrame follows a nested iframe address when the current page has
// no playable stream of its own.
func resolveInnerFrame(html, baseURL string) string {
	src := firstGroup(reInnerIframe, html)
	if src == "" || strings.Contains(src, "javascript:") || strings.Contains(src, "about:") || strings.Contains(src, "data:") {
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(strings.TrimSpace(src))
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	return resolved.String()
}

// firstNonEmpty returns the first non-empty value.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
