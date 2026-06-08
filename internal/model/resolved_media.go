package model

import (
	"fmt"
	"strings"
)

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
		return strings.TrimSpace(strings.Join([]string{prefix, episodeTag}, " - "))
	}
	if episodeTag == "" {
		return prefix + " - " + episode
	}
	return prefix + " - " + episodeTag + " - " + episode
}

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

func isMovieLike(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie", "film":
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
