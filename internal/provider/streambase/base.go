package streambase

import (
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

	"golang.org/x/sync/errgroup"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/search"
	"kari/internal/tmdb"
	"kari/internal/util"
)

var (
	reEpisodeNum = regexp.MustCompile(`(?i)\bepisode\s+(\d{1,4})\b`)
	reSeasonNum  = regexp.MustCompile(`(?i)\bseason\s+(\d{1,3})\b`)
)

type Base struct {
	httpClient *http.Client
	keyPool    *tmdb.KeyPool
}

func New(keyPool *tmdb.KeyPool) (*Base, error) {
	if keyPool == nil {
		return nil, fmt.Errorf("tmdb key pool is required")
	}
	return &Base{httpClient: httpclient.New(), keyPool: keyPool}, nil
}

func (b *Base) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debugf("stream search start mode=%q query=%q", mode, query)
	var results []model.SearchResult
	var err error

	searchClient := search.NewClient()
	switch mode {
	case provider.ModeMovies:
		results, err = searchClient.SearchMovies(ctx, query)
	case provider.ModeTV:
		results, err = searchClient.SearchTV(ctx, query)
	default:
		results, err = searchClient.Search(ctx, query)
	}
	if err != nil {
		logging.Errorf("stream search failed mode=%q query=%q err=%v", mode, query, err)
		return nil, err
	}

	providerResults := make([]provider.SearchResult, 0, len(results))
	for _, r := range results {
		mediaType := r.MediaType
		if mode == provider.ModeMovies {
			mediaType = "movie"
		} else if mode == provider.ModeTV {
			mediaType = "tv"
		}
		providerResults = append(providerResults, provider.SearchResult{
			Title:     r.Title,
			ID:        strconv.Itoa(r.TMDBID),
			Type:      mode,
			Year:      r.Year,
			MediaType: mediaType,
			TMDBID:    r.TMDBID,
		})
	}
	logging.Debugf("stream search done mode=%q query=%q results=%d", mode, query, len(providerResults))
	return providerResults, nil
}

func (b *Base) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	tmdbID := series.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(series.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid tmdb id in search result: %w", err)
		}
	}

	if series.MediaType == "movie" {
		return []provider.Episode{{
			Title:   series.Title,
			ID:      series.ID,
			Season:  0,
			Episode: 0,
			TMDBID:  tmdbID,
		}}, nil
	}

	if tmdbID <= 0 {
		return nil, errors.New("missing tmdb id")
	}

	details, err := b.fetchTMDBTVDetails(ctx, tmdbID)
	if err != nil {
		return nil, err
	}

	g, gCtx := errgroup.WithContext(ctx)
	results := make([][]provider.Episode, len(details.Seasons))
	sem := make(chan struct{}, 5)

	for i, season := range details.Seasons {
		if season.SeasonNumber <= 0 {
			continue
		}
		i, season := i, season
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			seasonDetails, err := b.fetchTMDBSeasonDetails(gCtx, tmdbID, season.SeasonNumber)
			if err != nil {
				return err
			}
			seasonEps := make([]provider.Episode, 0, len(seasonDetails.Episodes))
			for _, ep := range seasonDetails.Episodes {
				title := util.NormalizeSpace(ep.Name)
				if title == "" {
					title = fmt.Sprintf("Episode %d", ep.EpisodeNumber)
				}
				seasonEps = append(seasonEps, provider.Episode{
					Title:   title,
					ID:      series.ID,
					Season:  ep.SeasonNumber,
					Episode: ep.EpisodeNumber,
					TMDBID:  tmdbID,
				})
			}
			results[i] = seasonEps
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	episodes := make([]provider.Episode, 0, 256)
	for _, res := range results {
		episodes = append(episodes, res...)
	}

	sortEpisodesAscending(episodes)
	if len(episodes) == 0 {
		return nil, provider.ErrNoEpisodes
	}
	return episodes, nil
}

type tmdbTVDetails struct {
	ID              int                 `json:"id"`
	Name            string              `json:"name"`
	NumberOfSeasons int                 `json:"number_of_seasons"`
	Seasons         []tmdbSeasonSummary `json:"seasons"`
}

