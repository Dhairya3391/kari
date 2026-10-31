// Package atsu implements provider.MangaSource for the Atsu manga reader (https://atsu.moe).
// It queries Atsu's Typesense search cluster for manga titles, lists full chapter
// indexes, and resolves high-resolution WebP page images directly from Atsu's CDN.
package atsu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
)

var (
	_ provider.Provider      = (*Client)(nil)
	_ provider.MangaSource   = (*Client)(nil)
	_ provider.FeatureSource = (*Client)(nil)
	_ provider.Presenter     = (*Client)(nil)
)

var atsuLog = logging.With("component", "provider.atsu")

const (
	defaultBaseURL = "https://atsu.moe"
	defaultCDNURL  = "https://cdn.atsu.moe"
)

// Client implements provider.MangaSource against the Atsu manga reader service.
type Client struct {
	http    *http.Client
	baseURL string
	cdnURL  string
}

// NewClient constructs the Atsu manga provider pointing to the production endpoints.
func NewClient() (*Client, error) {
	return NewClientWithURLs(defaultBaseURL, defaultCDNURL, httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, 15*time.Second))
}

// NewClientWithURLs constructs the Atsu provider with custom API and CDN URLs
// for testing and mock environments.
func NewClientWithURLs(baseURL, cdnURL string, hc *http.Client) (*Client, error) {
	if hc == nil {
		hc = httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, 15*time.Second)
	}
	return &Client{
		http:    hc,
		baseURL: strings.TrimRight(baseURL, "/"),
		cdnURL:  strings.TrimRight(cdnURL, "/"),
	}, nil
}

// Name implements provider.Provider with the canonical identifier.
func (c *Client) Name() string { return "atsu" }

// Alias implements provider.Presenter with the user-facing display name.
func (c *Client) Alias() string { return "Atsu" }

// Modes implements provider.Provider, registering Atsu for manga reading.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeManga, Priority: 2}}
}

// Features implements provider.FeatureSource.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeManga {
		return provider.Features{}
	}
	return provider.Features{SearchPlaceholder: "Search manga…"}
}

// FetchEpisodes is inapplicable to manga titles.
func (c *Client) FetchEpisodes(_ context.Context, _ provider.SearchResult) ([]provider.Episode, error) {
	return nil, provider.ErrNoEpisodes
}

// ResolveSource is inapplicable to manga titles.
func (c *Client) ResolveSource(_ context.Context, _ string, _ provider.Episode) ([]provider.MediaSource, error) {
	return nil, provider.ErrNoSources
}

type searchResponse struct {
	Found int         `json:"found"`
	Hits  []searchHit `json:"hits"`
}

type searchHit struct {
	Document searchDoc `json:"document"`
}

type searchDoc struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	EnglishTitle string   `json:"englishTitle"`
	Poster       string   `json:"poster"`
	PosterMedium string   `json:"posterMedium"`
	Year         int      `json:"year"`
	Type         string   `json:"type"`
	Authors      []string `json:"authors"`
}

// Search queries Atsu's catalog for manga matching the given query string.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, provider.ErrNoResults
	}

	params := url.Values{}
	params.Set("q", trimmed)
	params.Set("query_by", "title,englishTitle,otherNames,authors,acronyms")
	params.Set("query_by_weights", "4,3,2,2,1")
	params.Set("num_typos", "4,3,2,1,0")
	params.Set("prefix", "true,true,true,true,false")
	params.Set("include_fields", "id,title,englishTitle,authors,poster,posterMedium,type,year")
	params.Set("page", "1")
	params.Set("per_page", "20")

	endpoint := fmt.Sprintf("%s/collections/manga/documents/search?%s", c.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create atsu search request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.baseURL+"/")
	req.Header.Set("Origin", c.baseURL)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("atsu search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("read atsu search response: %w", err)
	}

	var data searchResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("decode atsu search response: %w", err)
	}

	if len(data.Hits) == 0 {
		return nil, provider.ErrNoResults
	}

	results := make([]provider.SearchResult, 0, len(data.Hits))
	for _, hit := range data.Hits {
		doc := hit.Document
		title := doc.Title
		if title == "" {
			title = doc.EnglishTitle
		}
		if title == "" {
			continue
		}

		// Prefer JPEG Poster over AVIF PosterMedium so Go's image decoder succeeds
		cover := doc.Poster
		if cover == "" {
			cover = doc.PosterMedium
		}
		coverURL := c.formatAssetURL(cover)

		yearStr := ""
		if doc.Year > 0 {
			yearStr = strconv.Itoa(doc.Year)
		}

		results = append(results, provider.SearchResult{
			ID:           doc.ID,
			Title:        title,
			Type:         provider.ModeManga,
			MediaType:    provider.MediaTypeManga,
			Year:         yearStr,
			CoverURL:     coverURL,
			CoverReferer: c.baseURL + "/",
			Provider:     c.Name(),
		})
	}

	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	atsuLog.Debug("search hits", "query", trimmed, "count", len(results))
	return results, nil
}

type allChaptersResponse struct {
	Chapters  []atsuChapter     `json:"chapters"`
	MangaPage *mangaPageDetails `json:"mangaPage"`
}

