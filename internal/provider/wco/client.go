package wco

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	htmlpkg "html"
	"io"
	"net/http"
	"net/http/cookiejar"

	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
)

const (
	maxErrorExamples       = 3
	cookieBootstrapTimeout = 4 * time.Second
)

var (
	anchorTagRe     = regexp.MustCompile(`(?is)<a\s+([^>]*)>(.*?)</a>`)
	hrefAttrRe      = regexp.MustCompile(`(?is)\bhref\s*=\s*"([^"]+)"`)
	classAttrRe     = regexp.MustCompile(`(?is)\bclass\s*=\s*"([^"]*)"`)
	stripTagsRe     = regexp.MustCompile(`(?s)<[^>]+>`)
	episodeNumRe    = regexp.MustCompile(`(?i)\b(?:episode|ep)\.?\s*(\d{1,4})\b`)
	urlEpisodeRe    = regexp.MustCompile(`(?i)(?:^|[-_/])(?:episode|ep)[-_/]?(\d{1,4})(?:$|[-_/])`)
	seasonNumRe     = regexp.MustCompile(`(?i)\bseason\s*(\d{1,3})\b`)
	urlSeasonRe     = regexp.MustCompile(`(?i)(?:^|[-_/])season[-_/]?(\d{1,3})(?:$|[-_/])`)
	compactSpaceRe  = regexp.MustCompile(`\s+`)
	episodeLinkRe   = regexp.MustCompile(fmt.Sprintf(`(?is)<a\s+href="(%s/[^"]+)"[^>]*>(.*?)</a>`, regexp.QuoteMeta(config.WCOBaseURL)))
	itemsUlRe       = regexp.MustCompile(`(?is)<ul\s+class="items"\s*>(.*?)</ul>`)
	searchResultRe  = regexp.MustCompile(`(?is)<a\s+href="([^"]+)"[^>]*>(.*?)</a>`)
	animeListLinkRe = regexp.MustCompile(`(?i)href="([^"]*/anime/[^"]+)"`)
)

type Client struct {
	httpClient *http.Client
	cookieJar  http.CookieJar
}

func NewClient(cookieHeader string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	hc := httpclient.NewWithUserAgent(config.AndroidUA())
	hc.Jar = jar
	c := &Client{
		httpClient: hc,
		cookieJar:  jar,
	}

	if strings.TrimSpace(cookieHeader) != "" {
		c.setCookieHeader(cookieHeader)
	}
	go c.bootstrapCookiesWithCurl(cookieBootstrapTimeout)
	return c, nil
}

func (c *Client) Name() string {
	return "wco"
}

func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeCartoon, Priority: 1},
	}
}

