package tui

import (
	"strings"

	"kari/internal/provider"
)

func resultTypeLabel(item provider.SearchResult) string {
	switch strings.ToLower(strings.TrimSpace(item.MediaType)) {
	case provider.MediaTypeMovie:
		return "Movie"
	case provider.MediaTypeTV:
		return "Series"
	case provider.MediaTypeAnime:
		return "Anime"
	case provider.MediaTypeCartoon:
		return "Cartoon"
	default:
		return "Title"
	}
}

// historyKindLabel produces a short human-readable content kind (e.g. "Anime
// Movie", "TV", "Cartoon") from a history entry's Mode+MediaType — the two
// fields together disambiguate cases resultTypeLabel can't from MediaType
// alone, like an anime movie (Mode="anime", MediaType="movie") vs a
// live-action movie (Mode="movies", MediaType="movie").
func historyKindLabel(mode, mediaType string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	switch {
	case mode == string(provider.ModeAnime) && mt == provider.MediaTypeMovie:
		return "Anime Movie"
	case mode == string(provider.ModeAnime):
		return "Anime"
	case mt == provider.MediaTypeMovie:
		return "Movie"
	case mt == provider.MediaTypeCartoon || mode == string(provider.ModeCartoon):
		return "Cartoon"
	case mt == provider.MediaTypeTV:
		return "TV"
	default:
		return "Title"
	}
}

// modeFeatures returns the aggregated provider-declared features for the
// active content mode. The TUI consults this instead of hardcoding
// per-mode or per-provider behavior.
func (m *modelImpl) modeFeatures() provider.Features {
	return m.registry.Features(m.appMode)
}

// availableLanguages returns the audio languages declared across all providers.
// Audio language filters in settings are shown globally across all modes so
// users can configure language preferences for movies, TV, and cartoons
// without having to switch modes first.
func (m *modelImpl) availableLanguages() []provider.AudioLanguage {
	return m.registry.AudioLanguages()
}
