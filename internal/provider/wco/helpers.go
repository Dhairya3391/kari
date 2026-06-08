package wco

import (
	"encoding/base64"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/provider"
)

var (
	obfuscatedIframeRe = regexp.MustCompile(`(?is)var\s+(\w+)\s*=\s*\"\";\s*var\s+(\w+)\s*=\s*\[(.*?)\];\s*\w+\.forEach\(function\s+\w+\(value\)\s*\{\s*\w+\s*\+=\s*String\.fromCharCode\(parseInt\(atob\(value\)\.replace\(/\\D/g,''\)\)\s*-\s*(\d+)\);`)
	chunksRe           = regexp.MustCompile(`"([A-Za-z0-9+/=]+)"`)
	nonDigitRe         = regexp.MustCompile(`\D`)
	iframeSrcRe        = regexp.MustCompile(`(?is)<iframe[^>]+src="([^"]+)"`)
	getvidlinkRe       = regexp.MustCompile(`(/inc/embed/getvidlink\.php\?[^"]+)`)
	directMediaURLRe   = regexp.MustCompile(`(?is)https?://[^"'<>\s\\]+?\.(?:m3u8|mp4)(?:\?[^"'<>\s\\]*)?`)
)

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

type episodeLink struct {
	URL        string
	Title      string
	SeasonHint int
}

func normalizeEpisodeTitle(raw string) string {
	title := stripTagsRe.ReplaceAllString(raw, "")
	title = html.UnescapeString(title)
	return compactSpaceRe.ReplaceAllString(strings.TrimSpace(title), " ")
}

func absoluteWCOURL(href string) string {
	href = strings.TrimSpace(html.UnescapeString(href))
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "/") {
		return config.BaseURL + href
	}
	return href
}

