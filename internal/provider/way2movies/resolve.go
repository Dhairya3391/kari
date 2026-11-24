package way2movies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
	"kari/internal/timeouts"
)

// reQualityHeight reads the resolution Way2Movies encodes in each playlist
// path (for example /720/index_287.m3u8), avoiding a manifest probe just to
// label the row.
var reQualityHeight = regexp.MustCompile(`/(\d{3,4})/`)

// scraperResponse is one server's answer. Servers without the title answer
// with an empty media_urls list or a 404. Languages holds every language the
// server declares for the title; Language is its primary language.
type scraperResponse struct {
	MediaURLs []string `json:"media_urls"`
	Languages []string `json:"languages"`
	Language  string   `json:"language"`
}

// serverResult carries one server's answer back to the fan-in.
type serverResult struct {
	serverID int
	sources  []provider.MediaSource
	err      error
}

// scraperServers returns the server IDs that answer plain HTTP requests with
// playlists. IDs 58, 63, 69, and 79 are excluded: 58 is disabled upstream and
// the others require a browser-side resolve gate, so they can only answer 400
// or 409 and would surface as provider failures during fan-out. Each call
// allocates so callers own the slice they receive.
func scraperServers() []int {
	return []int{39, 42, 45, 53, 74}
}

// firstLanguage returns the primary canonical code used for the player's
// audio selection, or an empty string when no language was declared.
func firstLanguage(codes []string) string {
	if len(codes) == 0 {
		return ""
	}
	return codes[0]
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

// fetchSources queries every known server and keeps the first row for each
// distinct playlist. Per-server failures are expected during fan-out and never
// fail the whole resolve; only a total transport failure is returned.
func (c *Client) fetchSources(ctx context.Context, tmdbID int, mediaType string, season, epNum int) ([]provider.MediaSource, error) {
	var path string
	if mediaType == provider.MediaTypeTV {
		path = fmt.Sprintf("tv/%d/%d/%d", tmdbID, season, epNum)
	} else {
		path = fmt.Sprintf("movie/%d", tmdbID)
	}

	servers := scraperServers()
	results := make(chan serverResult, len(servers))
	for _, id := range servers {
		go func(serverID int) {
			sources, err := c.fetchServer(ctx, serverID, path)
			results <- serverResult{serverID: serverID, sources: sources, err: err}
		}(id)
	}

	var (
		all  []provider.MediaSource
		errs []error
		seen = make(map[string]bool)
	)
	for range servers {
		result := <-results
		if result.err != nil {
			if !errors.Is(result.err, provider.ErrNoSources) {
				logging.Debug("way2movies server failed", "server", result.serverID, "err", result.err)
				errs = append(errs, result.err)
			}
			continue
		}
		for _, source := range result.sources {
			if seen[source.URL] {
				continue
			}
			seen[source.URL] = true
			all = append(all, source)
		}
	}

	if len(all) > 0 {
		return all, nil
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("way2movies resolve: %w", errors.Join(errs...))
	}
	return nil, provider.ErrNoSources
}

// fetchServer asks one server for its playlists. A 404 means the server does
// not carry the title, which is an empty answer rather than a failure.
func (c *Client) fetchServer(ctx context.Context, serverID int, path string) ([]provider.MediaSource, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeouts.LaneBudget)
	defer cancel()

	target := fmt.Sprintf("%s/server/%d/%s", c.scraperBase, serverID, path)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("way2movies request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", c.origin)
	req.Header.Set("Referer", c.origin+"/")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("way2movies server %d: %w", serverID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, provider.ErrNoSources
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	raw, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("way2movies server %d read: %w", serverID, err)
	}
	var data scraperResponse
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("way2movies server %d decode: %w", serverID, err)
	}

	// The filter pipeline ignores AudioLabel outside live mode and treats
	// untagged on-demand rows as English, so stamp typed codes from the
	// scraper's language declarations or a Hindi title is dropped when the
	// user switches off English.
	languageCodes := lang.Canonicalize(data.Languages)
	if len(languageCodes) == 0 {
		languageCodes = lang.Canonicalize([]string{data.Language})
	}
	display := lang.Names(languageCodes)
	if display == "" {
		display = strings.TrimSpace(data.Language)
	}
	primary := firstLanguage(languageCodes)

	backend := fmt.Sprintf("Server %d", serverID)
	sources := make([]provider.MediaSource, 0, len(data.MediaURLs))
	for _, item := range data.MediaURLs {
		playlist := strings.TrimSpace(item)
		if playlist == "" {
			continue
		}
		source := provider.MediaSource{
			URL:            playlist,
			Type:           provider.SourceTypeHLS,
			Backend:        backend,
			Quality:        "Auto",
			Referer:        c.origin + "/",
			Audio:          provider.AudioInfo{Languages: languageCodes},
			AudioLabel:     display,
			Language:       primary,
			PreferredAudio: primary,
		}
		if match := reQualityHeight.FindStringSubmatch(playlist); len(match) == 2 {
			if height, err := strconv.Atoi(match[1]); err == nil {
				source.Height = height
				source.Quality = kit.QualityLabelFromHeight(height)
			}
		}
		sources = append(sources, source)
	}
	return sources, nil
}
