package anikoto

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
)

const directCacheTTL = 3600 // seconds

// anikotoIDEntry is a cached mapping from AniList ID to anikoto's internal
// animeID + slug. Cached to avoid redundant HTML scrapes on every ResolveSource call.
type anikotoIDEntry struct {
	animeID  string
	slug     string
	cachedAt time.Time
}

// idCache maps AniList ID strings to their anikoto equivalents.
// sync.Map is used for safe concurrent access without locking overhead on reads.
var idCache sync.Map // key: string anilistID → anikotoIDEntry

// anikotoHeaders builds request headers for direct anikoto site requests.
// The Referer is required by the CDN and server-list endpoints.
func anikotoHeaders(referer string) http.Header {
	h := http.Header{}
	h.Set("User-Agent", config.DesktopUserAgent)
	h.Set("Accept", "application/json, text/html, */*")
	if referer != "" {
		h.Set("Referer", referer)
	}
	return h
}

var (
	reWatchMainID  = regexp.MustCompile(`id="watch-main"[^>]*data-id="(\d+)"`)
	reDataID       = regexp.MustCompile(`data-id="(\d+)"`)
	reHeadingTitle = regexp.MustCompile(`(?i)<h1[^>]*>(.*?)</h1>`)
	rePageTitle    = regexp.MustCompile(`(?i)<title>(.*?)</title>`)
)

// anikotoCandidate is one search hit: site slug, display name, and the
// internal animeID when the search page already carried it (data-tip).
type anikotoCandidate struct {
	slug    string
	name    string
	animeID string
}

// posterPattern matches one search-result poster block, capturing the
// data-tip anime ID, the watch slug (bare or with an /ep-N suffix), and
// the poster's alt title. Host is injected per request so tests can serve
// fixtures from httptest servers.
func posterPattern(host string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)<div class="ani poster tip"[^>]*data-tip="(\d+)"[^>]*>.*?<a href="` + host + `/watch/([^"/]+)(?:/ep-\d+)?"[^>]*>.*?alt="([^"]+)"`)
}

// ajaxPattern matches one autocomplete hit: watch slug plus display name.
func ajaxPattern(host string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)href="` + host + `/watch/([^"/]+)(?:/ep-\d+)?"[^>]*>.*?<div class="name d-title"[^>]*>(.*?)</div>`)
}

