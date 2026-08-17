package piratex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/util"
)

// asCDNBase is the FirePlayer host the site's episode pages embed. The signed
// playlists it hands out are scoped to this host.
const asCDNBase = "https://as-cdn21.top"

var (
	reH1         = regexp.MustCompile(`(?s)<h1[^>]*>(.*?)</h1>`)
	reHTMLTag    = regexp.MustCompile(`<[^>]+>`)
	reSeasonSlug = regexp.MustCompile(`(?i)/series/([a-z0-9\-]+-season-(\d+)-\d+)/`)
	reEpisode    = regexp.MustCompile(`(?i)/episode/([a-z0-9\-]+?)-(\d+)x(\d+)/`)
	reCDN        = regexp.MustCompile(`(?i)https://as-cdn\d+\.top/video/([0-9a-f]+)`)
	rePlayer11   = regexp.MustCompile(`(?i)public/player/index11\.php\?id=([a-z0-9]+)`)
	reByseEmbed  = regexp.MustCompile(`(?i)https?://[a-z0-9\-\.]+/e/([a-z0-9]+)/?`)
	reAttr       = regexp.MustCompile(`([A-Z0-9\-]+)=("([^"]*)"|[^,]+)`)
)

// ── Types ────────────────────────────────────────────────────────────────────

type searchResponse struct {
	Status string       `json:"status"`
	Data   []searchItem `json:"data"`
}

type searchItem struct {
	TMDB *searchTMDB `json:"tmdb"`
}

