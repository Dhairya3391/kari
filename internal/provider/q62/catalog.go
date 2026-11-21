package q62

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"kari/internal/logging"
	"kari/internal/provider"
)

// catalogPage is the envelope Q62 wraps around every paged catalog response.
type catalogPage[T any] struct {
	Content []T `json:"content"`
	Page    struct {
		TotalElements int `json:"totalElements"`
		TotalPages    int `json:"totalPages"`
	} `json:"page"`
}

// catalogItem is the shared shape of a movie or TV show entry. Translations
// carry the localized titles and artwork; tags are localized genre labels.
type catalogItem struct {
	ID               string                        `json:"id"`
	TMDBID           int                           `json:"tmdbId"`
	ReleaseDate      string                        `json:"releaseDate"`
	FirstAirDate     string                        `json:"firstAirDate"`
	OriginalLanguage string                        `json:"originalLanguage"`
	ComingSoon       bool                          `json:"comingSoon"`
	Tags             []catalogTag                  `json:"tags"`
	Translations     map[string]catalogTranslation `json:"translations"`
}

type catalogTag struct {
	Key           string            `json:"key"`
	Name          string            `json:"name"`
	Localizations map[string]string `json:"localizations"`
}

type catalogTranslation struct {
	Title    string `json:"title"`
	Overview string `json:"overview"`
}

type seasonInfo struct {
	ID           string `json:"id"`
	TVShowID     string `json:"tvShowId"`
	SeasonNumber int    `json:"seasonNumber"`
	ComingSoon   bool   `json:"comingSoon"`
}

type episodeInfo struct {
	ID            string                        `json:"id"`
	SeasonID      string                        `json:"seasonId"`
	EpisodeNumber int                           `json:"episodeNumber"`
	ComingSoon    bool                          `json:"comingSoon"`
	Translations  map[string]catalogTranslation `json:"translations"`
}

// tmdbIndex maps TMDB IDs to Q62's own content IDs. It is built once from the
// whole (small) catalog so a title selected from another provider still
// resolves through Q62.
type tmdbIndex struct {
	movies  map[int]string
	tvshows map[int]string
}

// lookupID maps a TMDB ID to the corresponding Q62 content ID, building the
// index on first use. The index is instance-local and immutable once built.
func (c *Client) lookupID(ctx context.Context, tmdbID int, tv bool) (string, error) {
	if tmdbID <= 0 {
		return "", provider.ErrNotFound
	}
	c.indexMu.Lock()
	defer c.indexMu.Unlock()
	if c.index == nil {
		idx, err := c.buildIndex(ctx)
		if err != nil {
			return "", err
		}
		c.index = idx
	}
	target := c.index.movies
	if tv {
		target = c.index.tvshows
	}
	id, ok := target[tmdbID]
	if !ok {
		return "", provider.ErrNotFound
	}
	return id, nil
}

// buildIndex loads the complete movie and TV catalogs and indexes them by
// TMDB ID. Q62's catalog is only a few dozen titles, so one paged request per
// kind keeps the lookup cheap and avoids a title-based fuzzy match.
func (c *Client) buildIndex(ctx context.Context) (*tmdbIndex, error) {
	movies, err := getJSON[catalogPage[catalogItem]](ctx, c, "/movie?page=0&size=500")
	if err != nil {
		return nil, fmt.Errorf("q62 index movies: %w", err)
	}
	shows, err := getJSON[catalogPage[catalogItem]](ctx, c, "/tvshow?page=0&size=500&isAnime=false")
	if err != nil {
		return nil, fmt.Errorf("q62 index tvshows: %w", err)
	}

	idx := &tmdbIndex{movies: make(map[int]string, len(movies.Content)), tvshows: make(map[int]string, len(shows.Content))}
	for _, item := range movies.Content {
		if item.TMDBID > 0 && item.ID != "" {
			idx.movies[item.TMDBID] = item.ID
		}
	}
	for _, item := range shows.Content {
		if item.TMDBID > 0 && item.ID != "" {
			idx.tvshows[item.TMDBID] = item.ID
		}
	}
	return idx, nil
}

// Search delegates title discovery to the shared TMDB/noobsearch base. The
// returned handle is a TMDB ID; contentID maps it to Q62's site ID at resolve
// time via the lazily built catalog index.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	switch mode {
	case provider.ModeMovies, provider.ModeTV:
		return c.base.Search(ctx, query, mode)
	default:
		return nil, provider.ErrNoResults
	}
}

