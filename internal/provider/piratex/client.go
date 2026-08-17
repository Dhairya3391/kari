package piratex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/util"
)

// Client scrapes piratexplay.cc directly: site-native TMDB search plus its two
// playback transports (as-cdn's FirePlayer and the byse index11 player). Both
// transports resolve to a merged HLS master that carries every quality and all
// audio languages in a single URL, so each MediaSource is that master with the
// per-transport Referer/Cookie headers attached.
type Client struct {
	http        *http.Client
	searchCache *util.BoundedCache[[]provider.SearchResult]
	seriesCache *util.BoundedCache[*seriesData]
}

func NewClient() (*Client, error) {
	return &Client{
		http:        httpclient.New(),
		searchCache: util.NewBoundedCache[[]provider.SearchResult](64),
		seriesCache: util.NewBoundedCache[*seriesData](128),
	}, nil
}

func (c *Client) Name() string {
	return "piratex"
}

func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeCartoon, Priority: 1},
	}
}

var _ provider.Provider = (*Client)(nil)

func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debugf("piratex search start query=%q", query)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, provider.ErrNoResults
	}

	cacheKey := strings.ToLower(query)
	if cached, ok := c.searchCache.Get(cacheKey); ok {
		logging.Debugf("piratex search cached query=%q", query)
		return cached, nil
	}

	target := searchURL(query)
	body, status, err := c.getBytes(ctx, target, nil)
	if err != nil {
		return nil, fmt.Errorf("piratex search: %w", err)
	}
	if status != http.StatusOK {
		return nil, &provider.HTTPError{Code: status, URL: target}
	}

	var resp searchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("piratex search: decode response: %w", err)
	}

	results := make([]provider.SearchResult, 0, len(resp.Data))
	for _, item := range resp.Data {
		if item.TMDB == nil {
			continue
		}
		t := item.TMDB
		// Movies have no playable streams on this site; only series slugs resolve.
		if !strings.EqualFold(t.Type, "series") {
			continue
		}
		slug := strings.TrimSpace(t.URL)
		name := strings.TrimSpace(t.Title)
		if slug == "" || name == "" {
			continue
		}
		year := ""
		if y := toInt(t.Year); y > 0 {
			year = strconv.Itoa(y)
		}
		results = append(results, provider.SearchResult{
			Title:     name,
			ID:        slug,
			Type:      provider.ModeCartoon,
			Year:      year,
			MediaType: "cartoon",
		})
	}

	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}

	c.searchCache.Set(cacheKey, results)
	logging.Debugf("piratex search done results=%d", len(results))
	return results, nil
}

func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	slug := strings.TrimSpace(series.ID)
	if slug == "" {
		return nil, fmt.Errorf("piratex episodes: empty slug")
	}
	logging.Debugf("piratex fetch episodes slug=%q", slug)

	data, err := c.fetchSeries(ctx, slug)
	if err != nil {
		return nil, err
	}

	eps := episodesFromSeries(data)
	if len(eps) == 0 {
		return nil, provider.ErrNoEpisodes
	}

	sort.Slice(eps, func(i, j int) bool {
		if eps[i].Season != eps[j].Season {
			return eps[i].Season < eps[j].Season
		}
		return eps[i].Episode < eps[j].Episode
	})

	logging.Debugf("piratex fetch episodes done count=%d", len(eps))
	return eps, nil
}

func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	slug := strings.TrimSpace(mediaID)
	if slug == "" {
		return nil, fmt.Errorf("piratex resolve: empty slug")
	}
	logging.Debugf("piratex resolve source slug=%q season=%d episode=%d", slug, episode.Season, episode.Episode)

	streams, audioTracks, subtitles, err := c.resolveEpisode(ctx, slug, episode.Season, episode.Episode)
	if err != nil {
		return nil, err
	}
	if len(streams) == 0 {
		return nil, provider.ErrNoSources
	}
	primary := streams[0]

	subtitleOptions := make([]provider.SubtitleOption, 0, len(subtitles))
	for _, s := range subtitles {
		if subURL := strings.TrimSpace(s.URL); subURL != "" {
			subtitleOptions = append(subtitleOptions, provider.SubtitleOption{URL: subURL, Language: s.Language})
		}
	}

	// Every source is the merged master: video + all audio languages in one
	// URL. Language selection is a player feature (mpv --alang), not a
	// different stream, so per-language sources are the same master with the
	// --alang hint appended.
	base := func() provider.MediaSource {
		return provider.MediaSource{
			URL:          primary.URL,
			Type:         "hls",
			Referer:      primary.Referer,
			UserAgent:    primary.UserAgent,
			CookieHeader: primary.Cookie,
			Subtitles:    subtitleOptions,

			ExtraArgs:      []string{"--demuxer-lavf-o=http_persistent=0,hls_read_ahead_limit=-1"},
			SuppressOrigin: true,
		}
	}

	// No CDN/server name in the label — just quality, then the language (or
	// "Auto" when the master carries more than one). A single language is the
	// whole source set: one entry that pins it via --alang.
	heightLabel := ""
	if primary.Height > 0 {
		heightLabel = fmt.Sprintf(" %dp", primary.Height)
	}

	var langOrder []string
	langNames := make(map[string]string)
	for _, a := range audioTracks {
		lang := strings.TrimSpace(a.Language)
		name := strings.TrimSpace(a.Name)
		if lang == "" || name == "" {
			continue
		}
		if _, ok := langNames[lang]; ok {
			continue
		}
		langNames[lang] = name
		langOrder = append(langOrder, lang)
	}

	var sources []provider.MediaSource
	if len(langOrder) <= 1 {
		ms := base()
		label := "[PIRATEX]"
		if len(langOrder) == 1 {
			lang := langOrder[0]
			ms.Language = lang
			ms.ExtraArgs = append(ms.ExtraArgs, "--alang="+lang)
			label += " " + langNames[lang]
		}
		ms.Quality = label + heightLabel
		sources = []provider.MediaSource{ms}
	} else {
		primarySource := base()
		primarySource.Quality = "[PIRATEX] Auto" + heightLabel
		sources = []provider.MediaSource{primarySource}
		for _, lang := range langOrder {
			ms := base()
			ms.Quality = "[PIRATEX] " + langNames[lang] + heightLabel
			ms.Language = lang
			ms.ExtraArgs = append(ms.ExtraArgs, "--alang="+lang)
			sources = append(sources, ms)
		}
	}

	logging.Debugf("piratex resolve done sources=%d", len(sources))
	return sources, nil
}

func episodesFromSeries(data *seriesData) []provider.Episode {
	episodes := make([]provider.Episode, 0, len(data.Episodes))
	for _, episode := range data.Episodes {
		if episode.Number <= 0 {
			continue
		}
		season := episode.Season
		if season <= 0 {
			season = 1
		}
		id := strings.TrimSpace(episode.URL)
		if id == "" {
			id = strings.TrimSpace(episode.Slug)
		}
		if id == "" {
			continue
		}
		episodes = append(episodes, provider.Episode{
			Title:   fmt.Sprintf("Episode %d", episode.Number),
			ID:      id,
			Season:  season,
			Episode: episode.Number,
		})
	}
	return episodes
}
