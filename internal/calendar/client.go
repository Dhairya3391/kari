package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/tmdb"
)

var calLog = logging.With("component", "calendar")

const defaultCacheTTL = 15 * time.Minute

// Client coordinates schedule lookups from AniList (anime) and Trakt/TMDB (series).
type Client struct {
	http            *http.Client
	anilistEndpoint string
	traktEndpoint   string
	traktClientID   string
	tmdbPool        *tmdb.KeyPool
	tmdbBase        string

	cacheMu        sync.RWMutex
	cachedReleases []Release
	cachedWeek     time.Time
	cachedAt       time.Time
	cacheTTL       time.Duration
}

// NewClient constructs a calendar client using default production endpoints.
func NewClient(keyPool *tmdb.KeyPool) *Client {
	return NewClientWithEndpoints(
		httpclient.NewWithTimeout(15*time.Second),
		keyPool,
		config.AniListAPIBase,
		config.TraktAPIBase,
		config.DefaultTraktClientID,
		config.TMDBAPIBase,
	)
}

// NewClientWithEndpoints constructs a client with custom endpoints for testing.
func NewClientWithEndpoints(
	hc *http.Client,
	keyPool *tmdb.KeyPool,
	anilistEndpoint, traktEndpoint, traktClientID, tmdbBase string,
) *Client {
	if hc == nil {
		hc = httpclient.NewWithTimeout(15 * time.Second)
	}
	return &Client{
		http:            hc,
		anilistEndpoint: strings.TrimRight(anilistEndpoint, "/"),
		traktEndpoint:   strings.TrimRight(traktEndpoint, "/"),
		traktClientID:   traktClientID,
		tmdbPool:        keyPool,
		tmdbBase:        strings.TrimRight(tmdbBase, "/"),
		cacheTTL:        defaultCacheTTL,
	}
}

// SetCacheTTL updates the cache expiration duration.
func (c *Client) SetCacheTTL(d time.Duration) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.cacheTTL = d
}

// FetchWeekReleases retrieves scheduled anime and TV series episodes for a 7-day period.
func (c *Client) FetchWeekReleases(ctx context.Context, weekStart time.Time, forceRefresh bool) ([]Release, error) {
	// Truncate weekStart to start of day for stable cache keys.
	weekStart = time.Date(weekStart.Year(), weekStart.Month(), weekStart.Day(), 0, 0, 0, 0, time.UTC)
	weekEnd := weekStart.Add(7 * 24 * time.Hour)

	if !forceRefresh {
		c.cacheMu.RLock()
		if len(c.cachedReleases) > 0 && c.cachedWeek.Equal(weekStart) && time.Since(c.cachedAt) < c.cacheTTL {
			cp := make([]Release, len(c.cachedReleases))
			copy(cp, c.cachedReleases)
			c.cacheMu.RUnlock()
			return cp, nil
		}
		c.cacheMu.RUnlock()
	}

	var wg sync.WaitGroup
	var animeReleases []Release
	var seriesReleases []Release
	var animeErr, seriesErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		animeReleases, animeErr = c.FetchAnimeWeek(ctx, weekStart, weekEnd)
		if animeErr != nil {
			calLog.Warn("failed to fetch anime schedule", "err", animeErr)
		}
	}()

	go func() {
		defer wg.Done()
		seriesReleases, seriesErr = c.FetchSeriesWeek(ctx, weekStart, 7)
		if seriesErr != nil {
			calLog.Warn("failed to fetch TV series schedule", "err", seriesErr)
		}
	}()

	wg.Wait()

	if animeErr != nil && seriesErr != nil {
		return nil, fmt.Errorf("calendar: failed to fetch releases: anime: %w, series: %w", animeErr, seriesErr)
	}

	combined := make([]Release, 0, len(animeReleases)+len(seriesReleases))
	combined = append(combined, animeReleases...)
	combined = append(combined, seriesReleases...)

	sort.Slice(combined, func(i, j int) bool {
		if combined[i].AiringAt.Equal(combined[j].AiringAt) {
			return combined[i].Title < combined[j].Title
		}
		return combined[i].AiringAt.Before(combined[j].AiringAt)
	})

	c.cacheMu.Lock()
	c.cachedReleases = combined
	c.cachedWeek = weekStart
	c.cachedAt = time.Now()
	c.cacheMu.Unlock()

	return combined, nil
}

