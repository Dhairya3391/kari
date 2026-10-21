package animeskip

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	ID   string `json:"id"`
	Name string `json:"name"`
}

type episodeMeta struct {
	ID     string `json:"id"`
	Season string `json:"season"`
	Number string `json:"number"`
	Name   string `json:"name"`
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
	seasonNum int,
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
            searchShows(search: $s, limit: 3) { id name }
        }`
		var resp searchShowsResponse
		if err := c.do(ctx, q, gqlVariables{Search: seriesTitle}, &resp); err == nil {
			target := normalizeTitle(seriesTitle)
			for _, show := range resp.Data.SearchShows {
				if normalizeTitle(show.Name) == target {
					shows = append(shows, show)
				}
			}
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
				findEpisodesByShowId(showId: $showId) { id season number name }
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
	targetEp := bestEpisode(allEpisodes, seasonNum, episodeNum, episodeTitle)
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

// bestEpisode returns an episode only when its season and episode number or
// exact title identify one unambiguous Anime-Skip entry.
func bestEpisode(episodes []episodeMeta, seasonNum, episodeNum int, episodeTitle string) *episodeMeta {
	if seasonNum > 0 {
		seasonEpisodes := make([]episodeMeta, 0, len(episodes))
		hasSeasonData := false
		for _, ep := range episodes {
			season := parseIntLoose(ep.Season)
			if season > 0 {
				hasSeasonData = true
			}
			if season == seasonNum {
				seasonEpisodes = append(seasonEpisodes, ep)
			}
		}
		if len(seasonEpisodes) > 0 {
			episodes = seasonEpisodes
		} else if hasSeasonData {
			return nil
		}
	}

	cleanTarget := normalizeTitle(episodeTitle)

	var numbered *episodeMeta
	for i := range episodes {
		ep := episodes[i]
		if parseIntLoose(ep.Number) == episodeNum {
			if numbered != nil {
				numbered = nil
				break
			}
			epCopy := ep
			numbered = &epCopy
		}
	}
	if numbered != nil {
		return numbered
	}

	// Exact titles can recover a numbering offset, but partial title matches
	// may identify a different episode and are not safe skip data.
	if cleanTarget == "" {
		return nil
	}
	var exact *episodeMeta
	for i := range episodes {
		ep := episodes[i]
		if normalizeTitle(ep.Name) == cleanTarget {
			if exact != nil {
				return nil
			}
			epCopy := ep
			exact = &epCopy
		}
	}
	if exact != nil {
		return exact
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

// parseIntervals converts Anime-Skip's sequential at-markers into discrete
// intervals. An interval is returned only when a later marker supplies its
// end boundary.
func parseIntervals(timestamps []timestamp) *SkipTimes {
	if len(timestamps) == 0 {
		return nil
	}

	sorted := append([]timestamp(nil), timestamps...)
	slices.SortFunc(sorted, func(a, b timestamp) int {
		return cmp.Compare(a.At, b.At)
	})
	st := &SkipTimes{
		OpStart: -1, OpEnd: -1,
		EdStart: -1, EdEnd: -1,
		RecapStart: -1, RecapEnd: -1,
		PreviewStart: -1, PreviewEnd: -1,
	}

	introTypes := map[string]bool{
		"Intro": true, "New Intro": true, "Mixed Intro": true, "Opening": true, "OP": true,
	}
	creditsTypes := map[string]bool{
		"Credits": true, "New Credits": true, "Mixed Credits": true, "Ending": true, "Outro": true, "ED": true,
	}

	for i, ts := range sorted {
		startAt := ts.At
		if startAt < 0 || i+1 >= len(sorted) {
			continue
		}
		endAt := sorted[i+1].At

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
	if st.OpStart >= 0 && st.EdStart >= 0 && st.OpStart >= st.EdStart {
		st.OpStart, st.OpEnd = -1, -1
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
