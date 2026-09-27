package anicine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
)

var (
	reEmbedFileID    = regexp.MustCompile(`data-id="(\d+)"`)
	reEmbedFileIDAlt = regexp.MustCompile(`File\s+(\d+)\s+-`)
)

// malCacheTTL is long: MAL ids essentially never change, and caching them
// keeps resolves working through AniList stalls (its client retries can
// otherwise burn the whole provider deadline).
const malCacheTTL = 24 * time.Hour

// malEntry is a cached AniList → MAL id mapping.
type malEntry struct {
	malID    int
	cachedAt time.Time
}

// malCache maps AniList ID strings to MAL ids. sync.Map is safe for
// concurrent resolves without locking overhead on reads.
var malCache sync.Map // key: string anilistID → malEntry

// malID resolves an AniList ID to its MAL id, serving repeat titles from
// cache so only the first resolve of the day touches AniList.
func (c *Client) malID(ctx context.Context, anilistID string) (int, error) {
	idInt, err := strconv.Atoi(strings.TrimSpace(anilistID))
	if err != nil || idInt <= 0 {
		return 0, fmt.Errorf("anicine anime: invalid anilist ID %q", anilistID)
	}
	if v, ok := malCache.Load(anilistID); ok {
		if entry := v.(malEntry); time.Since(entry.cachedAt) < malCacheTTL && entry.malID > 0 {
			return entry.malID, nil
		}
	}
	media, err := anilist.FetchMediaWithEndpoint(ctx, c.http, idInt, c.gqlBase)
	if err != nil {
		return 0, fmt.Errorf("anicine anime: media lookup: %w", err)
	}
	if media.IDMal <= 0 {
		return 0, fmt.Errorf("anicine anime: no MAL id for anilist ID %s: %w", anilistID, provider.ErrNotFound)
	}
	malCache.Store(anilistID, malEntry{malID: media.IDMal, cachedAt: time.Now()})
	return media.IDMal, nil
}

// resolveAnime resolves one anime episode via its MAL id straight into a
// Megaplay embed: file id, getSources token, shared AES decrypt, CDN sign.
// This mirrors anicine's anicine-1 server without any site scraping.
func (c *Client) resolveAnime(ctx context.Context, anilistID, category string, epNum int) ([]provider.MediaSource, error) {
	malID, err := c.malID(ctx, anilistID)
	if err != nil {
		return nil, err
	}

	cat := strings.ToLower(strings.TrimSpace(category))
	if cat != "dub" {
		cat = "sub"
	}
	embedURL := fmt.Sprintf("%s/stream/mal/%d/%d/%s", c.embedBase, malID, epNum, cat)

	fileID, err := c.megaplayFileID(ctx, embedURL)
	if err != nil {
		return nil, fmt.Errorf("anicine anime: embed: %w", err)
	}

	parsed, err := url.Parse(embedURL)
	if err != nil {
		return nil, fmt.Errorf("anicine anime: parse embed: %w", err)
	}
	getSourcesURL := fmt.Sprintf("%s://%s/stream/getSources?id=%s", parsed.Scheme, parsed.Host, fileID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getSourcesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", embedURL)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anicine anime: getSources: %w", err)
	}
	body, err := httpclient.ReadCapped(resp)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("anicine anime: read sources: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: getSourcesURL}
	}

	var sr kit.MegaplaySourcesResp
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("anicine anime: decode sources: %w", err)
	}
	if sr.Enc == "" {
		return nil, provider.ErrNoSources
	}
	rawM3U8, err := kit.DecryptMegaplayEnc(sr.Enc)
	if err != nil {
		return nil, fmt.Errorf("anicine anime: decrypt: %w", err)
	}

	var subs []provider.SubtitleOption
	for _, track := range sr.Tracks {
		if strings.EqualFold(strings.TrimSpace(track.Kind), "thumbnails") ||
			strings.TrimSpace(track.File) == "" {
			continue
		}
		language := strings.TrimSpace(track.Label)
		if language == "" {
			language = "en"
		}
		subs = append(subs, provider.SubtitleOption{
			URL:      track.File,
			Language: language,
			Default:  track.Default,
		})
	}

	logging.Debug("resolve source done via megaplay", "provider", c.Name(), "mal", malID)
	return []provider.MediaSource{{
		URL:       kit.SignCDNURL(rawM3U8, 86400),
		Quality:   kit.TagQuality("1080p", "Megaplay", "anicine"),
		Referer:   parsed.Scheme + "://" + parsed.Host + "/",
		Type:      provider.SourceTypeHLS,
		UserAgent: config.DesktopUserAgent,
		Subtitles: subs,
	}}, nil
}

// megaplayFileID fetches an embed page and extracts the stream file id.
func (c *Client) megaplayFileID(ctx context.Context, embedURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, embedURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.embedBase+"/")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	body, err := httpclient.ReadCapped(resp)
	_ = resp.Body.Close()
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: embedURL}
	}
	if m := reEmbedFileID.FindSubmatch(body); len(m) >= 2 {
		return string(m[1]), nil
	}
	if m := reEmbedFileIDAlt.FindSubmatch(body); len(m) >= 2 {
		return string(m[1]), nil
	}
	// A 200 embed page explicitly titled as an error means the track does
	// not exist (e.g. unreleased dub) — verified absence, not drift.
	if bytes.Contains(body, []byte("<title>Error")) {
		return "", fmt.Errorf("no file id in embed page: %w", provider.ErrAudioUnavailable)
	}
	return "", fmt.Errorf("no file id in embed page")
}
