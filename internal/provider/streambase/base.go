package streambase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/tmdb"
	"kari/internal/util"
)

var (
	reEpisodeNum = regexp.MustCompile(`(?i)\bepisode\s+(\d{1,4})\b`)
	reSeasonNum  = regexp.MustCompile(`(?i)\bseason\s+(\d{1,3})\b`)
)

// Base is the shared TMDB-backed implementation used by providers that
// identify media by TMDB id: meilisearch title lookup plus TMDB episode
// listings. Concrete clients only implement ResolveSource.
type Base struct {
	httpClient *http.Client
	keyPool    *tmdb.KeyPool
	searcher   *Client
}

// New validates that a key pool is available and constructs the base.
func New(keyPool *tmdb.KeyPool) (*Base, error) {
	if keyPool == nil {
		return nil, fmt.Errorf("tmdb key pool is required")
	}
	return &Base{httpClient: httpclient.New(), keyPool: keyPool, searcher: NewClient()}, nil
}

// newForTest builds a Base with injected search and HTTP clients so
// offline tests can serve canned TMDB/meilisearch payloads without
// touching production construction.
func newForTest(keyPool *tmdb.KeyPool, hc *http.Client, searcher *Client) (*Base, error) {
	if keyPool == nil {
		return nil, fmt.Errorf("tmdb key pool is required")
	}
	if hc == nil || searcher == nil {
		return nil, fmt.Errorf("http and search clients are required")
	}
	return &Base{httpClient: hc, keyPool: keyPool, searcher: searcher}, nil
}

// tmdbAnimationGenreID is TMDB's canonical genre ID for animation, shared
// across both movie and TV classifications.
const tmdbAnimationGenreID = 16

// Search resolves titles to TMDB ids. For ModeCartoon, it queries TMDB
// multi-search directly and filters exclusively for animation titles (genre
// ID 16). For movies/TV, it queries the meilisearch index and normalizes
// media types.
func (b *Base) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("stream search start", "mode", mode, "query", query)
	if mode == provider.ModeCartoon {
		results, err := b.searchTMDBCartoons(ctx, query)
		if err != nil {
			logging.Debug("stream cartoon search failed", "query", query, "err", err)
			return nil, fmt.Errorf("streambase cartoon search: %w", err)
		}
		logging.Debug("stream cartoon search done", "query", query, "results", len(results))
		return results, nil
	}

	searcher := b.searcher
	if searcher == nil {
		searcher = NewClient()
	}
	results, err := searcher.SearchWithMode(ctx, query, mode)
	if err != nil {
		if errors.Is(err, provider.ErrNoResults) {
			return nil, err
		}
		logging.Debug("stream search index failed, falling back to direct TMDB search", "mode", mode, "query", query, "err", err)
		fallbackResults, fallbackErr := b.searchTMDB(ctx, query, mode)
		if fallbackErr == nil && len(fallbackResults) > 0 {
			logging.Debug("stream search fallback to TMDB succeeded", "mode", mode, "query", query, "results", len(fallbackResults))
			return fallbackResults, nil
		}
		logging.Debug("stream search failed", "mode", mode, "query", query, "err", err)
		return nil, fmt.Errorf("streambase search: %w", err)
	}

	// Normalize the media type to the selected mode — meilisearch results
	// may carry a different vocabulary than the mode the user picked.
	providerResults := make([]provider.SearchResult, 0, len(results))
	for _, r := range results {
		mediaType := r.MediaType
		switch mode {
		case provider.ModeMovies:
			mediaType = provider.MediaTypeMovie
		case provider.ModeTV:
			mediaType = provider.MediaTypeTV
		}
		r.Type = mode
		r.MediaType = mediaType
		providerResults = append(providerResults, r)
	}
	logging.Debug("stream search done", "mode", mode, "query", query, "results", len(providerResults))
	return providerResults, nil
}

type tmdbMultiSearchResponse struct {
	Page         int                  `json:"page"`
	TotalPages   int                  `json:"total_pages"`
	TotalResults int                  `json:"total_results"`
	Results      []tmdbMultiSearchHit `json:"results"`
}