type mangaPageDetails struct {
	ID            string        `json:"id"`
	Title         string        `json:"title"`
	TotalChapters int           `json:"totalChapterCount"`
	Chapters      []atsuChapter `json:"chapters"`
}

type atsuChapter struct {
	ID                string  `json:"id"`
	Title             string  `json:"title"`
	Number            float64 `json:"number"`
	PageCount         int     `json:"pageCount"`
	ScanlationMangaID string  `json:"scanlationMangaId"`
	ScanlationName    string  `json:"scanlationName"`
	Group             string  `json:"group"`
}

// FetchChapters lists every chapter for the given series, sorted in ascending order.
func (c *Client) FetchChapters(ctx context.Context, series provider.SearchResult) ([]provider.MangaChapter, error) {
	if strings.TrimSpace(series.ID) == "" {
		return nil, provider.ErrNoEpisodes
	}

	endpoint := fmt.Sprintf("%s/api/manga/allChapters?mangaId=%s", c.baseURL, url.QueryEscape(series.ID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create atsu chapters request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.baseURL+"/")
	req.Header.Set("Origin", c.baseURL)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("atsu chapters request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("read atsu chapters response: %w", err)
	}

	var data allChaptersResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("decode atsu chapters response: %w", err)
	}

	rawChapters := data.Chapters
	mangaID := series.ID
	if len(rawChapters) == 0 && data.MangaPage != nil {
		rawChapters = data.MangaPage.Chapters
		if data.MangaPage.ID != "" {
			mangaID = data.MangaPage.ID
		}
	}

	if len(rawChapters) == 0 {
		return nil, provider.ErrNoEpisodes
	}

	// Atsu returns newest chapters first; sort ascending so reader advances naturally
	sort.SliceStable(rawChapters, func(i, j int) bool {
		return rawChapters[i].Number < rawChapters[j].Number
	})

	chapters := make([]provider.MangaChapter, 0, len(rawChapters))
	for _, ch := range rawChapters {
		numStr := strconv.FormatFloat(ch.Number, 'f', -1, 64)
		title := ch.Title
		if title == "" {
			title = fmt.Sprintf("Chapter %s", numStr)
		}

		groupName := ch.ScanlationName
		if groupName == "" {
			groupName = ch.Group
		}

		chapters = append(chapters, provider.MangaChapter{
			ID:       fmt.Sprintf("%s:%s", mangaID, ch.ID),
			Number:   numStr,
			Title:    title,
			Language: "en",
			Group:    groupName,
			Provider: c.Name(),
		})
	}

	atsuLog.Debug("chapters resolved", "manga", series.Title, "count", len(chapters))
	return chapters, nil
}

type readChapterResponse struct {
	ReadChapter *readChapterDetails `json:"readChapter"`
}

type readChapterDetails struct {
	ID    string     `json:"id"`
	Pages []atsuPage `json:"pages"`
}

type atsuPage struct {
	Number int    `json:"number"`
	Image  string `json:"image"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// FetchPages resolves all page image URLs for a chapter.
func (c *Client) FetchPages(ctx context.Context, chapter provider.MangaChapter) ([]provider.MangaPage, error) {
	parts := strings.SplitN(chapter.ID, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return nil, fmt.Errorf("invalid atsu chapter handle %q: %w", chapter.ID, provider.ErrNoSources)
	}
	mangaID, chapterID := parts[0], parts[1]

	endpoint := fmt.Sprintf("%s/api/read/chapter?mangaId=%s&chapterId=%s",
		c.baseURL, url.QueryEscape(mangaID), url.QueryEscape(chapterID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create atsu pages request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.baseURL+"/")
	req.Header.Set("Origin", c.baseURL)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("atsu pages request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("read atsu pages response: %w", err)
	}

	var data readChapterResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("decode atsu pages response: %w", err)
	}

	if data.ReadChapter == nil || len(data.ReadChapter.Pages) == 0 {
		return nil, provider.ErrNoSources
	}

	pages := make([]provider.MangaPage, 0, len(data.ReadChapter.Pages))
	for _, p := range data.ReadChapter.Pages {
		imgURL := c.formatAssetURL(p.Image)
		if imgURL == "" {
			continue
		}
		pages = append(pages, provider.MangaPage{
			URL:       imgURL,
			Referer:   c.baseURL + "/",
			UserAgent: config.DesktopUserAgent,
			Width:     p.Width,
			Height:    p.Height,
		})
	}

	if len(pages) == 0 {
		return nil, provider.ErrNoSources
	}
	atsuLog.Debug("pages resolved", "chapter", chapter.Number, "count", len(pages))
	return pages, nil
}

func (c *Client) formatAssetURL(path string) string {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return ""
	}
	if strings.HasPrefix(clean, "http://") || strings.HasPrefix(clean, "https://") {
		return clean
	}
	if strings.HasPrefix(clean, "//") {
		return "https:" + clean
	}
	clean = strings.TrimPrefix(clean, "/")
	if !strings.HasPrefix(clean, "static/") {
		clean = "static/" + clean
	}
	return fmt.Sprintf("%s/%s", c.cdnURL, clean)
}
