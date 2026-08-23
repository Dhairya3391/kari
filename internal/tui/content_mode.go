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

// availableLanguages returns the audio languages declared by the providers
// supporting the active mode. Empty when no active provider tags audio
// languages. Movies and TV intentionally share one pool — the same providers
// serve both, so filters must behave identically across them.
func (m *modelImpl) availableLanguages() []provider.AudioLanguage {
	modes := []provider.ContentType{m.appMode}
	switch m.appMode {
	case provider.ModeMovies, provider.ModeTV:
		modes = []provider.ContentType{provider.ModeMovies, provider.ModeTV}
	}
	return m.registry.AudioLanguages(modes...)
}