type searchTMDB struct {
	URL   string `json:"url"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Year  any    `json:"release_year"`
}

type seasonInfo struct {
	Number int
	Slug   string
}

type seriesEpisode struct {
	Number int
	Season int
	Slug   string
	URL    string
}

type seriesData struct {
	Slug         string
	Name         string
	Seasons      []seasonInfo
	Episodes     []seriesEpisode
	EpisodeCount int
}

type variant struct {
	Height int
	Label  string
	URL    string
}

type audioTrack struct {
	Language string
	Name     string
	URL      string
}

type subtitleTrack struct {
	Language string
	Name     string
	URL      string
}

type resolvedStream struct {
	URL       string
	Server    string
	Height    int
	Label     string
	Master    bool
	Referer   string
	UserAgent string
	Cookie    string
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

func searchURL(query string) string {
	q := url.Values{}
	q.Set("keyword", query)
	return baseURL + "/api/search-ajax.php?" + q.Encode()
}

// do performs an HTTP request through the shared client, applying the
// per-transport headers (Referer/Cookie) and a Chrome User-Agent.
func (c *Client) do(ctx context.Context, method, target string, headers map[string]string, body io.Reader, contentType string) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		raw, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

func (c *Client) get(ctx context.Context, target string, headers map[string]string) (*http.Response, error) {
	return c.do(ctx, http.MethodGet, target, headers, nil, "")
}

func (c *Client) getBytes(ctx context.Context, target string, headers map[string]string) ([]byte, int, error) {
	resp, err := c.get(ctx, target, headers)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// ── Search ───────────────────────────────────────────────────────────────────

func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	default:
		return 0
	}
}

// ── Series / Episodes ─────────────────────────────────────────────────────────

func (c *Client) fetchSeries(ctx context.Context, slug string) (*seriesData, error) {
	cacheKey := strings.ToLower(slug)
	if cached, ok := c.seriesCache.Get(cacheKey); ok {
		return cached, nil
	}

	target := baseURL + "/series/" + slug + "/"
	body, status, err := c.getBytes(ctx, target, nil)
	if err != nil {
		return nil, fmt.Errorf("piratex episodes: %w", err)
	}
	if status != http.StatusOK {
		return nil, &provider.HTTPError{Code: status, URL: target}
	}
	page := string(body)

	data := &seriesData{
		Slug:    slug,
		Name:    parseSeriesTitle(page, slug),
		Seasons: parseSeasons(page),
	}
	if len(data.Seasons) == 0 {
		// The page lists no season links (rare); fall back to the requested
		// slug itself when it carries an explicit season number.
		if snum := seasonNumberFromSlug(slug); snum > 0 {
			data.Seasons = []seasonInfo{{Number: snum, Slug: slug}}
		}
	}

	// Gather episodes from every season page in parallel so a whole show is
	// returned in one call (no per-season round trips later).
	g, gCtx := errgroup.WithContext(ctx)
	results := make([][]seriesEpisode, len(data.Seasons))
	for i, s := range data.Seasons {
		i, s := i, s
		g.Go(func() error {
			seasonBody, st, err := c.getBytes(gCtx, baseURL+"/series/"+s.Slug+"/", nil)
			if err != nil || st != http.StatusOK {
				return nil
			}
			results[i] = parseEpisodes(string(seasonBody), s.Number)
			return nil
		})
	}
	_ = g.Wait()

	for _, eps := range results {
		data.Episodes = append(data.Episodes, eps...)
	}
	data.EpisodeCount = len(data.Episodes)
	sortEpisodes(data.Episodes)

	c.seriesCache.Set(cacheKey, data)
	return data, nil
}

func parseSeriesTitle(page, fallback string) string {
	m := reH1.FindStringSubmatch(page)
	if len(m) < 2 {
		return fallback
	}
	title := util.NormalizeSpace(reHTMLTag.ReplaceAllString(m[1], ""))
	if title == "" {
		return fallback
	}
	return title
}

func parseSeasons(page string) []seasonInfo {
	seen := make(map[int]string)
	for _, m := range reSeasonSlug.FindAllStringSubmatch(page, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		if _, ok := seen[n]; !ok {
			seen[n] = m[1]
		}
	}
	nums := make([]int, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	seasons := make([]seasonInfo, 0, len(nums))
	for _, n := range nums {
		seasons = append(seasons, seasonInfo{Number: n, Slug: seen[n]})
	}
	return seasons
}

func parseEpisodes(page string, season int) []seriesEpisode {
	var eps []seriesEpisode
	seen := make(map[string]bool)
	for _, m := range reEpisode.FindAllStringSubmatch(page, -1) {
		if seen[m[0]] {
			continue
		}
		seen[m[0]] = true
		number, err := strconv.Atoi(m[3])
		if err != nil {
			continue
		}
		eps = append(eps, seriesEpisode{
			Number: number,
			Season: season,
			Slug:   m[1],
			URL:    fmt.Sprintf("%s/episode/%s-%dx%d/", baseURL, m[1], season, number),
		})
	}
	return eps
}

func seasonNumberFromSlug(slug string) int {
	m := reSeasonSlug.FindStringSubmatch("/series/" + slug + "/")
	if len(m) < 3 {
		return 0
	}
	n, _ := strconv.Atoi(m[2])
	return n
}

func sortEpisodes(eps []seriesEpisode) {
	sort.Slice(eps, func(i, j int) bool {
		if eps[i].Season != eps[j].Season {
			return eps[i].Season < eps[j].Season
		}
		return eps[i].Number < eps[j].Number
	})
}

// ── Episode → playable HLS ────────────────────────────────────────────────────

func (c *Client) resolveEpisode(ctx context.Context, slug string, season, episode int) ([]resolvedStream, []audioTrack, []subtitleTrack, error) {
	episodeURL := fmt.Sprintf("%s/episode/%s-%dx%d/", baseURL, slug, season, episode)
	body, status, err := c.getBytes(ctx, episodeURL, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("piratex resolve: %w", err)
	}
	if status != http.StatusOK {
		return nil, nil, nil, &provider.HTTPError{Code: status, URL: episodeURL}
	}
	page := string(body)

	var (
		mu        sync.Mutex
		streams   []resolvedStream
		audio     []audioTrack
		subtitles []subtitleTrack
	)

	g, gCtx := errgroup.WithContext(ctx)

	// as-cdn FirePlayer embed(s) — parallel since pages may carry several,
	// and concurrent with the byse transport so one slow host can't gate the
	// other (worst case drops from two chained chains to the slower of the two).
	ids := extractCDNIDs(page)
	if len(ids) > 0 {
		g.Go(func() error {
			inner, innerCtx := errgroup.WithContext(gCtx)
			results := make([][]resolvedStream, len(ids))
			audios := make([][]audioTrack, len(ids))
			for i, id := range ids {
				i, id := i, id
				inner.Go(func() error {
					ss, aa, err := c.resolveAsCDN(innerCtx, id, episodeURL)
					if err != nil {
						logging.Debugf("piratex as-cdn resolve failed id=%s err=%v", id, err)
						return nil
					}
					results[i] = ss
					audios[i] = aa
					return nil
				})
			}
			_ = inner.Wait()
			mu.Lock()
			defer mu.Unlock()
			for i := range ids {
				streams = append(streams, results[i]...)
				for _, tr := range audios[i] {
					if !containsAudio(audio, tr) {
						audio = append(audio, tr)
					}
				}
			}
			return nil
		})
	}

	// byse (index11.php) embed, when also present on the page.
	g.Go(func() error {
		if s, aa, subs, ok := c.resolveByseEmbed(gCtx, page, episodeURL); ok {
			mu.Lock()
			defer mu.Unlock()
			streams = append(streams, *s)
			for _, tr := range aa {
				if !containsAudio(audio, tr) {
					audio = append(audio, tr)
				}
			}
			subtitles = append(subtitles, subs...)
		}
		return nil
	})

	_ = g.Wait()

	if len(streams) == 0 {
		return nil, nil, nil, fmt.Errorf("piratex resolve: no playable embed: %w", provider.ErrNoSources)
	}

	// Highest master first: byse's 1080p typically beats as-cdn's cap, and the
	// merged master of that transport becomes the preferred source.
	sort.Slice(streams, func(i, j int) bool {
		if streams[i].Height != streams[j].Height {
			return streams[i].Height > streams[j].Height
		}
		if streams[i].Master != streams[j].Master {
			return streams[i].Master
		}
		return streams[i].Server < streams[j].Server
	})
	return streams, audio, subtitles, nil
}

func extractCDNIDs(page string) []string {
	var ids []string
	seen := make(map[string]bool)
	for _, m := range reCDN.FindAllStringSubmatch(page, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			ids = append(ids, m[1])
		}
	}
	return ids
}

func containsAudio(list []audioTrack, track audioTrack) bool {
	for _, a := range list {
		if a.URL == track.URL {
			return true
		}
	}
	return false
}

// ── as-cdn (FirePlayer) transport ────────────────────────────────────────────

func (c *Client) resolveAsCDN(ctx context.Context, id, episodeURL string) ([]resolvedStream, []audioTrack, error) {
	videoURL := asCDNBase + "/video/" + id

	// 1. Prime the fireplayer_player session cookie from the video page.
	resp, err := c.get(ctx, videoURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("piratex as-cdn video page: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	var cookie string
	for _, ck := range resp.Cookies() {
		if ck.Name == "fireplayer_player" {
			cookie = ck.Name + "=" + ck.Value
			break
		}
	}

	// 2. POST getVideo for the signed master. The shared retryable client is
	//    safe here: re-running this mint just returns a fresh signed URL and
	//    the response we use is always the latest, so a retry never double-
	//    submits anything with side effects.
	form := url.Values{}
	form.Set("hash", id)
	form.Set("r", "")
	postURL := asCDNBase + "/player/index.php?data=" + id + "&do=getVideo"
	postHeaders := map[string]string{
		"Referer":          episodeURL,
		"X-Requested-With": "XMLHttpRequest",
	}
	if cookie != "" {
		postHeaders["Cookie"] = cookie
	}
	postResp, err := c.do(ctx, http.MethodPost, postURL, postHeaders, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded; charset=UTF-8")
	if err != nil {
		return nil, nil, fmt.Errorf("piratex as-cdn getVideo: %w", err)
	}
	postBody, err := io.ReadAll(postResp.Body)
	postResp.Body.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("piratex as-cdn getVideo: %w", err)
	}
	var gv struct {
		VideoSource string `json:"videoSource"`
	}
	if err := json.Unmarshal(postBody, &gv); err != nil {
		return nil, nil, fmt.Errorf("piratex as-cdn getVideo: decode: %w", err)
	}
	masterURL := strings.TrimSpace(gv.VideoSource)
	if masterURL == "" {
		return nil, nil, fmt.Errorf("piratex as-cdn getVideo: empty videoSource")
	}

	// 3. Fetch the master and split it into variants + audio tracks.
	masterHeaders := map[string]string{"Referer": episodeURL}
	if cookie != "" {
		masterHeaders["Cookie"] = cookie
	}
	masterBody, status, err := c.getBytes(ctx, masterURL, masterHeaders)
	if err != nil {
		return nil, nil, fmt.Errorf("piratex as-cdn master: %w", err)
	}
	if status != http.StatusOK {
		return nil, nil, &provider.HTTPError{Code: status, URL: masterURL}
	}

	variants, audio := parseMaster(string(masterBody), masterURL)

	server := "as-cdn-" + id
	if len(id) > 8 {
		server = "as-cdn-" + id[:8]
	}
	highest := 0
	for _, v := range variants {
		if v.Height > highest {
			highest = v.Height
		}
	}

	stream := resolvedStream{
		URL:       masterURL,
		Server:    server,
		Height:    highest,
		Master:    true,
		Referer:   episodeURL,
		UserAgent: config.DesktopUserAgent,
		Cookie:    cookie,
	}
	if highest > 0 {
		stream.Label = fmt.Sprintf("Auto (up to %dp)", highest)
	} else {
		stream.Label = "Auto"
	}
	return []resolvedStream{stream}, audio, nil
}

// parseMaster splits a merged HLS master into video variants (for height
// labeling) and the EXT-X-MEDIA audio group it carries.
func parseMaster(body, masterURL string) ([]variant, []audioTrack) {
	var variants []variant
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			continue
		}
		if i+1 >= len(lines) {
			break
		}
		uri := strings.TrimSpace(lines[i+1])
		if uri == "" || strings.HasPrefix(uri, "#") {
			continue
		}
		attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
		variants = append(variants, variant{
			Height: resolutionHeight(attrs["RESOLUTION"]),
			Label:  attrs["NAME"],
			URL:    absoluteURL(masterURL, uri),
		})
	}

	var audio []audioTrack
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			continue
		}
		attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
		if attrs["TYPE"] != "AUDIO" || attrs["URI"] == "" {
			continue
		}
		name := util.NormalizeSpace(attrs["NAME"])
		if name == "" {
			name = attrs["LANGUAGE"]
		}
		audio = append(audio, audioTrack{
			Language: attrs["LANGUAGE"],
			Name:     name,
			URL:      absoluteURL(masterURL, attrs["URI"]),
		})
	}

	sort.Slice(variants, func(i, j int) bool { return variants[i].Height > variants[j].Height })
	return variants, audio
}

func parseAttrs(s string) map[string]string {
	attrs := make(map[string]string)
	for _, m := range reAttr.FindAllStringSubmatch(s, -1) {
		if m[3] != "" {
			attrs[m[1]] = m[3]
		} else {
			attrs[m[1]] = m[2]
		}
	}
	return attrs
}

func resolutionHeight(res string) int {
	var w, h int
	if _, err := fmt.Sscanf(res, "%dx%d", &w, &h); err != nil {
		return 0
	}
	return h
}

func absoluteURL(masterURL, uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		return uri
	}
	base, err := url.Parse(masterURL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

const baseURL = config.PirateXOrigin
