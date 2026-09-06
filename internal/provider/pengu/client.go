// Package pengu implements provider.Provider against the PenguPlay aggregator
// API (https://pengu.uk). PenguPlay is an aggregator that queries multiple
// streaming backends (4KHDHub, VegaMovies, MoviesDrives, Miruro, CineFreak,
// VAPlayer, VidLink, VidFast, HDGharTV, KissKH, HDHub4u, Atlantic)
// and returns deduplicated direct and HLS streams.
package pengu

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

// log scopes every line from this package with its identity.
var pgLog = logging.With("provider", "pengu")

const (
	penguAPIBase = config.PenguAPIBase
	penguUA      = config.DesktopUserAgent
)

var (
	reResolution = regexp.MustCompile(`(?i)\b(4k|2160p|1440p|1080p|720p|480p|360p)\b`)
	reSourceTag  = regexp.MustCompile(`(?i)\b(4khdhub|vegamovies|moviesdrives|miruro|cinefreak|vaplayer|vidlink|vidfast|hdghartv|kisskh|hdhub4u|atlantic|debridscloud|5clover)\b`)
	reAudioLang  = regexp.MustCompile(`(?i)(?:audio|language|languages):\s*([a-zA-Z, /]+)`)
)

// Client implements provider.Provider against the PenguPlay Stremio addon API.
type Client struct {
	base          *streambase.Base
	httpClient    *http.Client
	authToken     string
	configSegment string
}

type penguStreamItem struct {
	Name          string             `json:"name"`
	Title         string             `json:"title"`
	Description   string             `json:"description"`
	URL           string             `json:"url"`
	BehaviorHints penguBehaviorHints `json:"behaviorHints"`
	Subtitles     []penguSubtitle    `json:"subtitles"`
}

type penguBehaviorHints struct {
	Headers      map[string]string `json:"headers"`
	ProxyHeaders *penguProxyHeader `json:"proxyHeaders"`
	NotWebReady  bool              `json:"notWebReady"`
	Filename     string            `json:"filename"`
}

type penguProxyHeader struct {
	Request map[string]string `json:"request"`
}

type penguSubtitle struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	Lang string `json:"lang"`
}

type penguResponse struct {
	Streams []penguStreamItem `json:"streams"`
}

// NewClient constructs the Pengu provider over the shared TMDB search base.
func NewClient(keyPool *tmdb.KeyPool, authToken string) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(authToken)
	segment, err := buildConfigSegment(token)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:          base,
		httpClient:    httpclient.NewWithUserAgent(penguUA),
		authToken:     token,
		configSegment: segment,
	}, nil
}

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "Pengu" }

// Name implements Provider.
func (c *Client) Name() string { return "pengu" }

// Modes implements Provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
	}
}

// audioLanguages is the full set of audio-track languages the PenguPlay API
// aggregates across its internal providers.
var audioLanguages = []provider.AudioLanguage{
	{Code: "en", Display: "English"},
	{Code: "hi", Display: "Hindi"},
	{Code: "ta", Display: "Tamil"},
	{Code: "te", Display: "Telugu"},
	{Code: "ko", Display: "Korean"},
	{Code: "ja", Display: "Japanese"},
	{Code: "zh", Display: "Chinese"},
	{Code: "es", Display: "Spanish"},
	{Code: "fr", Display: "French"},
	{Code: "de", Display: "German"},
	{Code: "it", Display: "Italian"},
	{Code: "pt", Display: "Portuguese"},
	{Code: "ru", Display: "Russian"},
	{Code: "ar", Display: "Arabic"},
	{Code: "th", Display: "Thai"},
	{Code: "vi", Display: "Vietnamese"},
	{Code: "ms", Display: "Malay"},
	{Code: "id", Display: "Indonesian"},
}

// AudioLanguages implements provider.AudioLanguagesSource.
func (c *Client) AudioLanguages() []provider.AudioLanguage {
	return audioLanguages
}

// Search delegates to the shared TMDB-keyed base.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes delegates to the shared TMDB-keyed base.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

