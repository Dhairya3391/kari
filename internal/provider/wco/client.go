package wco

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"

	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

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
			logging.Warnf("failed to close temp file: %v", err)
		}
		if err := os.Remove(tmp.Name()); err != nil && !os.IsNotExist(err) {
			logging.Warnf("failed to remove temp file: %v", err)
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