// resolveAnilistToAnikoto maps an AniList ID to anikoto's internal animeID and slug.
// Results are cached for directCacheTTL seconds to avoid redundant scrapes.
func (c *Client) resolveAnilistToAnikoto(ctx context.Context, anilistID string) (animeID, slug string, err error) {
	// Return cached entry if fresh.
	if v, ok := idCache.Load(anilistID); ok {
		entry := v.(anikotoIDEntry)
		if time.Since(entry.cachedAt) < directCacheTTL*time.Second {
			return entry.animeID, entry.slug, nil
		}
	}
	// Fetch title candidates from AniList to build search queries.
	idInt, err := strconv.Atoi(anilistID)
	if err != nil {
		return "", "", fmt.Errorf("invalid anilist ID %q: %w", anilistID, err)
	}
	media, err := anilist.FetchMediaWithEndpoint(ctx, c.http, idInt, c.gqlBase)
	if err != nil {
		return "", "", fmt.Errorf("anilist fetch for anikoto resolution: %w", err)
	}

	// Build title candidates: English, UserPreferred, Romaji, then synonyms.
	var titles []string
	seen := make(map[string]bool)
	for _, t := range append([]string{
		media.Title.English,
		media.Title.UserPreferred,
		media.Title.Romaji,
	}, media.Synonyms...) {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			titles = append(titles, t)
			seen[t] = true
		}
	}
	if len(titles) == 0 {
		return "", "", fmt.Errorf("no title available for anilist ID %s", anilistID)
	}

	// Collect candidates across (up to) the first three titles — later
	// titles only extend the scoring pool, mirroring the reference flow.
	var candidates []anikotoCandidate
	seenSlugs := make(map[string]bool)
	queryTitles := titles
	if len(queryTitles) > 3 {
		queryTitles = queryTitles[:3]
	}

	// 1. Probe direct watch page for slugs derived from titles.
	for _, s := range slugCandidates(titles) {
		if seenSlugs[s] {
			continue
		}
		seenSlugs[s] = true
		if cand, ok := c.fetchCandidateFromWatchPage(ctx, s); ok {
			candidates = append(candidates, cand)
			if scoreCandidate(cand.name, titles) == 1000 {
				foundID := cand.animeID
				entry := anikotoIDEntry{animeID: foundID, slug: cand.slug, cachedAt: time.Now()}
				idCache.Store(anilistID, entry)
				return foundID, cand.slug, nil
			}
		}
	}

	// 2. Generate clean search queries.
	var queryKeywords []string
	seenKeywords := make(map[string]bool)
	for _, t := range queryTitles {
		for _, q := range searchQueryVariants(t) {
			cleanQ := cleanQueryKeyword(q)
			if cleanQ != "" && !seenKeywords[strings.ToLower(cleanQ)] {
				seenKeywords[strings.ToLower(cleanQ)] = true
				queryKeywords = append(queryKeywords, cleanQ)
			}
		}
	}

	// 3. HTML search with query keywords (terminates early on strong match).
	bestScore := 0
	var best anikotoCandidate
	for _, cand := range candidates {
		if s := scoreCandidate(cand.name, titles); s > bestScore {
			bestScore = s
			best = cand
		}
	}

	for _, kw := range queryKeywords {
		if bestScore >= 600 {
			break
		}
		hits, err := c.searchAnikotoHTML(ctx, kw)
		if err != nil {
			logging.Debug("anikoto HTML search failed", "keyword", kw, "err", err)
			continue
		}
		for _, h := range hits {
			if seenSlugs[h.slug] {
				continue
			}
			seenSlugs[h.slug] = true
			candidates = append(candidates, h)
			if s := scoreCandidate(h.name, titles); s > bestScore {
				bestScore = s
				best = h
			}
		}
	}

	// 4. AJAX search fallback if no match found.
	if bestScore <= 0 {
		for _, kw := range queryKeywords {
			if bestScore >= 600 {
				break
			}
			hits, err := c.searchAnikotoAJAX(ctx, kw)
			if err != nil {
				logging.Debug("anikoto AJAX search failed", "keyword", kw, "err", err)
				continue
			}
			for _, h := range hits {
				if seenSlugs[h.slug] {
					continue
				}
				seenSlugs[h.slug] = true
				candidates = append(candidates, h)
				if s := scoreCandidate(h.name, titles); s > bestScore {
					bestScore = s
					best = h
				}
			}
		}
	}
	if bestScore <= 0 {
		return "", "", fmt.Errorf("anikoto: no candidate matched title for anilist ID %s", anilistID)
	}
	foundID := best.animeID
	if foundID == "" {
		foundID = c.fetchAnimeIDFromWatchPage(ctx, best.slug)
	}
	entry := anikotoIDEntry{animeID: foundID, slug: best.slug, cachedAt: time.Now()}
	idCache.Store(anilistID, entry)
	return foundID, best.slug, nil
}

// scoreCandidate ranks a display name against every known title: exact
// matches win outright, then prefix, then containment. Candidates with
// mismatched season numbers score 0 to prevent cross-season false matches.
func scoreCandidate(name string, titles []string) int {
	normName := kit.CanonicalTitle(name)
	candSeason := kit.ParseSeason(name)
	best := 0
	for _, t := range titles {
		normT := kit.CanonicalTitle(t)
		if normT == "" {
			continue
		}
		tSeason := kit.ParseSeason(t)

		// If target specifies a season number > 1, candidate must match it.
		if tSeason > 1 && candSeason != tSeason {
			continue
		}
		// If candidate specifies a season number > 1, target must match it.
		if candSeason > 1 && tSeason != candSeason {
			continue
		}
		switch {
		case normName == normT:
			return 1000
		case strings.HasPrefix(normName, normT):
			if s := 600 - (len(normName) - len(normT)); s > best {
				best = s
			}
		case strings.HasPrefix(normT, normName):
			if s := 500 - (len(normT) - len(normName)); s > best {
				best = s
			}
		case strings.Contains(normName, normT):
			if best < 300 {
				best = 300
			}
		}
	}
	return best
}

