package tui

import (
	"strconv"
	"strings"

	"kari/internal/model"
)

func resultTypeLabel(item model.SearchResult) string {
	switch strings.ToLower(strings.TrimSpace(item.MediaType)) {
	case "movie":
		return "Movie"
	case "tv":
		return "Series"
	case "anime":
		if count, ok := parseAnimeEpisodeCount(item.URL); ok && count <= 1 {
			return "Anime Movie"
		}
		return "Anime"
	case "cartoon":
		return "Cartoon"
	default:
		return "Title"
	}
}

func directEpisodeForResult(item model.SearchResult) (model.EpisodeResult, bool) {
	if strings.EqualFold(item.Provider, "tmdb") && strings.EqualFold(item.MediaType, "movie") {
		return model.EpisodeResult{
			Title:     item.Title,
			Kind:      "movie",
			Provider:  "tmdb",
			MediaType: "movie",
		}, true
	}

	return model.EpisodeResult{}, false
}

func parseAnimeEpisodeCount(url string) (int, bool) {
	parts := strings.Split(url, "||")
	if len(parts) != 2 {
		return 0, false
	}
	count, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, false
	}
	return count, true
}
