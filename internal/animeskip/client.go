package animeskip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"kari/internal/config"
	"kari/internal/logging"
)

// SkipTimes holds interval boundaries (in seconds) for each skippable zone.
// A value of -1 means the zone is absent for this episode.
type SkipTimes struct {
	OpStart      float64
	OpEnd        float64
	EdStart      float64
	EdEnd        float64
	RecapStart   float64
	RecapEnd     float64
	PreviewStart float64
	PreviewEnd   float64
}

// Client queries the Anime-Skip GraphQL API for episode timestamps.
type Client struct {
	http     *http.Client
	clientID string
}

var asLog = logging.With("component", "animeskip")

// NewClient constructs a Client with a shared HTTP client and client ID.
func NewClient(httpClient *http.Client, clientID string) (*Client, error) {
	if httpClient == nil {
		return nil, errors.New("anime-skip client: http client is required")
	}
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("anime-skip client: client ID is required")
	}

	return &Client{
		http:     httpClient,
		clientID: clientID,
	}, nil
}

type gqlRequest struct {
	Query     string       `json:"query"`
	Variables gqlVariables `json:"variables"`
}

type gqlVariables struct {
	AniListID string `json:"id,omitempty"`
	Search    string `json:"s,omitempty"`
	ShowID    string `json:"showId,omitempty"`
	EpisodeID string `json:"epId,omitempty"`
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlResponse struct {
	Errors []gqlError `json:"errors"`
}

func (r gqlResponse) graphqlErrors() []gqlError {
	return r.Errors
}

type graphqlErrorResponse interface {
	graphqlErrors() []gqlError
}

type showMeta struct {
	ID string `json:"id"`
}

type episodeMeta struct {
	ID             string `json:"id"`
	Number         string `json:"number"`
	AbsoluteNumber string `json:"absoluteNumber"`
	Name           string `json:"name"`
}

type timestampType struct {
	Name string `json:"name"`
}

type timestamp struct {
	At   float64       `json:"at"`
	Type timestampType `json:"type"`
}

type byExternalIDResponse struct {
	gqlResponse
	Data struct {
		FindShowsByExternalId []showMeta `json:"findShowsByExternalId"`
	} `json:"data"`
}

type searchShowsResponse struct {
	gqlResponse
	Data struct {
		SearchShows []showMeta `json:"searchShows"`
	} `json:"data"`
}

type findEpisodesResponse struct {
	gqlResponse
	Data struct {
		FindEpisodesByShowId []episodeMeta `json:"findEpisodesByShowId"`
	} `json:"data"`
}

type findTimestampsResponse struct {
	gqlResponse
	Data struct {
		FindTimestampsByEpisodeId []timestamp `json:"findTimestampsByEpisodeId"`
	} `json:"data"`
}

// GetTimestamps fetches skip-zone intervals for one episode using a fast
// 3-step targeted query: (1) resolve show IDs, (2) fetch episode metadata
// across matched shows to locate the target episode ID, and (3) query
// timestamps for only that episode. Returns nil, nil when not found.
func (c *Client) GetTimestamps(
	ctx context.Context,
	anilistID string,
	episodeNum int,
	seriesTitle string,
	episodeTitle string,
) (*SkipTimes, error) {
	shows := []showMeta{}
	var lookupErr error

	// Step 1: Resolve Show IDs (metadata only — no heavy unpaginated timestamp trees).
	if anilistID != "" {
		q := `query($id: String!) {
            findShowsByExternalId(service: ANILIST, serviceId: $id) { id }
        }`
		var resp byExternalIDResponse
		if err := c.do(ctx, q, gqlVariables{AniListID: anilistID}, &resp); err == nil {
			shows = resp.Data.FindShowsByExternalId
		} else {
			lookupErr = err
			asLog.Debug("animeskip resolve by anilist id failed", "anilistID", anilistID, "err", err)
		}
	}

	// Fallback: title search when unlinked.
	if len(shows) == 0 && strings.TrimSpace(seriesTitle) != "" {
		q := `query($s: String!) {
            searchShows(search: $s, limit: 3) { id }
        }`
		var resp searchShowsResponse
		if err := c.do(ctx, q, gqlVariables{Search: seriesTitle}, &resp); err == nil {
			shows = resp.Data.SearchShows
		} else {
			if lookupErr != nil {
				lookupErr = errors.Join(lookupErr, err)
			} else {
				lookupErr = err
			}
			asLog.Debug("animeskip resolve by title failed", "title", seriesTitle, "err", err)
		}
	}
	if len(shows) == 0 && lookupErr != nil {
		return nil, fmt.Errorf("animeskip resolve show: %w", lookupErr)
	}

	seenShows := map[string]bool{}
	uniqueShows := []showMeta{}
	for _, s := range shows {
		if !seenShows[s.ID] && s.ID != "" {
			seenShows[s.ID] = true
			uniqueShows = append(uniqueShows, s)
		}
	}
	shows = uniqueShows

	// Step 2: Fetch episode metadata across all matched shows concurrently.
	type showResult struct {
		eps []episodeMeta
		err error
	}
	results := make([]showResult, len(shows))
	var wg sync.WaitGroup
	for i, s := range shows {
		wg.Add(1)
		go func(idx int, showID string) {
			defer wg.Done()
			q := `query($showId: ID!) {
                findEpisodesByShowId(showId: $showId) { id number absoluteNumber name }
            }`
			var resp findEpisodesResponse
			if err := c.do(ctx, q, gqlVariables{ShowID: showID}, &resp); err == nil {
				results[idx] = showResult{eps: resp.Data.FindEpisodesByShowId}
			} else {
				results[idx] = showResult{err: err}
			}
		}(i, s.ID)
	}
	wg.Wait()

	allEpisodes := []episodeMeta{}
	var episodeErr error
	for _, res := range results {
		allEpisodes = append(allEpisodes, res.eps...)
		if res.err != nil {
			if episodeErr != nil {
				episodeErr = errors.Join(episodeErr, res.err)
			} else {
				episodeErr = res.err
			}
		}
	}

	// Match the best episode entry.
	targetEp := bestEpisode(allEpisodes, episodeNum, episodeTitle)
	if targetEp == nil {
		if len(allEpisodes) == 0 && episodeErr != nil {
			return nil, fmt.Errorf("animeskip resolve episodes: %w", episodeErr)
		}
		return nil, nil
	}

	// Step 3: Fetch timestamps for ONLY the selected episode.
	q := `query($epId: ID!) {
        findTimestampsByEpisodeId(episodeId: $epId) {
            at
            type { name }
        }
    }`
	var tsResp findTimestampsResponse
	if err := c.do(ctx, q, gqlVariables{EpisodeID: targetEp.ID}, &tsResp); err != nil {
		return nil, fmt.Errorf("animeskip timestamps: %w", err)
	}

	return parseIntervals(tsResp.Data.FindTimestampsByEpisodeId), nil
}

// bestEpisode selects the episode metadata entry matching episodeTitle (by
// normalized title string) or episodeNum (by number or absoluteNumber).
func bestEpisode(episodes []episodeMeta, episodeNum int, episodeTitle string) *episodeMeta {
	cleanTarget := normalizeTitle(episodeTitle)

	// Strategy 1: Title match (protects against scraper episode offsets).
	// The Contains check is fuzzy but can false-positive on short titles
	// ("go" matching "godzilla"), so it is only applied when both sides
	// have at least 4 characters after normalization.
	if cleanTarget != "" {
		for i := range episodes {
			ep := episodes[i]
			if ep.Name != "" {
				epClean := normalizeTitle(ep.Name)
				if epClean == cleanTarget {
					epCopy := ep
					return &epCopy
				}
				if len(cleanTarget) >= 4 && len(epClean) >= 4 {
					if strings.Contains(epClean, cleanTarget) || strings.Contains(cleanTarget, epClean) {
						epCopy := ep
						return &epCopy
					}
				}
			}
		}
	}

	// Strategy 2: Match by episode number or absoluteNumber.
	for i := range episodes {
		ep := episodes[i]
		if matchesNum(&ep, episodeNum) {
			epCopy := ep
			return &epCopy
		}
	}

	return nil
}

func normalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func matchesNum(ep *episodeMeta, n int) bool {
	if parseIntLoose(ep.Number) == n {
		return true
	}
	if ep.AbsoluteNumber != "" && parseIntLoose(ep.AbsoluteNumber) == n {
		return true
	}
	return false
}

// parseIntervals converts Anime-Skip's sequential at-markers into discrete
// start/end intervals for each zone type. Markers are sorted ascending; the
// end of each zone is the at-value of the next marker.
func parseIntervals(timestamps []timestamp) *SkipTimes {
	if len(timestamps) == 0 {
		return nil
	}

	sorted := make([]timestamp, len(timestamps))
	copy(sorted, timestamps)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].At < sorted[j].At
	})

	st := &SkipTimes{
		OpStart: -1, OpEnd: -1,
		EdStart: -1, EdEnd: -1,
		RecapStart: -1, RecapEnd: -1,
		PreviewStart: -1, PreviewEnd: -1,
	}

	introTypes := map[string]bool{
		"Intro": true, "New Intro": true, "Mixed Intro": true,
	}
	creditsTypes := map[string]bool{
		"Credits": true, "New Credits": true, "Mixed Credits": true,
	}

	for i, ts := range sorted {
		startAt := ts.At
		if startAt < 0 {
			startAt = 0
		}

		var endAt float64
		if i+1 < len(sorted) {
			endAt = sorted[i+1].At
		} else {
			// Unclosed last marker: synthesize a 90s zone. This can extend
			// past the real duration (e.g. preview at 1430 → 1520 on a
			// 1440s episode) but mpv clamps chapter times to EOF and the
			// active_zone check already handles overrun gracefully.
			endAt = startAt + 90
		}

		if endAt <= startAt {
			continue
		}

		switch {
		case introTypes[ts.Type.Name]:
			if st.OpStart < 0 {
				st.OpStart = startAt
				st.OpEnd = endAt
			}
		case creditsTypes[ts.Type.Name]:
			if st.EdStart < 0 {
				st.EdStart = startAt
				st.EdEnd = endAt
			}
		case ts.Type.Name == "Recap":
			if st.RecapStart < 0 {
				st.RecapStart = startAt
				st.RecapEnd = endAt
			}
		case ts.Type.Name == "Preview":
			if st.PreviewStart < 0 {
				st.PreviewStart = startAt
				st.PreviewEnd = endAt
			}
		}
	}

	// If nothing useful was found, return nil rather than an empty struct.
	if st.OpStart < 0 && st.EdStart < 0 && st.RecapStart < 0 && st.PreviewStart < 0 {
		return nil
	}
	return st
}

func (c *Client) do(ctx context.Context, query string, vars gqlVariables, out any) error {
	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return fmt.Errorf("animeskip marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.AnimeSkipAPIBase, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("animeskip request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-ID", c.clientID)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("animeskip fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("animeskip api status %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("animeskip decode: %w", err)
	}
	if errResponse, ok := out.(graphqlErrorResponse); ok && len(errResponse.graphqlErrors()) > 0 {
		return fmt.Errorf("animeskip graphql: %s", errResponse.graphqlErrors()[0].Message)
	}
	return nil
}

func parseIntLoose(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return -1
	}
	return int(f)
}