// FetchAnimeWeek queries AniList for anime airing between start and end.
func (c *Client) FetchAnimeWeek(ctx context.Context, start, end time.Time) ([]Release, error) {
	startSec := start.Unix()
	endSec := end.Unix()

	query := `
query ($airingAt_greater: Int, $airingAt_lesser: Int, $page: Int, $perPage: Int) {
  Page (page: $page, perPage: $perPage) {
    pageInfo {
      hasNextPage
    }
    airingSchedules (airingAt_greater: $airingAt_greater, airingAt_lesser: $airingAt_lesser, sort: TIME) {
      id
      airingAt
      episode
      media {
        id
        idMal
        title {
          romaji
          english
          userPreferred
        }
        format
        status
        episodes
        countryOfOrigin
        isAdult
        description
        averageScore
        coverImage {
          large
          extraLarge
        }
      }
    }
  }
}
`

	var allReleases []Release
	seen := make(map[string]bool)

	for page := 1; page <= 4; page++ {
		vars := map[string]any{
			"airingAt_greater": startSec,
			"airingAt_lesser":  endSec,
			"page":             page,
			"perPage":          50,
		}

		bodyBytes, err := json.Marshal(map[string]any{
			"query":     query,
			"variables": vars,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal anilist query: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.anilistEndpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("create anilist request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Origin", "https://anilist.co")
		req.Header.Set("Referer", "https://anilist.co/")
		req.Header.Set("User-Agent", config.DesktopUserAgent)

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("execute anilist request: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("anilist returned status %d", resp.StatusCode)
		}

		var gqlResp struct {
			Data struct {
				Page struct {
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
					AiringSchedules []struct {
						ID       int `json:"id"`
						AiringAt int `json:"airingAt"`
						Episode  int `json:"episode"`
						Media    struct {
							ID    int `json:"id"`
							IDMal int `json:"idMal"`
							Title struct {
								Romaji        string `json:"romaji"`
								English       string `json:"english"`
								UserPreferred string `json:"userPreferred"`
							} `json:"title"`
							Format          string `json:"format"`
							Status          string `json:"status"`
							Episodes        int    `json:"episodes"`
							CountryOfOrigin string `json:"countryOfOrigin"`
							IsAdult         bool   `json:"isAdult"`
							Description     string `json:"description"`
							AverageScore    int    `json:"averageScore"`
							CoverImage      struct {
								Large      string `json:"large"`
								ExtraLarge string `json:"extraLarge"`
							} `json:"coverImage"`
						} `json:"media"`
					} `json:"airingSchedules"`
				} `json:"Page"`
			} `json:"data"`
		}

		err = json.NewDecoder(resp.Body).Decode(&gqlResp)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode anilist response: %w", err)
		}

		for _, item := range gqlResp.Data.Page.AiringSchedules {
			if item.Media.IsAdult {
				continue
			}

			title := strings.TrimSpace(item.Media.Title.English)
			if title == "" {
				title = strings.TrimSpace(item.Media.Title.UserPreferred)
			}
			if title == "" {
				title = strings.TrimSpace(item.Media.Title.Romaji)
			}
			if title == "" {
				continue
			}

			cover := item.Media.CoverImage.ExtraLarge
			if cover == "" {
				cover = item.Media.CoverImage.Large
			}

			id := fmt.Sprintf("anilist:%d:%d", item.Media.ID, item.Episode)
			if seen[id] {
				continue
			}
			seen[id] = true

			rating := 0.0
			if item.Media.AverageScore > 0 {
				rating = float64(item.Media.AverageScore) / 10.0
			}

			allReleases = append(allReleases, Release{
				ID:            id,
				Title:         title,
				RomajiTitle:   strings.TrimSpace(item.Media.Title.Romaji),
				Mode:          provider.ModeAnime,
				Season:        1,
				Episode:       item.Episode,
				AiringAt:      time.Unix(int64(item.AiringAt), 0).UTC(),
				AniListID:     item.Media.ID,
				CoverURL:      cover,
				Country:       item.Media.CountryOfOrigin,
				Status:        item.Media.Status,
				TotalEpisodes: item.Media.Episodes,
				Description:   cleanDescription(item.Media.Description),
				Rating:        rating,
			})
		}

		if !gqlResp.Data.Page.PageInfo.HasNextPage {
			break
		}
	}

	return allReleases, nil
}

// FetchSeriesWeek queries Trakt for TV series airing starting from start for days count.
func (c *Client) FetchSeriesWeek(ctx context.Context, start time.Time, days int) ([]Release, error) {
	if days <= 0 {
		days = 7
	}
	startDateStr := start.Format("2006-01-02")
	traktURL := fmt.Sprintf("%s/calendars/all/shows/%s/%d", c.traktEndpoint, startDateStr, days)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, traktURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create trakt request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", "2")
	if c.traktClientID != "" {
		req.Header.Set("trakt-api-key", c.traktClientID)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		calLog.Debug("trakt calendar request failed; attempting TMDB fallback", "err", err)
		return c.fetchSeriesTMDBFallback(ctx)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		calLog.Debug("trakt returned non-200 status; attempting TMDB fallback", "status", resp.StatusCode)
		return c.fetchSeriesTMDBFallback(ctx)
	}

	type traktCalendarItem struct {
		FirstAired string `json:"first_aired"`
		Episode    struct {
			Season int    `json:"season"`
			Number int    `json:"number"`
			Title  string `json:"title"`
		} `json:"episode"`
		Show struct {
			Title string `json:"title"`
			Year  int    `json:"year"`
			IDs   struct {
				Trakt int    `json:"trakt"`
				Slug  string `json:"slug"`
				TMDB  int    `json:"tmdb"`
				IMDb  string `json:"imdb"`
			} `json:"ids"`
		} `json:"show"`
	}

	var items []traktCalendarItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode trakt calendar response: %w", err)
	}

	var releases []Release
	seen := make(map[string]bool)

	for _, item := range items {
		title := strings.TrimSpace(item.Show.Title)
		if title == "" {
			continue
		}

		airingAt, err := time.Parse(time.RFC3339, item.FirstAired)
		if err != nil {
			// Try without fractional seconds if needed
			airingAt, err = time.Parse("2006-01-02T15:04:05Z", item.FirstAired)
			if err != nil {
				continue
			}
		}

		id := fmt.Sprintf("trakt:%d:s%02de%02d", item.Show.IDs.Trakt, item.Episode.Season, item.Episode.Number)
		if seen[id] {
			continue
		}
		seen[id] = true

		releases = append(releases, Release{
			ID:           id,
			Title:        title,
			Mode:         provider.ModeTV,
			Season:       item.Episode.Season,
			Episode:      item.Episode.Number,
			EpisodeTitle: strings.TrimSpace(item.Episode.Title),
			AiringAt:     airingAt.UTC(),
			TMDBID:       item.Show.IDs.TMDB,
			Country:      "US",
			Status:       "Airing",
		})
	}

	return releases, nil
}

