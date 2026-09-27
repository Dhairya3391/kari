package anilight

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

type anilightWatchResp struct {
	ID       int                    `json:"id"`
	Episodes []anilightWatchEpisode `json:"episodes"`
}

type anilightWatchEpisode struct {
	Number   int               `json:"number"`
	Title    string            `json:"title"`
	EmbedURL map[string]string `json:"embed_url"`
}

type anilightSourcesResp struct {
	Sources  []anilightSource    `json:"sources"`
	Tracks   []kit.MegaplayTrack `json:"tracks"`
	Chapters []anilightChap      `json:"chapters"`
}

type anilightSource struct {
	URL     string `json:"url"`
	Quality string `json:"quality"`
}

type anilightChap struct {
	Title string  `json:"title"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// resolveDirectStreams attempts to resolve playable streams using direct Megaplay and AniLight APIs.
func (c *Client) resolveDirectStreams(ctx context.Context, anilistID string, category string, episodeNum int) ([]provider.MediaSource, error) {
	// 1. Resolve slug from AniList ID
	slug, err := c.resolveSlug(ctx, anilistID)
	if err != nil {
		return nil, fmt.Errorf("anilight resolve slug: %w", err)
	}

	// 2. Fetch watch data for slug
	watchData, err := c.fetchWatchData(ctx, slug)
	if err == nil && watchData.ID > 0 {
		// Try fetching direct sources from api.anilight.live/api/sources
		provs := []string{"mello", "ryu", "vid", "l"}
		for _, p := range provs {
			srcs, sErr := c.fetchSourcesForProvider(ctx, watchData.ID, episodeNum, category, p)
			if sErr == nil && len(srcs) > 0 {
				return srcs, nil
			}
		}

		// Also check if embed_url is present for direct Megaplay resolution
		foundEp := false
		for _, ep := range watchData.Episodes {
			if ep.Number == episodeNum {
				foundEp = true
				if embedURL, ok := ep.EmbedURL[category]; ok && embedURL != "" {
					sources, mErr := c.resolveMegaplayEmbed(ctx, embedURL)
					if mErr == nil && len(sources) > 0 {
						return sources, nil
					}
				}
			}
		}
		if foundEp {
			return nil, fmt.Errorf("anilight direct: no %s servers: %w", category, provider.ErrAudioUnavailable)
		}
	}

	return nil, provider.ErrNoResults
}

func (c *Client) resolveSlug(ctx context.Context, anilistID string) (string, error) {
	u := fmt.Sprintf("%s/anime/check-exists?anilistId=%s", c.apiBase, url.QueryEscape(anilistID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", "https://anilight.live/")
	req.Header.Set("Origin", "https://anilight.live")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		body, err := httpclient.ReadCapped(resp)
		if err == nil {
			var parsed struct {
				Exists bool   `json:"exists"`
				Slug   string `json:"slug"`
			}
			if err := json.Unmarshal(body, &parsed); err == nil && parsed.Slug != "" {
				return parsed.Slug, nil
			}
		}
	}

	return "", fmt.Errorf("slug not found for %s", anilistID)
}

func (c *Client) fetchWatchData(ctx context.Context, slug string) (*anilightWatchResp, error) {
	u := fmt.Sprintf("%s/watch/%s", c.apiBase, url.PathEscape(slug))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", "https://anilight.live/")
	req.Header.Set("Origin", "https://anilight.live")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, err
	}

	var data anilightWatchResp
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (c *Client) fetchSourcesForProvider(ctx context.Context, animeID, epNum int, category, providerID string) ([]provider.MediaSource, error) {
	u := fmt.Sprintf("%s/sources?id=%d&epNum=%d&type=%s&providerId=%s", c.apiBase, animeID, epNum, url.QueryEscape(category), url.QueryEscape(providerID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", "https://anilight.live/")
	req.Header.Set("Origin", "https://anilight.live")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, err
	}

	var parsed anilightSourcesResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	if len(parsed.Sources) == 0 {
		return nil, provider.ErrNoResults
	}

	var subs []provider.SubtitleOption
	for _, track := range parsed.Tracks {
		if strings.EqualFold(strings.TrimSpace(track.Kind), "thumbnails") ||
			strings.TrimSpace(track.File) == "" {
			continue
		}
		subs = append(subs, provider.SubtitleOption{
			URL:      track.File,
			Language: track.Label,
			Default:  track.Default,
		})
	}

	var sources []provider.MediaSource
	for _, s := range parsed.Sources {
		sURL := strings.TrimSpace(s.URL)
		if sURL == "" {
			continue
		}

		headers := map[string]string{
			"User-Agent": config.DesktopUserAgent,
		}
		var mpvArgs []string
		referer := "https://anilight.live/"

		if strings.Contains(sURL, "animegg.org") {
			referer = "https://www.animegg.org/"
			headers["Referer"] = referer
		} else if strings.Contains(sURL, "krussdomi.com") {
			referer = "https://krussdomi.com/"
			headers["Referer"] = referer
			headers["Origin"] = "https://krussdomi.com"
		} else if strings.Contains(sURL, "echovideo.to") || strings.Contains(sURL, "roburnt10.store") {
			headers["Referer"] = "https://anilight.live/"
			headers["Origin"] = "https://anilight.live"
			mpvArgs = []string{"--demuxer=lavf", "--demuxer-lavf-format=hls"}
		} else {
			headers["Referer"] = referer
		}

		streamType := provider.SourceTypeHLS
		if strings.Contains(sURL, ".mp4") {
			streamType = provider.SourceTypeMP4
		}

		quality := kit.QualityLabel(kit.Clean(s.Quality), streamType)
		quality = kit.TagQuality(quality, providerID, "anilight")

		sources = append(sources, provider.MediaSource{
			URL:       sURL,
			Quality:   quality,
			Referer:   referer,
			Type:      streamType,
			UserAgent: config.DesktopUserAgent,
			ExtraArgs: mpvArgs,
			Subtitles: subs,
		})
	}

	return sources, nil
}

func (c *Client) resolveMegaplayEmbed(ctx context.Context, embedURL string) ([]provider.MediaSource, error) {
	// Extract file ID from embedURL (e.g. https://megaplay.buzz/stream/s-2/107257/sub)
	parts := strings.Split(strings.TrimRight(embedURL, "/"), "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid embed url: %s", embedURL)
	}
	fileID := parts[len(parts)-2]
	if _, err := url.QueryUnescape(fileID); err != nil {
		fileID = parts[len(parts)-1]
	}

	parsedEmbed, err := url.Parse(embedURL)
	if err != nil {
		return nil, fmt.Errorf("parse embed url: %w", err)
	}
	// getSources lives on the embed host itself, so mirrors keep working.
	getSourcesURL := fmt.Sprintf("%s://%s/stream/getSources?id=%s", parsedEmbed.Scheme, parsedEmbed.Host, fileID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getSourcesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", embedURL)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("megaplay getSources status %d", resp.StatusCode)
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, err
	}

	var sr kit.MegaplaySourcesResp
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, err
	}

	if sr.Enc == "" {
		return nil, fmt.Errorf("empty enc token")
	}

	rawM3U8, err := kit.DecryptMegaplayEnc(sr.Enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt megaplay: %w", err)
	}

	signedM3U8 := kit.SignCDNURL(rawM3U8, 86400)

	var subs []provider.SubtitleOption
	for _, track := range sr.Tracks {
		if strings.EqualFold(strings.TrimSpace(track.Kind), "thumbnails") ||
			strings.TrimSpace(track.File) == "" {
			continue
		}
		subs = append(subs, provider.SubtitleOption{
			URL:      track.File,
			Language: track.Label,
			Default:  track.Default,
		})
	}

	source := provider.MediaSource{
		URL:       signedM3U8,
		Quality:   kit.TagQuality("1080p", "Megaplay", "anilight"),
		Referer:   parsedEmbed.Scheme + "://" + parsedEmbed.Host + "/",
		Type:      provider.SourceTypeHLS,
		UserAgent: config.DesktopUserAgent,
		Subtitles: subs,
	}

	return []provider.MediaSource{source}, nil
}
