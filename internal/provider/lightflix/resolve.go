package lightflix

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
)

// maxPowNonce bounds the CPU spent on a proof-of-work challenge. A difficulty
// of 18 bits succeeds well below this in practice; the cap keeps a malformed
// or hostile challenge from stalling a resolve.
const maxPowNonce = 5_000_000

// mediaCoordinates is the TMDB coordinate set Lightflix's playback API expects.
type mediaCoordinates struct {
	mediaID   int
	mediaType string
	tvSlug    string
	season    int
	episode   int
}

// streamSource names one Lightflix playback lane and its user-facing label.
type streamSource struct {
	id   string
	name string
}

type sessionResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
}

type playbackInitResponse struct {
	Success     bool         `json:"success"`
	Error       string       `json:"error"`
	RequiresPow bool         `json:"requiresPow"`
	Pow         powChallenge `json:"pow"`
	Token       string       `json:"token"`
	ExpiresIn   int          `json:"expiresIn"`
}

type powChallenge struct {
	ChallengeID string `json:"challengeId"`
	Challenge   string `json:"challenge"`
	Difficulty  int    `json:"difficulty"`
}

type powSolution struct {
	ChallengeID string `json:"challengeId"`
	Nonce       string `json:"nonce"`
}

type sealResponse struct {
	URL string `json:"url"`
}

type streamResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Data    struct {
		StreamURL string `json:"stream_url"`
		TMDBID    string `json:"tmdb_id"`
		MediaType string `json:"media_type"`
		Source    string `json:"source"`
	} `json:"data"`
}

// safeStreamError hides a token-bearing request URL from a transport error's
// message while keeping the cause available to errors.Is and errors.As.
type safeStreamError struct {
	cause error
}

// Error implements error with a message that cannot leak credentials.
func (e *safeStreamError) Error() string { return "lightflix stream request failed" }

// Unwrap exposes the original error for errors.Is and errors.As.
func (e *safeStreamError) Unwrap() error { return e.cause }

// streamSources lists Lightflix playback lanes in fallback order. Lumen is the
// site's default lane; Spectrum is the first alternate the player probes.
func streamSources() []streamSource {
	return []streamSource{
		{id: "s0", name: "Lumen"},
		{id: "s2", name: "Spectrum"},
	}
}

// parseCoordinates derives the TMDB coordinates and watch path from the
// provider's media handle and the selected episode.
func parseCoordinates(mediaID string, episode provider.Episode) (mediaCoordinates, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return mediaCoordinates{}, fmt.Errorf("lightflix invalid media ID %q: %w", mediaID, err)
		}
	}
	if tmdbID <= 0 {
		return mediaCoordinates{}, fmt.Errorf("lightflix invalid media ID %q", mediaID)
	}

	coords := mediaCoordinates{mediaID: tmdbID, mediaType: provider.MediaTypeMovie}
	if episode.Season > 0 || episode.Episode > 0 {
		season, ep := episode.Season, episode.Episode
		if season <= 0 {
			season = 1
		}
		if ep <= 0 {
			ep = 1
		}
		coords.mediaType = provider.MediaTypeTV
		coords.season = season
		coords.episode = ep
		coords.tvSlug = fmt.Sprintf("%d-%d", season, ep)
	}
	return coords, nil
}

// watchPath is the page path carried by the page request token.
func (m mediaCoordinates) watchPath() string {
	if m.mediaType == provider.MediaTypeTV {
		return fmt.Sprintf("/en/watch/tv/%d-%d-%d", m.mediaID, m.season, m.episode)
	}
	return fmt.Sprintf("/en/watch/movie/%d", m.mediaID)
}

// ResolveSource resolves a title to a directly playable HLS master, retrying
// once with fresh credentials when Lightflix rejects a cached session.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	coords, err := parseCoordinates(mediaID, episode)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		sources, err := c.resolveOnce(ctx, coords)
		if err == nil {
			if len(sources) == 0 {
				return nil, provider.ErrNoSources
			}
			return sources, nil
		}
		lastErr = err
		if !isSessionError(err) {
			break
		}
		c.resetSession()
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, provider.ErrNoSources
}

// resolveOnce performs one complete credential-and-stream resolution attempt.
func (c *Client) resolveOnce(ctx context.Context, coords mediaCoordinates) ([]provider.MediaSource, error) {
	requestToken, err := c.getRequestToken(ctx, coords.watchPath())
	if err != nil {
		return nil, err
	}
	streamToken, err := c.playbackToken(ctx, coords, requestToken)
	if err != nil {
		return nil, err
	}
	cookie, err := c.siteSessionCookie(ctx)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, source := range streamSources() {
		streamURL, err := c.resolveLane(ctx, coords, requestToken, streamToken, cookie, source.id)
		if err != nil {
			logging.Debug("lightflix lane failed", "source", source.id, "err", err)
			lastErr = err
			continue
		}
		return []provider.MediaSource{{
			URL:     streamURL,
			Quality: "Auto",
			Backend: source.name,
			Referer: c.siteBase + "/",
			Type:    provider.SourceTypeHLS,
		}}, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, provider.ErrNoSources
}

// playbackToken completes the proof-of-work handshake and returns the short
// stream token bound to this media and client IP.
func (c *Client) playbackToken(ctx context.Context, coords mediaCoordinates, requestToken string) (string, error) {
	cookie, err := c.siteSessionCookie(ctx)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"mediaId":      coords.mediaID,
		"mediaType":    coords.mediaType,
		"tv_slug":      coords.tvSlug,
		"requestToken": requestToken,
	}

	var init playbackInitResponse
	if err := c.postJSON(ctx, "/api/playback-init", body, cookie, &init); err != nil {
		return "", fmt.Errorf("lightflix playback init: %w", err)
	}
	if err := initError(init); err != nil {
		return "", err
	}
	if init.RequiresPow {
		solution, err := solvePow(ctx, init.Pow)
		if err != nil {
			return "", fmt.Errorf("lightflix proof of work: %w", err)
		}
		body["pow"] = solution
		init = playbackInitResponse{}
		if err := c.postJSON(ctx, "/api/playback-init", body, cookie, &init); err != nil {
			return "", fmt.Errorf("lightflix playback init after proof of work: %w", err)
		}
		if err := initError(init); err != nil {
			return "", err
		}
	}
	if init.Token == "" {
		return "", fmt.Errorf("lightflix playback init: empty stream token")
	}
	return init.Token, nil
}