// fetchSeriesTMDBFallback queries TMDB for shows airing on the air as fallback.
func (c *Client) fetchSeriesTMDBFallback(ctx context.Context) ([]Release, error) {
	if c.tmdbPool == nil {
		return nil, nil
	}

	apiKey, err := c.tmdbPool.NextKey()
	if err != nil || apiKey == "" {
		return nil, nil
	}

	endpoint := fmt.Sprintf("%s/tv/on_the_air?api_key=%s", c.tmdbBase, url.QueryEscape(apiKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tmdb on_the_air returned %d", resp.StatusCode)
	}

	var tmdbResp struct {
		Results []struct {
			ID           int     `json:"id"`
			Name         string  `json:"name"`
			Overview     string  `json:"overview"`
			PosterPath   string  `json:"poster_path"`
			VoteAverage  float64 `json:"vote_average"`
			FirstAirDate string  `json:"first_air_date"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tmdbResp); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var releases []Release
	for _, show := range tmdbResp.Results {
		name := strings.TrimSpace(show.Name)
		if name == "" {
			continue
		}

		coverURL := ""
		if show.PosterPath != "" {
			coverURL = config.TMDBImageBase + "w500" + show.PosterPath
		}

		releases = append(releases, Release{
			ID:          fmt.Sprintf("tmdb:%d", show.ID),
			Title:       name,
			Mode:        provider.ModeTV,
			Season:      1,
			Episode:     1,
			AiringAt:    now,
			TMDBID:      show.ID,
			CoverURL:    coverURL,
			Status:      "Airing",
			Description: cleanDescription(show.Overview),
			Rating:      show.VoteAverage,
		})
	}

	return releases, nil
}

func cleanDescription(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = strings.ReplaceAll(s, "<i>", "")
	s = strings.ReplaceAll(s, "</i>", "")
	s = strings.ReplaceAll(s, "<b>", "")
	s = strings.ReplaceAll(s, "</b>", "")
	return strings.TrimSpace(s)
}
