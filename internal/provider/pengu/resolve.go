package pengu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// streamResponse is the stream resource envelope PenguPlay returns.
type streamResponse struct {
	Streams []streamItem `json:"streams"`
}

// streamItem is one playback lane. Rows without a URL are promotional or
// external entries and carry no stream of their own.
type streamItem struct {
	URL         string           `json:"url"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Subtitles   []streamSubtitle `json:"subtitles"`
	Behavior    streamBehavior   `json:"behaviorHints"`
}

// streamSubtitle is one sidecar subtitle attached to a lane.
type streamSubtitle struct {
	URL   string `json:"url"`
	Lang  string `json:"lang"`
	Label string `json:"label"`
}

// streamBehavior carries the filename and size hints the stream row declares.
type streamBehavior struct {
	Filename  string `json:"filename"`
	VideoSize int64  `json:"videoSize"`
}

// ResolveSource fetches every playable lane PenguPlay advertises for one
// TMDB coordinate, or the single lane set behind a live catalog item. Movies
// resolve from the title id alone; series add season and episode numbers to
// the same handle.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	if isLiveHandle(mediaID) || isLiveHandle(episode.ID) {
		return c.resolveLive(ctx, mediaID, episode)
	}

	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		parsed, err := strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("pengu: invalid TMDB id %q: %w", mediaID, provider.ErrNoSources)
		}
		tmdbID = parsed
	}

	// PenguPlay follows Stremio's resource vocabulary: episodic titles are
	// addressed as "series", never "tv".
	kind := "movie"
	handle := "tmdb:" + strconv.Itoa(tmdbID)
	if episode.Season > 0 || episode.Episode > 0 {
		kind = "series"
		handle += fmt.Sprintf(":%d:%d", max(1, episode.Season), max(1, episode.Episode))
	}

	endpoint := c.apiBase + "/stream/" + kind + "/" + url.PathEscape(handle) + ".json"
	items, err := c.fetchStreams(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	sources := provider.FilterDirectSources(c.buildSources(items))
	if len(sources) == 0 {
		logging.Debug("pengu returned no playable lanes", "tmdb", tmdbID, "season", episode.Season, "episode", episode.Episode)
		return nil, provider.ErrNoSources
	}
	logging.Debug("pengu resolve done", "provider", c.Name(), "tmdb", tmdbID, "sources", len(sources))
	return sources, nil
}

// fetchStreams reads the stream resource envelope for one endpoint.
func (c *Client) fetchStreams(ctx context.Context, endpoint string) ([]streamItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("pengu stream request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, provider.WrapHint("pengu", "stream", provider.KindUpstream, "stream request failed", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, provider.WrapHint("pengu", "stream", provider.KindAuthRequired,
			"PenguPlay rejected the bundled access token",
			&provider.HTTPError{Code: resp.StatusCode})
	default:
		// endpoint carries the bundled token in its path; never attach it to
		// an error that could reach a log or a user-facing message.
		return nil, &provider.HTTPError{Code: resp.StatusCode}
	}

	var payload streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("pengu decode streams: %w", err)
	}
	return payload.Streams, nil
}

// buildSources converts every lane with a play URL into a media source and
// removes duplicate transports while preserving lane order.
func (c *Client) buildSources(items []streamItem) []provider.MediaSource {
	sources := make([]provider.MediaSource, 0, len(items))
	for _, item := range items {
		raw := strings.TrimSpace(item.URL)
		if raw == "" {
			continue
		}
		quality, height, sourceType := laneMetadata(item)
		sources = append(sources, provider.MediaSource{
			URL:       raw,
			Quality:   quality,
			Height:    height,
			Type:      sourceType,
			Backend:   backendFromDescription(item.Description),
			Subtitles: subtitleOptions(item.Subtitles),
			// The play endpoint redirects to PenguPlay's own signed direct
			// file, so no referer or browser identity is needed.
		})
	}
	return kit.DedupeSources(sources)
}

// laneMetadata reads the quality, height and container from the lane's
// "🎞️ 4K • HLS • HEVC • ~5.1 Mbps" description line, falling back to the
// filename hint and then the display name.
func laneMetadata(item streamItem) (string, int, string) {
	var quality, sourceType string
	var height int
	for _, raw := range []string{metadataLine(item.Description), item.Behavior.Filename, item.Name} {
		q, h, s := parseMetadata(raw)
		if quality == "" && q != "" {
			quality, height = q, h
		}
		if sourceType == "" && s != "" {
			sourceType = s
		}
		if quality != "" && sourceType != "" {
			return quality, height, sourceType
		}
	}
	if quality != "" {
		return quality, height, sourceType
	}
	return "Auto", 0, ""
}

// metadataLine returns the description line that names the stream properties.
func metadataLine(description string) string {
	for _, line := range strings.Split(description, "\n") {
		if strings.Contains(line, "🎞️") {
			return line
		}
	}
	lines := strings.Split(strings.TrimSpace(description), "\n")
	if len(lines) > 1 {
		return lines[1]
	}
	return ""
}

// parseMetadata splits a bullet-separated metadata line and keeps the first
// resolution token plus the declared container. Filename hints concatenate
// the container and bitrate (for example "4KHLSHEVC~5.1Mbps"), so HLS, MP4
// and MKV are matched case-insensitively anywhere in a token.
func parseMetadata(raw string) (string, int, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, ""
	}
	var (
		quality    string
		height     int
		sourceType string
	)
	for _, token := range strings.Split(raw, "•") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		upper := strings.ToUpper(token)
		switch {
		case strings.Contains(upper, "HLS") && sourceType == "":
			sourceType = provider.SourceTypeHLS
		case (strings.Contains(upper, "MP4") || strings.Contains(upper, "MKV")) && sourceType == "":
			sourceType = provider.SourceTypeMP4
		}
		if quality != "" {
			continue
		}
		if parsedHeight, label := kit.NormalizeQuality(token); parsedHeight > 0 {
			quality, height = label, parsedHeight
		}
	}
	return quality, height, sourceType
}

// backendFromDescription extracts the lane's declared source name from the
// "🛰️ Source: 2Peckle" line.
func backendFromDescription(description string) string {
	for _, line := range strings.Split(description, "\n") {
		index := strings.Index(line, "Source:")
		if index < 0 {
			continue
		}
		if value := strings.TrimSpace(line[index+len("Source:"):]); value != "" {
			return value
		}
	}
	return ""
}

// subtitleOptions keeps one track per URL and normalizes its language tag so
// the player can match the user's preferred language.
func subtitleOptions(subs []streamSubtitle) []provider.SubtitleOption {
	if len(subs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(subs))
	options := make([]provider.SubtitleOption, 0, len(subs))
	for _, sub := range subs {
		raw := strings.TrimSpace(sub.URL)
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		options = append(options, provider.SubtitleOption{
			URL:      raw,
			Language: subtitleLanguage(sub),
		})
	}
	return options
}

// subtitleLanguage normalizes either an ISO code or a display name, preferring
// the declared tag and falling back to the label.
func subtitleLanguage(sub streamSubtitle) string {
	if code := lang.Normalize(sub.Lang); code != "" {
		return code
	}
	if code := lang.FromLabel(sub.Lang); code != "" {
		return code
	}
	if code := lang.FromLabel(sub.Label); code != "" {
		return code
	}
	return "und"
}
