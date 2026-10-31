package bingr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
)

type animeSearchResponse struct {
	Page    int               `json:"page"`
	Results []animeSearchItem `json:"results"`
}

type animeSearchItem struct {
	ID          int    `json:"id"`
	IDMal       int    `json:"idMal"`
	Title       string `json:"title"`
	TitleNative string `json:"title_native"`
	Year        any    `json:"year"`
	Poster      string `json:"poster"`
	Format      string `json:"format"`
	Episodes    int    `json:"episodes"`
}

type animeMetaResponse struct {
	ID          int    `json:"id"`
	IDMal       int    `json:"idMal"`
	Title       string `json:"title"`
	TitleNative string `json:"title_native"`
	Year        any    `json:"year"`
	Format      string `json:"format"`
	Episodes    int    `json:"episodes"`
	TMDBID      int    `json:"tmdb_id"`
	TMDBType    string `json:"tmdb_type"`
}

type megaplayResponse struct {
	Streams []megaplayStream `json:"streams"`
}

type megaplayStream struct {
	Server    string              `json:"server"`
	URL       string              `json:"url"`
	Type      string              `json:"type"`
	Referer   string              `json:"referer"`
	DubType   string              `json:"dubType"`
	Subtitles []megaplaySubOption `json:"subtitles"`
}

type megaplaySubOption struct {
	Label string `json:"label"`
	Lang  string `json:"lang"`
	URL   string `json:"url"`
}

func (c *Client) searchAnimeBingr(ctx context.Context, query string) ([]provider.SearchResult, error) {
	reqURL := fmt.Sprintf("%s/anime/search?q=%s&page=1", c.apiBase, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", config.BingrReferer)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: reqURL}
	}

	var sr animeSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}

	results := make([]provider.SearchResult, 0, len(sr.Results))
	for _, r := range sr.Results {
		if r.ID <= 0 {
			continue
		}
		var year string
		switch y := r.Year.(type) {
		case string:
			year = y
		case float64:
			year = strconv.Itoa(int(y))
		case int:
			year = strconv.Itoa(y)
		}
		results = append(results, provider.SearchResult{
			ID:        strconv.Itoa(r.ID),
			Title:     r.Title,
			Type:      provider.ModeAnime,
			MediaType: provider.MediaTypeAnime,
			Year:      year,
			AniListID: r.ID,
			MALID:     r.IDMal,
			CoverURL:  r.Poster,
		})
	}
	return results, nil
}

func (c *Client) fetchAnimeEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	anilistID := series.AniListID
	if anilistID <= 0 {
		raw := strings.TrimSpace(series.ID)
		raw = strings.TrimPrefix(raw, "anilist:")
		if i := strings.Index(raw, ":"); i >= 0 {
			raw = raw[:i]
		}
		var err error
		anilistID, err = strconv.Atoi(raw)
		if err != nil || anilistID <= 0 {
			return nil, fmt.Errorf("bingr anime episodes: invalid AniList id %q", series.ID)
		}
	}

	if series.MediaType == provider.MediaTypeMovie {
		return []provider.Episode{
			{
				Title:   series.Title,
				ID:      fmt.Sprintf("watch/bingr/%d/sub/1", anilistID),
				Episode: 1,
				Season:  1,
				Audio:   provider.AudioSub,
				TMDBID:  series.TMDBID,
			},
			{
				Title:   series.Title,
				ID:      fmt.Sprintf("watch/bingr/%d/dub/1", anilistID),
				Episode: 1,
				Season:  1,
				Audio:   provider.AudioDub,
				TMDBID:  series.TMDBID,
			},
		}, nil
	}

	count := 0
	media, err := anilist.FetchMediaWithEndpoint(ctx, c.httpClient, anilistID, c.anilistBase)
	if err == nil {
		count = media.EpisodeCount()
	}

	if count <= 0 {
		meta, mErr := c.fetchAnimeMeta(ctx, strconv.Itoa(anilistID))
		if mErr == nil && meta.Episodes > 0 {
			count = meta.Episodes
		}
	}
	if count <= 0 {
		count = 1
	}

	episodes := make([]provider.Episode, 0, count*2)
	for i := 1; i <= count; i++ {
		episodes = append(episodes,
			provider.Episode{
				Title:   fmt.Sprintf("Episode %d", i),
				ID:      fmt.Sprintf("watch/bingr/%d/sub/%d", anilistID, i),
				Episode: i,
				Season:  1,
				Audio:   provider.AudioSub,
				TMDBID:  series.TMDBID,
			},
			provider.Episode{
				Title:   fmt.Sprintf("Episode %d", i),
				ID:      fmt.Sprintf("watch/bingr/%d/dub/%d", anilistID, i),
				Episode: i,
				Season:  1,
				Audio:   provider.AudioDub,
				TMDBID:  series.TMDBID,
			},
		)
	}
	return episodes, nil
}

