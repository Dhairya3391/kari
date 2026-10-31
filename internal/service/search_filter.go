package service

import (
	"strings"

	"kari/internal/provider"
)

// FilterSearchResults returns matching results and their indices in the
// original catalog slice. An empty query preserves the original slice.
func FilterSearchResults(results []provider.SearchResult, query string) ([]provider.SearchResult, []int) {
	query = strings.ToLower(strings.TrimSpace(query))
	indices := make([]int, 0, len(results))
	filtered := make([]provider.SearchResult, 0, len(results))
	for index, result := range results {
		if query != "" {
			haystack := strings.ToLower(result.Title) + " " + strings.ToLower(strings.TrimSpace(result.Year))
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		filtered = append(filtered, result)
		indices = append(indices, index)
	}
	return filtered, indices
}