func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debugf("wco search start query=%q mode=%q", query, mode)
	if strings.TrimSpace(query) == "" {
		logging.Warnf("wco search rejected empty query")
		return nil, fmt.Errorf("empty query")
	}

	// Try multiple query variants for better results
	attempts := c.suggestQueryVariants(query)
	if !containsStr(attempts, query) {
		attempts = append([]string{query}, attempts...)
	}

	var mu sync.Mutex
	allSeen := make(map[string]provider.SearchResult)
	bestQuery := query

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	g, gCtx := errgroup.WithContext(raceCtx)
	for _, q := range attempts {
		q := q
		g.Go(func() error {
			logging.Debugf("wco searching variant q=%q", q)
			html, err := c.postSearchSeries(gCtx, q)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				logging.Debugf("wco search variant failed q=%q err=%v", q, err)
				return nil
			}
			results := c.parseSearchResults(html)
			if len(results) > 0 {
				mu.Lock()
				for _, r := range results {
					allSeen[r.ID] = r
				}
				if len(results) >= 3 {
					bestQuery = q
					cancel()
				}
				mu.Unlock()
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		logging.Warnf("wco search workers failed query=%q err=%v", query, err)
		return nil, fmt.Errorf("wco search workers: %w", err)
	}

	// Mode-based filtering/prioritization
	var filtered []provider.SearchResult
	for _, r := range allSeen {
		if mode == provider.ModeMovies && !strings.Contains(r.ID, "/anime/") {
			filtered = append(filtered, r)
		} else if (mode == provider.ModeAnime || mode == provider.ModeCartoon) && strings.Contains(r.ID, "/anime/") {
			filtered = append(filtered, r)
		} else if mode == "" {
			filtered = append(filtered, r)
		}
	}

	if len(filtered) == 0 {
		// Fallback to index lists
		logging.Debugf("wco no direct results, trying index candidates query=%q", query)
		indexCandidates := c.fetchIndexCandidates(ctx)
		if len(indexCandidates) > 0 {
			ranked := c.rankSeriesByQuery(indexCandidates, query)

			// Filter index candidates by mode
			var modeFiltered []provider.SearchResult
			for _, r := range ranked {
				if mode == provider.ModeMovies {
					continue
				}
				modeFiltered = append(modeFiltered, r)
			}
			logging.Debugf("wco index candidates found count=%d", len(modeFiltered))
			return modeFiltered, nil
		}
		if len(filtered) == 0 && len(allSeen) > 0 {
			results := make([]provider.SearchResult, 0, len(allSeen))
			for _, r := range allSeen {
				results = append(results, r)
			}
			return c.rankSeriesByQuery(results, bestQuery), nil
		}
	} else {
		results := filtered
		return c.rankSeriesByQuery(results, bestQuery), nil
	}

	logging.Warnf("wco search no results found query=%q", query)
	return nil, provider.ErrNoResults
}

func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	logging.Debugf("wco FetchEpisodes start mediaID=%q", mediaID)
	html, err := c.fetchSeriesPage(ctx, mediaID)
	if err != nil {
		logging.Errorf("wco FetchEpisodes fetchSeriesPage failed mediaID=%q err=%v", mediaID, err)
		return nil, err
	}

	var episodes []provider.Episode
	modernLinks := c.extractModernEpisodeLinks(html)
	if len(modernLinks) > 0 {
		episodes = buildEpisodesFromLinks(modernLinks, true)
	} else {
		start := strings.Index(html, `id="episodeList"`)
		searchBlock := html
		if start >= 0 {
			searchBlock = html[start:]
		}
		matches := episodeLinkRe.FindAllStringSubmatch(searchBlock, -1)
		links := make([]episodeLink, 0, len(matches))
		for _, m := range matches {
			url := htmlpkg.UnescapeString(strings.TrimSpace(m[1]))
			title := normalizeEpisodeTitle(m[2])
			if strings.Contains(url, "/anime/") || parseEpisodeNumber(title, url) <= 0 {
				continue
			}
			links = append(links, episodeLink{
				URL:        url,
				Title:      title,
				SeasonHint: seasonHintBefore(searchBlock, m[0]),
			})
		}
		episodes = buildEpisodesFromLinks(links, false)
	}
	if len(episodes) == 0 {
		logging.Warnf("wco FetchEpisodes found no episodes mediaID=%q", mediaID)
		return nil, provider.ErrNoEpisodes
	}

	sort.Slice(episodes, func(i, j int) bool {
		// Sort non-numbered episodes to the end
		if episodes[i].Episode <= 0 && episodes[j].Episode > 0 {
			return false
		}
		if episodes[i].Episode > 0 && episodes[j].Episode <= 0 {
			return true
		}
		// Sort by season
		if episodes[i].Season != episodes[j].Season {
			return episodes[i].Season < episodes[j].Season
		}
		// Sort by episode number
		if episodes[i].Episode != episodes[j].Episode {
			return episodes[i].Episode < episodes[j].Episode
		}
		// Sort by title
		return strings.ToLower(episodes[i].Title) < strings.ToLower(episodes[j].Title)
	})

	logging.Debugf("wco FetchEpisodes success mediaID=%q episodes=%d", mediaID, len(episodes))
	return episodes, nil
}

