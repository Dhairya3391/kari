package wco

import (
	"context"
	"errors"
	"fmt"
	htmlpkg "html"
	"io"
	"net/http"

	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/sync/errgroup"

	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/provider"
)

func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debugf("wco search start query=%q mode=%q", query, mode)
	if strings.TrimSpace(query) == "" {
		logging.Warnf("wco search rejected empty query")
		return nil, fmt.Errorf("empty query")
	}

	// Try multiple query variants for better results
	attempts := c.suggestQueryVariants(query)
	if !containsStr(attempts, query) {
		attempts = append([]string{query}, attempts...)
	}

	var mu sync.Mutex
	allSeen := make(map[string]provider.SearchResult)
	bestQuery := query

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	g, gCtx := errgroup.WithContext(raceCtx)
	for _, q := range attempts {
		q := q
		g.Go(func() error {
			logging.Debugf("wco searching variant q=%q", q)
			html, err := c.postSearchSeries(gCtx, q)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				logging.Debugf("wco search variant failed q=%q err=%v", q, err)
				return nil
			}
			results := c.parseSearchResults(html)
			if len(results) > 0 {
				mu.Lock()
				for _, r := range results {
					allSeen[r.ID] = r
				}
				if len(results) >= 3 {
					bestQuery = q
					cancel()
				}
				mu.Unlock()
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		logging.Warnf("wco search workers failed query=%q err=%v", query, err)
		return nil, fmt.Errorf("wco search workers: %w", err)
	}

	// Mode-based filtering/prioritization
	var filtered []provider.SearchResult
	for _, r := range allSeen {
		if mode == provider.ModeMovies && !strings.Contains(r.ID, "/anime/") {
			filtered = append(filtered, r)
		} else if (mode == provider.ModeAnime || mode == provider.ModeCartoon) && strings.Contains(r.ID, "/anime/") {
			filtered = append(filtered, r)
		} else if mode == "" {
			filtered = append(filtered, r)
		}
	}

	if len(filtered) > 0 {
		return c.rankSeriesByQuery(filtered, bestQuery), nil
	}

	// Fallback to index lists
	logging.Debugf("wco no direct results, trying index candidates query=%q", query)
	indexCandidates := c.fetchIndexCandidates(ctx)
	if len(indexCandidates) > 0 {
		ranked := c.rankSeriesByQuery(indexCandidates, query)

		// Filter index candidates by mode, consistent with the direct path.
		var modeFiltered []provider.SearchResult
		for _, r := range ranked {
			switch mode {
			case provider.ModeMovies:
				if !strings.Contains(r.ID, "/anime/") {
					modeFiltered = append(modeFiltered, r)
				}
			case provider.ModeAnime, provider.ModeCartoon:
				if strings.Contains(r.ID, "/anime/") {
					modeFiltered = append(modeFiltered, r)
				}
			default:
				modeFiltered = append(modeFiltered, r)
			}
		}
		if len(modeFiltered) > 0 {
			logging.Debugf("wco index candidates found count=%d", len(modeFiltered))
			return modeFiltered, nil
		}
	}

	// Best-effort: return whatever direct matches we had even if the mode
	// filter excluded them, unless the mode explicitly asked for movies.
	if len(allSeen) > 0 && mode != provider.ModeMovies {
		results := make([]provider.SearchResult, 0, len(allSeen))
		for _, r := range allSeen {
			results = append(results, r)
		}
		return c.rankSeriesByQuery(results, bestQuery), nil
	}

	logging.Warnf("wco search no results found query=%q", query)
	return nil, provider.ErrNoResults
}

func (c *Client) suggestQueryVariants(query string) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	variants := []string{q}

	// 1. Remove punctuation
	noPunct := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			return r
		}
		return ' '
	}, q)
	noPunct = strings.Join(strings.Fields(noPunct), " ")
	if noPunct != "" && noPunct != q {
		variants = append(variants, noPunct)
	}

	// 2. Squash repeated characters (e.g., "shinn chan" -> "shin chan")
	tokens := strings.Fields(q)
	if len(tokens) > 0 {
		squashed := make([]string, 0, len(tokens))
		for _, t := range tokens {
			var sb strings.Builder
			for i, r := range t {
				if i > 0 && r == rune(t[i-1]) {
					continue
				}
				sb.WriteRune(r)
			}
			squashed = append(squashed, sb.String())
		}
		squashedQuery := strings.Join(squashed, " ")
		if squashedQuery != q {
			variants = append(variants, squashedQuery)
		}

		// 3. Transposed characters (basic)
		transposed := make([]string, 0, len(tokens))
		for _, t := range tokens {
			if len(t) < 4 {
				transposed = append(transposed, t)
				continue
			}
			best := t
			bestScore := 0.0
			runes := []rune(t)
			for i := 0; i < len(runes)-1; i++ {
				swapped := make([]rune, len(runes))
				copy(swapped, runes)
				swapped[i], swapped[i+1] = swapped[i+1], swapped[i]
				cand := string(swapped)
				// Simple similarity check
				score := 0.0
				if strings.Contains(t, cand) || strings.Contains(cand, t) {
					score = 1.0
				}
				if score > bestScore {
					bestScore = score
					best = cand
				}
			}
			transposed = append(transposed, best)
		}
		transposedQuery := strings.Join(transposed, " ")
		if transposedQuery != q {
			variants = append(variants, transposedQuery)
		}

		// 4. Concatenated tokens
		concatenated := strings.Join(tokens, "")
		if concatenated != q {
			variants = append(variants, concatenated)
		}
	}

	// Deduplicate
	seen := make(map[string]bool)
	unique := make([]string, 0, len(variants))
	for _, v := range variants {
		if !seen[v] {
			seen[v] = true
			unique = append(unique, v)
		}
	}
	return unique
}

