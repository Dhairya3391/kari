package anistream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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

// server is one catalogue streaming backend for an episode's audio track.
type server struct {
	id      string
	def     bool
	subType string // provider.SubTypeHard | provider.SubTypeSoft | ""
}

// sourceItem is one raw stream entry from a provider's sources response.
type sourceItem struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

// trackItem is one subtitle/caption track from a sources response.
type trackItem struct {
	File    string `json:"file"`
	URL     string `json:"url"`
	Label   string `json:"label"`
	Lang    string `json:"lang"`
	Kind    string `json:"kind"`
	Default bool   `json:"default"`
}

var reTokenURL = regexp.MustCompile(`token=`)

// catalogID maps an AniList ID to the catalogue's internal id via the
// catalogue GraphQL API, caching the stable mapping.
func (c *Client) catalogID(ctx context.Context, anilistID string) (string, error) {
	idInt, err := strconv.Atoi(strings.TrimSpace(anilistID))
	if err != nil || idInt <= 0 {
		return "", fmt.Errorf("invalid anilist ID %q", anilistID)
	}

	c.mu.Lock()
	if e, ok := c.catalogs[anilistID]; ok && time.Since(e.cachedAt) < catalogTTL*time.Second {
		c.mu.Unlock()
		return e.catalogID, nil
	}
	c.mu.Unlock()

	const gql = `query AnimeDetailBase($anilistId: Int) { anime(anilistId: $anilistId) { id anilistId } }`
	body, err := gqlPost(ctx, c.http, c.gqlBase, gql, map[string]any{"anilistId": idInt})
	if err != nil {
		return "", fmt.Errorf("catalogue lookup: %w", err)
	}
	var parsed struct {
		Data struct {
			Anime *struct {
				ID string `json:"id"`
			} `json:"anime"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("catalogue lookup: decode: %w", err)
	}
	if parsed.Data.Anime == nil || strings.TrimSpace(parsed.Data.Anime.ID) == "" {
		return "", fmt.Errorf("catalogue lookup: no entry for %s: %w", anilistID, provider.ErrNotFound)
	}

	c.mu.Lock()
	c.catalogs[anilistID] = catalogEntry{catalogID: parsed.Data.Anime.ID, cachedAt: time.Now()}
	c.mu.Unlock()
	return parsed.Data.Anime.ID, nil
}

// gqlPost encodes a GraphQL request, POSTs it, and returns raw bytes.
func gqlPost(ctx context.Context, hc *http.Client, endpoint, gql string, vars map[string]any) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{"query": gql, "variables": vars})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)

	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// restGet GETs one REST path with query params, trying each API base.
func (c *Client) restGet(ctx context.Context, path string, params map[string]string) ([]byte, error) {
	var errs []string
	for _, base := range c.bases {
		u, err := url.Parse(strings.TrimRight(base, "/") + path)
		if err != nil {
			continue
		}
		q := u.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", config.DesktopUserAgent)
		req.Header.Set("Referer", siteReferer)
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			errs = append(errs, base+" -> "+err.Error())
			continue
		}
		body, err := httpclient.ReadCapped(resp)
		status := resp.StatusCode
		_ = resp.Body.Close()
		if err != nil {
			errs = append(errs, base+" -> read: "+err.Error())
			continue
		}
		if status != http.StatusOK {
			errs = append(errs, fmt.Sprintf("%s -> HTTP %d", base, status))
			continue
		}
		return body, nil
	}
	return nil, fmt.Errorf("anistream REST %s failed: %s", path, strings.Join(errs, "; "))
}

// servers lists the episode's backends for one audio track. The opposite
// track is never substituted: a missing dub list fails loudly instead of
// silently playing sub audio.
func (c *Client) servers(ctx context.Context, catalogID string, epNum int, category string) ([]server, error) {
	body, err := c.restGet(ctx, "/rest/api/servers", map[string]string{
		"id": catalogID, "epNum": strconv.Itoa(epNum),
	})
	if err != nil {
		return nil, fmt.Errorf("anistream servers: %w", err)
	}
	var parsed struct {
		SubProviders []serverJSON `json:"subProviders"`
		DubProviders []serverJSON `json:"dubProviders"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("anistream servers: decode: %w", err)
	}
	list := parsed.SubProviders
	if category == "dub" {
		list = parsed.DubProviders
	}
	out := make([]server, 0, len(list))
	for _, s := range list {
		if strings.TrimSpace(s.ID) == "" || unsupportedAnistreamServer(s.ID) {
			continue
		}
		out = append(out, server{id: s.ID, def: s.Default, subType: subTypeForServer(s.ID, s.Tip)})
	}
	return out, nil
}

type serverJSON struct {
	ID      string `json:"id"`
	Default bool   `json:"default"`
	Tip     string `json:"tip"`
}

// subTypeOf maps the upstream server tip ("Hard sub, Fast") to a subtitle
// kind; unknown tips stay blank.
func subTypeOf(tip string) string {
	lower := strings.ToLower(tip)
	switch {
	case strings.Contains(lower, "hard sub"):
		return provider.SubTypeHard
	case strings.Contains(lower, "soft sub"):
		return provider.SubTypeSoft
	default:
		return ""
	}
}

func unsupportedAnistreamServer(serverID string) bool {
	switch strings.ToLower(strings.TrimSpace(serverID)) {
	case "sora", "uwu":
		return true
	default:
		return false
	}
}

func subTypeForServer(serverID, tip string) string {
	declared := subTypeOf(tip)
	if declared != provider.SubTypeHard {
		return declared
	}
	if strings.EqualFold(strings.TrimSpace(serverID), "loli") {
		return provider.SubTypeHard
	}
	return ""
}

// resolveServer resolves one backend into direct streams plus subtitles.
// Token/poisoned URLs needing server-side rewriting are dropped: kari
// hosts no proxy, and playback fallback covers the remaining servers.
func (c *Client) resolveServer(ctx context.Context, catalogID string, epNum int, category string, srv server) []provider.MediaSource {
	body, err := c.restGet(ctx, "/rest/api/sources", map[string]string{
		"id": catalogID, "epNum": strconv.Itoa(epNum), "type": category,
		"providerId": srv.id, "_cb": strconv.FormatInt(time.Now().UnixMilli(), 10),
	})
	if err != nil {
		logging.Debug("anistream provider failed", "server", srv.id, "err", err)
		return nil
	}
	var parsed struct {
		Sources []sourceItem      `json:"sources"`
		Tracks  []trackItem       `json:"tracks"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		logging.Debug("anistream provider decode failed", "server", srv.id, "err", err)
		return nil
	}

	referer := ""
	userAgent := config.DesktopUserAgent
	origin := ""
	if parsed.Headers != nil {
		referer = strings.TrimSpace(parsed.Headers["Referer"])
		if ua := strings.TrimSpace(parsed.Headers["User-Agent"]); ua != "" {
			userAgent = ua
		}
		origin = strings.TrimSpace(parsed.Headers["Origin"])
	}
	if referer == "" {
		referer = siteReferer
	}

	var subs []provider.SubtitleOption
	seenSubs := make(map[string]struct{})
	for _, t := range parsed.Tracks {
		if strings.EqualFold(strings.TrimSpace(t.Kind), "thumbnails") {
			continue
		}
		file := strings.TrimSpace(t.File)
		if file == "" {
			file = strings.TrimSpace(t.URL)
		}
		if file == "" {
			continue
		}
		if _, ok := seenSubs[file]; ok {
			continue
		}
		seenSubs[file] = struct{}{}
		name := lang.Normalize(strings.TrimSpace(t.Lang))
		if name == "" {
			name = lang.Normalize(strings.TrimSpace(t.Label))
		}
		if name == "" {
			name = "en"
		}
		subs = append(subs, provider.SubtitleOption{
			URL:      file,
			Language: name,
			Default:  t.Default,
		})
	}

	subType := srv.subType
	if len(subs) > 0 {
		subType = provider.SubTypeSoft
	}

	var sources []provider.MediaSource
	for _, raw := range parsed.Sources {
		streamURL := strings.TrimSpace(raw.URL)
		if streamURL == "" {
			continue
		}
		if strings.EqualFold(srv.id, "yuki") || reTokenURL.MatchString(streamURL) {
			continue // poisoned without server-side rewriting
		}
		quality := c.detectQuality(ctx, streamURL, referer, userAgent)
		quality = kit.TagQuality(quality, srv.id, "anistream")

		var extraArgs []string
		if origin != "" {
			extraArgs = append(extraArgs, "--http-header-fields=Origin: "+origin)
		}
		sources = append(sources, provider.MediaSource{
			URL:       streamURL,
			Quality:   quality,
			Referer:   referer,
			Type:      streamTypeOf(streamURL),
			UserAgent: userAgent,
			ExtraArgs: extraArgs,
			Subtitles: subs,
			SubType:   subType,
		})
	}
	return sources
}

// detectQuality labels a master playlist via the shared HLS probe.
func (c *Client) detectQuality(ctx context.Context, masterURL, referer, userAgent string) string {
	headers := map[string]string{}
	if strings.TrimSpace(referer) != "" {
		headers["Referer"] = referer
	}
	return kit.DetectHLSQuality(ctx, c.http, masterURL, headers)
}

// streamTypeOf classifies a stream URL for the player layer.
func streamTypeOf(rawURL string) string {
	lower := strings.ToLower(rawURL)
	switch {
	case strings.Contains(lower, ".m3u8"):
		return provider.SourceTypeHLS
	case strings.Contains(lower, ".mp4") || strings.Contains(lower, ".mkv"):
		return provider.SourceTypeMP4
	default:
		return provider.SourceTypeHLS
	}
}
