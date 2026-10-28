package vidrift

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

const (
	// embedMetaMarker prefixes the JSON object VidRift renders with the
	// playback token and lane metadata.
	embedMetaMarker = "var embedMeta = "
	// subtitleMarker prefixes the page-wide subtitle list.
	subtitleMarker = "var subtitleTracks = "
)

// embedMeta is the server-rendered player state needed for resolution.
type embedMeta struct {
	TMDBID        string          `json:"tmdbId"`
	Type          string          `json:"type"`
	Season        *int            `json:"season"`
	Episode       *int            `json:"episode"`
	PlaybackToken string          `json:"playbackToken"`
	Anime         json.RawMessage `json:"anime"`
	BigBangURL    string          `json:"bigbangUrl"`
	BigBangSubs   []subtitleTrack `json:"bigbangSubtitles"`
	EvionURL      string          `json:"evionUrl"`
	VexonURL      string          `json:"vexonUrl"`
	SelfhostURL   string          `json:"selfhostUrl"`
	SelfhostKind  string          `json:"selfhostKind"`
}

// embedPage is one parsed embed document.
type embedPage struct {
	Meta      embedMeta
	Subtitles []subtitleTrack
}

// subtitleTrack is one subtitle or caption entry from the embed page or a
// lane response.
type subtitleTrack struct {
	Code    string `json:"code"`
	Label   string `json:"label"`
	Lang    string `json:"lang"`
	URL     string `json:"url"`
	Default bool   `json:"default"`
}

// streamRung is one resolution declared by a lane stream.
type streamRung struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// apiStream is one playable lane stream returned by /api/source.
type apiStream struct {
	Index           int          `json:"index"`
	URL             string       `json:"url"`
	ProxyURL        string       `json:"proxyUrl"`
	Type            string       `json:"type"`
	Provider        string       `json:"provider"`
	Direct          bool         `json:"direct"`
	Name            string       `json:"name"`
	Rungs           []streamRung `json:"rungs"`
	SubtitleListURL string       `json:"subtitleListUrl"`
}

// sourceResponse is the /api/source payload for one lane.
type sourceResponse struct {
	Success   bool            `json:"success"`
	Provider  string          `json:"provider"`
	Source    string          `json:"source"`
	FellBack  bool            `json:"fellBack"`
	Streams   []apiStream     `json:"streams"`
	Subtitles []subtitleTrack `json:"subtitles"`
	MaxRes    int             `json:"maxRes"`
}

// laneResult is one lane's resolved payload, keeping the lane's own
// identity so sub/dub fallbacks can be rejected, and a per-lane error so
// total transport failure is distinguishable from an unavailable track.
type laneResult struct {
	lane      string
	streams   []apiStream
	subtitles []subtitleTrack
	maxRes    int
	provider  string
	source    string
	fellBack  bool
	err       error
}

// isDub reports whether a lane response actually carries dubbed audio.
func (r laneResult) isDub() bool {
	source := strings.ToLower(strings.TrimSpace(r.source))
	if source == "" {
		source = strings.ToLower(strings.TrimSpace(r.provider))
	}
	if source == "" {
		source = r.lane
	}
	return strings.Contains(source, "dub")
}

// isAnime reports whether the title has VidRift anime lanes. The upstream
// field is either false or the array of anime lane names.
func (m embedMeta) isAnime() bool {
	raw := strings.TrimSpace(string(m.Anime))
	return raw != "" && raw != "false" && raw != "null"
}

// animeLanes returns the anime lane names the page advertises. VidRift
// always renders both lanes even when a title has no dub, so this only
// tells us which lookups are worth making, never that a track exists.
func (m embedMeta) animeLanes() []string {
	if !m.isAnime() {
		return nil
	}
	var lanes []string
	if err := json.Unmarshal(m.Anime, &lanes); err == nil && len(lanes) > 0 {
		return lanes
	}
	return []string{providerAnime, providerAnimeDub}
}

// hasAnimeLane reports whether the page advertises lane.
func (m embedMeta) hasAnimeLane(lane string) bool {
	return slices.Contains(m.animeLanes(), lane)
}

// sourcePath is the /api/source route for this title. AniList lookups
// redirect to the TMDB route, so the metadata's TMDB coordinates are used.
func (m embedMeta) sourcePath() string {
	if strings.EqualFold(m.Type, provider.MediaTypeTV) && m.Season != nil && m.Episode != nil {
		return fmt.Sprintf("tv/%s/%d/%d", m.TMDBID, *m.Season, *m.Episode)
	}
	return "movie/" + m.TMDBID
}

