package bingr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
	"kari/internal/timeouts"
)

const (
	// metaTimeout bounds the metadata lookup that must finish before any
	// shard can start: a stalled details endpoint used to hold the whole
	// fan-out behind the service's much longer deadline.
	metaTimeout = timeouts.BingrMeta
	// fanoutTimeout bounds the whole shard fan-out. Healthy shards answer
	// within three seconds; a longer window only holds the provider spinner.
	fanoutTimeout = timeouts.BingrFanout
	// shardTimeout bounds one shard request inside the fan-out so a single
	// stalled shard cannot consume the entire budget.
	shardTimeout = timeouts.BingrShard
)

type serverSpec struct {
	id      string
	name    string
	country string
}

// activeServers lists only shards that currently serve stream responses.
// Moscow (s80) and Novo (s81) were retired upstream and answer every request
// with HTTP 500, so probing them only added failures and spinner wait time.
var activeServers = []serverSpec{
	{id: "s40", name: "Aphelion", country: "GL"},
	{id: "s70", name: "Polaris", country: "US"},
	{id: "s62", name: "Bastion", country: "IN"},
}

type streamRequestBody struct {
	Srv   string             `json:"srv"`
	T     string             `json:"t"`
	ID    string             `json:"id"`
	Query streamRequestQuery `json:"query"`
}

type streamRequestQuery struct {
	Title   string `json:"title,omitempty"`
	Year    string `json:"year,omitempty"`
	Season  string `json:"season,omitempty"`
	Episode string `json:"episode,omitempty"`
}

type streamResponse struct {
	ScraperName string         `json:"scraperName"`
	Sources     []streamItem   `json:"sources"`
	Subtitles   []subtitleItem `json:"subtitles"`
	Error       string         `json:"error,omitempty"`
}

type streamItem struct {
	URL     string            `json:"url"`
	Quality string            `json:"quality"`
	Type    string            `json:"type"`
	Label   string            `json:"label"`
	Name    string            `json:"name"`
	Headers map[string]string `json:"headers"`
}

type subtitleItem struct {
	URL   string `json:"url"`
	Label string `json:"label"`
	Lang  string `json:"lang"`
}

