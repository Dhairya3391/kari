package cno

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

var (
	reResolution = regexp.MustCompile(`(?i)\bRESOLUTION=(\d+)x(\d+)\b`)
	reBandwidth  = regexp.MustCompile(`(?i)\bBANDWIDTH=(\d+)\b`)
)

type subListResponse struct {
	Files []subFileInfo `json:"files"`
}

type subFileInfo struct {
	ID    string `json:"id"`
	Lang  string `json:"lang"`
	Label string `json:"label"`
}

type hlsVariant struct {
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
		return nil, fmt.Errorf("cno request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.siteBase+"/")
	req.Header.Set("Accept", "application/json, text/plain, */*")
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
		subWg     sync.WaitGroup
		subtitles []provider.SubtitleOption
	)

	subWg.Add(1)
	go func() {
		defer subWg.Done()
		var err error
		subtitles, err = c.fetchSubtitles(ctx, tmdbID, mediaType, season, epNum)
		if err != nil {
			logging.Debug("cno subtitles failed", "tmdb", tmdbID, "err", err)
		}
	}()

	var targetPath string
	if mediaType == provider.MediaTypeTV {
		targetPath = fmt.Sprintf("tv/%d/%d/%d", tmdbID, season, epNum)
	} else {
		targetPath = fmt.Sprintf("movie/%d", tmdbID)
	}

	var allSources []provider.MediaSource

	// 1. Fetch Server 20 (Direct HLS on v.cno.to / cno.to)
	s20URL := fmt.Sprintf("%s/api/s/20/%s", c.siteBase, targetPath)
	s20Sources, err20 := c.fetchServer20(ctx, s20URL)
	if err20 == nil && len(s20Sources) > 0 {
		allSources = append(allSources, s20Sources...)
	} else {
		logging.Debug("cno server 20 fetch failed", "url", s20URL, "err", err20)
	}

	// 2. Fetch Server 21 (Vault CDN network)
	s21URL := fmt.Sprintf("%s/api/s/21/%s?v=3", c.siteBase, targetPath)
	s21Sources, err21 := c.fetchServer21(ctx, s21URL)
	if err21 == nil && len(s21Sources) > 0 {
		allSources = append(allSources, s21Sources...)
	} else {
		logging.Debug("cno server 21 fetch failed", "url", s21URL, "err", err21)
	}

	subWg.Wait()

	if len(allSources) == 0 {
		if err20 != nil {
			return err20
		}
		if err21 != nil {
			return err21
		}
		return provider.ErrNoSources
	}

	for i := range allSources {
		allSources[i].Subtitles = subtitles
	}

	if updates != nil {
		updates <- allSources
	}

	return nil
}

func (c *Client) fetchSubtitles(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, epNum int,
) ([]provider.SubtitleOption, error) {
	var endpoint string
	if mediaType == provider.MediaTypeTV {
		endpoint = fmt.Sprintf("%s/api/subs/list/tv/%d/%d/%d", c.siteBase, tmdbID, season, epNum)
	} else {
		endpoint = fmt.Sprintf("%s/api/subs/list/movie/%d", c.siteBase, tmdbID)
	}

	req, err := c.newRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cno subtitle list status %d", resp.StatusCode)
	}

	var data subListResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("cno decode subtitles: %w", err)
	}

	subType := "movie"
	subID := strconv.Itoa(tmdbID)
	if mediaType == provider.MediaTypeTV {
		subType = "tv"
		subID = fmt.Sprintf("%d/%d/%d", tmdbID, season, epNum)
	}

	seen := make(map[string]bool)
	var options []provider.SubtitleOption
	for _, f := range data.Files {
		normalizedLang := lang.Normalize(f.Lang)
		if normalizedLang == "" {
			normalizedLang = f.Lang
		}
		if seen[normalizedLang] {
			continue
		}
		seen[normalizedLang] = true

		fileURL := fmt.Sprintf("%s/api/subs/file/%s/%s/%s.vtt", c.siteBase, subType, subID, f.ID)
		options = append(options, provider.SubtitleOption{
			URL:      fileURL,
			Language: normalizedLang,
			Default:  normalizedLang == "en",
			Referer:  c.siteBase + "/",
		})
	}

	return options, nil
}