func (c *Client) parseSearchResults(html string) []provider.SearchResult {
	// Simplified HTML parsing using regexp (similar to Python)
	// <ul class="items">...</ul>
	match := itemsUlRe.FindStringSubmatch(html)
	if len(match) < 2 {
		return nil
	}
	block := match[1]

	// <a href="...">Title</a>
	matches := searchResultRe.FindAllStringSubmatch(block, -1)

	results := make([]provider.SearchResult, 0)
	for _, m := range matches {
		href := htmlpkg.UnescapeString(strings.TrimSpace(m[1]))
		title := normalizeEpisodeTitle(m[2])

		fullURL := href
		if strings.HasPrefix(href, "/") {
			fullURL = config.BaseURL + href
		}

		if !strings.Contains(fullURL, "/anime/") {
			continue
		}

		results = append(results, provider.SearchResult{
			Title:     title,
			ID:        fullURL,
			Type:      provider.ModeCartoon,
			MediaType: "cartoon",
		})
	}
	return results
}

func (c *Client) fetchIndexCandidates(ctx context.Context) []provider.SearchResult {
	pages := []struct {
		url  string
		mode provider.ContentType
	}{
		{config.BaseURL + "/dubbed-anime-list", provider.ModeCartoon},
		{config.BaseURL + "/subbed-anime-list", provider.ModeCartoon},
		{config.BaseURL + "/cartoon-list", provider.ModeCartoon},
	}
	var mu sync.Mutex
	allItems := make(map[string]provider.SearchResult)

	g, gCtx := errgroup.WithContext(ctx)
	for _, p := range pages {
		p := p
		g.Go(func() error {
			resp, err := c.doRequest(gCtx, http.MethodGet, p.url, map[string]string{"Referer": config.BaseURL + "/"}, nil)
			if err != nil {
				logging.Debugf("wco index candidate fetch failed url=%q: %v", p.url, err)
				return nil
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				logging.Debugf("wco index candidate read failed url=%q: %v", p.url, err)
				return nil
			}

			matches := animeListLinkRe.FindAllStringSubmatch(string(raw), -1)
			mu.Lock()
			defer mu.Unlock()
			for _, m := range matches {
				href := m[1]
				fullURL := href
				if strings.HasPrefix(href, "/") {
					fullURL = config.BaseURL + href
				}

				parts := strings.Split(fullURL, "/")
				slug := parts[len(parts)-1]
				if slug == "" {
					continue
				}
				title := strings.ReplaceAll(slug, "-", " ")
				runes := []rune(title)
				runes[0] = unicode.ToUpper(runes[0])
				title = string(runes)

				allItems[fullURL] = provider.SearchResult{
					Title:     title,
					ID:        fullURL,
					Type:      p.mode,
					MediaType: "cartoon",
				}
			}
			return nil
		})
	}
	_ = g.Wait()

	res := make([]provider.SearchResult, 0, len(allItems))
	for _, r := range allItems {
		res = append(res, r)
	}
	return res
}

func (c *Client) rankSeriesByQuery(results []provider.SearchResult, query string) []provider.SearchResult {
	// Simple ranking based on title similarity
	type scored struct {
		r     provider.SearchResult
		score float64
	}

	scoredResults := make([]scored, 0, len(results))
	q := strings.ToLower(query)
	for _, r := range results {
		s := 0.0
		t := strings.ToLower(r.Title)
		if t == q {
			s += 1.0
		} else if strings.HasPrefix(t, q) {
			s += 0.8
		} else if strings.Contains(t, q) {
			s += 0.5
		}
		scoredResults = append(scoredResults, scored{r, s})
	}

	sort.Slice(scoredResults, func(i, j int) bool {
		return scoredResults[i].score > scoredResults[j].score
	})

	final := make([]provider.SearchResult, 0, len(results))
	for _, sr := range scoredResults {
		final = append(final, sr.r)
	}
	return final
}

func (c *Client) postSearchSeries(ctx context.Context, query string) (string, error) {
	logging.Debugf("wco postSearchSeries query=%q", query)
	form := url.Values{}
	form.Set("catara", query)
	form.Set("konuara", "series")

	body := []byte(form.Encode())
	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Origin":       config.BaseURL,
		"Referer":      config.BaseURL + "/",
	}
	resp, err := c.doRequest(ctx, http.MethodPost, config.BaseURL+"/search", headers, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: config.BaseURL + "/search"}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}
	return string(raw), nil
}
