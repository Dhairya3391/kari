package vumeto

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// rePlayerSession extracts the short-lived session token minted with each
// embed document. The resolve endpoint rejects requests without it.
var rePlayerSession = regexp.MustCompile(`name="vumeto-session"[^>]*content="([^"]+)"`)

// playerResolveResponse is the embed player's resolution envelope. Only the
// sources array is used: lanes whose URL is player-relative cannot be played
// by mpv and are dropped by the direct-URL filter.
type playerResolveResponse struct {
	Sources []playerSource `json:"sources"`
}

// playerSource is one stream lane returned by /_player/resolve.
type playerSource struct {
	URL                string           `json:"url"`
	Type               string           `json:"type"`
	Quality            string           `json:"quality"`
	Label              string           `json:"label"`
	Language           string           `json:"language"`
	PreferredAudioLang string           `json:"preferredAudioLang"`
	Provider           string           `json:"provider"`
	Subtitles          []playerSubtitle `json:"subtitles"`
}

// playerSubtitle is one sidecar track attached to a player source.
type playerSubtitle struct {
	URL   string `json:"url"`
	Lang  string `json:"lang"`
	Label string `json:"label"`
}

// resolvePlayer resolves movies and TV through the embed player and keeps only
// the lanes that are directly playable outside the iframe.
func (c *Client) resolvePlayer(ctx context.Context, tmdbID int, episode provider.Episode) ([]provider.MediaSource, error) {
	embedPath := fmt.Sprintf("/embed/movie/%d", tmdbID)
	mediaKind := provider.MediaTypeMovie
	season, number := 0, 0
	if episode.Season > 0 || episode.Episode > 0 {
		season = episode.Season
		if season <= 0 {
			season = 1
		}
		number = episode.Episode
		if number <= 0 {
			number = 1
		}
		embedPath = fmt.Sprintf("/embed/tv/%d/%d/%d", tmdbID, season, number)
		mediaKind = provider.MediaTypeTV
	}

	embedURL := fmt.Sprintf("%s%s?parentOrigin=%s", c.endpoints.Player, embedPath, url.QueryEscape(c.endpoints.Site))
	body, err := c.get(ctx, embedURL, map[string]string{"Accept": "text/html"})
	if err != nil {
		return nil, fmt.Errorf("vumeto player embed: %w", err)
	}
	match := rePlayerSession.FindSubmatch(body)
	if len(match) < 2 {
		return nil, fmt.Errorf("vumeto player embed: %w", provider.ErrUpstreamChanged)
	}
	session := string(match[1])

	params := url.Values{}
	params.Set("type", mediaKind)
	params.Set("id", strconv.Itoa(tmdbID))
	if mediaKind == provider.MediaTypeTV {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(number))
	}
	params.Set("provider", "auto")
	params.Set("mode", "all")
	target := fmt.Sprintf("%s/_player/resolve?%s", c.endpoints.Player, params.Encode())
	var payload playerResolveResponse
	if err := c.getJSON(ctx, target, map[string]string{"X-Vumeto-Session": session}, &payload); err != nil {
		return nil, fmt.Errorf("vumeto player resolve: %w", err)
	}

	var sources []provider.MediaSource
	for _, lane := range payload.Sources {
		if !provider.IsDirectURL(lane.URL) {
			continue
		}
		quality := strings.TrimSpace(lane.Quality)
		if quality == "" {
			quality = strings.TrimSpace(lane.Label)
		}
		height := 0
		if !kit.IsAdaptiveQuality(quality) {
			height = kit.QualityHeight(quality)
		}
		if quality == "" || kit.IsAdaptiveQuality(quality) {
			quality = "Auto"
		}
		source := provider.MediaSource{
			URL:     lane.URL,
			Quality: quality,
			Backend: playerBackend(lane.Provider),
			Type:    kit.NormalizeSourceKind(lane.URL),
			Height:  height,
		}
		if code := lang.Normalize(lane.PreferredAudioLang); code != "" {
			source.Language = code
			source.PreferredAudio = code
			source.Audio = provider.AudioInfo{Languages: []string{code}, Track: provider.TrackUnknown}
		} else if code := lang.FromLabel(lane.Language); code != "" && code != "original" {
			source.Language = code
		}
		for _, sub := range lane.Subtitles {
			subURL := strings.TrimSpace(sub.URL)
			if !provider.IsDirectURL(subURL) {
				continue
			}
			code := lang.Normalize(sub.Lang)
			if code == "" {
				code = lang.FromLabel(sub.Label)
			}
			if code == "" {
				code = "und"
			}
			source.Subtitles = append(source.Subtitles, provider.SubtitleOption{URL: subURL, Language: code})
		}
		sources = append(sources, source)
	}
	sources = kit.DedupeSources(sources)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	logging.Debug("player resolve done", "provider", c.Name(), "tmdb", tmdbID, "sources", len(sources))
	return sources, nil
}

// playerBackend renders a player lane id as a stable backend label.
func playerBackend(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "uwu":
		return "Kumo"
	case "stream-library":
		return "Astra"
	case "moviebox-0930":
		return "Orion"
	case "streamm4u":
		return "Flux"
	case "vidy-miami":
		return "Vega"
	case "aniembed":
		return "Zen"
	default:
		return strings.TrimSpace(id)
	}
}
