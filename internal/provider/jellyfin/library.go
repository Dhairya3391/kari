package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"kari/internal/logging"
	"kari/internal/provider"
)

const (
	libraryTTL       = 5 * time.Minute
	maxSearchResults = 50
)

// getLibrary returns the cached Movie/Series library, refreshing it when stale.
// A stale cache is served if the refresh fails, so search keeps working during
// transient server hiccups.
func (c *Client) getLibrary(ctx context.Context) ([]provider.SearchResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.library != nil && time.Since(c.libraryAt) < libraryTTL {
		return c.library, nil
	}

	results, err := c.fetchLibrary(ctx)
	if err != nil {
		if c.library != nil {
			logging.Debugf("jellyfin library refresh failed, serving stale cache: %v", err)
			return c.library, nil
		}
		return nil, err
	}

	c.library = results
	c.libraryAt = time.Now()
	logging.Debugf("jellyfin library cached items=%d", len(results))
	return results, nil
}

func (c *Client) fetchLibrary(ctx context.Context) ([]provider.SearchResult, error) {
	u := fmt.Sprintf("%s/Items?includeItemTypes=Movie,Series&Recursive=true&sortBy=SortName&sortOrder=Ascending", c.server)
	resp, err := c.authGET(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	var ir itemsResult
	if err := json.NewDecoder(resp.Body).Decode(&ir); err != nil {
		return nil, err
	}

	results := make([]provider.SearchResult, 0, len(ir.Items))
	for _, it := range ir.Items {
		mediaType := ""
		switch it.Type {
		case "Movie":
			mediaType = "movie"
		case "Series":
			mediaType = "tv"
		default:
			continue
		}
		if it.ID == "" {
			continue
		}

		year := ""
		if it.ProductionYear > 0 {
			year = fmt.Sprintf("%d", it.ProductionYear)
		}

		results = append(results, provider.SearchResult{
			Title:     it.Name,
			ID:        it.ID,
			Type:      provider.ModeJellyfin,
			Year:      year,
			MediaType: mediaType,
		})
	}
	return results, nil
}

// rankLibrary filters library items matching query and orders them best-first.
// An empty query returns the full library (browse mode).
func rankLibrary(library []provider.SearchResult, query string) []provider.SearchResult {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return library
	}

	type scored struct {
		result provider.SearchResult
		score  int
	}
	var matched []scored
	for _, r := range library {
		s := matchScore(strings.ToLower(r.Title), q)
		if s < 0 {
			continue
		}
		matched = append(matched, scored{result: r, score: s})
	}

	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].score != matched[j].score {
			return matched[i].score < matched[j].score
		}
		return matched[i].result.Title < matched[j].result.Title
	})

	if len(matched) > maxSearchResults {
		matched = matched[:maxSearchResults]
	}
	results := make([]provider.SearchResult, 0, len(matched))
	for _, s := range matched {
		results = append(results, s.result)
	}
	return results
}

// matchScore ranks how well title matches query. Lower is better, -1 means no match.
// Both inputs must already be lowercased.
func matchScore(title, query string) int {
	switch {
	case title == query:
		return 0
	case strings.HasPrefix(title, query):
		return 1
	case hasWordPrefix(title, query):
		return 2
	case strings.Contains(title, query):
		return 3
	case containsAllWords(title, query):
		return 4
	case len(query) >= 3 && isSubsequence(title, query):
		return 5
	}
	return -1
}

// hasWordPrefix reports whether any word in title starts with query,
// so "w" matches "The Walking Dead".
func hasWordPrefix(title, query string) bool {
	for _, w := range strings.FieldsFunc(title, isWordSeparator) {
		if strings.HasPrefix(w, query) {
			return true
		}
	}
	return false
}

// containsAllWords reports whether every word of a multi-word query appears
// somewhere in title, so "star clone" matches "Star Wars: The Clone Wars".
func containsAllWords(title, query string) bool {
	words := strings.Fields(query)
	if len(words) < 2 {
		return false
	}
	for _, w := range words {
		if !strings.Contains(title, w) {
			return false
		}
	}
	return true
}

func isWordSeparator(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsNumber(r)
}

// isSubsequence reports whether query's characters appear in order in title,
// so "brba" matches "Breaking Bad".
func isSubsequence(title, query string) bool {
	qr := []rune(query)
	i := 0
	for _, r := range title {
		if i < len(qr) && r == qr[i] {
			i++
		}
	}
	return i == len(qr)
}
