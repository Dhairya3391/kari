// Package vyla implements provider.Provider against the self-hosted Vyla
// Stream API (https://gitlab.com/vyla-entertainment/stream-api), querying 44
// upstream streaming scrapers with real-time stream verification, HLS playlist
// rewriting, and Server-Sent Event (SSE) progressive candidate delivery.
package vyla

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	_ provider.Provider          = (*Client)(nil)
	_ provider.Presenter         = (*Client)(nil)
	_ provider.StreamingProvider = (*Client)(nil)
)
var reResolution = regexp.MustCompile(`(?i)\b(2160p|4k|1440p|1080p|720p|480p|360p)\b`)
var vyLog = logging.With("provider", "vyla")

var defaultDemuxerArgs = []string{
	"--demuxer=lavf",
	"--demuxer-lavf-format=hls",
	"--demuxer-lavf-o=extension_picky=0",
}

// curatedVylaSources lists verified, working sources from Vyla SDK that Kari
// does not already provide natively, excluding dead backends and native duplicates
// (cno, movy, pengu, bcine, vidora, 111477).
var curatedVylaSources = []string{
	"xdownloader",
	"hexa",
	"dulo",
	"lookmovie",
	"vidapi",
	"vidfast",
	"vidup",
	"vidy",
	"rivo",
	"yapgrid",
	"pstream",
	"streamaggregator",
	"lmscript",
	"flaxmovies",
	"vidvault",
	"movenerds",
}

// Client implements provider.Provider against a self-hosted Vyla Stream API instance.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	apiBase    string
}

// NewClient constructs the Vyla provider using the shared TMDB search base
// and our canonical hosted endpoint (config.VylaAPIBase).
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithURL(keyPool, config.VylaAPIBase)
}

// NewClientWithURL constructs the Vyla provider with a custom endpoint,
// enabling integration tests to mock the resolution backend.
func NewClientWithURL(keyPool *tmdb.KeyPool, apiBase string) (*Client, error) {
	return NewClientWithHTTPClient(keyPool, apiBase, httpclient.New())
}

// NewClientWithHTTPClient constructs the Vyla provider with an injected HTTP
// client, allowing unit tests to serve canned SSE payloads without network access.
func NewClientWithHTTPClient(keyPool *tmdb.KeyPool, apiBase string, hc *http.Client) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("vyla streambase: %w", err)
	}
	cleanBase := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if cleanBase == "" {
		cleanBase = config.VylaAPIBase
	}
	if hc == nil {
		hc = httpclient.New()
	}
	return &Client{
		base:       base,
		httpClient: hc,
		apiBase:    cleanBase,
	}, nil
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "Vyla" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "vyla" }

// Modes implements provider.Provider, declaring movie and television catalog support.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 15},
		{Name: provider.ModeTV, Priority: 15},
	}
}

// Search delegates title discovery to the shared TMDB catalog index.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes delegates season and episode indexing to the shared TMDB catalog.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

// ResolveSource queries Vyla Stream API collecting all playable streams into a single slice.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	ch := make(chan []provider.MediaSource, 16)
	errCh := make(chan error, 1)

	go func() {
		defer close(ch)
		errCh <- c.ResolveStream(ctx, mediaID, episode, ch)
	}()

	var all []provider.MediaSource
	seen := make(map[string]int)
	for batch := range ch {
		for _, s := range batch {
			if idx, exists := seen[s.URL]; exists {
				if len(s.Subtitles) > 0 {
					all[idx].Subtitles = s.Subtitles
				}
			} else {
				seen[s.URL] = len(all)
				all = append(all, s)
			}
		}
	}
	if err := <-errCh; err != nil && len(all) == 0 {
		return nil, err
	}
	if len(all) == 0 {
		return nil, provider.ErrNoSources
	}
	return all, nil
}

// ResolveStream implements provider.StreamingProvider, consuming progressive
// Server-Sent Events from Vyla Stream API and yielding verified sources to the
// updates channel as each backend finishes verification.
func (c *Client) ResolveStream(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	tmdbID, isTv, season, epNum, err := parseCoords(mediaID, episode)
	if err != nil {
		return err
	}

	endpoint := c.buildStreamURL(tmdbID, isTv, season, epNum)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create vyla stream request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vyla stream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}

	var currentSubs []provider.SubtitleOption
	var emitted []provider.MediaSource
	seenURLs := make(map[string]struct{})
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}

		var msg sseMessage
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			vyLog.Debug("skipping malformed sse event", "err", err, "payload", payload)
			continue
		}

		switch msg.Type {
		case "meta":
			if len(msg.Subtitles) > 0 {
				currentSubs = toSubtitleOptions(msg.Subtitles)
				// Backfill existing emitted sources with newly arrived subtitles
				if len(emitted) > 0 {
					updated := make([]provider.MediaSource, len(emitted))
					for i := range emitted {
						emitted[i].Subtitles = currentSubs
						updated[i] = emitted[i]
					}
					if updates != nil {
						select {
						case updates <- updated:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
				}
			}

		case "source":
			if msg.Source == nil || strings.TrimSpace(msg.Source.URL) == "" {
				continue
			}
			cleanURL := strings.TrimSpace(msg.Source.URL)
			// Filter unplayable DASH/MPD streams
			if strings.EqualFold(msg.Source.Type, "dash") || strings.Contains(cleanURL, ".mpd") {
				continue
			}
			if _, exists := seenURLs[cleanURL]; exists {
				continue
			}
			seenURLs[cleanURL] = struct{}{}

			src := toMediaSource(msg.Source, currentSubs)
			emitted = append(emitted, src)

			if updates != nil {
				select {
				case updates <- []provider.MediaSource{src}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		case "done":
			if len(emitted) == 0 {
				return provider.ErrNoSources
			}
			return nil
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		vyLog.Debug("vyla sse scanner finished with error", "err", err)
	}

	if len(emitted) == 0 {
		return provider.ErrNoSources
	}
	return nil
}

func (c *Client) buildStreamURL(tmdbID int, isTv bool, season, episode int) string {
	sourcesParam := strings.Join(curatedVylaSources, ",")
	if isTv {
		params := url.Values{}
		params.Set("id", strconv.Itoa(tmdbID))
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(episode))
		params.Set("sources", sourcesParam)
		return fmt.Sprintf("%s/api/tv?%s", c.apiBase, params.Encode())
	}
	params := url.Values{}
	params.Set("id", strconv.Itoa(tmdbID))
	params.Set("sources", sourcesParam)
	return fmt.Sprintf("%s/api/movie?%s", c.apiBase, params.Encode())
}

func parseCoords(mediaID string, episode provider.Episode) (int, bool, int, int, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return 0, false, 0, 0, fmt.Errorf("invalid tmdb media id %q: %w", mediaID, err)
		}
	}
	isTv := episode.Season > 0 || episode.Episode > 0
	season := episode.Season
	epNum := episode.Episode
	if isTv {
		if season <= 0 {
			season = 1
		}
		if epNum <= 0 {
			epNum = 1
		}
	}
	return tmdbID, isTv, season, epNum, nil
}