// initError surfaces an explicit upstream failure from a playback-init response.
func initError(init playbackInitResponse) error {
	if init.Success && init.Error == "" {
		return nil
	}
	if init.Error != "" {
		return fmt.Errorf("lightflix playback init: %s", init.Error)
	}
	return fmt.Errorf("lightflix playback init: unsuccessful response")
}

// solvePow finds a nonce whose SHA-256 digest of challenge+nonce starts with
// the declared number of zero bits, as required by Lightflix's worker.
func solvePow(ctx context.Context, challenge powChallenge) (powSolution, error) {
	if challenge.ChallengeID == "" || challenge.Challenge == "" {
		return powSolution{}, errors.New("incomplete proof-of-work challenge")
	}
	if challenge.Difficulty <= 0 || challenge.Difficulty > sha256.Size*8 {
		return powSolution{}, fmt.Errorf("unsupported proof-of-work difficulty %d", challenge.Difficulty)
	}

	for nonce := 0; nonce < maxPowNonce; nonce++ {
		if nonce%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return powSolution{}, err
			}
		}
		digest := sha256.Sum256([]byte(challenge.Challenge + strconv.Itoa(nonce)))
		if leadingZeroBits(digest[:]) >= challenge.Difficulty {
			return powSolution{ChallengeID: challenge.ChallengeID, Nonce: strconv.Itoa(nonce)}, nil
		}
	}
	return powSolution{}, fmt.Errorf("no proof-of-work nonce found within %d attempts", maxPowNonce)
}

// leadingZeroBits counts the zero bits at the start of a digest.
func leadingZeroBits(digest []byte) int {
	count := 0
	for _, b := range digest {
		if b != 0 {
			return count + bits.LeadingZeros8(b)
		}
		count += 8
	}
	return count
}

// resolveLane seals one source request and reads its signed HLS URL.
func (c *Client) resolveLane(
	ctx context.Context,
	coords mediaCoordinates,
	requestToken, streamToken, cookie, sourceID string,
) (string, error) {
	sealBody := map[string]any{
		"data": map[string]any{
			"mediaId":   coords.mediaID,
			"mediaType": coords.mediaType,
			"tv_slug":   coords.tvSlug,
			"source":    sourceID,
		},
		"endpoint":     "stream-encrypted",
		"requestToken": requestToken,
	}
	var seal sealResponse
	if err := c.postJSON(ctx, "/api/encrypt", sealBody, cookie, &seal); err != nil {
		return "", fmt.Errorf("lightflix seal: %w", err)
	}
	if seal.URL == "" {
		return "", errors.New("lightflix seal: empty stream URL")
	}

	target, err := c.streamEndpoint(seal.URL, requestToken, streamToken)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("lightflix stream request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.siteBase+"/en/")
	if cookie != "" {
		req.Header.Set("Cookie", siteCookieName+"="+cookie)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", &safeStreamError{cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: c.siteBase + "/api/stream-encrypted"}
	}
	raw, err := httpclient.ReadCapped(resp)
	if err != nil {
		return "", fmt.Errorf("lightflix stream read: %w", err)
	}
	var out streamResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("lightflix stream decode: %w", err)
	}
	if !out.Success {
		if out.Error != "" {
			return "", fmt.Errorf("lightflix stream: %s", out.Error)
		}
		return "", errors.New("lightflix stream: unsuccessful response")
	}
	if out.Data.TMDBID == "0" || !provider.IsDirectURL(out.Data.StreamURL) {
		return "", errors.New("lightflix stream: no playable URL")
	}
	return out.Data.StreamURL, nil
}

// streamEndpoint resolves the sealed relative path against the site origin and
// appends the two short-lived tokens the stream endpoint requires.
func (c *Client) streamEndpoint(rawURL, requestToken, streamToken string) (string, error) {
	ref, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("lightflix sealed URL: %w", err)
	}
	// The seal response is upstream-controlled. ResolveReference keeps an
	// absolute or scheme-relative URL's host, which would send both tokens and
	// the site cookie to that host instead of the Lightflix origin.
	if ref.Scheme != "" || ref.Host != "" {
		return "", errors.New("lightflix sealed URL must be site-relative")
	}
	base, err := url.Parse(c.siteBase + "/")
	if err != nil {
		return "", fmt.Errorf("lightflix base URL: %w", err)
	}
	target := base.ResolveReference(ref)
	query := target.Query()
	query.Set("requestToken", requestToken)
	query.Set("token", streamToken)
	target.RawQuery = query.Encode()
	return target.String(), nil
}

// isSessionError reports whether a failure means cached credentials were
// rejected and the chain is worth retrying once with fresh ones.
func isSessionError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) && httpErr.Code == http.StatusUnauthorized {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{
		"invalid request token",
		"playback session required",
		"token expired",
		"request token required",
	} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}
