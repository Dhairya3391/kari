package cinecat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
)

var (
	reResolution        = regexp.MustCompile(`(?i)\bRESOLUTION=(\d+)x(\d+)\b`)
	reBandwidth         = regexp.MustCompile(`(?i)\bBANDWIDTH=(\d+)\b`)
	reSubtitleLabelTrim = regexp.MustCompile(`(?i)\s*(?:hi\d*|\d+)$`)
)

type zcdnResponse struct {
	Ready  bool   `json:"ready"`
	URL    string `json:"url"`
	Status string `json:"status,omitempty"`
}

type variantStream struct {
	URL       string
	Quality   string
	Bandwidth int
	Height    int
}

func parseCoordinates(mediaID string, episode provider.Episode) (int, string, int, int, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %w", err)
		}
		if tmdbID <= 0 {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %q", mediaID)
		}
	}

	mediaType := provider.MediaTypeMovie
	season, epNum := 1, 1
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = provider.MediaTypeTV
		if episode.Season > 0 {
			season = episode.Season
		}
		if episode.Episode > 0 {
			epNum = episode.Episode
		}
	}
	return tmdbID, mediaType, season, epNum, nil
}

func (c *Client) newRequest(ctx context.Context, target string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("cinecat request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.siteBase+"/")
	req.Header.Set("Origin", c.siteBase)
	return req, nil
}

func (c *Client) fetchSources(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, epNum int,
	updates chan<- []provider.MediaSource,
) error {
	var (
		wg        sync.WaitGroup
		masterURL string
		zcdnErr   error
		subtitles []provider.SubtitleOption
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		masterURL, zcdnErr = c.fetchZCDN(ctx, tmdbID, mediaType, season, epNum)
	}()

	go func() {
		defer wg.Done()
		var err error
		subtitles, err = c.fetchSubtitles(ctx, tmdbID, mediaType, season, epNum)
		if err != nil {
			logging.Debug("cinecat subtitles failed", "tmdb", tmdbID, "err", err)
		}
	}()

	wg.Wait()

	if zcdnErr != nil {
		return zcdnErr
	}
	if strings.TrimSpace(masterURL) == "" {
		return provider.ErrNoSources
	}

	variants, err := c.fetchVariants(ctx, masterURL)
	if err != nil {
		logging.Debug("cinecat variant parsing failed; falling back to master", "url", masterURL, "err", err)
	}

	var sources []provider.MediaSource
	for _, v := range variants {
		sources = append(sources, provider.MediaSource{
			URL:            v.URL,
			Quality:        v.Quality,
			Type:           provider.SourceTypeHLS,
			Referer:        c.siteBase + "/",
			UserAgent:      config.DesktopUserAgent,
			SuppressOrigin: true,
			Subtitles:      subtitles,
		})
	}

	// Always offer the adaptive master playlist alongside individual tiers.
	sources = append(sources, provider.MediaSource{
		URL:            masterURL,
		Quality:        "Auto (Adaptive)",
		Type:           provider.SourceTypeHLS,
		Referer:        c.siteBase + "/",
		UserAgent:      config.DesktopUserAgent,
		SuppressOrigin: true,
		Subtitles:      subtitles,
	})

	select {
	case updates <- sources:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) fetchZCDN(ctx context.Context, tmdbID int, mediaType string, season, epNum int) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("tmdb", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	if mediaType == provider.MediaTypeTV {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(epNum))
	}

	target := fmt.Sprintf("%s/cdn?%s", c.cdnBase, params.Encode())
	req, err := c.newRequest(reqCtx, target)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cinecat cdn: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		// 202 Accepted signifies stream is currently being transcoded/built upstream.
		return "", provider.ErrNoSources
	}
	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	var data zcdnResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("cinecat decode: %w", err)
	}

	if !data.Ready || strings.TrimSpace(data.URL) == "" {
		return "", provider.ErrNoSources
	}
	return data.URL, nil
}

func resolutionLabel(height int) string {
	switch {
	case height >= 2160:
		return "4K (2160p)"
	case height >= 1440:
		return "1440p"
	case height >= 1080:
		return "1080p"
	case height >= 720:
		return "720p"
	case height >= 480:
		return "480p"
	case height >= 360:
		return "360p"
	default:
		return fmt.Sprintf("%dp", height)
	}
}

func (c *Client) fetchVariants(ctx context.Context, masterURL string) ([]variantStream, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := c.newRequest(reqCtx, masterURL)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch master: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: masterURL}
	}

	baseURL, err := url.Parse(masterURL)
	if err != nil {
		return nil, fmt.Errorf("parse master url: %w", err)
	}

	var (
		scanner   = bufio.NewScanner(resp.Body)
		variants  []variantStream
		curHeight int
		curBW     int
		hasStream bool
	)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			hasStream = true
			curHeight = 0
			curBW = 0

			if m := reResolution.FindStringSubmatch(line); len(m) == 3 {
				if h, err := strconv.Atoi(m[2]); err == nil {
					curHeight = h
				}
			}
			if m := reBandwidth.FindStringSubmatch(line); len(m) == 2 {
				if bw, err := strconv.Atoi(m[1]); err == nil {
					curBW = bw
				}
			}
			continue
		}

		if hasStream && !strings.HasPrefix(line, "#") {
			hasStream = false
			refURL, err := url.Parse(line)
			if err != nil {
				continue
			}
			fullURL := baseURL.ResolveReference(refURL).String()
			label := resolutionLabel(curHeight)
			if curHeight == 0 {
				label = "1080p"
			}

			variants = append(variants, variantStream{
				URL:       fullURL,
				Quality:   label,
				Bandwidth: curBW,
				Height:    curHeight,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan playlist: %w", err)
	}

	// Sort variants highest resolution and bandwidth first.
	sort.Slice(variants, func(i, j int) bool {
		if variants[i].Height != variants[j].Height {
			return variants[i].Height > variants[j].Height
		}
		return variants[i].Bandwidth > variants[j].Bandwidth
	})

	return variants, nil
}

func (c *Client) fetchSubtitles(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, epNum int,
) ([]provider.SubtitleOption, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var target string
	if mediaType == provider.MediaTypeTV {
		target = fmt.Sprintf("%s/v1/tv/%d/%d/%d", c.subsBase, tmdbID, season, epNum)
	} else {
		target = fmt.Sprintf("%s/v1/movie/%d", c.subsBase, tmdbID)
	}

	req, err := c.newRequest(reqCtx, target)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	var items []struct {
		Label string `json:"label"`
		File  string `json:"file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("granite decode: %w", err)
	}

	var out []provider.SubtitleOption
	seen := make(map[string]bool)

	for _, item := range items {
		u := strings.TrimSpace(item.File)
		if u == "" {
			continue
		}
		clean := strings.TrimSpace(reSubtitleLabelTrim.ReplaceAllString(item.Label, ""))
		norm := lang.Normalize(clean)
		if norm == "" {
			norm = lang.Normalize(item.Label)
		}
		if norm == "" {
			norm = "und"
		}

		key := u + ":" + norm
		if seen[key] {
			continue
		}
		seen[key] = true

		out = append(out, provider.SubtitleOption{
			URL:      u,
			Language: norm,
		})
	}

	return out, nil
}