func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debugf("wco ResolveSource start: mediaID=%q episode=%v", mediaID, episode)
	episodeURL := episode.ID
	logging.Debugf("wco ResolveSource: fetching episode page %q", episodeURL)
	html, err := c.fetchEpisodePage(ctx, episodeURL)
	if err != nil {
		logging.Errorf("wco ResolveSource: fetchEpisodePage failed: %v", err)
		return nil, err
	}
	logging.Debugf("wco ResolveSource: decoding obfuscated iframe")
	decoded := c.decodeObfuscatedIframeHTML(html)
	if decoded == "" {
		logging.Debugf("wco ResolveSource: decodeObfuscatedIframeHTML returned empty, using raw html")
		decoded = html
	}
	logging.Debugf("wco ResolveSource: extracting iframe src")
	iframeURL := c.extractIframeSrcFromHTML(decoded)
	if iframeURL == "" {
		logging.Errorf("wco ResolveSource: could not locate embed iframe URL")
		return nil, fmt.Errorf("could not locate embed iframe URL")
	}
	logging.Debugf("wco ResolveSource: fetching embed page %q", iframeURL)
	embedHTML, err := c.fetchEmbedPage(ctx, iframeURL)
	if err != nil {
		logging.Errorf("wco ResolveSource: fetchEmbedPage failed: %v", err)
		return nil, err
	}
	var directSources []provider.MediaSource
	for _, directURL := range extractDirectMediaURLs(decoded + "\n" + embedHTML) {
		referer := iframeURL
		if mediaSourceType(directURL) == "hls" {
			directSources = appendUniqueMediaSource(directSources, wcoMediaSource(directURL, referer, c.CookieHeader(), "HLS"))
		}
	}
	for _, directURL := range extractDirectMediaURLs(decoded + "\n" + embedHTML) {
		if mediaSourceType(directURL) != "hls" {
			directSources = appendUniqueMediaSource(directSources, wcoMediaSource(directURL, iframeURL, c.CookieHeader(), "HD"))
		}
	}

	logging.Debugf("wco ResolveSource: finding getvidlink URL")
	getvidlinkURL := c.findGetvidlinkURL(embedHTML, iframeURL)
	if getvidlinkURL == "" {
		logging.Errorf("wco ResolveSource: could not derive getvidlink URL")
		if len(directSources) > 0 {
			return directSources, nil
		}
		return nil, fmt.Errorf("could not derive getvidlink URL")
	}
	logging.Debugf("wco ResolveSource: calling getvidlink %q", getvidlinkURL)
	payload, err := c.callGetvidlink(ctx, getvidlinkURL, iframeURL)
	if err != nil {
		logging.Errorf("wco ResolveSource: callGetvidlink failed: %v", err)
		if len(directSources) > 0 {
			return directSources, nil
		}
		return nil, err
	}
	logging.Debugf("wco ResolveSource: parsed payload: %v", payload)
	server, ok := payload["server"].(string)
	if !ok {
		logging.Warnf("wco ResolveSource: payload server field is not a string: %T", payload["server"])
	}
	if server == "" {
		server, ok = payload["cdn"].(string)
		if !ok {
			logging.Warnf("wco ResolveSource: payload cdn field is not a string: %T", payload["cdn"])
		}
	}

	if server == "" {
		logging.Errorf("wco ResolveSource: missing server in getvidlink response")
		if len(directSources) > 0 {
			return directSources, nil
		}
		return nil, fmt.Errorf("missing server in getvidlink response")
	}

	// Resolve all available quality tokens
	sources := directSources
	qualityKeys := []string{"fhd", "hd", "sd", "enc"}
	// Also look for numeric quality keys like "1080", "720"
	for k := range payload {
		if strings.Contains(k, "1080") || strings.Contains(k, "720") || strings.Contains(k, "480") {
			found := false
			for _, qk := range qualityKeys {
				if qk == k {
					found = true
					break
				}
			}
			if !found {
				qualityKeys = append(qualityKeys, k)
			}
		}
	}

	for _, key := range qualityKeys {
		token, ok := payload[key].(string)
		if !ok || token == "" {
			continue
		}

		logging.Debugf("wco ResolveSource: resolving quality %q with token %q", key, token)
		mediaURL, err := c.resolveFinalMediaURL(ctx, server, token)
		if err != nil {
			logging.Warnf("wco ResolveSource: resolveFinalMediaURL failed for %q: %v", key, err)
			continue
		}

		label := strings.ToUpper(key)
		if key == "enc" {
			label = "HD" // Default
		}

		finalReferer := "https://www.wcostream.com/"
		sources = appendUniqueMediaSource(sources, wcoMediaSource(mediaURL, finalReferer, c.CookieHeader(), label))
	}

	if len(sources) > 0 {
		return sources, nil
	}

	return nil, fmt.Errorf("no media sources found")
}

