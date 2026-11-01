package service

import (
	"strings"

	"kari/internal/provider"
)

// FilterSearchResults returns matching results and their original indices.
// An empty query returns the input slice directly with no index allocation.
func FilterSearchResults(results []provider.SearchResult, query string) ([]provider.SearchResult, []int) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return results, nil
	}
	indices := make([]int, 0, len(results))
	filtered := make([]provider.SearchResult, 0, len(results))
	for index, result := range results {
		matched := strings.Contains(strings.ToLower(result.Title), query) ||
			strings.Contains(strings.ToLower(strings.TrimSpace(result.Year)), query)
		if !matched && !strings.EqualFold(result.Group, "Channel") {
			matched = strings.Contains(strings.ToLower(result.Group), query)
		}
		for _, genre := range result.Genres {
			if matched {
				break
			}
			if strings.Contains(strings.ToLower(genre), query) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		filtered = append(filtered, result)
		indices = append(indices, index)
	}
	return filtered, indices
}
