package cno

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
)

// errNotOnServer marks a master playlist that the current Cno node does not
// host. The same title is usually present on another node listed alongside it,
// so resolution skips this master instead of surfacing a failure.
var errNotOnServer = errors.New("not on this server")

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

// detailResponse is the site's movie/episode source listing. The HLS entry
// carries the current server path; embeds in the same list are not direct
// streams and are ignored here.
type detailResponse struct {
	Sources []detailSource `json:"sources"`
}

type detailSource struct {
	Kind  string `json:"kind"`
	URL   string `json:"url"`
	Label string `json:"label"`
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
	// The HLS nodes reject requests that look cross-site from their own
	// origin: without this header every /api/s/<n> master answers 404 "Not on
	// this server", even for titles the listing just advertised.
	req.Header.Set("Sec-Fetch-Site", "same-origin")
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

	detail, detailErr := c.fetchDetail(ctx, tmdbID, mediaType, season, epNum)
	var (
		allSources []provider.MediaSource
		resolveErr error
	)
	if detailErr != nil {
		resolveErr = detailErr
	} else {
		for _, hls := range c.hlsSources(detail) {
			sources, err := c.fetchHLSSources(ctx, hls.url, hls.label)
			if errors.Is(err, errNotOnServer) || errors.Is(err, provider.ErrNoSources) {
				continue
			}
			allSources, resolveErr = sources, err
			break
		}
		if allSources == nil && resolveErr == nil {
			resolveErr = provider.ErrNoSources
		}
	}

	subWg.Wait()

	if len(allSources) == 0 {
		if resolveErr != nil {
			return resolveErr
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

// fetchDetail reads the site's own source listing so the current HLS path
// (including its version query) comes from the site instead of being guessed.
func (c *Client) fetchDetail(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, epNum int,
) (detailResponse, error) {
	var endpoint string
	if mediaType == provider.MediaTypeTV {
		endpoint = fmt.Sprintf("%s/api/tv/%d/episode/%d/%d", c.siteBase, tmdbID, season, epNum)
	} else {
		endpoint = fmt.Sprintf("%s/api/movie/%d", c.siteBase, tmdbID)
	}

	req, err := c.newRequest(ctx, endpoint)
	if err != nil {
		return detailResponse{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return detailResponse{}, fmt.Errorf("cno detail request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return detailResponse{}, fmt.Errorf("cno detail status %d", resp.StatusCode)
	}
	var detail detailResponse
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return detailResponse{}, fmt.Errorf("cno decode detail: %w", err)
	}
	return detail, nil
}

type discoveredSource struct {
	url   string
	label string
}

// hlsSources collects every direct HLS entry in listing order, resolving each
// site-relative path against the site base. The listing carries one entry per
// server, and a given title can be missing from several of them.
func (c *Client) hlsSources(detail detailResponse) []discoveredSource {
	base, err := url.Parse(c.siteBase + "/")
	if err != nil {
		return nil
	}
	var sources []discoveredSource
	for _, src := range detail.Sources {
		if src.Kind != "hls" || src.URL == "" {
			continue
		}
		ref, err := url.Parse(src.URL)
		if err != nil {
			continue
		}
		sources = append(sources, discoveredSource{url: base.ResolveReference(ref).String(), label: src.Label})
	}
	return sources
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

func (c *Client) fetchHLSSources(
	ctx context.Context,
	masterURL, label string,
) ([]provider.MediaSource, error) {
	req, err := c.newRequest(ctx, masterURL)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cno HLS request: %w", err)
	}

	// Surface transport/5xx failures as before; only 404 and not-on-server
	// bodies are skippable signals that another listed server may work.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		defer resp.Body.Close()
		return nil, fmt.Errorf("cno HLS status %d", resp.StatusCode)
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("cno read HLS: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound || bytes.Contains(body, []byte("Not on this server")) {
		return nil, errNotOnServer
	}

	variants, err := parseHLSVariants(bytes.NewReader(body), masterURL)
	if err != nil {
		return nil, fmt.Errorf("cno parse HLS variants: %w", err)
	}
	if len(variants) == 0 {
		return nil, provider.ErrNoSources
	}
	if label == "" {
		label = "Cno HLS"
	}

	var sources []provider.MediaSource
	for _, v := range variants {
		sources = append(sources, provider.MediaSource{
			URL:         v.URL,
			ManifestURL: masterURL,
			Quality:     v.Quality,
			Height:      v.Height,
			Bitrate:     v.Bandwidth,
			Backend:     label,
			Type:        provider.SourceTypeHLS,
			Referer:     c.siteBase + "/",
			UserAgent:   config.DesktopUserAgent,
			ExtraArgs:   []string{"--demuxer-lavf-o=extension_picky=0,allowed_segment_extensions=ALL"},
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