type tmdbSeasonSummary struct {
	SeasonNumber int `json:"season_number"`
}

type tmdbSeasonDetails struct {
	SeasonNumber int           `json:"season_number"`
	Episodes     []tmdbEpisode `json:"episodes"`
}

type tmdbEpisode struct {
	EpisodeNumber int    `json:"episode_number"`
	SeasonNumber  int    `json:"season_number"`
	Name          string `json:"name"`
}

func (b *Base) fetchTMDBTVDetails(ctx context.Context, tmdbID int) (tmdbTVDetails, error) {
	var lastAuthErr error
	for {
		apiKey, err := b.keyPool.NextKey()
		if err != nil {
			if lastAuthErr != nil {
				return tmdbTVDetails{}, fmt.Errorf("tmdb tv details auth failed after key rotation: %w", lastAuthErr)
			}
			return tmdbTVDetails{}, err
		}
		target := fmt.Sprintf("%s/tv/%d?api_key=%s", config.TMDBAPIBase, tmdbID, url.QueryEscape(apiKey))
		details, err := fetchTMDBJSON[tmdbTVDetails](b, ctx, target)
		if err == nil {
			return details, nil
		}
		if !isAuthError(err) {
			return details, err
		}
		logging.Warnf("tmdb tv request unauthorized tmdb_id=%d err=%v", tmdbID, err)
		b.keyPool.MarkFailed(apiKey)
		lastAuthErr = err
	}
}

func (b *Base) fetchTMDBSeasonDetails(ctx context.Context, tmdbID, season int) (tmdbSeasonDetails, error) {
	var lastAuthErr error
	for {
		apiKey, err := b.keyPool.NextKey()
		if err != nil {
			if lastAuthErr != nil {
				return tmdbSeasonDetails{}, fmt.Errorf("tmdb season details auth failed after key rotation: %w", lastAuthErr)
			}
			return tmdbSeasonDetails{}, err
		}
		target := fmt.Sprintf("%s/tv/%d/season/%d?api_key=%s", config.TMDBAPIBase, tmdbID, season, url.QueryEscape(apiKey))
		details, err := fetchTMDBJSON[tmdbSeasonDetails](b, ctx, target)
		if err == nil {
			return details, nil
		}
		if !isAuthError(err) {
			return details, err
		}
		logging.Warnf("tmdb season request unauthorized tmdb_id=%d season=%d err=%v", tmdbID, season, err)
		b.keyPool.MarkFailed(apiKey)
		lastAuthErr = err
	}
}

func fetchTMDBJSON[T any](b *Base, ctx context.Context, target string) (T, error) {
	var zero T
	headers := map[string]string{"Accept": "application/json"}
	resp, err := b.doRequest(ctx, target, headers)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return zero, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, err
	}
	var payload T
	if err := json.Unmarshal(raw, &payload); err != nil {
		return zero, err
	}
	return payload, nil
}

func (b *Base) doRequest(ctx context.Context, target string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return b.httpClient.Do(req)
}

func isAuthError(err error) bool {
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Code == http.StatusUnauthorized || httpErr.Code == http.StatusTooManyRequests
	}
	return false
}

func sortEpisodesAscending(results []provider.Episode) {
	sort.Slice(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if numberForSort(a) < 0 && numberForSort(b) >= 0 {
			return false
		}
		if numberForSort(a) >= 0 && numberForSort(b) < 0 {
			return true
		}
		if seasonForSort(a) != seasonForSort(b) {
			return seasonForSort(a) < seasonForSort(b)
		}
		if numberForSort(a) != numberForSort(b) {
			return numberForSort(a) < numberForSort(b)
		}
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	})
}

func numberForSort(ep provider.Episode) int {
	if ep.Episode > 0 {
		return ep.Episode
	}
	m := reEpisodeNum.FindStringSubmatch(ep.Title)
	if len(m) < 2 {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func seasonForSort(ep provider.Episode) int {
	if ep.Season > 0 {
		return ep.Season
	}
	m := reSeasonNum.FindStringSubmatch(ep.Title)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
