package model

import (
	"fmt"
	"strings"

	"kari/internal/provider"
)

// DisplayTitle renders "Series - S01E02 - Episode" (or "Movie (Year)")
// for status lines and download titles.
func (r ResolvedMedia) DisplayTitle() string {
	series := strings.TrimSpace(r.SeriesTitle)
	episode := strings.TrimSpace(r.EpisodeTitle)
	year := strings.TrimSpace(r.Year)

	if isMovieLike(r.MediaType) {
		title := firstNonEmpty(series, episode)
		if title == "" {
			return ""
		}
		if year != "" && !strings.Contains(title, "("+year+")") {
			return title + " (" + year + ")"
		}
		return title
	}

	prefix := series
	if prefix == "" {
		prefix = episode
	}
	if prefix == "" {
		return ""
	}

	episodeTag := episodeNumberTag(r.SeasonNumber, r.EpisodeNumber)
	if sameTitle(series, episode) || episode == "" {
		if episodeTag == "" {
			return prefix
		}
		return prefix + " - " + episodeTag
	}
	if episodeTag == "" {
		return prefix + " - " + episode
	}
	return prefix + " - " + episodeTag + " - " + episode
}

// SubtitlePaths returns on-disk paths of downloaded subtitle tracks, for
// players that take subtitle files as arguments.
func (r ResolvedMedia) SubtitlePaths() []string {
	out := make([]string, 0, len(r.Subtitles))
	for _, sub := range r.Subtitles {
		path := strings.TrimSpace(sub.Path)
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}

// IsEpisodeBased reports whether the given media type is episodic content
// (anime, TV, cartoons) as opposed to a one-shot movie. It is the canonical
// predicate for episode-driven behavior such as autoplay-next.
func IsEpisodeBased(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case provider.MediaTypeAnime, provider.MediaTypeTV, provider.MediaTypeCartoon:
		return true
	default:
		return false
	}
}

func isMovieLike(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case provider.MediaTypeMovie, "film":
		return true
	default:
		return false
	}
}

func episodeNumberTag(season, episode int) string {
	switch {
	case season > 0 && episode > 0:
		return fmt.Sprintf("S%02dE%02d", season, episode)
	case episode > 0:
		return fmt.Sprintf("E%02d", episode)
	default:
		return ""
	}
}

func sameTitle(a, b string) bool {
	return normalizeTitle(a) != "" && normalizeTitle(a) == normalizeTitle(b)
}

func normalizeTitle(v string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(v)), " "))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
