package weebcentral

import (
	"context"
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
)

// wcLog scopes every line from this package with its identity.
var wcLog = logging.With("component", "provider.weebcentral")

// Client implements provider.MangaSource by scraping WeebCentral's
// HTMX HTML (no public API): the search/data fragment for titles, the
// chapter-select fragment for complete chapter listings, and the
// chapter images fragment for page URLs. All selectors were verified
// live; the site answers plain curl with no bot checks, so no special
// headers beyond a desktop UA and the site Referer are needed.
type Client struct {
	http    *http.Client
	baseURL string

	// throttle serializes requests to stay polite to a small site.
	throttleMu sync.Mutex
	lastReq    time.Time
}

// Name implements provider.Provider, returning the stable identifier.
func (c *Client) Name() string { return "weebcentral" }

// Alias implements provider.Presenter, providing the display codename.
func (c *Client) Alias() string { return "WeebCentral" }

// Modes implements provider.Provider, registering WeebCentral for manga.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeManga, Priority: 1}}
}

// Features implements provider.FeatureSource with the search hint.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeManga {
		return provider.Features{}
	}
	return provider.Features{SearchPlaceholder: "Search manga…"}
}

// FetchEpisodes is inapplicable to paged media; chapters flow through
// FetchChapters instead.
func (c *Client) FetchEpisodes(_ context.Context, _ provider.SearchResult) ([]provider.Episode, error) {
	return nil, provider.ErrNoEpisodes
}

// ResolveSource is inapplicable to paged media; pages flow through
// FetchPages instead.
func (c *Client) ResolveSource(_ context.Context, _ string, _ provider.Episode) ([]provider.MediaSource, error) {
	return nil, provider.ErrNoSources
}

// NewClient constructs the WeebCentral provider against production.
func NewClient() (*Client, error) {
	return NewClientWithBaseURL(config.WeebCentralBase)
}

// NewClientWithBaseURL constructs the WeebCentral provider against a
// custom base URL. Tests point it at httptest servers.
func NewClientWithBaseURL(baseURL string) (*Client, error) {
	return &Client{
		http:    httpclient.New(),
		baseURL: strings.TrimRight(baseURL, "/"),
	}, nil
}

// throttle spaces requests so bursts (search + chapter listing + page
// resolution) never hammer the site.
func (c *Client) throttle(ctx context.Context) error {
	const minInterval = 300 * time.Millisecond
	c.throttleMu.Lock()
	defer c.throttleMu.Unlock()
	if wait := minInterval - time.Since(c.lastReq); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	c.lastReq = time.Now()
	return nil
}

// getHTML performs a throttled GET with browser headers and returns the
// response body as a string. HTMX fragments require the HX-Request
// header; without it some endpoints redirect instead of rendering.
func (c *Client) getHTML(ctx context.Context, rawURL, referer string) (string, error) {
	if err := c.throttle(ctx); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("weebcentral request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", referer)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Accept", "text/html")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("weebcentral fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: rawURL}
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return "", fmt.Errorf("weebcentral read body: %w", err)
	}
	return string(body), nil
}

// seriesIDRe matches WeebCentral series links; IDs are Crockford-style
// uppercase alphanumerics.
var seriesIDRe = regexp.MustCompile(`/series/([A-Za-z0-9]+)`)

// Search finds manga titles by name through the search/data fragment.
// One article per result carries the cover, title, year, and tags.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	u, _ := url.Parse(c.baseURL + "/search/data")
	q := u.Query()
	q.Set("text", query)
	q.Set("display_mode", "Full Display")
	u.RawQuery = q.Encode()

	body, err := c.getHTML(ctx, u.String(), c.baseURL+"/")
	if err != nil {
		return nil, err
	}
	var out []provider.SearchResult
	seen := make(map[string]struct{})
	// Positional slicing (not first-match search): the fragment
	// repeats each result for mobile and desktop, so windows must start
	// at their own match and run to the next *different* series — same-id
	// repeats (cover link, title link) belong to one window.
	locs := seriesIDRe.FindAllStringSubmatchIndex(body, -1)
	for i, loc := range locs {
		id := body[loc[2]:loc[3]]
		end := len(body)
		for _, nl := range locs[i+1:] {
			if body[nl[2]:nl[3]] != id {
				end = nl[0]
				break
			}
		}
		if end-loc[1] > 12000 {
			end = loc[1] + 12000
		}
		seg := body[loc[1]:end]
		seg = trimPartialTag(seg)
		title := seriesTitle(seg)
		if title == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, provider.SearchResult{
			Title:        title,
			ID:           id,
			Type:         provider.ModeManga,
			Year:         seriesYear(seg),
			MediaType:    provider.MediaTypeManga,
			CoverURL:     seriesCover(seg, id),
			CoverReferer: config.WeebCentralReferer,
			Genres:       seriesGenres(seg),
		})
		if len(out) >= 25 {
			break
		}
	}
	if len(out) == 0 {
		return nil, provider.ErrNoResults
	}
	wcLog.Debug("search done", "query", query, "results", len(out))
	return out, nil
}