func toMediaSource(s *vylaSource, subs []provider.SubtitleOption) provider.MediaSource {
	srcType := provider.SourceTypeHLS
	if strings.EqualFold(s.Type, "mp4") || strings.Contains(s.URL, ".mp4") {
		srcType = provider.SourceTypeMP4
	}

	backend := cleanBackendName(s.Source)
	res := cleanResolution(s.Quality, s.Label)
	src := provider.MediaSource{
		URL:       strings.TrimSpace(s.URL),
		Quality:   res,
		Backend:   backend,
		Type:      srcType,
		Subtitles: subs,
		Language:  detectLanguage(s.Language, s.Label),
	}
	if srcType == provider.SourceTypeHLS {
		src.ExtraArgs = append([]string(nil), defaultDemuxerArgs...)
	}

	if s.Headers != nil {
		if ref, ok := s.Headers["Referer"]; ok && ref != "" {
			src.Referer = ref
		}
		if ua, ok := s.Headers["User-Agent"]; ok && ua != "" {
			src.UserAgent = ua
		}
	}
	if src.UserAgent == "" {
		src.UserAgent = config.DesktopUserAgent
	}

	return src
}

func toSubtitleOptions(subs []vylaSubtitle) []provider.SubtitleOption {
	var out []provider.SubtitleOption
	seen := make(map[string]struct{})

	for _, sub := range subs {
		u := strings.TrimSpace(sub.File)
		if u == "" {
			continue
		}
		normLang := lang.Normalize(sub.Label)
		if normLang == "" {
			normLang = "und"
		}
		key := u + ":" + normLang
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		out = append(out, provider.SubtitleOption{
			URL:      u,
			Language: normLang,
		})
	}
	return out
}
func cleanBackendName(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "111477":
		return "111477"
	case "xdownloader":
		return "XDownloader"
	case "thugtv":
		return "ThugTV"
	case "lmscript":
		return "LMScript"
	case "lookmovie":
		return "LookMovie"
	case "cinezo":
		return "Cinezo"
	case "cno":
		return "Cno"
	case "movy":
		return "Movy"
	case "vidapi":
		return "VidAPI"
	case "vidlink":
		return "Vidlink"
	case "vidfast":
		return "VidFast"
	case "vidrock":
		return "VidRock"
	case "streamaggregator":
		return "StreamAggregator"
	case "flaxmovies":
		return "FlaxMovies"
	case "fsonline":
		return "FSOnline"
	case "dulo":
		return "Dulo"
	case "kisskh":
		return "KissKH"
	case "kickassanime":
		return "KickAssAnime"
	default:
		clean := strings.TrimSpace(raw)
		if clean == "" {
			return "Vyla"
		}
		r, size := utf8.DecodeRuneInString(clean)
		return string(unicode.ToUpper(r)) + clean[size:]
	}
}

func cleanResolution(rawQuality, label string) string {
	for _, text := range []string{rawQuality, label} {
		m := reResolution.FindString(text)
		if m != "" {
			return strings.ToUpper(m)
		}
	}
	return "1080P"
}
func detectLanguage(rawLang, label string) string {
	if norm := lang.Normalize(rawLang); norm != "" && norm != "und" {
		return norm
	}
	lbl := strings.ToLower(label)
	for _, l := range []struct {
		token string
		code  string
	}{
		{"hindi", "hi"},
		{"japanese", "ja"},
		{"spanish", "es"},
		{"french", "fr"},
		{"german", "de"},
		{"italian", "it"},
		{"portuguese", "pt"},
		{"russian", "ru"},
		{"arabic", "ar"},
		{"korean", "ko"},
		{"chinese", "zh"},
		{"tamil", "ta"},
		{"telugu", "te"},
	} {
		if strings.Contains(lbl, l.token) {
			return l.code
		}
	}
	return ""
}
