package cinejoy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
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
	"kari/internal/timeouts"
)

const (
	// serverListingTimeout bounds the /servers lookup that precedes the
	// lane fan-out.
	serverListingTimeout = timeouts.CinejoyServer
	// laneRequestTimeout bounds one sealed gateway call.
	laneRequestTimeout = timeouts.CinejoyLane
	// laneProbeBudget bounds the master probing that follows a lane answer, so
	// one slow CDN cannot stretch a lane past its share of the provider budget.
	laneProbeBudget = timeouts.CinejoyLane
)

var (
	// reLaneName keeps gateway-supplied server names path-safe: they become
	// one URL segment in the sealed request path.
	reLaneName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

// lane is one advertised stream server. fourK marks lanes the gateway itself
// flags as 4K-capable; they are queried first so their 2160p tiers rank above
// the 1080p-only lanes.
type lane struct {
	name  string
	fourK bool
}

// defaultLanes returns the gateway's known server set. It is the fallback when
// the small /servers listing is unavailable, so a listing hiccup cannot take
// playback down. Each call allocates: callers own the slice they get back.
func defaultLanes() []lane {
	return []lane{
		{name: "Light", fourK: true},
		{name: "Lisbon", fourK: true},
		{name: "Sirius", fourK: true},
		{name: "Nebula"},
	}
}

type serversResponse struct {
	Servers []serverEntry `json:"servers"`
}

type serverEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	FourK  bool   `json:"4k"`
}

// resolvePayload is the decoded payload a lane receives. The gateway takes
// every value as a string, exactly as the site's own player sends it.
type resolvePayload struct {
	TMDB    string `json:"tmdb"`
	Season  string `json:"season,omitempty"`
	Episode string `json:"episode,omitempty"`
}

type gateRequest struct {
	Path    string         `json:"path"`
	Payload resolvePayload `json:"payload"`
}

type gateEnvelope struct {
	Status int `json:"status"`
	Data   struct {
		Stream []gateStream `json:"stream"`
		Error  string       `json:"error"`
	} `json:"data"`
}

type gateStream struct {
	Type     string        `json:"type"`
	Playlist string        `json:"playlist"`
	Captions []gateCaption `json:"captions"`
}

type gateCaption struct {
	URL      string `json:"url"`
	Language string `json:"language"`
	Type     string `json:"type"`
}

// laneResult is one lane's answer, tagged with the lane it came from so
// failures name the server the user could switch away from.
type laneResult struct {
	name    string
	sources []provider.MediaSource
	err     error
}

// variantRow is one quality tier of one master, ready to become a source.
type variantRow struct {
	URL        string
	Quality    string
	Bandwidth  int
	Height     int
	AudioGroup string
	// videoOnly marks a tier whose audio lives in a separate EXT-X-MEDIA
	// rendition. Its bare URL plays silence, so the master row carries it.
	videoOnly bool
}

func parseCoordinates(mediaID string, episode provider.Episode) (int, string, int, int, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %w", err)
		}
		if tmdbID <= 0 {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %q", mediaID)
		}
	}

	mediaType := provider.MediaTypeMovie
	season, epNum := 1, 1
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = provider.MediaTypeTV
		if episode.Season > 0 {
			season = episode.Season
		}
		if episode.Episode > 0 {
			epNum = episode.Episode
		}
	}
	return tmdbID, mediaType, season, epNum, nil
}