// trimPartialTag drops a trailing unclosed tag left by window slicing,
// which would otherwise leak raw markup (e.g. a cut-off href) into the
// text lines parsed for year and genres.
func trimPartialTag(seg string) string {
	if i := strings.LastIndex(seg, "<"); i >= 0 && !strings.Contains(seg[i:], ">") {
		return seg[:i]
	}
	return seg
}

// htmlTagRe matches one HTML tag including quoted attribute values
// that may themselves contain ">" (e.g. data-tip="A > B"). Naive
// `<[^>]+>` stripping breaks on those and leaks raw markup into the
// text lines parsed for year and genres.
var htmlTagRe = regexp.MustCompile(`<(?:[^>"']|"[^"]*"|'[^']*')*>`)

// textLines strips tags from an HTML window into clean text lines.
func textLines(seg string) []string {
	noTags := htmlTagRe.ReplaceAllString(seg, "\n")
	var lines []string
	for _, l := range strings.Split(noTags, "\n") {
		if l = strings.TrimSpace(html.UnescapeString(l)); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// seriesTitle prefers the cover alt text ("{Title} cover"), which sits
// closest to the link and never collides with neighboring results.
var seriesTitleRe = regexp.MustCompile(`alt="([^"]+) cover"`)

func seriesTitle(seg string) string {
	if m := seriesTitleRe.FindStringSubmatch(seg); m != nil {
		return strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return ""
}

// seriesCover takes the JPEG fallback cover for the result. The webp
// variants decode nowhere in the app's image pipeline, so they are
// deliberately never used.
func seriesCover(seg, id string) string {
	// Per-result substring match (not a shared regex) so a neighboring
	// article's cover can never leak in, with no per-call regexp
	// compilation: the ID is dynamic per result.
	marker := "/cover/fallback/" + id + ".jpg"
	idx := strings.Index(seg, marker)
	if idx < 0 {
		return ""
	}
	end := idx + len(marker)
	start := strings.LastIndex(seg[:idx], "https://")
	if h := strings.LastIndex(seg[:idx], "http://"); h > start {
		start = h
	}
	if start < 0 {
		return ""
	}
	// Reject matches spanning a delimiter (quote, space, bracket).
	for _, c := range seg[start:idx] {
		if c == '"' || c == '\'' || c == ' ' || c == '<' || c == '>' {
			return ""
		}
	}
	return seg[start:end]
}

// seriesYear reads the "Year:" label value in the result window.
var yearRe = regexp.MustCompile(`^[12][0-9]{3}$`)

func seriesYear(seg string) string {
	lines := textLines(seg)
	for i, l := range lines {
		if l == "Year:" && i+1 < len(lines) {
			if y := strings.TrimSpace(lines[i+1]); yearRe.MatchString(y) {
				return y
			}
		}
	}
	return ""
}

// seriesGenres collects the comma-separated tags after the "Tag(s):"
// label, stopping at the next "Label:" line or the window end.
func seriesGenres(seg string) []string {
	lines := textLines(seg)
	var out []string
	inTags := false
	for _, l := range lines {
		if l == "Tag(s):" {
			inTags = true
			continue
		}
		if !inTags {
			continue
		}
		if strings.HasSuffix(l, ":") {
			break
		}
		for _, g := range strings.Split(l, ",") {
			if g = strings.TrimSpace(strings.Trim(strings.TrimSpace(g), ",")); g != "" {
				out = append(out, g)
			}
		}
	}
	return out
}

// chapterRowRe matches chapter links in both fragments: the clean
// chapter-select buttons (`>Chapter N</a>`) and the series page's
// nested rows (badge icons, "Last Read" timestamps). (?s) because row
// markup spans lines in the live HTML.
var chapterRowRe = regexp.MustCompile(`(?s)href="[^"]*/chapters/([A-Za-z0-9]+)"[^>]*>(.*?)</a>`)

// chapterNumberRe pulls the number out of a row's cleaned text.
var chapterNumberRe = regexp.MustCompile(`(?i)\bchapter\s+([\d.]+)`)

// parseChapterRows extracts (id, number, title) rows from a chapter
// listing fragment in document order. Trailing site junk ("Last Read
// …" timestamps on series-page rows) is dropped.
func parseChapterRows(body string) []provider.MangaChapter {
	var out []provider.MangaChapter
	for _, m := range chapterRowRe.FindAllStringSubmatch(body, -1) {
		text := strings.TrimSpace(html.UnescapeString(htmlTagRe.ReplaceAllString(m[2], " ")))
		text = strings.Join(strings.Fields(text), " ")
		number, title := "", text
		if loc := chapterNumberRe.FindStringSubmatchIndex(text); loc != nil {
			number = text[loc[2]:loc[3]]
			title = strings.TrimSpace(strings.Trim(strings.TrimSpace(text[loc[1]:]), ":"))
			if i := strings.Index(title, "Last Read"); i >= 0 {
				title = strings.TrimSpace(title[:i])
			}
		}
		out = append(out, provider.MangaChapter{ID: m[1], Number: number, Title: title, Language: "en"})
	}
	return out
}

// FetchChapters lists every chapter for the series, oldest first. The
// series page embeds only recent chapters while the chapter-select
// fragment holds the complete listing, so both are merged (recent
// overlap deduplicated by chapter ID): series without a chapter-select
// route still list what the page carries.
func (c *Client) FetchChapters(ctx context.Context, series provider.SearchResult) ([]provider.MangaChapter, error) {
	seriesBody, err := c.getHTML(ctx, c.baseURL+"/series/"+series.ID, c.baseURL+"/")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]provider.MangaChapter)
	var order []string
	add := func(rows []provider.MangaChapter) {
		for _, ch := range rows {
			if _, dup := byID[ch.ID]; dup {
				continue
			}
			byID[ch.ID] = ch
			order = append(order, ch.ID)
		}
	}
	add(parseChapterRows(seriesBody))

	var firstID string
	for _, id := range order {
		firstID = id
		break
	}
	if firstID != "" {
		u, _ := url.Parse(c.baseURL + "/series/" + series.ID + "/chapter-select")
		q := u.Query()
		q.Set("current_chapter", firstID)
		u.RawQuery = q.Encode()

		if listBody, lerr := c.getHTML(ctx, u.String(), c.baseURL+"/series/"+series.ID); lerr == nil {
			add(parseChapterRows(listBody))
		} else {
			wcLog.Debug("chapter-select failed; keeping series-page rows", "title", series.Title, "err", lerr)
		}
	}
	if len(order) == 0 {
		return nil, provider.ErrNoEpisodes
	}
	out := make([]provider.MangaChapter, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	sortChapters(out)
	wcLog.Debug("chapters done", "title", series.Title, "count", len(out))
	return out, nil
}

// sortChapters orders numerically, pushing unnumbered rows last.
// Numbers stay strings (fractional "12.5"), hence float parsing.
func sortChapters(chapters []provider.MangaChapter) {
	type parsedChapter struct {
		ch  provider.MangaChapter
		val float64
	}
	parsed := make([]parsedChapter, len(chapters))
	for i, ch := range chapters {
		parsed[i] = parsedChapter{ch: ch, val: chapterFloat(ch.Number)}
	}
	slices.SortStableFunc(parsed, func(a, b parsedChapter) int {
		if a.val < b.val {
			return -1
		}
		if a.val > b.val {
			return 1
		}
		return 0
	})
	for i, p := range parsed {
		chapters[i] = p.ch
	}
}

// chapterFloat parses a chapter number, mapping empty or non-numeric
// values to +Inf so they sort last.
func chapterFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 1e18
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return 1e18
}

// pageImgSrcRe matches chapter page images off the image CDN, in
// document order (which is page order). Scoped to <img> src attributes
// so page chrome (logos, placeholders, OG meta URLs) never becomes a
// page; the extension may carry a query string.
var pageImgSrcRe = regexp.MustCompile(`(?i)<img[^>]+src=["'](https?://[^"'\s<>]+?\.(?:png|jpe?g|webp)(?:\?[^"'\s<>]*)?)["']`)

// FetchPages resolves a chapter's page-image URLs through the chapter
// images fragment. The CDN enforces hotlink protection, hence the site
// Referer stamped on every page URL.
func (c *Client) FetchPages(ctx context.Context, chapter provider.MangaChapter) ([]provider.MangaPage, error) {
	u, _ := url.Parse(c.baseURL + "/chapters/" + chapter.ID + "/images")
	q := u.Query()
	q.Set("is_prev", "False")
	u.RawQuery = q.Encode()

	body, err := c.getHTML(ctx, u.String(), c.baseURL+"/chapters/"+chapter.ID)
	if err != nil {
		return nil, err
	}
	var pages []provider.MangaPage
	seen := make(map[string]struct{})
	for _, m := range pageImgSrcRe.FindAllStringSubmatch(body, -1) {
		u := m[1]
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		pages = append(pages, provider.MangaPage{
			URL:       u,
			Referer:   config.WeebCentralReferer,
			UserAgent: config.DesktopUserAgent,
		})
	}
	if len(pages) == 0 {
		return nil, provider.ErrNoSources
	}
	return pages, nil
}

var (
	_ provider.Provider      = (*Client)(nil)
	_ provider.MangaSource   = (*Client)(nil)
	_ provider.FeatureSource = (*Client)(nil)
	_ provider.Presenter     = (*Client)(nil)
)
