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
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
	"kari/internal/timeouts"
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

	if meta.IDMal > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sources, err := c.fetchMegaPlayAnime(ctx, meta.IDMal, epNum, category)
			if err != nil {
				logging.Debug("bingr megaplay failed", "malID", meta.IDMal, "ep", epNum, "cat", category, "err", err)
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
					reqCtx, cancel := context.WithTimeout(ctx, timeouts.BingrShard)
					defer cancel()

					sources, err := c.fetchServerStreams(reqCtx, srv, mType, meta.TMDBID, 1, epNum, meta.Title, "")
					if err != nil {
						logging.Debug("bingr anime shard failed", "srv", srv.id, "err", err)
						mu.Lock()
						lastErr = err
						mu.Unlock()
						return
					}
					if len(sources) == 0 {
						return
					}

					for i := range sources {
						sources[i].Audio.Track = animeTrack(category)
					}
					pushSources(sources)
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

// animeTrack maps the anime episode handle's category onto the typed audio
// track. It is content metadata, not a user language preference.
func animeTrack(category string) provider.AudioTrack {
	if strings.EqualFold(strings.TrimSpace(category), provider.AudioDub) {
		return provider.TrackDub
	}
	return provider.TrackSub
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

// fetchMegaPlayAnime resolves one anime episode through MegaPlay's direct
// embeds. The MAL id is the primary index; when that embed fails the cached
// AniList id is tried as a fallback so titles indexed only there still work.
func (c *Client) fetchMegaPlayAnime(ctx context.Context, malID, epNum int, category string) ([]provider.MediaSource, error) {
	cat := strings.ToLower(strings.TrimSpace(category))
	if cat != provider.AudioDub {
		cat = provider.AudioSub
	}

	malEmbedURL := fmt.Sprintf("%s/stream/mal/%d/%d/%s", c.embedBase, malID, epNum, cat)
	sources, err := c.resolveMegaplayEmbed(ctx, malEmbedURL, cat)
	if err == nil {
		return sources, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}

	if anilistID := c.anilistIDForMAL(malID); anilistID != "" {
		aniEmbedURL := fmt.Sprintf("%s/stream/ani/%s/%d/%s", c.embedBase, anilistID, epNum, cat)
		if aniSources, aniErr := c.resolveMegaplayEmbed(ctx, aniEmbedURL, cat); aniErr == nil {
			return aniSources, nil
		}
	}
	return nil, fmt.Errorf("megaplay anime mal %d ep %d: %w", malID, epNum, err)
}

// anilistIDForMAL recovers the AniList id from the metadata cache populated
// by the preceding fetchAnimeMeta call in resolveAnime.
func (c *Client) anilistIDForMAL(malID int) string {
	var found string
	c.animeMetaCache.Range(func(key, value any) bool {
		if id, ok := key.(string); ok {
			if entry, ok := value.(animeMetaCacheEntry); ok && entry.meta != nil && entry.meta.IDMal == malID {
				found = id
				return false
			}
		}
		return true
	})
	return found
}

// resolveMegaplayEmbed turns a MegaPlay embed page into a playable source:
// extract the file id, fetch getSources with the embed as Referer, then
// decrypt and sign the returned token using the shared kit helpers.
func (c *Client) resolveMegaplayEmbed(ctx context.Context, embedURL, category string) ([]provider.MediaSource, error) {
	fileID, err := c.megaplayFileID(ctx, embedURL)
	if err != nil {
		return nil, fmt.Errorf("megaplay embed: %w", err)
	}

	parsed, err := url.Parse(embedURL)
	if err != nil {
		return nil, fmt.Errorf("parse megaplay embed: %w", err)
	}
	origin := parsed.Scheme + "://" + parsed.Host
	getSourcesURL := fmt.Sprintf("%s/stream/getSources?id=%s", origin, fileID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getSourcesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", embedURL)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("megaplay getSources: %w", err)
	}
	body, err := httpclient.ReadCapped(resp)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("megaplay read sources: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: getSourcesURL}
	}

	var sr kit.MegaplaySourcesResp
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("megaplay decode sources: %w", err)
	}
	if sr.Enc == "" {
		return nil, provider.ErrNoSources
	}
	rawM3U8, err := kit.DecryptMegaplayEnc(sr.Enc)
	if err != nil {
		return nil, fmt.Errorf("megaplay decrypt: %w", err)
	}

	subs := make([]provider.SubtitleOption, 0, len(sr.Tracks))
	for _, track := range sr.Tracks {
		if strings.EqualFold(strings.TrimSpace(track.Kind), "thumbnails") || strings.TrimSpace(track.File) == "" {
			continue
		}
		subs = append(subs, provider.SubtitleOption{
			URL:      track.File,
			Language: lang.FromLabel(kit.Clean(track.Label)),
			Default:  track.Default,
			Referer:  origin + "/",
		})
	}

	return []provider.MediaSource{{
		URL:            kit.SignCDNURL(rawM3U8, 86400),
		Quality:        "Auto",
		Backend:        "MegaPlay",
		Referer:        origin + "/",
		Type:           provider.SourceTypeHLS,
		UserAgent:      config.DesktopUserAgent,
		SuppressOrigin: true,
		Subtitles:      subs,
		Audio:          provider.AudioInfo{Track: animeTrack(category)},
		SubType:        provider.SubTypeSoft,
	}}, nil
}

// megaplayFileID fetches an embed page and extracts its stream file id.
func (c *Client) megaplayFileID(ctx context.Context, embedURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, embedURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.embedBase+"/")

	resp, err := c.httpClient.Do(req)
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
	return parseMegaplayFileID(body)
}

// parseMegaplayFileID reads the file id from an embed page. A 200 page
// explicitly titled as an error means the track does not exist (for example
// an unreleased dub), which maps to the audio-unavailable sentinel.
func parseMegaplayFileID(body []byte) (string, error) {
	if id := digitsAfter(body, []byte(`data-id="`)); id != "" {
		return id, nil
	}
	if id := digitsAfter(body, []byte("File ")); id != "" {
		return id, nil
	}
	if bytes.Contains(body, []byte("<title>Error")) {
		return "", fmt.Errorf("no file id in embed page: %w", provider.ErrAudioUnavailable)
	}
	return "", fmt.Errorf("no file id in embed page")
}

// digitsAfter returns the run of digits that follows marker, or empty.
func digitsAfter(body, marker []byte) string {
	i := bytes.Index(body, marker)
	if i < 0 {
		return ""
	}
	start := i + len(marker)
	end := start
	for end < len(body) && body[end] >= '0' && body[end] <= '9' {
		end++
	}
	if end == start {
		return ""
	}
	return string(body[start:end])
}