// fetchSources queries every advertised lane and pushes each lane's playable
// rows to updates as soon as that lane answers, so a fast lane is playable
// while a slow one is still probing masters. Lanes that fail are logged and
// never fail the whole resolve.
func (c *Client) fetchSources(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, epNum int,
	updates chan<- []provider.MediaSource,
) error {
	kind := "movie"
	payload := resolvePayload{TMDB: strconv.Itoa(tmdbID)}
	if mediaType == provider.MediaTypeTV {
		kind = "series"
		payload.Season = strconv.Itoa(season)
		payload.Episode = strconv.Itoa(epNum)
	}

	lanes := c.fetchLanes(ctx)
	results := make(chan laneResult, len(lanes))
	var wg sync.WaitGroup
	for _, current := range lanes {
		wg.Add(1)
		go func(target lane) {
			defer wg.Done()
			sources, err := c.resolveLane(ctx, target, kind, payload)
			results <- laneResult{name: target.name, sources: sources, err: err}
		}(current)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var (
		seen = make(map[string]bool)
		errs []error
		sent int
	)
	for result := range results {
		if result.err != nil {
			if !errors.Is(result.err, provider.ErrNoSources) {
				logging.Debug("cinejoy lane failed", "lane", result.name, "tmdb", tmdbID, "err", result.err)
			}
			errs = append(errs, result.err)
			continue
		}

		batch := make([]provider.MediaSource, 0, len(result.sources))
		for _, source := range provider.FilterDirectSources(result.sources) {
			if seen[source.URL] {
				continue
			}
			seen[source.URL] = true
			batch = append(batch, source)
		}
		if len(batch) == 0 {
			continue
		}

		select {
		case updates <- batch:
			sent += len(batch)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if sent == 0 {
		if len(errs) > 0 {
			return fmt.Errorf("cinejoy resolve: %w", errors.Join(errs...))
		}
		return provider.ErrNoSources
	}
	return nil
}

// fetchLanes asks the gateway which servers are currently available. A listing
// failure or an all-disabled answer falls back to the known lane set.
func (c *Client) fetchLanes(ctx context.Context) []lane {
	reqCtx, cancel := context.WithTimeout(ctx, serverListingTimeout)
	defer cancel()

	req, err := c.newRequest(reqCtx, c.apiBase+"/servers")
	if err != nil {
		logging.Debug("cinejoy server listing request failed; using known lanes", "err", err)
		return defaultLanes()
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logging.Debug("cinejoy server listing failed; using known lanes", "err", err)
		return defaultLanes()
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		logging.Debug("cinejoy server listing rejected; using known lanes", "status", resp.StatusCode)
		return defaultLanes()
	}

	raw, err := httpclient.ReadCapped(resp)
	if err != nil {
		logging.Debug("cinejoy server listing unreadable; using known lanes", "err", err)
		return defaultLanes()
	}
	var data serversResponse
	if err := json.Unmarshal(raw, &data); err != nil {
		logging.Debug("cinejoy server listing decode failed; using known lanes", "err", err)
		return defaultLanes()
	}

	lanes := make([]lane, 0, len(data.Servers))
	for _, srv := range data.Servers {
		name := strings.TrimSpace(srv.Name)
		if srv.Status != "ok" || !reLaneName.MatchString(name) {
			continue
		}
		lanes = append(lanes, lane{name: name, fourK: srv.FourK})
	}
	if len(lanes) == 0 {
		logging.Debug("cinejoy server listing empty; using known lanes")
		return defaultLanes()
	}

	sort.SliceStable(lanes, func(i, j int) bool {
		return lanes[i].fourK && !lanes[j].fourK
	})
	return lanes
}

// resolveLane seals one request for one server and expands its playlists into
// quality rows. A lane without the title is an empty answer, not a failure.
func (c *Client) resolveLane(ctx context.Context, target lane, kind string, payload resolvePayload) ([]provider.MediaSource, error) {
	reqCtx, cancel := context.WithTimeout(ctx, laneRequestTimeout)
	defer cancel()

	raw, err := json.Marshal(gateRequest{Path: "/" + target.name + "/" + kind, Payload: payload})
	if err != nil {
		return nil, fmt.Errorf("cinejoy encode request: %w", err)
	}
	body, session, err := sealRequest(c.gate, raw)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.apiBase+"/g", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cinejoy request: %w", err)
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cinejoy lane %s: %w", target.name, err)
	}

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		resp.Body.Close()
		return nil, provider.ErrNoSources
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: c.apiBase + "/g"}
	}

	sealed, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("cinejoy lane %s read: %w", target.name, err)
	}
	plaintext, err := session.openResponse(sealed)
	if err != nil {
		return nil, fmt.Errorf("cinejoy lane %s: %w", target.name, err)
	}

	var envelope gateEnvelope
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		return nil, fmt.Errorf("cinejoy lane %s decode: %w", target.name, err)
	}
	if envelope.Status == http.StatusNotFound {
		return nil, provider.ErrNoSources
	}
	if envelope.Status < 200 || envelope.Status >= 300 {
		if envelope.Data.Error != "" {
			return nil, fmt.Errorf("cinejoy lane %s status %d: %s", target.name, envelope.Status, envelope.Data.Error)
		}
		return nil, fmt.Errorf("cinejoy lane %s status %d", target.name, envelope.Status)
	}

	probeCtx, probeCancel := context.WithTimeout(ctx, laneProbeBudget)
	defer probeCancel()
	return c.streamSources(probeCtx, target.name, envelope.Data.Stream), nil
}