func parseNumber(m []string) int {
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

func parseEpisodeNumber(title, episodeURL string) int {
	if n := parseNumber(episodeNumRe.FindStringSubmatch(title)); n > 0 {
		return n
	}
	return parseNumber(urlEpisodeRe.FindStringSubmatch(episodeURL))
}

func parseSeasonNumber(title, episodeURL string) int {
	if n := parseNumber(seasonNumRe.FindStringSubmatch(title)); n > 0 {
		return n
	}
	return parseNumber(urlSeasonRe.FindStringSubmatch(episodeURL))
}

func buildEpisodesFromLinks(links []episodeLink, allowUntitled bool) []provider.Episode {
	episodes := make([]provider.Episode, 0, len(links))
	seen := make(map[string]bool, len(links))
	for _, link := range links {
		fullURL := absoluteWCOURL(link.URL)
		title := strings.TrimSpace(link.Title)
		if fullURL == "" || seen[fullURL] {
			continue
		}
		if title == "" && !allowUntitled {
			continue
		}
		seen[fullURL] = true
		season := parseSeasonNumber(title, fullURL)
		if season <= 0 {
			season = link.SeasonHint
		}
		episodes = append(episodes, provider.Episode{
			Title:   title,
			ID:      fullURL,
			Season:  season,
			Episode: parseEpisodeNumber(title, fullURL),
		})
	}
	return episodes
}

func (c *Client) extractModernEpisodeLinks(seriesHTML string) []episodeLink {
	var links []episodeLink
	for _, m := range anchorTagRe.FindAllStringSubmatch(seriesHTML, -1) {
		attrs := m[1]
		classMatch := classAttrRe.FindStringSubmatch(attrs)
		if len(classMatch) < 2 || !hasClass(classMatch[1], "dark-episode-item") {
			continue
		}
		hrefMatch := hrefAttrRe.FindStringSubmatch(attrs)
		if len(hrefMatch) < 2 {
			continue
		}
		title := normalizeEpisodeTitle(m[2])
		if title == "" {
			continue
		}
		links = append(links, episodeLink{
			URL:        hrefMatch[1],
			Title:      title,
			SeasonHint: seasonHintBefore(seriesHTML, m[0]),
		})
	}
	return links
}

func seasonHintBefore(doc, anchor string) int {
	idx := strings.Index(doc, anchor)
	if idx <= 0 {
		return 0
	}
	start := idx - 2500
	if start < 0 {
		start = 0
	}
	context := doc[start:idx]
	matches := seasonNumRe.FindAllStringSubmatch(normalizeEpisodeTitle(context), -1)
	if len(matches) == 0 {
		return 0
	}
	return parseNumber(matches[len(matches)-1])
}

func mediaSourceType(mediaURL string) string {
	u := strings.ToLower(mediaURL)
	if strings.Contains(u, ".m3u8") || strings.Contains(u, "/hls/") || strings.Contains(u, "playlist.m3u8") || strings.Contains(u, "master.m3u8") {
		return "hls"
	}
	return "mp4"
}

func hasClass(classes, target string) bool {
	for _, class := range strings.Fields(classes) {
		if class == target {
			return true
		}
	}
	return false
}

func extractDirectMediaURLs(text string) []string {
	text = strings.ReplaceAll(text, `\/`, `/`)
	matches := directMediaURLRe.FindAllString(text, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, raw := range matches {
		mediaURL := html.UnescapeString(raw)
		mediaURL = strings.TrimSpace(mediaURL)
		if mediaURL == "" || seen[mediaURL] {
			continue
		}
		seen[mediaURL] = true
		out = append(out, mediaURL)
	}
	return out
}

func wcoMediaSource(mediaURL, referer, cookieHeader, qualityLabel string) provider.MediaSource {
	sourceType := mediaSourceType(mediaURL)
	quality := "[WCO] " + qualityLabel
	if qualityLabel == "" {
		quality = "[WCO] HD"
		if sourceType == "hls" {
			quality = "[WCO] HLS"
		}
	}
	return provider.MediaSource{
		URL:          mediaURL,
		Quality:      quality,
		Type:         sourceType,
		Referer:      referer,
		UserAgent:    config.AndroidUA(),
		CookieHeader: cookieHeader,
	}
}

func appendUniqueMediaSource(sources []provider.MediaSource, source provider.MediaSource) []provider.MediaSource {
	if source.URL == "" {
		return sources
	}
	for _, existing := range sources {
		if existing.URL == source.URL {
			return sources
		}
	}
	return append(sources, source)
}

func (c *Client) decodeObfuscatedIframeHTML(episodeHTML string) string {
	m := obfuscatedIframeRe.FindStringSubmatch(episodeHTML)
	if len(m) < 5 {
		return ""
	}
	arrContent := m[3]

	var sub int
	fmt.Sscanf(m[4], "%d", &sub)

	chunks := chunksRe.FindAllStringSubmatch(arrContent, -1)
	if len(chunks) == 0 {
		return ""
	}

	var out []rune
	for _, chunk := range chunks {
		decoded, err := base64.StdEncoding.DecodeString(chunk[1])
		if err != nil {
			continue
		}
		digits := nonDigitRe.ReplaceAllString(string(decoded), "")
		if digits == "" {
			continue
		}
		var val int
		fmt.Sscanf(digits, "%d", &val)
		out = append(out, rune(val-sub))
	}

	raw := string(out)
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		return raw
	}
	return decoded
}

func (c *Client) extractIframeSrcFromHTML(htmlText string) string {
	m := iframeSrcRe.FindStringSubmatch(htmlText)
	if len(m) < 2 {
		return ""
	}
	href := m[1]
	if strings.HasPrefix(href, "/") {
		return config.BaseURL + href
	}
	return href
}

func (c *Client) findGetvidlinkURL(embedHTML, iframeURL string) string {
	m := getvidlinkRe.FindStringSubmatch(embedHTML)
	if len(m) >= 2 {
		return config.EmbedOrigin + m[1]
	}

	u, err := url.Parse(iframeURL)
	if err != nil {
		return ""
	}
	q := u.Query()
	fileV := q.Get("file")
	if fileV == "" {
		return ""
	}
	embed := q.Get("embed")
	if embed == "" {
		embed = "ndisk"
	}

	// We notice that fullhd=1 is usually required
	fullhd := q.Get("fullhd")
	if fullhd == "" {
		fullhd = "1"
	}

	v := strings.ReplaceAll(fileV, ".flv", ".mp4")
	v = fmt.Sprintf("%s/%s", embed, v)
	return fmt.Sprintf("%s/inc/embed/getvidlink.php?v=%s&embed=%s&fullhd=%s", config.EmbedOrigin, url.QueryEscape(v), url.QueryEscape(embed), url.QueryEscape(fullhd))
}