type tmdbMultiSearchHit struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	Name         string  `json:"name"`
	MediaType    string  `json:"media_type"`
	ReleaseDate  string  `json:"release_date"`
	FirstAirDate string  `json:"first_air_date"`
	GenreIDs     []int   `json:"genre_ids"`
	Popularity   float64 `json:"popularity"`
}

// searchTMDB queries TMDB multi-search directly when the search index is unreachable.
func (b *Base) searchTMDB(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	page1, err := b.fetchTMDBMultiSearchPage(ctx, query, 1)
	if err != nil {
		return nil, err
	}

	hits := page1.Results
	if page1.TotalPages > 1 && len(hits) < 10 {
		page2, err := b.fetchTMDBMultiSearchPage(ctx, query, 2)
		if err == nil && len(page2.Results) > 0 {
			hits = append(hits, page2.Results...)
		}
	}

	var results []provider.SearchResult
	seen := make(map[int]bool)

	targetMediaType := "movie"
	expectedMode := provider.ModeMovies
	if mode == provider.ModeTV {
		targetMediaType = "tv"
		expectedMode = provider.ModeTV
	}

	for _, h := range hits {
		if h.ID <= 0 || seen[h.ID] {
			continue
		}
		if h.MediaType != targetMediaType {
			continue
		}

		seen[h.ID] = true
		title := util.NormalizeSpace(h.Title)
		mediaType := provider.MediaTypeMovie
		yearStr := extractYear(h.ReleaseDate)

		if h.MediaType == "tv" {
			title = util.NormalizeSpace(h.Name)
			mediaType = provider.MediaTypeTV
			yearStr = extractYear(h.FirstAirDate)
		}
		if title == "" {
			continue
		}

		results = append(results, provider.SearchResult{
			Title:     title,
			ID:        strconv.Itoa(h.ID),
			Type:      expectedMode,
			Year:      yearStr,
			TMDBID:    h.ID,
			MediaType: mediaType,
		})
	}

	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

// searchTMDBCartoons queries the TMDB multi-search endpoint directly and filters
// results to include only animation titles (genre ID 16), ensuring cartoon mode
// returns only animated TV shows and movies.
func (b *Base) searchTMDBCartoons(ctx context.Context, query string) ([]provider.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	page1, err := b.fetchTMDBMultiSearchPage(ctx, query, 1)
	if err != nil {
		return nil, err
	}

	hits := page1.Results
	// If page 1 has fewer than 10 animation items and more pages exist, fetch
	// page 2 to provide a richer search result set.
	if page1.TotalPages > 1 && countAnimationHits(hits) < 10 {
		page2, err := b.fetchTMDBMultiSearchPage(ctx, query, 2)
		if err == nil && len(page2.Results) > 0 {
			hits = append(hits, page2.Results...)
		}
	}

	var results []provider.SearchResult
	seen := make(map[int]bool)

	for _, h := range hits {
		if h.ID <= 0 || seen[h.ID] {
			continue
		}
		// Exclude persons and non-animation content.
		if h.MediaType != "movie" && h.MediaType != "tv" {
			continue
		}
		if !hasAnimationGenre(h.GenreIDs) {
			continue
		}

		seen[h.ID] = true
		title := util.NormalizeSpace(h.Title)
		mediaType := provider.MediaTypeMovie
		yearStr := extractYear(h.ReleaseDate)

		if h.MediaType == "tv" {
			title = util.NormalizeSpace(h.Name)
			mediaType = provider.MediaTypeTV
			yearStr = extractYear(h.FirstAirDate)
		}
		if title == "" {
			continue
		}

		results = append(results, provider.SearchResult{
			Title:     title,
			ID:        strconv.Itoa(h.ID),
			Type:      provider.ModeCartoon,
			Year:      yearStr,
			TMDBID:    h.ID,
			MediaType: mediaType,
		})
	}

	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

func (b *Base) fetchTMDBMultiSearchPage(ctx context.Context, query string, page int) (tmdbMultiSearchResponse, error) {
	var lastAuthErr error
	for {
		apiKey, err := b.keyPool.NextKey()
		if err != nil {
			if lastAuthErr != nil {
				return tmdbMultiSearchResponse{}, fmt.Errorf("tmdb multi search auth failed after key rotation: %w", lastAuthErr)
			}
			return tmdbMultiSearchResponse{}, err
		}
		target := fmt.Sprintf("%s/search/multi?api_key=%s&query=%s&include_adult=false&page=%d",
			config.TMDBAPIBase,
			url.QueryEscape(apiKey),
			url.QueryEscape(query),
			page,
		)
		resp, err := fetchTMDBJSON[tmdbMultiSearchResponse](ctx, b, target)
		if err == nil {
			return resp, nil
		}
		if !isAuthError(err) {
			return resp, err
		}
		logging.Debug("tmdb multi search unauthorized, rotating key", "query", query, "page", page, "err", err)
		b.keyPool.MarkFailed(apiKey)
		lastAuthErr = err
	}
}

func countAnimationHits(hits []tmdbMultiSearchHit) int {
	count := 0
	for _, h := range hits {
		if (h.MediaType == "movie" || h.MediaType == "tv") && hasAnimationGenre(h.GenreIDs) {
			count++
		}
	}
	return count
}

func hasAnimationGenre(genres []int) bool {
	for _, g := range genres {
		if g == tmdbAnimationGenreID {
			return true
		}
	}
	return false
}

func extractYear(dateStr string) string {
	dateStr = strings.TrimSpace(dateStr)
	if len(dateStr) >= 4 {
		return dateStr[:4]
	}
	return ""
}

// FetchEpisodes returns a synthetic single episode for movies or the full
// season/episode listing from TMDB for series.
func (b *Base) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	tmdbID := series.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(series.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid tmdb id in search result: %w", err)
		}
	}

	if series.MediaType == provider.MediaTypeMovie {
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
		return nil, fmt.Errorf("streambase fetch episodes: %w", err)
	}

	seasonMap := make(map[int]tmdbSeasonDetails)
	var positiveSeasons []int
	for _, season := range details.Seasons {
		if season.SeasonNumber > 0 {
			positiveSeasons = append(positiveSeasons, season.SeasonNumber)
		}
	}

	// Batch fetch up to 20 seasons per call using append_to_response
	const maxAppendPerCall = 20
	for i := 0; i < len(positiveSeasons); i += maxAppendPerCall {
		end := i + maxAppendPerCall
		if end > len(positiveSeasons) {
			end = len(positiveSeasons)
		}
		chunk := positiveSeasons[i:end]
		batchRes, berr := b.fetchTMDBTVSeasonsBatch(ctx, tmdbID, chunk)
		if berr == nil {
			for k, v := range batchRes {
				seasonMap[k] = v
			}
		}
	}

	// For any season not returned in the batch, fall back to individual season fetch
	var missingSeasons []int
	for _, s := range positiveSeasons {
		if _, ok := seasonMap[s]; !ok {
			missingSeasons = append(missingSeasons, s)
		}
	}

	if len(missingSeasons) > 0 {
		g, gCtx := errgroup.WithContext(ctx)
		var mu sync.Mutex
		sem := make(chan struct{}, 5)
		for _, seasonNum := range missingSeasons {
			g.Go(func() error {
				sem <- struct{}{}
				defer func() { <-sem }()

				seasonDetails, ferr := b.fetchTMDBSeasonDetails(gCtx, tmdbID, seasonNum)
				if ferr != nil {
					return ferr
				}
				mu.Lock()
				seasonMap[seasonNum] = seasonDetails
				mu.Unlock()
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, fmt.Errorf("streambase fetch episodes: %w", err)
		}
	}

	episodes := make([]provider.Episode, 0, 256)
	for _, s := range positiveSeasons {
		sd, ok := seasonMap[s]
		if !ok {
			continue
		}
		for _, ep := range sd.Episodes {
			title := util.NormalizeSpace(ep.Name)
			if title == "" {
				title = fmt.Sprintf("Episode %d", ep.EpisodeNumber)
			}
			episodes = append(episodes, provider.Episode{
				Title:   title,
				ID:      series.ID,
				Season:  ep.SeasonNumber,
				Episode: ep.EpisodeNumber,
				TMDBID:  tmdbID,
			})
		}
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
		details, err := fetchTMDBJSON[tmdbTVDetails](ctx, b, target)
		if err == nil {
			return details, nil
		}
		if !isAuthError(err) {
			return details, err
		}
		logging.Debug("tmdb tv unauthorized, rotating key", "tmdbID", tmdbID, "err", err)
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
		details, err := fetchTMDBJSON[tmdbSeasonDetails](ctx, b, target)
		if err == nil {
			return details, nil
		}
		if !isAuthError(err) {
			return details, err
		}
		logging.Debug("tmdb season unauthorized, rotating key", "tmdbID", tmdbID, "season", season, "err", err)
		b.keyPool.MarkFailed(apiKey)
		lastAuthErr = err
	}
}
func (b *Base) fetchTMDBTVSeasonsBatch(ctx context.Context, tmdbID int, seasons []int) (map[int]tmdbSeasonDetails, error) {
	if len(seasons) == 0 {
		return nil, nil
	}
	parts := make([]string, len(seasons))
	for i, s := range seasons {
		parts[i] = fmt.Sprintf("season/%d", s)
	}
	appendToResponse := strings.Join(parts, ",")

	var lastAuthErr error
	for {
		apiKey, err := b.keyPool.NextKey()
		if err != nil {
			if lastAuthErr != nil {
				return nil, fmt.Errorf("tmdb tv seasons batch auth failed after key rotation: %w", lastAuthErr)
			}
			return nil, err
		}
		target := fmt.Sprintf("%s/tv/%d?api_key=%s&append_to_response=%s",
			config.TMDBAPIBase,
			tmdbID,
			url.QueryEscape(apiKey),
			url.QueryEscape(appendToResponse),
		)
		rawMap, err := fetchTMDBJSON[map[string]json.RawMessage](ctx, b, target)
		if err == nil {
			out := make(map[int]tmdbSeasonDetails, len(seasons))
			for _, s := range seasons {
				key := fmt.Sprintf("season/%d", s)
				if raw, ok := rawMap[key]; ok {
					var sd tmdbSeasonDetails
					if uerr := json.Unmarshal(raw, &sd); uerr == nil && len(sd.Episodes) > 0 {
						out[s] = sd
					}
				}
			}
			return out, nil
		}
		if !isAuthError(err) {
			return nil, err
		}
		logging.Debug("tmdb tv seasons batch unauthorized, rotating key", "tmdbID", tmdbID, "err", err)
		b.keyPool.MarkFailed(apiKey)
		lastAuthErr = err
	}
}

func fetchTMDBJSON[T any](ctx context.Context, b *Base, target string) (T, error) {
	var zero T
	headers := map[string]string{"Accept": "application/json"}
	resp, err := b.doRequest(ctx, target, headers)
	if err != nil {
		return zero, fmt.Errorf("streambase request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return zero, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	raw, err := httpclient.ReadCapped(resp)
	if err != nil {
		return zero, fmt.Errorf("streambase read body: %w", err)
	}
	var payload T
	if err := json.Unmarshal(raw, &payload); err != nil {
		return zero, fmt.Errorf("streambase decode response: %w", err)
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
	type epSortKey struct {
		ep         provider.Episode
		season     int
		number     int
		lowerTitle string
	}
	keys := make([]epSortKey, len(results))
	for i, ep := range results {
		keys[i] = epSortKey{
			ep:         ep,
			season:     seasonForSort(ep),
			number:     numberForSort(ep),
			lowerTitle: strings.ToLower(ep.Title),
		}
	}
	slices.SortFunc(keys, func(a, b epSortKey) int {
		if a.number < 0 && b.number >= 0 {
			return 1
		}
		if a.number >= 0 && b.number < 0 {
			return -1
		}
		if a.season != b.season {
			return a.season - b.season
		}
		if a.number != b.number {
			return a.number - b.number
		}
		return strings.Compare(a.lowerTitle, b.lowerTitle)
	})
	for i, k := range keys {
		results[i] = k.ep
	}
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