// streamSources turns one lane's streams into playable rows. Masters are probed
// so every quality tier, including 4K, is listed separately from the adaptive
// stream.
func (c *Client) streamSources(ctx context.Context, laneName string, streams []gateStream) []provider.MediaSource {
	var sources []provider.MediaSource
	seen := make(map[string]bool)
	type rowMeta struct {
		height    int
		bitrate   int
		subType   string
		subtitles []provider.SubtitleOption
	}
	add := func(target, quality, sourceType string, meta rowMeta) {
		if target == "" || seen[target] {
			return
		}
		seen[target] = true
		src := c.newMediaSource(target, quality, sourceType, meta.bitrate, laneName, meta.subtitles)
		src.Height = meta.height
		src.SubType = meta.subType
		sources = append(sources, src)
	}

	for _, stream := range streams {
		playlist := strings.TrimSpace(stream.Playlist)
		if playlist == "" {
			continue
		}
		sourceType := sourceTypeFor(stream.Type)
		if sourceType == "" {
			logging.Debug("cinejoy stream type unsupported; skipping", "lane", laneName, "type", stream.Type, "url", playlist)
			continue
		}
		meta := rowMeta{
			subtitles: c.subtitleOptions(stream.Captions),
			subType:   captionSubType(stream.Captions),
		}
		bestHeight, bestBitrate := 0, 0
		droppedVideoOnly := false
		for _, variant := range c.fetchVariants(ctx, playlist) {
			if variant.Height > bestHeight {
				bestHeight = variant.Height
			}
			if variant.Bandwidth > bestBitrate {
				bestBitrate = variant.Bandwidth
			}
			if variant.videoOnly {
				logging.Debug("cinejoy variant is video-only; keeping master",
					"lane", laneName, "quality", variant.Quality, "audio_group", variant.AudioGroup)
				droppedVideoOnly = true
				continue
			}
			add(variant.URL, variant.Quality, sourceType, rowMeta{
				height: variant.Height, bitrate: variant.Bandwidth,
				subType: meta.subType, subtitles: meta.subtitles,
			})
		}

		masterQuality := "Auto"
		if droppedVideoOnly && bestHeight > 0 {
			masterQuality = resolutionLabel(bestHeight)
		}
		meta.height, meta.bitrate = bestHeight, bestBitrate
		add(playlist, masterQuality, sourceType, meta)
	}
	return sources
}

// captionSubType marks a lane's subtitles as soft when the gateway declares a
// caption type. A missing type leaves the delivery kind unknown rather than
// guessing that every track is toggleable.
func captionSubType(captions []gateCaption) string {
	for _, caption := range captions {
		if strings.TrimSpace(caption.Type) != "" {
			return provider.SubTypeSoft
		}
	}
	return ""
}

