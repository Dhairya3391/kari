package vidora

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// Lane labels shown in the source list. The content edge is Vidora's own
// Aphrodite lane; the remaining names describe the upstream player each lane
// scrapes.
const (
	backendAphrodite = "Aphrodite"
	backendShegu     = "Shegu"
	// sourceTypeDASH names the DASH container. The shared provider package
	// declares no DASH constant because only kit's container inference and a
	// few lanes use the value.
	sourceTypeDASH = "dash"
	// placeholderMaxSegments and placeholderMaxSeconds bound the CDN's
	// unauthenticated stand-in clip (eight eight-second segments). Real
	// movies and episodes far exceed both, so either bound being crossed
	// means the playlist is genuine.
	placeholderMaxSegments = 24
	placeholderMaxSeconds  = 180
)

// coordinates are the TMDB lookup keys every Vidora lane accepts.
type coordinates struct {
	tmdbID    int
	mediaType string
	season    int
	episode   int
}

// contentResponse is the Aphrodite content lookup. URL is a short-lived HLS
// master on the totallyacdn.cc edge. Title is the title's display name; a
// missing title marks the placeholder entry the edge returns for TMDB ids it
// does not stock (it still reports found and a playable but unrelated URL).
type contentResponse struct {
	Found bool   `json:"found"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Type  string `json:"type"`
}

// sheguResponse is the Shegu resolver result: a direct signed HLS master and
// the highest rendition height its manifest advertises.
type sheguResponse struct {
	Found     bool   `json:"found"`
	URL       string `json:"url"`
	MaxHeight int    `json:"maxHeight"`
}

// streamResponse is shared by the Vidbox and Prism (mbox) players on
// stream.hls.lol. Moscow returns the same envelope but with an unusable
// player-internal token, so it is not queried.
type streamResponse struct {
	Server  string                  `json:"server"`
	Sources map[string]streamSource `json:"sources"`
	Tracks  []streamTrack           `json:"tracks"`
}

// streamSource is one adaptive stream a player advertises.
type streamSource struct {
	Label     string          `json:"label"`
	Server    string          `json:"server"`
	URL       string          `json:"url"`
	Qualities []streamQuality `json:"qualities"`
}

// streamQuality is one rendition declaration; it names the resolution, not a
// separate URL.
type streamQuality struct {
	Resolution string `json:"resolution"`
}

// streamTrack is one single-file rendition (DASH or HLS) declared by Prism.
type streamTrack struct {
	URL     string `json:"url"`
	Kind    string `json:"kind"`
	Quality string `json:"quality"`
	Lang    string `json:"lang"`
	Mode    string `json:"mode"`
}

// banditResponse is Bandit's per-server listing. Each stream is already a
// direct master playlist.
type banditResponse struct {
	Found   bool           `json:"found"`
	Servers []banditServer `json:"servers"`
}

// banditServer is one Bandit lane (Bolo, bandit, …).
type banditServer struct {
	Name    string         `json:"name"`
	Streams []banditStream `json:"streams"`
}

// banditStream is one Bandit playlist. Bandit also advertises HLS subtitle
// playlists per stream, but those are not VTT files; the global subtitle
// lookup supplies usable tracks instead.
type banditStream struct {
	Quality string `json:"quality"`
	URL     string `json:"url"`
}

// subsResponse is Vidora's subtitle lookup result.
type subsResponse struct {
	Subtitles []subEntry `json:"subtitles"`
}

// subEntry is one subtitle track. LangCode is the canonical ISO 639-1 form.
type subEntry struct {
	URL      string `json:"url"`
	Language string `json:"language"`
	LangCode string `json:"langCode"`
}

// lane is one resolution source and its display name.
type lane struct {
	name  string
	fetch func(context.Context, coordinates) ([]provider.MediaSource, error)
}

// laneResult is the outcome of one lane request. Results stay in lane order
// so output does not depend on which upstream answered first.
type laneResult struct {
	sources []provider.MediaSource
	err     error
}

// parseCoordinates extracts the TMDB id and media shape from a search handle
// and episode. Movies resolve directly; any season/episode coordinates make
// the request a TV episode.
func parseCoordinates(mediaID string, episode provider.Episode) (coordinates, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return coordinates{}, fmt.Errorf("invalid tmdb media ID: %w", err)
		}
		if tmdbID <= 0 {
			return coordinates{}, fmt.Errorf("invalid tmdb media ID: %q", mediaID)
		}
	}

	coords := coordinates{tmdbID: tmdbID, mediaType: provider.MediaTypeMovie, season: 1, episode: 1}
	if episode.Season > 0 || episode.Episode > 0 {
		coords.mediaType = provider.MediaTypeTV
		if episode.Season > 0 {
			coords.season = episode.Season
		}
		if episode.Episode > 0 {
			coords.episode = episode.Episode
		}
	}
	return coords, nil
}

// ResolveSource fans out over Vidora's directly playable lanes and returns
// every playlist they advertise.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	coords, err := parseCoordinates(mediaID, episode)
	if err != nil {
		return nil, err
	}
	return c.fetchSources(ctx, coords)
}

// fetchSources queries every lane and the subtitle service concurrently.
// Lane failures are logged and skipped: one dead upstream never fails a
// resolution that another lane can serve.
func (c *Client) fetchSources(ctx context.Context, coords coordinates) ([]provider.MediaSource, error) {
	lanes := []lane{
		{name: "aphrodite", fetch: c.fetchAphrodite},
		{name: "shegu", fetch: c.fetchShegu},
		{name: "vidbox", fetch: c.fetchVidbox},
		{name: "prism", fetch: c.fetchPrism},
		{name: "bandit", fetch: c.fetchBandit},
	}

	results := make([]laneResult, len(lanes))
	var (
		wg        sync.WaitGroup
		subtitles []provider.SubtitleOption
	)
	for i, entry := range lanes {
		wg.Add(1)
		go func(i int, entry lane) {
			defer wg.Done()
			laneCtx, cancel := context.WithTimeout(ctx, c.laneBudget)
			defer cancel()
			sources, err := entry.fetch(laneCtx, coords)
			if err != nil && !errors.Is(err, provider.ErrNoSources) {
				logging.Debug("vidora lane failed", "lane", entry.name, "tmdb", coords.tmdbID, "err", err)
			}
			results[i] = laneResult{sources: sources, err: err}
		}(i, entry)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		subCtx, cancel := context.WithTimeout(ctx, c.laneBudget)
		defer cancel()
		fetched, err := c.fetchSubtitles(subCtx, coords)
		if err != nil {
			logging.Debug("vidora subtitles failed", "tmdb", coords.tmdbID, "err", err)
			return
		}
		subtitles = fetched
	}()

	wg.Wait()

	var (
		sources  []provider.MediaSource
		firstErr error
		hardErr  error
	)
	for _, result := range results {
		if len(result.sources) > 0 {
			sources = append(sources, result.sources...)
			continue
		}
		if result.err == nil {
			continue
		}
		if firstErr == nil {
			firstErr = result.err
		}
		if !errors.Is(result.err, provider.ErrNoSources) && hardErr == nil {
			hardErr = result.err
		}
	}

	sources = kit.DedupeSources(sources)
	sources = provider.FilterDirectSources(sources)
	if len(sources) == 0 {
		// Report a real transport/status failure over the empty lanes' "no
		// sources" sentinel so the cause survives in logs and callers.
		if hardErr != nil {
			return nil, hardErr
		}
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, provider.ErrNoSources
	}

	if len(subtitles) > 0 {
		for i := range sources {
			sources[i].Subtitles = subtitles
		}
	}
	logging.Debug("vidora resolve done", "tmdb", coords.tmdbID, "sources", len(sources))
	return sources, nil
}

// fetchAphrodite asks the Content edge for its HLS master.
func (c *Client) fetchAphrodite(ctx context.Context, coords coordinates) ([]provider.MediaSource, error) {
	target := fmt.Sprintf("%s/content/movie/%d", c.endpoints.Content, coords.tmdbID)
	if coords.mediaType == provider.MediaTypeTV {
		target = fmt.Sprintf("%s/content/tv/%d/%d/%d", c.endpoints.Content, coords.tmdbID, coords.season, coords.episode)
	}

	var payload contentResponse
	if err := c.getJSON(ctx, target, &payload); err != nil {
		return nil, err
	}
	playlist := strings.TrimSpace(payload.URL)
	// The edge answers found:true for unknown ids with a signed master for a
	// different title. Only stocked titles carry a display title, so an empty
	// one is the marker that this row must not be surfaced.
	if !payload.Found || strings.TrimSpace(payload.Title) == "" || playlist == "" || payload.Type != provider.SourceTypeHLS {
		return nil, provider.ErrNoSources
	}
	// The content edge gates real playlists behind the site's proof-of-work
	// challenge. An unauthenticated lookup still answers with a signed URL,
	// but that URL serves a ~64s "not available" clip. Never surface it: a
	// sibling lane can provide real playback instead.
	if c.isPlaceholderPlaylist(ctx, playlist) {
		logging.Debug("vidora aphrodite served a placeholder playlist", "tmdb", coords.tmdbID)
		return nil, provider.ErrNoSources
	}
	return []provider.MediaSource{
		c.newSource(playlist, "Auto ("+backendAphrodite+")", backendAphrodite, provider.SourceTypeHLS, 0),
	}, nil
}

// isPlaceholderPlaylist reports whether an Aphrodite master resolves to the
// short stand-in clip the CDN returns to callers without a challenge token.
// The clip is a handful of eight-second segments totalling about a minute;
// real movies and episodes are far longer. Probe failures fail open so a
// transient upstream error cannot hide an otherwise valid row.
func (c *Client) isPlaceholderPlaylist(ctx context.Context, master string) bool {
	body, err := c.getPlaylist(ctx, master)
	if err != nil {
		return false
	}
	variant := firstHLSVariant(body)
	if variant == "" {
		return false
	}
	if base, err := url.Parse(master); err == nil {
		if ref, err := url.Parse(variant); err == nil {
			variant = base.ResolveReference(ref).String()
		}
	}
	segments, seconds, err := c.playlistDuration(ctx, variant)
	if err != nil {
		return false
	}
	return segments > 0 && segments <= placeholderMaxSegments && seconds <= placeholderMaxSeconds
}

// getPlaylist fetches an HLS playlist with the site's hotlink headers.
func (c *Client) getPlaylist(ctx context.Context, target string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("vidora playlist request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.endpoints.Site+"/")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("vidora playlist request %s: %w", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return "", fmt.Errorf("vidora playlist read %s: %w", target, err)
	}
	return string(body), nil
}

// firstHLSVariant returns the first media playlist a master references.
func firstHLSVariant(master string) string {
	lines := strings.Split(master, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "#EXT-X-STREAM-INF") {
			continue
		}
		for _, next := range lines[i+1:] {
			candidate := strings.TrimSpace(next)
			if candidate != "" && !strings.HasPrefix(candidate, "#") {
				return candidate
			}
		}
	}
	return ""
}

// playlistDuration returns the segment count and total declared duration of
// a media playlist.
func (c *Client) playlistDuration(ctx context.Context, target string) (int, float64, error) {
	body, err := c.getPlaylist(ctx, target)
	if err != nil {
		return 0, 0, err
	}
	segments, seconds := 0, 0.0
	for _, line := range strings.Split(body, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "#EXTINF:")
		if !ok {
			continue
		}
		duration, err := strconv.ParseFloat(strings.SplitN(value, ",", 2)[0], 64)
		if err != nil {
			continue
		}
		segments++
		seconds += duration
	}
	return segments, seconds, nil
}

// fetchShegu asks the Shegu resolver for a direct signed master.
func (c *Client) fetchShegu(ctx context.Context, coords coordinates) ([]provider.MediaSource, error) {
	target := fmt.Sprintf("%s/resolve/movie/%d", c.endpoints.Shegu, coords.tmdbID)
	if coords.mediaType == provider.MediaTypeTV {
		target = fmt.Sprintf("%s/resolve/tv/%d/%d/%d", c.endpoints.Shegu, coords.season, coords.episode, coords.tmdbID)
	}

	var payload sheguResponse
	if err := c.getJSON(ctx, target, &payload); err != nil {
		return nil, err
	}
	playlist := strings.TrimSpace(payload.URL)
	if !payload.Found || playlist == "" {
		return nil, provider.ErrNoSources
	}

	quality := "Auto (" + backendShegu + ")"
	// QualityLabelFromHeight renders non-positive heights as "0p" or "-1p".
	// Only a real height yields a usable resolution label.
	if payload.MaxHeight > 0 {
		quality = kit.QualityLabelFromHeight(payload.MaxHeight)
	}
	return []provider.MediaSource{
		c.newSource(playlist, quality, backendShegu, provider.SourceTypeHLS, payload.MaxHeight),
	}, nil
}

// fetchVidbox resolves the Vidbox player lane.
func (c *Client) fetchVidbox(ctx context.Context, coords coordinates) ([]provider.MediaSource, error) {
	var payload streamResponse
	if err := c.getJSON(ctx, c.streamTarget("vidbox", coords), &payload); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(payload.Sources))
	for key := range payload.Sources {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var sources []provider.MediaSource
	for _, key := range keys {
		source := payload.Sources[key]
		playlist := strings.TrimSpace(source.URL)
		if playlist == "" {
			continue
		}
		backend := strings.TrimSpace(source.Server)
		if backend == "" {
			backend = key
		}
		height := maxDeclaredHeight(source.Qualities)
		quality := "Auto (" + backend + ")"
		if height == 0 {
			if label := strings.TrimSpace(source.Label); label != "" && !strings.EqualFold(label, "auto") {
				quality = label
			}
		}
		sources = append(sources, c.newSource(playlist, quality, backend, provider.SourceTypeHLS, height))
	}
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

// fetchPrism resolves the Prism (mbox) player lane. Its tracks are
// single-file DASH or HLS renditions rather than one adaptive master.
func (c *Client) fetchPrism(ctx context.Context, coords coordinates) ([]provider.MediaSource, error) {
	var payload streamResponse
	if err := c.getJSON(ctx, c.streamTarget("mbox", coords), &payload); err != nil {
		return nil, err
	}

	backend := strings.TrimSpace(payload.Server)
	if backend == "" {
		backend = "Prism"
	}
	var sources []provider.MediaSource
	for _, track := range payload.Tracks {
		playlist := strings.TrimSpace(track.URL)
		if playlist == "" {
			continue
		}
		height := kit.QualityHeight(track.Quality)
		quality := strings.TrimSpace(track.Quality)
		if quality == "" {
			quality = "Auto (" + backend + ")"
		}
		// Classify by the declared kind first: every stream.hls.lol URL
		// contains "hls", which would otherwise mask a DASH track.
		sourceType := kit.NormalizeSourceKind(track.Kind)
		if strings.TrimSpace(track.Kind) == "" {
			sourceType = kit.NormalizeSourceKind(playlist)
		}
		source := c.newSource(playlist, quality, backend, sourceType, height)
		// Prism advertises one row per audio track at the same resolution, so
		// the language is what tells otherwise identical rows apart.
		source.AudioLabel = strings.TrimSpace(track.Lang)
		if strings.EqualFold(strings.TrimSpace(track.Mode), provider.AudioDub) {
			source.Language = lang.Normalize(track.Lang)
		}
		if sourceType == sourceTypeDASH {
			// The lane URL is extensionless (/mbox/mpd?q=…), so mpv cannot
			// infer the container and needs the format forced.
			source.ExtraArgs = []string{"--demuxer=lavf", "--demuxer-lavf-format=dash"}
		}
		sources = append(sources, source)
	}
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

// fetchBandit resolves Bandit's listing across every server it advertises.
func (c *Client) fetchBandit(ctx context.Context, coords coordinates) ([]provider.MediaSource, error) {
	params := url.Values{}
	params.Set("type", coords.mediaType)
	params.Set("tmdb", strconv.Itoa(coords.tmdbID))
	if coords.mediaType == provider.MediaTypeTV {
		params.Set("season", strconv.Itoa(coords.season))
		params.Set("episode", strconv.Itoa(coords.episode))
	}
	target := fmt.Sprintf("%s/bandit/streams?%s", c.endpoints.Pal, params.Encode())

	var payload banditResponse
	if err := c.getJSON(ctx, target, &payload); err != nil {
		return nil, err
	}
	if !payload.Found {
		return nil, provider.ErrNoSources
	}

	var sources []provider.MediaSource
	for _, server := range payload.Servers {
		backend := strings.TrimSpace(server.Name)
		if backend == "" {
			backend = "Bandit"
		}
		for _, stream := range server.Streams {
			playlist := strings.TrimSpace(stream.URL)
			if playlist == "" {
				continue
			}
			height := kit.QualityHeight(stream.Quality)
			quality := strings.TrimSpace(stream.Quality)
			if quality == "" {
				quality = "Auto (" + backend + ")"
			}
			sources = append(sources, c.newSource(playlist, quality, backend, provider.SourceTypeHLS, height))
		}
	}
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

// fetchSubtitles asks Vidora's subtitle service for the title's tracks. The
// first track per normalized language wins so the picker does not fill with
// a dozen English releases.
func (c *Client) fetchSubtitles(ctx context.Context, coords coordinates) ([]provider.SubtitleOption, error) {
	params := url.Values{}
	params.Set("tmdbId", strconv.Itoa(coords.tmdbID))
	if coords.mediaType == provider.MediaTypeTV {
		params.Set("season", strconv.Itoa(coords.season))
		params.Set("episode", strconv.Itoa(coords.episode))
	}
	target := fmt.Sprintf("%s/subs?%s", c.endpoints.Subs, params.Encode())

	var payload subsResponse
	if err := c.getJSON(ctx, target, &payload); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(payload.Subtitles))
	var subtitles []provider.SubtitleOption
	for _, entry := range payload.Subtitles {
		subtitleURL := strings.TrimSpace(entry.URL)
		if subtitleURL == "" {
			continue
		}
		language := lang.Normalize(entry.LangCode)
		if language == "" {
			language = lang.Normalize(entry.Language)
		}
		if language == "" {
			language = "und"
		}
		if _, exists := seen[language]; exists {
			continue
		}
		seen[language] = struct{}{}
		subtitles = append(subtitles, provider.SubtitleOption{
			URL:      subtitleURL,
			Language: language,
			Referer:  c.endpoints.Site + "/",
		})
	}
	return subtitles, nil
}

// streamTarget builds a stream.hls.lol player query. Both players name the
// season and episode with their own parameter spelling.
func (c *Client) streamTarget(path string, coords coordinates) string {
	params := url.Values{}
	params.Set("tmdbId", strconv.Itoa(coords.tmdbID))
	params.Set("type", coords.mediaType)
	if coords.mediaType == provider.MediaTypeTV {
		params.Set("seasonId", strconv.Itoa(coords.season))
		params.Set("episodeId", strconv.Itoa(coords.episode))
	}
	return fmt.Sprintf("%s/%s?%s", c.endpoints.Stream, path, params.Encode())
}

// maxDeclaredHeight returns the tallest resolution a player advertises.
func maxDeclaredHeight(qualities []streamQuality) int {
	height := 0
	for _, quality := range qualities {
		if parsed := kit.QualityHeight(quality.Resolution); parsed > height {
			height = parsed
		}
	}
	return height
}

// newSource builds a directly playable row tagged with its backend.
func (c *Client) newSource(playlist, quality, backend, sourceType string, height int) provider.MediaSource {
	return provider.MediaSource{
		URL:            playlist,
		Quality:        quality,
		Backend:        backend,
		Type:           sourceType,
		Height:         height,
		Referer:        c.endpoints.Site + "/",
		UserAgent:      config.DesktopUserAgent,
		SuppressOrigin: true,
	}
}

// getJSON performs one lane request and decodes its JSON body.
func (c *Client) getJSON(ctx context.Context, target string, payload any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("vidora request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.endpoints.Site+"/")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vidora request %s: %w", target, err)
	}
	defer resp.Body.Close()

	// A 404 is an empty lane, but 5xx is a real outage: fetchSources ignores
	// it while a sibling lane serves the title and returns it when none do.
	if resp.StatusCode == http.StatusNotFound {
		return provider.ErrNoSources
	}
	if resp.StatusCode != http.StatusOK {
		return &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return fmt.Errorf("vidora read %s: %w", target, err)
	}
	if err := json.Unmarshal(body, payload); err != nil {
		return fmt.Errorf("vidora decode %s: %w", target, err)
	}
	return nil
}