// parseEmbedPage extracts the metadata and page-wide subtitle list from an
// embed document.
func parseEmbedPage(body []byte) (embedPage, error) {
	meta, err := decodeInlineJSON[embedMeta](body, embedMetaMarker)
	if err != nil {
		return embedPage{}, fmt.Errorf("decode embed metadata: %w", err)
	}
	subtitles, err := decodeInlineJSON[[]subtitleTrack](body, subtitleMarker)
	if err != nil {
		logging.Debug("vidrift subtitle list unavailable", "err", err)
	}
	return embedPage{Meta: meta, Subtitles: subtitles}, nil
}

// decodeInlineJSON decodes the first JSON value after marker. The embed
// document is HTML with the payloads in inline scripts, so decoding stops
// at the value's closing brace instead of scanning the rest of the page.
func decodeInlineJSON[T any](body []byte, marker string) (T, error) {
	var zero T
	index := bytes.Index(body, []byte(marker))
	if index < 0 {
		return zero, fmt.Errorf("marker %q missing", marker)
	}
	decoder := json.NewDecoder(bytes.NewReader(body[index+len(marker):]))
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, err
	}
	return value, nil
}

// buildSources maps every lane stream onto a playable media source, sharing
// the page-wide subtitles plus any lane-specific tracks.
func buildSources(page embedPage, results []laneResult, base string) []provider.MediaSource {
	generic := subtitleOptions(page.Subtitles, base)
	var sources []provider.MediaSource
	for _, result := range results {
		subtitles := make([]provider.SubtitleOption, 0, len(generic)+len(result.subtitles))
		subtitles = append(subtitles, generic...)
		subtitles = append(subtitles, subtitleOptions(result.subtitles, base)...)
		subtitles = dedupeSubtitles(subtitles)
		for _, stream := range result.streams {
			sources = append(sources, mediaSource(stream, result, base, subtitles, page.Meta.isAnime()))
		}
	}
	return sources
}

// mediaSource converts one lane stream. The proxy URL is used only when the
// lane has no direct address; both are played by mpv as remote HLS.
func mediaSource(stream apiStream, lane laneResult, base string, subtitles []provider.SubtitleOption, anime bool) provider.MediaSource {
	rawURL := strings.TrimSpace(stream.URL)
	if rawURL == "" {
		rawURL = strings.TrimSpace(stream.ProxyURL)
	}
	resolvedURL := resolveURL(base, rawURL)
	sourceType := kit.NormalizeSourceKind(stream.Type)
	if sourceType == provider.SourceTypeMP4 {
		sourceType = kit.NormalizeSourceKind(resolvedURL)
	}
	// Only the anime lane's delivery model is known: it ships soft subtitle
	// tracks. General fallback lanes may be hard-subbed, so they stay
	// untagged instead of claiming soft subs they may not have.
	subType := ""
	if anime && len(subtitles) > 0 && (lane.lane == providerAnime || lane.lane == providerAnimeDub) {
		subType = provider.SubTypeSoft
	}
	return provider.MediaSource{
		URL:            resolvedURL,
		Quality:        streamQuality(stream, lane.maxRes),
		Referer:        base + "/",
		Type:           sourceType,
		Subtitles:      subtitles,
		UserAgent:      config.DesktopUserAgent,
		Language:       streamLanguage(stream),
		SubType:        subType,
		SuppressOrigin: true,
	}
}

// bakedSources returns the directly signed lanes VidRift renders into the
// page, which are playable without an /api/source round trip. These are
// only used for movies and TV: their audio language is not declared, so
// they cannot satisfy a picked anime sub/dub track.
func bakedSources(meta embedMeta, base string) []provider.MediaSource {
	var sources []provider.MediaSource
	appendSource := func(rawURL, quality string, subtitles []provider.SubtitleOption) {
		resolved := resolveURL(base, rawURL)
		if resolved == "" {
			return
		}
		sources = append(sources, provider.MediaSource{
			URL:            resolved,
			Quality:        quality,
			Referer:        base + "/",
			Type:           kit.NormalizeSourceKind(resolved),
			Subtitles:      subtitles,
			UserAgent:      config.DesktopUserAgent,
			SuppressOrigin: true,
		})
	}
	appendSource(meta.BigBangURL, "Auto", subtitleOptions(meta.BigBangSubs, base))
	appendSource(meta.EvionURL, "Auto", nil)
	appendSource(meta.VexonURL, "Auto", nil)

	if resolved := resolveURL(base, meta.SelfhostURL); resolved != "" {
		sources = append(sources, provider.MediaSource{
			URL:            resolved,
			Quality:        "Auto",
			Referer:        base + "/",
			Type:           kit.NormalizeSourceKind(resolved),
			UserAgent:      config.DesktopUserAgent,
			SuppressOrigin: true,
		})
	}
	return sources
}