// FetchEpisodes returns a synthetic movie episode or Q62's season/episode
// listing for a TV show. Episode IDs carry the season and episode handles
// resolution needs, since Q62 addresses episodes by opaque IDs.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if series.MediaType == provider.MediaTypeMovie {
		return []provider.Episode{{
			Title:  series.Title,
			ID:     series.ID,
			TMDBID: series.TMDBID,
		}}, nil
	}

	showID, err := c.contentID(ctx, series.ID, series.TMDBID, true)
	if err != nil {
		return nil, fmt.Errorf("q62 episodes: %w", err)
	}
	seasons, err := getJSON[[]seasonInfo](ctx, c, "/tvshow/"+url.PathEscape(showID)+"/season")
	if err != nil {
		return nil, fmt.Errorf("q62 seasons: %w", err)
	}

	var (
		mu       sync.Mutex
		episodes []provider.Episode
	)
	g, gCtx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, 5)
	for _, season := range seasons {
		if season.ID == "" || season.ComingSoon {
			continue
		}
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
			case <-gCtx.Done():
				return gCtx.Err()
			}
			defer func() { <-sem }()

			items, err := getJSON[[]episodeInfo](gCtx, c, "/season/"+url.PathEscape(season.ID)+"/episode")
			if err != nil {
				return fmt.Errorf("q62 season %s episodes: %w", season.ID, err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, item := range items {
				if item.ID == "" || item.ComingSoon {
					continue
				}
				title := localizedText(item.Translations, "title")
				if title == "" {
					title = fmt.Sprintf("Episode %d", item.EpisodeNumber)
				}
				episodes = append(episodes, provider.Episode{
					Title:   title,
					ID:      season.ID + episodeHandleSep + item.ID,
					Season:  season.SeasonNumber,
					Episode: item.EpisodeNumber,
					TMDBID:  series.TMDBID,
				})
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil && len(episodes) == 0 {
		return nil, err
	}
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].Season != episodes[j].Season {
			return episodes[i].Season < episodes[j].Season
		}
		return episodes[i].Episode < episodes[j].Episode
	})
	if len(episodes) == 0 {
		return nil, provider.ErrNoEpisodes
	}
	return episodes, nil
}

// contentID returns series.ID when it is a Q62 ID, otherwise treats it as a
// TMDB ID and looks up the matching Q62 ID.
func (c *Client) contentID(ctx context.Context, raw string, tmdbID int, tv bool) (string, error) {
	if id := strings.TrimSpace(raw); id != "" {
		if !isNumericID(id) {
			return id, nil
		}
	}
	if tmdbID <= 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			tmdbID = n
		}
	}
	id, err := c.lookupID(ctx, tmdbID, tv)
	if err != nil {
		return "", fmt.Errorf("q62 map tmdb id %d: %w", tmdbID, err)
	}
	return id, nil
}

// isNumericID reports whether raw is an integer ID rather than an opaque Q62
// site ID. Q62 site IDs are not numbers, so parsing failure means a usable ID.
func isNumericID(raw string) bool {
	_, err := strconv.Atoi(raw)
	return err == nil
}

// localizedText picks the English rendering of a localized field, falling
// back to the first non-empty translation when English is absent.
func localizedText(translations map[string]catalogTranslation, field string) string {
	keys := []string{"en", "en-US", "en-GB", "en-AU"}
	for _, key := range keys {
		if t, ok := translations[key]; ok {
			if value := fieldValue(t, field); value != "" {
				return value
			}
		}
	}
	for _, t := range translations {
		if value := fieldValue(t, field); value != "" {
			return value
		}
	}
	return ""
}

func fieldValue(t catalogTranslation, field string) string {
	if field == "title" {
		return strings.TrimSpace(t.Title)
	}
	return strings.TrimSpace(t.Overview)
}

// isNotFound reports whether err is an upstream 404, which resolution treats
// as "no sources" rather than a transport failure.
func isNotFound(err error) bool {
	var httpErr *provider.HTTPError
	return errors.As(err, &httpErr) && httpErr.Code == http.StatusNotFound
}

// logPartialFailure records a single failed release without aborting the
// remaining releases in the fan-out.
func logPartialFailure(err error) {
	if err != nil && !errors.Is(err, provider.ErrNotFound) {
		logging.Debug("q62 release failed", "err", err)
	}
}