// sourceTypeFor maps the gateway's container declaration onto the player's
// source type. Declarations the player cannot open return "" so the row is
// dropped instead of being mislabelled as HLS.
func sourceTypeFor(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "hls", "m3u8":
		return provider.SourceTypeHLS
	case "mp4":
		return provider.SourceTypeMP4
	default:
		return ""
	}
}

func (c *Client) newMediaSource(target, quality, sourceType string, bitrate int, backend string, subtitles []provider.SubtitleOption) provider.MediaSource {
	return provider.MediaSource{
		URL:       target,
		Quality:   quality,
		Backend:   backend,
		Type:      sourceType,
		Referer:   c.siteBase + "/",
		UserAgent: config.DesktopUserAgent,
		Subtitles: subtitles,
		Bitrate:   bitrate,
	}
}

// subtitleOptions converts a stream's caption list, normalizing language tags
// and skipping entries the player cannot fetch. The caption CDN enforces the
// Cinejoy origin (a bare request answers 404), so every option carries the
// site referer, which the downloader turns into Origin as well.
func (c *Client) subtitleOptions(captions []gateCaption) []provider.SubtitleOption {
	out := make([]provider.SubtitleOption, 0, len(captions))
	seen := make(map[string]bool)
	for _, caption := range captions {
		target := strings.TrimSpace(caption.URL)
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		code := lang.Normalize(caption.Language)
		if code == "" {
			code = "und"
		}
		out = append(out, provider.SubtitleOption{URL: target, Language: code, Referer: c.siteBase + "/"})
	}
	return out
}

// fetchVariants reads an HLS master and returns its quality tiers, highest
// first. A media playlist simply yields no variants.
func (c *Client) fetchVariants(ctx context.Context, masterURL string) []variantRow {
	variants, err := kit.ParseHLSVariants(ctx, c.httpClient, masterURL, c.cdnHeaders())
	if err != nil {
		logging.Debug("cinejoy variant parsing failed; keeping master", "url", masterURL, "err", err)
	}

	rows := make([]variantRow, 0, len(variants))
	for _, variant := range variants {
		height := credibleHeight(variant.Height, variant.Codecs)
		quality := "Auto"
		if height > 0 {
			quality = resolutionLabel(height)
		}
		rows = append(rows, variantRow{
			URL:        variant.URL,
			Quality:    quality,
			Bandwidth:  variant.Bandwidth,
			Height:     height,
			AudioGroup: variant.AudioGroup,
			videoOnly:  variant.AudioGroup != "",
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Height != rows[j].Height {
			return rows[i].Height > rows[j].Height
		}
		return rows[i].Bandwidth > rows[j].Bandwidth
	})
	return rows
}

// cdnHeaders returns the browser identity the lane CDNs check. The Cinejoy
// origin is mandatory on manifests and captions alike.
func (c *Client) cdnHeaders() map[string]string {
	return map[string]string{
		"User-Agent": config.DesktopUserAgent,
		"Referer":    c.siteBase + "/",
		"Origin":     c.siteBase,
	}
}

// newRequest builds a gateway/CDN GET carrying the browser identity the lane
// CDNs check. The Cinejoy origin is mandatory for stream manifests.
func (c *Client) newRequest(ctx context.Context, target string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("cinejoy request: %w", err)
	}
	c.applyHeaders(req)
	return req, nil
}

func (c *Client) applyHeaders(req *http.Request) {
	for key, value := range c.cdnHeaders() {
		req.Header.Set(key, value)
	}
}

// credibleHeight delegates the codec sanity check to the shared kit helper so
// every provider rejects the same impossible 4K claims. A declared 2160p tier
// is kept unless its AVC level proves it cannot physically carry 4K; that
// documented CredibleHeight policy is the only case where height is downgraded.
func credibleHeight(height int, codecs string) int {
	return kit.CredibleHeight(height, codecs)
}

// resolutionLabel buckets a pixel height into the shared canonical label.
func resolutionLabel(height int) string {
	return kit.QualityLabelFromHeight(height)
}