// streamQuality labels a stream by its highest declared rung, falling back
// to the lane's max resolution and then to Auto.
func streamQuality(stream apiStream, laneMaxRes int) string {
	height := 0
	for _, rung := range stream.Rungs {
		if rung.Height > height {
			height = rung.Height
		}
	}
	if height == 0 {
		height = laneMaxRes
	}
	switch {
	case height >= 2160:
		return "4K"
	case height > 0:
		return fmt.Sprintf("%dp", height)
	default:
		return "Auto"
	}
}

// streamLanguage derives an audio-language code from a lane's display name
// or provider label. Titan and Lyra name tracks without a provider prefix
// ("English", "Tamil"), so the whole label is normalized when no separator
// is present. Unknown labels stay empty so no filter can drop them.
func streamLanguage(stream apiStream) string {
	if code := languageFromLabel(stream.Name); code != "" {
		return code
	}
	switch strings.ToLower(strings.TrimSpace(stream.Provider)) {
	case "anime":
		return "ja"
	case "anime dub":
		return "en"
	}
	return ""
}

// languageFromLabel extracts the audio language from a lane label. Orion
// prefixes labels with "Orion · ", while Titan and Lyra use the bare
// language name.
func languageFromLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	if i := strings.LastIndex(label, "·"); i >= 0 {
		label = label[i+len("·"):]
	}
	return normalizeLanguage(label)
}

// normalizeLanguage folds VidRift's regional labels onto canonical codes and
// drops non-language descriptors. Labels that do not normalize onto the
// declared language set stay empty rather than becoming bogus --alang tags.
func normalizeLanguage(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch lower {
	case "", "original", "original audio", "multi", "multi-audio", "multi audio", "dual audio":
		return ""
	case "latin american spanish", "castilian spanish":
		return "es"
	case "brazilian portuguese":
		return "pt"
	}
	code := lang.Normalize(lower)
	if !declaresAudioLanguage(code) {
		return ""
	}
	return code
}

// declaresAudioLanguage reports whether code is part of the provider's
// declared audio-language surface.
func declaresAudioLanguage(code string) bool {
	return slices.ContainsFunc(vidriftAudioLanguages, func(language provider.AudioLanguage) bool {
		return language.Code == code
	})
}

// subtitleOptions converts tracks to subtitle options, resolving relative
// URLs against the embed origin.
func subtitleOptions(tracks []subtitleTrack, base string) []provider.SubtitleOption {
	options := make([]provider.SubtitleOption, 0, len(tracks))
	seen := make(map[string]bool, len(tracks))
	for _, track := range tracks {
		rawURL := resolveURL(base, track.URL)
		if rawURL == "" || seen[rawURL] {
			continue
		}
		seen[rawURL] = true
		code := subtitleLanguage(track)
		options = append(options, provider.SubtitleOption{
			URL:      rawURL,
			Language: code,
			Default:  track.Default || (code == "en" && strings.EqualFold(strings.TrimSpace(track.Label), "English")),
			Referer:  base + "/",
		})
	}
	return options
}

// subtitleLanguage prefers an explicit language tag and falls back to the
// human label when the tag is empty or a placeholder ("x2").
func subtitleLanguage(track subtitleTrack) string {
	if code := lang.Normalize(track.Lang); code != "" && !isPlaceholderLanguage(code) {
		return code
	}
	if code := lang.Normalize(track.Label); code != "" && !isPlaceholderLanguage(code) {
		return code
	}
	return ""
}

// isPlaceholderLanguage reports whether a code is one of VidRift's
// positional "xN" language slots rather than a real language tag.
func isPlaceholderLanguage(code string) bool {
	if len(code) < 2 || code[0] != 'x' {
		return false
	}
	for _, digit := range code[1:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// dedupeSubtitles keeps one track per language so the player does not list
// dozens of English variants. Tracks without a language are kept by URL.
func dedupeSubtitles(tracks []provider.SubtitleOption) []provider.SubtitleOption {
	seen := make(map[string]bool, len(tracks))
	out := make([]provider.SubtitleOption, 0, len(tracks))
	for _, track := range tracks {
		key := track.Language
		if key == "" {
			key = track.URL
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, track)
	}
	return out
}

// dedupeSources removes duplicate transports while preserving lane order.
func dedupeSources(sources []provider.MediaSource) []provider.MediaSource {
	seen := make(map[string]bool, len(sources))
	out := sources[:0]
	for _, source := range sources {
		if source.URL == "" {
			continue
		}
		key := source.URL + "\x00" + source.Language
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, source)
	}
	return out
}

// resolveURL makes a relative VidRift path absolute against the embed
// origin. Absolute URLs pass through unchanged.
func resolveURL(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return baseURL.ResolveReference(parsed).String()
}