func (c *Client) suggestQueryVariants(query string) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	variants := []string{q}

	// 1. Remove punctuation
	noPunct := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			return r
		}
		return ' '
	}, q)
	noPunct = strings.Join(strings.Fields(noPunct), " ")
	if noPunct != "" && noPunct != q {
		variants = append(variants, noPunct)
	}

	// 2. Squash repeated characters (e.g., "shinn chan" -> "shin chan")
	tokens := strings.Fields(q)
	if len(tokens) > 0 {
		squashed := make([]string, 0, len(tokens))
		for _, t := range tokens {
			var sb strings.Builder
			for i, r := range t {
				if i > 0 && r == rune(t[i-1]) {
					continue
				}
				sb.WriteRune(r)
			}
			squashed = append(squashed, sb.String())
		}
		squashedQuery := strings.Join(squashed, " ")
		if squashedQuery != q {
			variants = append(variants, squashedQuery)
		}

		// 3. Transposed characters (basic)
		transposed := make([]string, 0, len(tokens))
		for _, t := range tokens {
			if len(t) < 4 {
				transposed = append(transposed, t)
				continue
			}
			best := t
			bestScore := 0.0
			runes := []rune(t)
			for i := 0; i < len(runes)-1; i++ {
				swapped := make([]rune, len(runes))
				copy(swapped, runes)
				swapped[i], swapped[i+1] = swapped[i+1], swapped[i]
				cand := string(swapped)
				// Simple similarity check
				score := 0.0
				if strings.Contains(t, cand) || strings.Contains(cand, t) {
					score = 1.0
				}
				if score > bestScore {
					bestScore = score
					best = cand
				}
			}
			transposed = append(transposed, best)
		}
		transposedQuery := strings.Join(transposed, " ")
		if transposedQuery != q {
			variants = append(variants, transposedQuery)
		}

		// 4. Concatenated tokens
		concatenated := strings.Join(tokens, "")
		if concatenated != q {
			variants = append(variants, concatenated)
		}
	}

	// Deduplicate
	seen := make(map[string]bool)
	unique := make([]string, 0, len(variants))
	for _, v := range variants {
		if !seen[v] {
			seen[v] = true
			unique = append(unique, v)
		}
	}
	return unique
}

func (c *Client) parseSearchResults(html string) []provider.SearchResult {
	// Simplified HTML parsing using regexp (similar to Python)
	// <ul class="items">...</ul>
	match := itemsUlRe.FindStringSubmatch(html)
	if len(match) < 2 {
		return nil
	}
	block := match[1]

	// <a href="...">Title</a>
	matches := searchResultRe.FindAllStringSubmatch(block, -1)

	results := make([]provider.SearchResult, 0)
	for _, m := range matches {
		href := htmlpkg.UnescapeString(strings.TrimSpace(m[1]))
		title := normalizeEpisodeTitle(m[2])

		fullURL := href
		if strings.HasPrefix(href, "/") {
			fullURL = config.BaseURL + href
		}

		if !strings.Contains(fullURL, "/anime/") {
			continue
		}

		results = append(results, provider.SearchResult{
			Title:     title,
			ID:        fullURL,
			Type:      provider.ModeCartoon,
			MediaType: "cartoon",
		})
	}
	return results
}