// ResolveSource resolves playable sources for the given media ID and episode,
// ignoring VidKing (which is handled independently in Kari) and deduplicating
// identical provider variants.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(mediaID)
		if err != nil {
			return nil, fmt.Errorf("invalid media ID: %w", err)
		}
	}

	mediaType := provider.MediaTypeMovie
	stremioID := fmt.Sprintf("tmdb:%d", tmdbID)
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = provider.MediaTypeTV
		stremioID = fmt.Sprintf("tmdb:%d:%d:%d", tmdbID, episode.Season, episode.Episode)
	}

	resp, err := c.fetchPenguStreams(ctx, mediaType, stremioID)
	if err != nil {
		return nil, err
	}
	if len(resp.Streams) == 0 {
		return nil, provider.ErrNoSources
	}

	// Check if the only returned stream is an auth prompt
	if isAuthPrompt(resp.Streams) {
		pgLog.Warn("pengu authentication required or expired", "tmdbID", tmdbID)
		return nil, fmt.Errorf("pengu: %w", provider.ErrAuthRequired)
	}

	seenURLs := make(map[string]bool)
	seenGroup := make(map[string]bool)
	sources := make([]provider.MediaSource, 0, len(resp.Streams))

	for _, stream := range resp.Streams {
		if strings.TrimSpace(stream.URL) == "" {
			continue
		}
		// Ignore VidKing (maintained independently in Kari) and 2Peckle.
		if isExcludedStream(stream) {
			continue
		}
		if seenURLs[stream.URL] {
			continue
		}
		seenURLs[stream.URL] = true

		source := parseStreamItem(stream)
		// Deduplicate: preserve the first/cleanest stream per (Quality, Language, StreamType)
		groupKey := fmt.Sprintf("%s|%s|%s", source.Quality, source.Language, source.Type)
		if seenGroup[groupKey] {
			continue
		}
		seenGroup[groupKey] = true
		sources = append(sources, source)
	}

	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

// isExcludedStream checks if a stream originated from an excluded backend
// (VidKing is maintained independently; 2Peckle is excluded).
func isExcludedStream(stream penguStreamItem) bool {
	combined := strings.ToLower(stream.Name + " " + stream.Title + " " + stream.Description + " " + stream.URL)
	return strings.Contains(combined, "vidking") || strings.Contains(combined, "2peckle")
}

// isAuthPrompt checks if the response is Pengu's authentication requirement notice.
func isAuthPrompt(streams []penguStreamItem) bool {
	if len(streams) != 1 {
		return false
	}
	s := streams[0]
	return strings.Contains(s.URL, "signin.mp4") ||
		strings.Contains(strings.ToLower(s.Title), "sign in") ||
		strings.Contains(strings.ToLower(s.Description), "authentication is missing")
}

// buildConfigSegment compiles the functional multi-provider configuration into a
// compact raw-deflated base64url segment expected by PenguPlay.
// VidKing is deliberately omitted because Kari manages VidKing independently.
func buildConfigSegment(token string) (string, error) {
	cfg := map[string]any{
		"source_4khdhub":      "on",
		"source_vegamovies":   "on",
		"source_moviesdrives": "on",
		"source_vaplayer":     "on",
		"source_miruro":       "on",
		"source_hdghartv":     "on",
		"source_vidlink":      "on",
		"source_vidfast":      "on",
		"source_cinefreak":    "on",
		"source_kisskh":       "on",
		"source_hdhub4u":      "on",
		"source_atlantic":     "on",
		"res_2160":            "on",
		"res_1080":            "on",
		"res_720":             "on",
		"res_480":             "on",
		"res_360":             "on",
	}
	if token != "" {
		cfg["auth_token"] = token
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("pengu marshal config: %w", err)
	}

	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return "", fmt.Errorf("pengu flate writer: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return "", fmt.Errorf("pengu deflate: %w", err)
	}
	if err := fw.Close(); err != nil {
		return "", fmt.Errorf("pengu close flate: %w", err)
	}

	return "z" + base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// fetchPenguStreams issues the Stremio stream request against the Pengu endpoint.
func (c *Client) fetchPenguStreams(ctx context.Context, mediaType, stremioID string) (*penguResponse, error) {
	pgLog.Debug("fetch start", "mediaType", mediaType, "id", stremioID)

	stremioType := "movie"
	if mediaType == provider.MediaTypeTV {
		stremioType = "series"
	}

	url := fmt.Sprintf("%s/%s/stream/%s/%s.json", penguAPIBase, c.configSegment, stremioType, stremioID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("pengu request: %w", err)
	}
	req.Header.Set("User-Agent", penguUA)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pengu do request: %w", redactRequestError(err, c.configSegment))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("pengu read body: %w", err)
	}

	var payload penguResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("pengu decode response: %w", err)
	}

	pgLog.Debug("fetch success", "mediaType", mediaType, "id", stremioID, "streams", len(payload.Streams))
	return &payload, nil
}

// redactRequestError prevents the encoded configuration segment, which can
// contain a user's Pengu auth token, from escaping through transport errors.
func redactRequestError(err error, configSegment string) error {
	message := err.Error()
	if configSegment != "" {
		message = strings.ReplaceAll(message, configSegment, "[redacted]")
	}
	return errors.New(message)
}