func (c *Client) fetchServer20(
	ctx context.Context,
	masterURL string,
) ([]provider.MediaSource, error) {
	req, err := c.newRequest(ctx, masterURL)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server 20 status %d", resp.StatusCode)
	}

	variants, err := parseHLSVariants(resp.Body, masterURL)
	if err != nil {
		return nil, fmt.Errorf("server 20 parse variants: %w", err)
	}
	if len(variants) > 0 {
		var sources []provider.MediaSource
		for _, v := range variants {
			sources = append(sources, provider.MediaSource{
				URL:            v.URL,
				Quality:        v.Quality,
				Type:           provider.SourceTypeHLS,
				Referer:        c.siteBase + "/",
				UserAgent:      config.DesktopUserAgent,
				SuppressOrigin: true,
			})
		}
		return sources, nil
	}

	quality := kit.DetectHLSQuality(ctx, c.httpClient, masterURL, map[string]string{
		"Referer": c.siteBase + "/",
	})
	if quality == "auto" {
		quality = "1080p"
	}

	return []provider.MediaSource{
		{
			URL:            masterURL,
			Quality:        quality,
			Type:           provider.SourceTypeHLS,
			Referer:        c.siteBase + "/",
			UserAgent:      config.DesktopUserAgent,
			SuppressOrigin: true,
		},
	}, nil
}

func (c *Client) fetchServer21(
	ctx context.Context,
	masterURL string,
) ([]provider.MediaSource, error) {
	req, err := c.newRequest(ctx, masterURL)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server 21 status %d", resp.StatusCode)
	}

	variants, err := parseHLSVariants(resp.Body, masterURL)
	if err != nil {
		return nil, fmt.Errorf("server 21 parse variants: %w", err)
	}
	if len(variants) == 0 {
		return nil, provider.ErrNoSources
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
			ExtraArgs:      []string{"--demuxer-lavf-o=extension_picky=0,allowed_segment_extensions=ALL"},
		})
	}

	return sources, nil
}

func parseHLSVariants(r io.Reader, baseURL string) ([]hlsVariant, error) {
	scanner := bufio.NewScanner(r)
	var (
		variants    []hlsVariant
		lastWidth   int
		lastHeight  int
		lastBandw   int
		inStreamInf bool
	)

	baseParsed, _ := url.Parse(baseURL)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			inStreamInf = true
			lastWidth, lastHeight, lastBandw = 0, 0, 0

			if m := reResolution.FindStringSubmatch(line); len(m) == 3 {
				lastWidth, _ = strconv.Atoi(m[1])
				lastHeight, _ = strconv.Atoi(m[2])
			}
			if m := reBandwidth.FindStringSubmatch(line); len(m) == 2 {
				lastBandw, _ = strconv.Atoi(m[1])
			}
			continue
		}

		if inStreamInf && !strings.HasPrefix(line, "#") {
			inStreamInf = false
			dim := max(lastWidth, lastHeight)
			quality := "1080p"
			switch {
			case dim >= 3800:
				quality = "2160p"
			case dim >= 2500:
				quality = "1440p"
			case dim >= 1700:
				quality = "1080p"
			case dim >= 1100:
				quality = "720p"
			case dim >= 700:
				quality = "480p"
			case dim > 0:
				quality = "360p"
			}

			urlStr := line
			if baseParsed != nil {
				if refParsed, err := url.Parse(line); err == nil {
					urlStr = baseParsed.ResolveReference(refParsed).String()
				}
			}

			variants = append(variants, hlsVariant{
				URL:       urlStr,
				Quality:   quality,
				Bandwidth: lastBandw,
				Height:    lastHeight,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Sort highest bandwidth/resolution first
	sort.Slice(variants, func(i, j int) bool {
		if variants[i].Height != variants[j].Height {
			return variants[i].Height > variants[j].Height
		}
		return variants[i].Bandwidth > variants[j].Bandwidth
	})

	return variants, nil
}