func (c *Client) fetchIndexCandidates(ctx context.Context) []provider.SearchResult {
	pages := []struct {
		url  string
		mode provider.ContentType
	}{
		{config.BaseURL + "/dubbed-anime-list", provider.ModeCartoon},
		{config.BaseURL + "/subbed-anime-list", provider.ModeCartoon},
		{config.BaseURL + "/cartoon-list", provider.ModeCartoon},
	}
	allItems := make(map[string]provider.SearchResult)
	for _, p := range pages {
		p := p
		func() {
			resp, err := c.doRequest(ctx, http.MethodGet, p.url, map[string]string{"Referer": config.BaseURL + "/"}, nil)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)

			matches := animeListLinkRe.FindAllStringSubmatch(string(raw), -1)
			for _, m := range matches {
				href := m[1]
				fullURL := href
				if strings.HasPrefix(href, "/") {
					fullURL = config.BaseURL + href
				}

				parts := strings.Split(fullURL, "/")
				slug := parts[len(parts)-1]
				title := strings.ReplaceAll(slug, "-", " ")
				title = strings.ToUpper(title[:1]) + title[1:]

				allItems[fullURL] = provider.SearchResult{
					Title:     title,
					ID:        fullURL,
					Type:      p.mode,
					MediaType: "cartoon",
				}
			}
		}()
	}

	res := make([]provider.SearchResult, 0, len(allItems))
	for _, r := range allItems {
		res = append(res, r)
	}
	return res
}

func (c *Client) rankSeriesByQuery(results []provider.SearchResult, query string) []provider.SearchResult {
	// Simple ranking based on title similarity
	type scored struct {
		r     provider.SearchResult
		score float64
	}

	scoredResults := make([]scored, 0, len(results))
	q := strings.ToLower(query)
	for _, r := range results {
		s := 0.0
		t := strings.ToLower(r.Title)
		if t == q {
			s += 1.0
		} else if strings.HasPrefix(t, q) {
			s += 0.8
		} else if strings.Contains(t, q) {
			s += 0.5
		}
		scoredResults = append(scoredResults, scored{r, s})
	}

	sort.Slice(scoredResults, func(i, j int) bool {
		return scoredResults[i].score > scoredResults[j].score
	})

	final := make([]provider.SearchResult, 0, len(results))
	for _, sr := range scoredResults {
		final = append(final, sr.r)
	}
	return final
}

func (c *Client) CookieHeader() string {
	u, _ := url.Parse(config.BaseURL)
	parts := make([]string, 0)
	for _, ck := range c.cookieJar.Cookies(u) {
		parts = append(parts, ck.Name+"="+ck.Value)
	}
	return strings.Join(parts, "; ")
}

var _ provider.Provider = (*Client)(nil)

func (c *Client) doRequest(ctx context.Context, method, target string, headers map[string]string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	logging.Debugf("wco request %s target=%q", method, target)
	return c.httpClient.Do(req)
}