// parseStreamItem extracts quality, headers, subtitles, and audio language from a stream entry.
func parseStreamItem(stream penguStreamItem) provider.MediaSource {
	combined := stream.Name + " " + stream.Title + " " + stream.Description + " " + stream.BehaviorHints.Filename

	// 1. Resolution
	res := "1080p"
	if m := reResolution.FindString(combined); m != "" {
		upper := strings.ToUpper(m)
		if upper == "4K" || upper == "2160P" {
			res = "4K"
		} else {
			res = strings.ToLower(m)
		}
	}

	// 2. Source Provider Tag
	sourceName := ""
	if m := reSourceTag.FindString(combined); m != "" {
		sourceName = canonProviderName(m)
	}

	// 3. Audio Language
	audioLang := mapAudioLanguageFromText(combined)
	audioLangDisplay := ""
	if audioLang != "" && audioLang != "en" {
		audioLangDisplay = lang.Name(audioLang)
	}

	quality := res
	if sourceName != "" {
		if audioLangDisplay != "" {
			quality = fmt.Sprintf("%s [%s] (%s)", res, sourceName, audioLangDisplay)
		} else {
			quality = fmt.Sprintf("%s [%s]", res, sourceName)
		}
	} else if audioLangDisplay != "" {
		quality = fmt.Sprintf("%s (%s)", res, audioLangDisplay)
	}

	// 4. Stream Type
	streamType := provider.SourceTypeHLS
	if strings.Contains(stream.URL, ".mp4") {
		streamType = provider.SourceTypeMP4
	}

	// 5. Headers (Referer, User-Agent, Cookie)
	referer := ""
	userAgent := penguUA
	cookie := ""

	extractHeaders := func(h map[string]string) {
		for k, v := range h {
			switch strings.ToLower(k) {
			case "referer":
				referer = v
			case "user-agent":
				userAgent = v
			case "cookie":
				cookie = v
			}
		}
	}

	if stream.BehaviorHints.Headers != nil {
		extractHeaders(stream.BehaviorHints.Headers)
	}
	if stream.BehaviorHints.ProxyHeaders != nil && stream.BehaviorHints.ProxyHeaders.Request != nil {
		extractHeaders(stream.BehaviorHints.ProxyHeaders.Request)
	}

	// 6. Subtitles
	subtitles := []provider.SubtitleOption{}
	for _, sub := range stream.Subtitles {
		if sub.URL == "" {
			continue
		}
		subtitles = append(subtitles, provider.SubtitleOption{
			URL:      sub.URL,
			Language: lang.Normalize(sub.Lang),
		})
	}

	return provider.MediaSource{
		URL:          stream.URL,
		Quality:      quality,
		Referer:      referer,
		Type:         streamType,
		Subtitles:    subtitles,
		UserAgent:    userAgent,
		CookieHeader: cookie,
		Language:     audioLang,
	}
}

// canonProviderName returns the canonical display name for a matched provider token.
func canonProviderName(token string) string {
	for _, canon := range []string{
		"4KHDHub", "VegaMovies", "MoviesDrives",
		"Miruro", "CineFreak", "VAPlayer", "VidLink",
		"VidFast", "HDGharTV", "KissKH", "HDHub4u", "Atlantic",
		"DebridsCloud", "5Clover",
	} {
		if strings.EqualFold(canon, token) {
			return canon
		}
	}
	return strings.ToUpper(token)
}

// mapAudioLanguageFromText inspects the combined stream metadata for language tags.
func mapAudioLanguageFromText(combined string) string {
	if m := reAudioLang.FindStringSubmatch(combined); len(m) > 1 {
		if code := mapAudioLanguage(m[1]); code != "" {
			return code
		}
	}
	lower := strings.ToLower(combined)
	for _, l := range audioLanguages {
		if l.Code == "en" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(l.Display)) || strings.Contains(lower, "["+l.Code+"]") {
			return l.Code
		}
	}
	if strings.Contains(lower, "english") {
		return "en"
	}
	return ""
}

// mapAudioLanguage normalizes language text to a standard 2-letter language code.
func mapAudioLanguage(raw string) string {
	for _, candidate := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '/' || r == '|'
	}) {
		if code := lang.Normalize(candidate); isPenguAudioLanguage(code) {
			return code
		}
	}
	return ""
}

func isPenguAudioLanguage(code string) bool {
	for _, language := range audioLanguages {
		if language.Code == code {
			return true
		}
	}
	return false
}