type detailsResponse struct {
	ID    int    `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Year  any    `json:"year"`
}

func (c *Client) resolveDirect(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	tmdbID, mediaType, season, epNum, err := parseEpisodeCoords(mediaID, episode)
	if err != nil {
		return err
	}

	title, year := c.findMeta(ctx, mediaType, tmdbID, episode.Title)

	var wg sync.WaitGroup
	var mu sync.Mutex
	totalSources := 0
	var lastErr error

	fanoutCtx, cancel := context.WithTimeout(ctx, fanoutTimeout)
	defer cancel()

	for _, srv := range activeServers {
		wg.Add(1)
		go func(srv serverSpec) {
			defer wg.Done()
			reqCtx, reqCancel := context.WithTimeout(fanoutCtx, shardTimeout)
			defer reqCancel()

			sources, err := c.fetchServerStreams(reqCtx, srv, mediaType, tmdbID, season, epNum, title, year)
			if err != nil {
				logging.Debug("bingr server failed", "srv", srv.id, "name", srv.name, "err", err)
				mu.Lock()
				lastErr = err
				mu.Unlock()
				return
			}
			if len(sources) > 0 {
				mu.Lock()
				totalSources += len(sources)
				mu.Unlock()
				if updates != nil {
					select {
					case updates <- sources:
					case <-ctx.Done():
					}
				}
			}
		}(srv)
	}

	wg.Wait()

	if totalSources == 0 {
		if lastErr != nil {
			return fmt.Errorf("bingr resolve: %w", errors.Join(provider.ErrNoSources, lastErr))
		}
		return provider.ErrNoSources
	}
	return nil
}

func (c *Client) fetchServerStreams(
	ctx context.Context,
	srv serverSpec,
	mediaType string,
	tmdbID, season, epNum int,
	title, year string,
) ([]provider.MediaSource, error) {
	// Aphelion (s40) has a dedicated TV endpoint that resolves directly.
	if srv.id == "s40" && mediaType == provider.MediaTypeTV && season > 0 && epNum > 0 {
		sources, err := c.fetchAphelionTV(ctx, tmdbID, season, epNum)
		if err == nil && len(sources) > 0 {
			return sources, nil
		}
	}

	query := streamRequestQuery{
		Title: title,
		Year:  year,
	}
	if mediaType == provider.MediaTypeTV {
		query.Season = strconv.Itoa(season)
		query.Episode = strconv.Itoa(epNum)
	}

	payload := streamRequestBody{
		Srv:   srv.id,
		T:     mediaType,
		ID:    strconv.Itoa(tmdbID),
		Query: query,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	reqURL := fmt.Sprintf("%s/stream", c.apiBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Origin", config.BingrOrigin)
	req.Header.Set("Referer", config.BingrReferer)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: reqURL}
	}

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if sr.Error != "" {
		return nil, errors.New(sr.Error)
	}

	return c.mapStreamResponse(sr, srv.name)
}

func (c *Client) fetchAphelionTV(ctx context.Context, tmdbID, season, epNum int) ([]provider.MediaSource, error) {
	reqURL := fmt.Sprintf("%s/stream/aphelion-tv/%d/%d/%d", c.apiBase, tmdbID, season, epNum)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Origin", config.BingrOrigin)
	req.Header.Set("Referer", config.BingrReferer)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: reqURL}
	}

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("decode aphelion tv: %w", err)
	}
	return c.mapStreamResponse(sr, "Aphelion")
}

func (c *Client) mapStreamResponse(sr streamResponse, serverName string) ([]provider.MediaSource, error) {
	if len(sr.Sources) == 0 {
		return nil, nil
	}

	subs := make([]provider.SubtitleOption, 0, len(sr.Subtitles))
	seenSub := make(map[string]bool)
	for _, sub := range sr.Subtitles {
		subURL := strings.TrimSpace(sub.URL)
		if subURL == "" {
			continue
		}
		normLang := lang.FromCodeOrLabel(sub.Lang, sub.Label)
		key := normLang + ":" + subURL
		if seenSub[key] {
			continue
		}
		seenSub[key] = true
		subs = append(subs, provider.SubtitleOption{
			URL:      subURL,
			Language: normLang,
			Referer:  config.BingrReferer,
		})
	}
	scraperLabel := sr.ScraperName
	if scraperLabel == "" {
		scraperLabel = serverName
	}

	var out []provider.MediaSource
	for _, s := range sr.Sources {
		rawURL := strings.TrimSpace(s.URL)
		if rawURL == "" {
			continue
		}

		quality := strings.TrimSpace(s.Quality)
		if quality == "" {
			quality = "HD"
		}
		if bracket := strings.Index(quality, "["); bracket >= 0 {
			quality = strings.TrimSpace(quality[:bracket])
		}
		height := kit.QualityHeight(quality)
		ref := config.BingrReferer
		if r, ok := s.Headers["Referer"]; ok && r != "" {
			ref = r
		}
		ua := ""
		if u, ok := s.Headers["User-Agent"]; ok && u != "" {
			ua = u
		}

		srcType := provider.SourceTypeHLS
		if strings.Contains(s.Type, "mp4") || strings.HasSuffix(rawURL, ".mp4") {
			srcType = provider.SourceTypeMP4
		}

		// Stream names and labels are not a reliable audio language source;
		// leave the typed audio identity unknown rather than guessing one.
		out = append(out, provider.MediaSource{
			URL:       rawURL,
			Quality:   quality,
			Backend:   scraperLabel,
			Type:      srcType,
			Referer:   ref,
			UserAgent: ua,
			Height:    height,
			Subtitles: subs,
		})
	}

	return out, nil
}

type metaCacheEntry struct {
	title    string
	year     string
	cachedAt time.Time
}

func (c *Client) findMeta(ctx context.Context, mediaType string, tmdbID int, defaultTitle string) (string, string) {
	if tmdbID <= 0 {
		return defaultTitle, ""
	}
	cacheKey := mediaType + ":" + strconv.Itoa(tmdbID)
	if val, ok := c.metaCache.Load(cacheKey); ok {
		if entry, ok := val.(metaCacheEntry); ok && time.Since(entry.cachedAt) < 24*time.Hour {
			return entry.title, entry.year
		}
	}
	metaCtx, cancel := context.WithTimeout(ctx, metaTimeout)
	defer cancel()
	reqURL := fmt.Sprintf("%s/details/%s/%d?v=1", c.apiBase, mediaType, tmdbID)
	req, err := http.NewRequestWithContext(metaCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return defaultTitle, ""
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", config.BingrReferer)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return defaultTitle, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return defaultTitle, ""
	}

	var dt detailsResponse
	if err := json.NewDecoder(resp.Body).Decode(&dt); err != nil {
		return defaultTitle, ""
	}

	title := strings.TrimSpace(dt.Title)
	if title == "" {
		title = defaultTitle
	}

	var year string
	switch y := dt.Year.(type) {
	case string:
		year = y
	case float64:
		year = strconv.Itoa(int(y))
	case int:
		year = strconv.Itoa(y)
	}

	c.metaCache.Store(cacheKey, metaCacheEntry{
		title:    title,
		year:     year,
		cachedAt: time.Now(),
	})

	return title, year
}

func parseEpisodeCoords(mediaID string, episode provider.Episode) (int, string, int, int, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID %q: %w", mediaID, err)
		}
		if tmdbID <= 0 {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %q", mediaID)
		}
	}

	mediaType := provider.MediaTypeMovie
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = provider.MediaTypeTV
	}
	season := episode.Season
	if mediaType == provider.MediaTypeTV && season <= 0 {
		season = 1
	}
	epNum := episode.Episode
	if mediaType == provider.MediaTypeTV && epNum <= 0 {
		epNum = 1
	}
	return tmdbID, mediaType, season, epNum, nil
}