func (c *Client) resolveAnime(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	h := kit.ParseHandle(mediaID, episode.ID, episode.Audio, episode.Episode, "bingr")
	anilistID := strings.TrimPrefix(h.AniListID, "anilist:")
	if i := strings.Index(anilistID, ":"); i >= 0 {
		anilistID = anilistID[:i]
	}
	category := h.Category
	if category == "" {
		category = provider.AudioSub
	}
	epNum := h.Number
	if epNum <= 0 {
		epNum = 1
	}

	meta, err := c.fetchAnimeMeta(ctx, anilistID)
	if err != nil {
		return fmt.Errorf("bingr anime meta: %w", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	totalSources := 0
	var lastErr error

	pushSources := func(sources []provider.MediaSource) {
		if len(sources) == 0 {
			return
		}
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

	// Branch 1: MegaPlay resolution (via MAL ID).
	if meta.IDMal > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sources, err := c.fetchMegaPlayAnime(ctx, meta.IDMal, epNum, category)
			if err != nil {
				logging.Debug("bingr megaplay failed", "malID", meta.IDMal, "ep", epNum, "cat", category, "err", err)
				mu.Lock()
				lastErr = err
				mu.Unlock()
				return
			}
			pushSources(sources)
		}()
	}

	// Branch 2: Bingr server shards (if TMDB mapping is present).
	if meta.TMDBID > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mType := meta.TMDBType
			if mType == "" {
				mType = provider.MediaTypeTV
			}
			if meta.Format == "MOVIE" {
				mType = provider.MediaTypeMovie
			}

			var subWG sync.WaitGroup
			for _, srv := range activeServers {
				subWG.Add(1)
				go func(srv serverSpec) {
					defer subWG.Done()
					reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
					defer cancel()

					sources, err := c.fetchServerStreams(reqCtx, srv, mType, meta.TMDBID, 1, epNum, meta.Title, "")
					if err != nil || len(sources) == 0 {
						return
					}

					filtered := filterAnimeAudioSources(sources, category)
					pushSources(filtered)
				}(srv)
			}
			subWG.Wait()
		}()
	}

	wg.Wait()

	if totalSources == 0 {
		if lastErr != nil {
			return fmt.Errorf("bingr anime resolve: %w", lastErr)
		}
		return provider.ErrNoSources
	}
	return nil
}

func filterAnimeAudioSources(sources []provider.MediaSource, category string) []provider.MediaSource {
	var kept []provider.MediaSource
	for i := range sources {
		s := &sources[i]
		langCode := strings.ToLower(s.Language)
		switch category {
		case provider.AudioSub:
			if langCode == "" || langCode == "ja" {
				kept = append(kept, *s)
			}
		case provider.AudioDub:
			if langCode != "" && langCode != "ja" {
				kept = append(kept, *s)
			}
		}
	}
	return kept
}

type animeMetaCacheEntry struct {
	meta     *animeMetaResponse
	cachedAt time.Time
}

func (c *Client) fetchAnimeMeta(ctx context.Context, anilistID string) (*animeMetaResponse, error) {
	anilistID = strings.TrimPrefix(strings.TrimSpace(anilistID), "anilist:")
	if i := strings.Index(anilistID, ":"); i >= 0 {
		anilistID = anilistID[:i]
	}
	if val, ok := c.animeMetaCache.Load(anilistID); ok {
		if entry, ok := val.(animeMetaCacheEntry); ok && time.Since(entry.cachedAt) < 24*time.Hour {
			return entry.meta, nil
		}
	}

	reqURL := fmt.Sprintf("%s/anime/%s?v=1", c.apiBase, anilistID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", config.BingrReferer)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: reqURL}
	}

	var meta animeMetaResponse
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, err
	}
	c.animeMetaCache.Store(anilistID, animeMetaCacheEntry{
		meta:     &meta,
		cachedAt: time.Now(),
	})
	return &meta, nil
}

func (c *Client) fetchMegaPlayAnime(ctx context.Context, malID, epNum int, category string) ([]provider.MediaSource, error) {
	token, err := c.getFilmuToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("filmu auth: %w", err)
	}

	reqURL := fmt.Sprintf("%s/hianime/megaplay?malId=%d&ep=%d&type=%s", c.filmuBase, malID, epNum, category)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", config.BingrReferer)
	if token != "" {
		req.Header.Set("x-api-key", token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: reqURL}
	}

	var mr megaplayResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, err
	}

	var sources []provider.MediaSource
	for _, stream := range mr.Streams {
		rawURL := strings.TrimSpace(stream.URL)
		if rawURL == "" {
			continue
		}

		referer := stream.Referer
		if referer == "" {
			referer = config.MegaPlayReferer
		}

		var subs []provider.SubtitleOption
		for _, s := range stream.Subtitles {
			subURL := unwrapProxyURL(s.URL)
			if subURL == "" {
				continue
			}
			subs = append(subs, provider.SubtitleOption{
				URL:      subURL,
				Language: lang.Normalize(s.Lang),
				Referer:  referer,
			})
		}

		subType := ""
		if category == provider.AudioSub {
			subType = provider.SubTypeSoft
		}

		sources = append(sources, provider.MediaSource{
			URL:            rawURL,
			Quality:        "1080p",
			Backend:        "MegaPlay",
			Type:           provider.SourceTypeHLS,
			Referer:        referer,
			SuppressOrigin: true,
			SubType:        subType,
			Subtitles:      subs,
		})
	}

	return sources, nil
}

func unwrapProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "url=") {
		if u, err := url.Parse(raw); err == nil {
			if target := u.Query().Get("url"); target != "" {
				return target
			}
		}
	}
	return raw
}

func (c *Client) getFilmuToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		tok := c.token
		c.tokenMu.Unlock()
		return tok, nil
	}
	c.tokenMu.Unlock()

	reqURL := fmt.Sprintf("%s/token", c.filmuBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader([]byte{}))
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Origin", config.BingrOrigin)
	req.Header.Set("Referer", config.BingrReferer)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: reqURL}
	}

	var tr struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", err
	}
	if tr.Token == "" {
		return "", fmt.Errorf("empty token from %s", reqURL)
	}

	c.tokenMu.Lock()
	c.token = tr.Token
	c.tokenExp = time.Now().Add(2 * time.Hour)
	c.tokenMu.Unlock()

	return tr.Token, nil
}