func (c *Client) postSearchSeries(ctx context.Context, query string) (string, error) {
	logging.Debugf("wco postSearchSeries query=%q", query)
	form := url.Values{}
	form.Set("catara", query)
	form.Set("konuara", "series")

	body := []byte(form.Encode())
	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Origin":       config.BaseURL,
		"Referer":      config.BaseURL + "/",
	}
	resp, err := c.doRequest(ctx, http.MethodPost, config.BaseURL+"/search", headers, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: config.BaseURL + "/search"}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *Client) fetchSeriesPage(ctx context.Context, seriesURL string) (string, error) {
	logging.Debugf("wco fetchSeriesPage url=%q", seriesURL)
	u, err := url.Parse(seriesURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("season", "all")
	u.RawQuery = q.Encode()
	target := u.String()
	if !strings.Contains(target, "season=all") {
		target = strings.TrimRight(seriesURL, "/") + "/?season=all"
	}

	resp, err := c.doRequest(ctx, http.MethodGet, target, map[string]string{"Referer": config.BaseURL + "/"}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *Client) fetchEpisodePage(ctx context.Context, episodeURL string) (string, error) {
	logging.Debugf("wco fetchEpisodePage url=%q", episodeURL)
	resp, err := c.doRequest(ctx, http.MethodGet, episodeURL, map[string]string{"Referer": config.BaseURL + "/"}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: episodeURL}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *Client) fetchEmbedPage(ctx context.Context, iframeURL string) (string, error) {
	logging.Debugf("wco fetchEmbedPage url=%q", iframeURL)
	resp, err := c.doRequest(ctx, http.MethodGet, iframeURL, map[string]string{"Referer": config.BaseURL + "/"}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: iframeURL}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *Client) callGetvidlink(ctx context.Context, getvidlinkURL, referer string) (map[string]any, error) {
	logging.Debugf("wco callGetvidlink url=%q referer=%q", getvidlinkURL, referer)
	headers := map[string]string{
		"Accept":           "application/json, text/javascript, */*; q=0.01",
		"X-Requested-With": "XMLHttpRequest",
		"Referer":          referer,
		"Origin":           config.EmbedOrigin,
	}
	resp, err := c.doRequest(ctx, http.MethodGet, getvidlinkURL, headers, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: getvidlinkURL}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (c *Client) resolveFinalMediaURL(ctx context.Context, server, token string) (string, error) {
	target := strings.TrimRight(server, "/") + "/getvid?evid=" + url.QueryEscape(token) + "&json"
	headers := map[string]string{
		"Referer":          "https://www.wcostream.com/",
		"X-Requested-With": "XMLHttpRequest",
		"Accept":           "*/*",
	}
	resp, err := c.doRequest(ctx, http.MethodGet, target, headers, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(raw))

	// Check if it's a JSON object
	var mediaObj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(text), &mediaObj); err == nil && mediaObj.URL != "" {
		return mediaObj.URL, nil
	}

	// Otherwise try parsing as a JSON string
	var mediaStr string
	if err := json.Unmarshal([]byte(text), &mediaStr); err == nil && mediaStr != "" {
		return mediaStr, nil
	}

	// Fallback cleanup
	fallback := strings.ReplaceAll(strings.Trim(text, `"`), `\/`, `/`)
	return fallback, nil
}

func (c *Client) setCookieHeader(cookieHeader string) {
	parts := strings.Split(cookieHeader, ";")
	u, _ := url.Parse(config.BaseURL)
	cks := make([]*http.Cookie, 0)
	for _, p := range parts {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) != 2 || kv[0] == "" {
			continue
		}
		cks = append(cks, &http.Cookie{Name: strings.TrimSpace(kv[0]), Value: strings.TrimSpace(kv[1])})
	}
	if len(cks) > 0 {
		c.cookieJar.SetCookies(u, cks)
	}
}

func (c *Client) bootstrapCookiesWithCurl(timeout time.Duration) {
	if _, err := exec.LookPath("curl"); err != nil {
		logging.Debugf("bootstrapCookiesWithCurl: curl not found, falling back to HTTP client")
		return
	}

	tmp, err := os.CreateTemp("", "kari-cookies-*.txt")
	if err != nil {
		logging.Debugf("bootstrapCookiesWithCurl: failed to create temp file: %v", err)
		return
	}
	defer func() {
		if err := tmp.Close(); err != nil {
			logging.Warn("failed to close temp file: %v", err)
		}
		if err := os.Remove(tmp.Name()); err != nil && !os.IsNotExist(err) {
			logging.Warn("failed to remove temp file: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "curl", "-sS", "-L", "-A", config.AndroidUA(), "-c", tmp.Name(), "-b", tmp.Name(), config.BaseURL+"/")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		logging.Debugf("bootstrapCookiesWithCurl: curl failed: %v", err)
		return
	}

	raw, err := os.ReadFile(tmp.Name())
	if err != nil {
		logging.Debugf("bootstrapCookiesWithCurl: failed to read cookie file: %v", err)
		return
	}
	u, _ := url.Parse(config.BaseURL)
	cks := make([]*http.Cookie, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			continue
		}
		name := strings.TrimSpace(parts[5])
		val := strings.TrimSpace(parts[6])
		if name == "" {
			continue
		}
		cks = append(cks, &http.Cookie{Name: name, Value: val})
	}
	if len(cks) > 0 {
		c.cookieJar.SetCookies(u, cks)
	}
}