// cleanQueryKeyword strips punctuation for the site search keyword.
func cleanQueryKeyword(t string) string {
	var b strings.Builder
	for _, r := range t {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == ' ' {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.TrimSpace(b.String())
}

// searchAnikotoHTML scrapes one search-results page into candidates.
func (c *Client) searchAnikotoHTML(ctx context.Context, title string) ([]anikotoCandidate, error) {
	keyword := cleanQueryKeyword(title)
	if keyword == "" {
		return nil, nil
	}
	u := c.siteBase + "/search?keyword=" + url.QueryEscape(keyword)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range anikotoHeaders(c.siteBase + "/") {
		req.Header[k] = vs
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := httpclient.ReadCapped(resp)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	var out []anikotoCandidate
	for _, m := range posterPattern(regexp.QuoteMeta(c.siteBase)).FindAllStringSubmatch(string(body), -1) {
		if len(m) < 4 {
			continue
		}
		slug := strings.SplitN(m[2], "/", 2)[0]
		if slug == "" {
			continue
		}
		out = append(out, anikotoCandidate{
			slug:    slug,
			name:    cleanTitle(m[3]),
			animeID: m[1],
		})
	}
	return out, nil
}

// searchAnikotoAJAX queries the autocomplete endpoint into candidates.
func (c *Client) searchAnikotoAJAX(ctx context.Context, title string) ([]anikotoCandidate, error) {
	keyword := cleanQueryKeyword(title)
	if keyword == "" {
		return nil, nil
	}
	u := c.siteBase + "/ajax/anime/search?keyword=" + url.QueryEscape(keyword)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	h := anikotoHeaders(c.siteBase + "/")
	h.Set("X-Requested-With", "XMLHttpRequest")
	for k, vs := range h {
		req.Header[k] = vs
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := httpclient.ReadCapped(resp)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	// The AJAX response contains {result: {html: "..."}} with rendered HTML.
	var ajaxResp struct {
		Result struct {
			HTML string `json:"html"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &ajaxResp); err != nil {
		return nil, nil
	}

	var out []anikotoCandidate
	for _, m := range ajaxPattern(regexp.QuoteMeta(c.siteBase)).FindAllStringSubmatch(ajaxResp.Result.HTML, -1) {
		if len(m) < 3 {
			continue
		}
		slug := strings.SplitN(m[1], "/", 2)[0]
		if slug == "" {
			continue
		}
		out = append(out, anikotoCandidate{slug: slug, name: cleanTitle(m[2])})
	}
	return out, nil
}

// fetchAnimeIDFromWatchPage fetches the anikoto watch page for the given slug
// and extracts the data-id attribute which is the internal animeID.
func (c *Client) fetchAnimeIDFromWatchPage(ctx context.Context, slug string) string {
	u := c.siteBase + "/watch/" + slug
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	for k, vs := range anikotoHeaders(c.siteBase + "/") {
		req.Header[k] = vs
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ""
	}
	body, err := httpclient.ReadCapped(resp)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	// The canonical player node carries the anime ID; fall back to the
	// first data-id on the page.
	if m := reWatchMainID.FindSubmatch(body); len(m) >= 2 {
		return string(m[1])
	}
	if m := reDataID.FindSubmatch(body); len(m) >= 2 {
		return string(m[1])
	}
	return ""
}

// fetchCandidateFromWatchPage fetches the anikoto watch page for the given slug,
// returning the candidate with its internal animeID and display title if valid.
func (c *Client) fetchCandidateFromWatchPage(ctx context.Context, slug string) (anikotoCandidate, bool) {
	u := c.siteBase + "/watch/" + slug
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return anikotoCandidate{}, false
	}
	for k, vs := range anikotoHeaders(c.siteBase + "/") {
		req.Header[k] = vs
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return anikotoCandidate{}, false
	}
	body, err := httpclient.ReadCapped(resp)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return anikotoCandidate{}, false
	}

	var animeID string
	if m := reWatchMainID.FindSubmatch(body); len(m) >= 2 {
		animeID = string(m[1])
	} else if m := reDataID.FindSubmatch(body); len(m) >= 2 {
		animeID = string(m[1])
	}
	if animeID == "" {
		return anikotoCandidate{}, false
	}

	title := slug
	if m := reHeadingTitle.FindSubmatch(body); len(m) >= 2 {
		title = cleanTitle(string(m[1]))
	} else if m := rePageTitle.FindSubmatch(body); len(m) >= 2 {
		title = cleanTitle(string(m[1]))
	}

	return anikotoCandidate{
		slug:    slug,
		name:    title,
		animeID: animeID,
	}, true
}

// slugCandidates generates potential anikoto URL slugs from media titles.
func slugCandidates(titles []string) []string {
	var slugs []string
	seen := make(map[string]bool)

	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		slugs = append(slugs, s)
	}

	for _, t := range titles {
		baseSlug := slugify(t)
		add(baseSlug)

		if strings.Contains(baseSlug, "-2nd-season") {
			add(strings.ReplaceAll(baseSlug, "-2nd-season", "-season-2"))
		}
		if strings.Contains(baseSlug, "-season-2") {
			add(strings.ReplaceAll(baseSlug, "-season-2", "-2nd-season"))
		}
		if strings.Contains(baseSlug, "-3rd-season") {
			add(strings.ReplaceAll(baseSlug, "-3rd-season", "-season-3"))
		}
		if strings.Contains(baseSlug, "-season-3") {
			add(strings.ReplaceAll(baseSlug, "-season-3", "-3rd-season"))
		}
		if strings.Contains(baseSlug, "-4th-season") {
			add(strings.ReplaceAll(baseSlug, "-4th-season", "-season-4"))
		}
		if strings.Contains(baseSlug, "-season-4") {
			add(strings.ReplaceAll(baseSlug, "-season-4", "-4th-season"))
		}
	}
	return slugs
}

func slugify(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}


// searchQueryVariants returns search query variations for an anime title.
// Sequences with "Season 2" or "2nd Season" generate variants like "Title 2"
// and "Title" to avoid keyword noise from the word "Season".
func searchQueryVariants(title string) []string {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	var variants []string
	seen := make(map[string]bool)

	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			variants = append(variants, s)
		}
	}

	add(title)

	if season := kit.ParseSeason(title); season > 1 {
		base := kit.StripSeason(title)
		if base != "" {
			add(fmt.Sprintf("%s %d", base, season))
			add(base)
		}
	}

	return variants
}

// cleanTitle strips tags, unescapes entities, and collapses whitespace.
func cleanTitle(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
}

var (
	// reEpBlock matches one episode anchor: open tag with data-ids plus
	// inner HTML carrying the display title.
	reEpBlock   = regexp.MustCompile(`(?s)<a\s([^>]*data-ids="[^"]*"[^>]*)>(.*?)</a>`)
	reEpNum     = regexp.MustCompile(`data-num="([^"]+)"`)
	reEpSub     = regexp.MustCompile(`data-sub="([^"]*)"`)
	reEpDub     = regexp.MustCompile(`data-dub="([^"]*)"`)
	reEpDataIDs = regexp.MustCompile(`data-ids="([^"]+)"`)
	reLeadNum   = regexp.MustCompile(`^\d+\s*`)
)

// fetchEpisodesDirect fetches the episode list directly from anikoto's AJAX
// endpoint, returning episodes whose IDs embed the data-ids attribute needed
// for server resolution (see ResolveSource).
func (c *Client) fetchEpisodesDirect(ctx context.Context, animeID, anilistID string, season int) ([]provider.Episode, error) {
	if season <= 0 {
		season = 1
	}
	watchRef := c.siteBase + "/watch/"
	u := c.siteBase + "/ajax/episode/list/" + animeID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("anikoto direct episodes: build request: %w", err)
	}
	h := anikotoHeaders(watchRef)
	h.Set("X-Requested-With", "XMLHttpRequest")
	for k, vs := range h {
		req.Header[k] = vs
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anikoto direct episodes: %w", err)
	}
	body, err := httpclient.ReadCapped(resp)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("anikoto direct episodes: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	// The endpoint returns JSON with a "result" key containing HTML.
	var epListResp struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(body, &epListResp); err != nil {
		return nil, fmt.Errorf("anikoto direct episodes: decode JSON: %w", err)
	}
	htmlStr := epListResp.Result

	blocks := reEpBlock.FindAllStringSubmatch(htmlStr, -1)
	eps := make([]provider.Episode, 0, len(blocks)*2)

	for _, block := range blocks {
		if len(block) < 3 {
			continue
		}
		attrs := block[1]

		numMatch := reEpNum.FindStringSubmatch(attrs)
		dataIDsMatch := reEpDataIDs.FindStringSubmatch(attrs)
		if len(numMatch) < 2 || len(dataIDsMatch) < 2 {
			continue
		}
		numF, err := strconv.ParseFloat(numMatch[1], 64)
		if err != nil || numF <= 0 {
			continue
		}
		numInt := int(numF)
		if float64(numInt) != numF {
			continue // skip fractional episodes
		}
		dataIDs := dataIDsMatch[1]

		// Display title from the anchor body ("1 I'm used to it"),
		// dropping the leading episode number.
		epTitle := reLeadNum.ReplaceAllString(cleanTitle(block[2]), "")
		if epTitle == "" {
			epTitle = fmt.Sprintf("Episode %d", numInt)
		}

		hasSub := false
		if m := reEpSub.FindStringSubmatch(attrs); len(m) >= 2 {
			hasSub = m[1] == "1"
		}
		hasDub := false
		if m := reEpDub.FindStringSubmatch(attrs); len(m) >= 2 {
			hasDub = m[1] == "1"
		}

		// Embed dataIDs into episode ID via pipe separator so ResolveSource
		// can use them without re-fetching the episode list.
		if hasSub {
			eps = append(eps, provider.Episode{
				Title:   epTitle,
				ID:      fmt.Sprintf("watch/anikoto/%s/sub/%d|%s", anilistID, numInt, dataIDs),
				Episode: numInt,
				Season:  season,
				Audio:   "sub",
			})
		}
		if hasDub {
			eps = append(eps, provider.Episode{
				Title:   epTitle,
				ID:      fmt.Sprintf("watch/anikoto/%s/dub/%d|%s", anilistID, numInt, dataIDs),
				Episode: numInt,
				Season:  season,
				Audio:   "dub",
			})
		}
	}

	if len(eps) == 0 {
		return nil, provider.ErrNoEpisodes
	}

	return eps, nil
}

var (
	// reServerBlockHead matches one audio block header (sub/dub). Items are
	// attributed by div-depth scope rather than by cutting the block at the
	// first closing div, so nested markup inside a block — or rows outside
	// every block — cannot misassign servers.
	reServerBlockHead = regexp.MustCompile(`<div[^>]+class="type"[^>]+data-type="([^"]+)"[^>]*>`)
	reServerItem      = regexp.MustCompile(`<li[^>]+data-sv-id="([^"]+)"[^>]+data-link-id="([^"]+)"[^>]*>([^<]+)</li>`)
	reDivOpen         = regexp.MustCompile(`<div[\s>]`)
	reDivClose        = regexp.MustCompile(`</div>`)
)

// serverEntry is one playable server row from the server list.
type serverEntry struct {
	svID   string
	linkID string
	name   string
}

// parseServerList attributes every server row to its enclosing audio block
// and returns the rows for category. Rows outside any block are ignored.
func parseServerList(serverListHTML, category string) []serverEntry {
	catLower := strings.ToLower(strings.TrimSpace(category))
	type scope struct {
		category string
		start    int
		end      int
	}
	var scopes []scope
	for _, m := range reServerBlockHead.FindAllStringSubmatchIndex(serverListHTML, -1) {
		if len(m) < 4 {
			continue
		}
		scopes = append(scopes, scope{
			category: strings.ToLower(serverListHTML[m[2]:m[3]]),
			start:    m[0],
			end:      blockEnd(serverListHTML, m[1]),
		})
	}
	var servers []serverEntry
	for _, m := range reServerItem.FindAllStringSubmatchIndex(serverListHTML, -1) {
		if len(m) < 8 {
			continue
		}
		inCategory := false
		for _, s := range scopes {
			if s.start < m[0] && m[0] < s.end {
				inCategory = s.category == catLower
				break
			}
		}
		if !inCategory {
			continue
		}
		servers = append(servers, serverEntry{
			svID:   serverListHTML[m[2]:m[3]],
			linkID: serverListHTML[m[4]:m[5]],
			name:   strings.TrimSpace(serverListHTML[m[6]:m[7]]),
		})
	}
	return servers
}

// blockEnd returns the offset just past the closing div matching the open
// div that ends at openEnd, or the end of input when unbalanced.
func blockEnd(htmlStr string, openEnd int) int {
	type tag struct {
		offset int
		delta  int
	}
	var tags []tag
	for _, m := range reDivOpen.FindAllStringIndex(htmlStr[openEnd:], -1) {
		tags = append(tags, tag{offset: openEnd + m[0], delta: 1})
	}
	for _, m := range reDivClose.FindAllStringIndex(htmlStr[openEnd:], -1) {
		tags = append(tags, tag{offset: openEnd + m[0], delta: -1})
	}
	slices.SortFunc(tags, func(a, b tag) int { return a.offset - b.offset })
	depth := 1
	for _, t := range tags {
		depth += t.delta
		if depth == 0 {
			return t.offset + len("</div>")
		}
	}
	return len(htmlStr)
}

// resolveServerStream resolves a single server link ID to a signed HLS stream.
// serverName is used for quality tagging.
func (c *Client) resolveServerStream(ctx context.Context, linkID, watchReferer, serverName string) ([]provider.MediaSource, []provider.SubtitleOption, error) {
	// Step 1: Get the embed URL for this server link.
	serverURL := c.siteBase + "/ajax/server?get=" + linkID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL, nil)
	if err != nil {
		return nil, nil, err
	}
	sh := anikotoHeaders(watchReferer)
	sh.Set("X-Requested-With", "XMLHttpRequest")
	for k, vs := range sh {
		req.Header[k] = vs
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, err := httpclient.ReadCapped(resp)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("server get: status %d", resp.StatusCode)
	}

	var serverResp struct {
		Result struct {
			URL string `json:"url"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &serverResp); err != nil {
		return nil, nil, fmt.Errorf("server get: decode: %w", err)
	}
	embedURL := strings.TrimSpace(serverResp.Result.URL)
	if embedURL == "" {
		return nil, nil, fmt.Errorf("server get: empty embed URL")
	}

	// Step 2: Fetch the embed page to extract the file ID.
	embedReq, err := http.NewRequestWithContext(ctx, http.MethodGet, embedURL, nil)
	if err != nil {
		return nil, nil, err
	}
	embedReq.Header.Set("User-Agent", config.DesktopUserAgent)
	embedReq.Header.Set("Referer", watchReferer)

	embedResp, err := c.http.Do(embedReq)
	if err != nil {
		return nil, nil, err
	}
	embedBody, err := httpclient.ReadCapped(embedResp)
	_ = embedResp.Body.Close()
	if err != nil || embedResp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("embed fetch: status %d", embedResp.StatusCode)
	}

	// Extract file ID from embed HTML: look for data-id="<digits>" first,
	// then fall back to "File <digits> -" pattern.
	fileID := ""
	if m := reDataID.FindSubmatch(embedBody); len(m) >= 2 {
		fileID = string(m[1])
	}
	if fileID == "" {
		reFile := regexp.MustCompile(`File\s+(\d+)\s+-`)
		if m := reFile.FindSubmatch(embedBody); len(m) >= 2 {
			fileID = string(m[1])
		}
	}
	if fileID == "" {
		return nil, nil, fmt.Errorf("embed page: no file ID found")
	}

	// Step 3: Call getSources to get the encrypted stream token.
	parsedEmbed, err := url.Parse(embedURL)
	if err != nil {
		return nil, nil, err
	}
	getSourcesURL := fmt.Sprintf("%s://%s/stream/getSources?id=%s", parsedEmbed.Scheme, parsedEmbed.Host, fileID)

	srcReq, err := http.NewRequestWithContext(ctx, http.MethodGet, getSourcesURL, nil)
	if err != nil {
		return nil, nil, err
	}
	srcReq.Header.Set("User-Agent", config.DesktopUserAgent)
	srcReq.Header.Set("Referer", embedURL)
	srcReq.Header.Set("X-Requested-With", "XMLHttpRequest")

	srcResp, err := c.http.Do(srcReq)
	if err != nil {
		return nil, nil, err
	}
	srcBody, err := httpclient.ReadCapped(srcResp)
	_ = srcResp.Body.Close()
	if err != nil || srcResp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("getSources: status %d", srcResp.StatusCode)
	}

	var sourcesResp kit.MegaplaySourcesResp
	if err := json.Unmarshal(srcBody, &sourcesResp); err != nil {
		return nil, nil, fmt.Errorf("getSources decode: %w", err)
	}

	// Step 4: Decrypt the AES-256-CBC token and sign the CDN URL.
	rawM3U8, err := kit.DecryptMegaplayEnc(sourcesResp.Enc)
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt megaplay enc: %w", err)
	}
	signedM3U8 := kit.SignCDNURL(rawM3U8, 86400)

	// Step 5: Build subtitle options from tracks (English and "und" only).
	var subs []provider.SubtitleOption
	for _, track := range sourcesResp.Tracks {
		if strings.EqualFold(strings.TrimSpace(track.Kind), "thumbnails") {
			continue
		}
		lang := track.Label
		labelLower := strings.ToLower(track.Label)
		if strings.Contains(labelLower, "eng") || strings.Contains(labelLower, "english") {
			lang = "en"
		}
		subs = append(subs, provider.SubtitleOption{
			URL:      track.File,
			Language: lang,
			Default:  track.Default,
		})
	}

	embedScheme := parsedEmbed.Scheme
	embedHost := parsedEmbed.Host
	streamReferer := fmt.Sprintf("%s://%s/", embedScheme, embedHost)

	source := provider.MediaSource{
		URL:       signedM3U8,
		Quality:   kit.TagQuality("1080p", serverName, "anikoto"),
		Referer:   streamReferer,
		UserAgent: config.DesktopUserAgent,
		Type:      provider.SourceTypeHLS,
		Subtitles: subs,
	}
	return []provider.MediaSource{source}, subs, nil
}

// resolveDirectStreams resolves a full stream for the given anilist ID, audio
// category (sub/dub), and episode number by scraping anikoto directly. When
// dataIDs (from a FetchEpisodes episode ID suffix) is non-empty, the episode
// re-fetch is skipped and the server list is queried straight away.
func (c *Client) resolveDirectStreams(ctx context.Context, anilistID, category string, epNum int, dataIDs string) ([]provider.MediaSource, error) {
	animeID, slug, err := c.resolveAnilistToAnikoto(ctx, anilistID)
	if err != nil {
		return nil, fmt.Errorf("anikoto direct: resolve slug: %w", err)
	}
	if slug == "" {
		return nil, fmt.Errorf("anikoto direct: no slug for anilist ID %s", anilistID)
	}

	if dataIDs == "" {
		if animeID == "" {
			return nil, fmt.Errorf("anikoto direct: no animeID for anilist ID %s", anilistID)
		}

		episodes, err := c.fetchEpisodesDirect(ctx, animeID, anilistID, 1)
		if err != nil {
			return nil, fmt.Errorf("anikoto direct: fetch episodes: %w", err)
		}

		// Find the matching episode by number and audio category.
		var matchedEp *provider.Episode
		for i := range episodes {
			ep := &episodes[i]
			if ep.Episode == epNum && strings.EqualFold(ep.Audio, category) {
				matchedEp = ep
				break
			}
		}
		// Fallback: any audio for this episode number.
		if matchedEp == nil {
			for i := range episodes {
				ep := &episodes[i]
				if ep.Episode == epNum {
					matchedEp = ep
					break
				}
			}
		}
		if matchedEp == nil {
			return nil, fmt.Errorf("anikoto direct: episode %d (%s) not found", epNum, category)
		}

		// Extract dataIDs from the episode ID (after the pipe separator).
		epIDParts := strings.SplitN(matchedEp.ID, "|", 2)
		if len(epIDParts) != 2 || epIDParts[1] == "" {
			return nil, fmt.Errorf("anikoto direct: no data-ids in episode ID")
		}
		dataIDs = epIDParts[1]
	}

	return c.resolveFromDataIDs(ctx, dataIDs, slug, category)
}

// resolveFromDataIDs fetches the server list for one episode's data-ids
// and returns the first working server's streams.
func (c *Client) resolveFromDataIDs(ctx context.Context, dataIDs, slug, category string) ([]provider.MediaSource, error) {
	watchRef := c.siteBase + "/watch/" + slug

	// Fetch server list for this episode's data-ids.
	serverListURL := c.siteBase + "/ajax/server/list?servers=" + url.QueryEscape(dataIDs)
	slReq, err := http.NewRequestWithContext(ctx, http.MethodGet, serverListURL, nil)
	if err != nil {
		return nil, err
	}
	slHeaders := anikotoHeaders(watchRef)
	slHeaders.Set("X-Requested-With", "XMLHttpRequest")
	for k, vs := range slHeaders {
		slReq.Header[k] = vs
	}

	slResp, err := c.http.Do(slReq)
	if err != nil {
		return nil, fmt.Errorf("anikoto direct: server list: %w", err)
	}
	slBody, err := httpclient.ReadCapped(slResp)
	_ = slResp.Body.Close()
	if err != nil || slResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anikoto direct: server list: status %d", slResp.StatusCode)
	}

	var slJSON struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(slBody, &slJSON); err != nil {
		return nil, fmt.Errorf("anikoto direct: server list decode: %w", err)
	}
	serverListHTML := slJSON.Result

	// Parse server rows for the requested audio only. Never fall back to
	// the opposite track: silently playing sub audio for a dub request
	// (or vice versa) is worse than a clear miss the service can retry
	// against the next provider.
	servers := parseServerList(serverListHTML, category)
	if len(servers) == 0 {
		// Verified absence (list parsed, track missing), not a transport
		// failure: retrying cannot help, and callers may hide the entry.
		return nil, fmt.Errorf("anikoto direct: no %s servers: %w", strings.ToLower(category), provider.ErrAudioUnavailable)
	}

	// Sort: prefer Vidstream servers first (most reliable megaplay backend).
	sortedServers := make([]serverEntry, 0, len(servers))
	var rest []serverEntry
	for _, sv := range servers {
		if strings.Contains(strings.ToLower(sv.name), "vidstream") {
			sortedServers = append(sortedServers, sv)
		} else {
			rest = append(rest, sv)
		}
	}
	sortedServers = append(sortedServers, rest...)

	for _, sv := range sortedServers {
		sources, _, svErr := c.resolveServerStream(ctx, sv.linkID, watchRef, sv.name)
		if svErr != nil {
			logging.Debug("anikoto direct: server failed", "server", sv.name, "err", svErr)
			continue
		}
		if len(sources) > 0 {
			return sources, nil
		}
	}

	return nil, provider.ErrNoResults
}
