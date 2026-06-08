package subtitles

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/model"
)

const (
	apiBase   = config.OpenSubtitlesAPI
	userAgent = config.OpenSubtitlesUA
)

type Client struct {
	apiKey   string
	username string
	password string

	token       string
	tokenExpiry time.Time
	tokenMu     sync.Mutex
	http        *http.Client
}

func NewClient(apiKey, username, password string) *Client {
	return &Client{
		apiKey:   apiKey,
		username: username,
		password: password,
		http:     httpclient.NewWithUserAgent(userAgent),
	}
}

func (c *Client) Configured() bool {
	return c.apiKey != "" && c.username != "" && c.password != ""
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token  string `json:"token"`
	Status int    `json:"status"`
}

type tokenCache struct {
	Token  string    `json:"token"`
	Expiry time.Time `json:"expiry"`
}

func (c *Client) ensureToken(ctx context.Context) error {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		return nil
	}

	// Try loading from disk
	if cachePath, err := resolveTokenCachePath(); err == nil {
		if data, err := os.ReadFile(cachePath); err == nil {
			var tc tokenCache
			if err := json.Unmarshal(data, &tc); err == nil {
				if time.Now().Before(tc.Expiry) {
					c.token = tc.Token
					c.tokenExpiry = tc.Expiry
					logging.Debugf("opensubtitles: loaded cached token from disk (expires %v)", tc.Expiry)
					return nil
				}
				logging.Debugf("opensubtitles: cached token expired at %v", tc.Expiry)
			} else {
				logging.Debugf("opensubtitles: failed to unmarshal cached token: %v", err)
			}
		} else if !os.IsNotExist(err) {
			logging.Debugf("opensubtitles: failed to read cached token file: %v", err)
		}
	}

	return c.login(ctx)
}

func resolveTokenCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "kari")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "os_token.json"), nil
}

func (c *Client) downloadFile(ctx context.Context, fileURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles download request creation failed: %w", err)
	}
	c.setHeaders(req, true)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	return raw, err
}

func (c *Client) login(ctx context.Context) error {
	logging.Debugf("opensubtitles login start username=%q", c.username)
	body, err := json.Marshal(loginRequest{Username: c.username, Password: c.password})
	if err != nil {
		return fmt.Errorf("opensubtitles login marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+"/login", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("opensubtitles login request: %w", err)
	}
	c.setHeaders(req, false)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("opensubtitles login: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("opensubtitles login: status %d: %s", resp.StatusCode, string(raw))
	}

	var lr loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return fmt.Errorf("opensubtitles login decode: %w", err)
	}
	if lr.Token == "" {
		return fmt.Errorf("opensubtitles login: empty token")
	}

	c.token = lr.Token
	c.tokenExpiry = time.Now().Add(23 * time.Hour) // Slightly less than 24h to be safe

	// Save to disk
	if cachePath, err := resolveTokenCachePath(); err == nil {
		tc := tokenCache{Token: c.token, Expiry: c.tokenExpiry}
		if data, err := json.Marshal(tc); err == nil {
			if err := os.WriteFile(cachePath, data, 0o600); err != nil {
				logging.Warn("opensubtitles: failed to write token cache: %v", err)
				return fmt.Errorf("opensubtitles write token cache: %w", err)
			}
		}
	}

	logging.Infof("opensubtitles: logged in as %s", c.username)
	return nil
}

type searchResponse struct {
	Data []searchEntry `json:"data"`
}

type searchEntry struct {
	Attributes subtitleAttributes `json:"attributes"`
}

type subtitleAttributes struct {
	Language      string         `json:"language"`
	DownloadCount int            `json:"download_count"`
	Format        string         `json:"format"`
	Files         []subtitleFile `json:"files"`
	Release       string         `json:"release"`
}

type subtitleFile struct {
	FileID   int    `json:"file_id"`
	FileName string `json:"file_name"`
}

func (c *Client) Search(ctx context.Context, query string, tmdbID, season, episode int) ([]searchEntry, error) {
	logging.Debugf("opensubtitles search start query=%q tmdbID=%d S%dE%d", query, tmdbID, season, episode)
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("languages", "en")
	if tmdbID > 0 {
		params.Set("tmdb_id", strconv.Itoa(tmdbID))
		if season > 0 {
			params.Set("season_number", strconv.Itoa(season))
		}
		if episode > 0 {
			params.Set("episode_number", strconv.Itoa(episode))
		}
	} else {
		params.Set("query", query)
		if season > 0 {
			params.Set("season_number", strconv.Itoa(season))
		}
		if episode > 0 {
			params.Set("episode_number", strconv.Itoa(episode))
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+"/subtitles?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles search request: %w", err)
	}
	c.setHeaders(req, true)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("opensubtitles search: status %d: %s", resp.StatusCode, string(raw))
	}

	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("opensubtitles search decode: %w", err)
	}

	results := sr.Data

	var filtered []searchEntry
	for _, r := range results {
		if strings.EqualFold(r.Attributes.Language, "en") {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) == 0 {
		filtered = results
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Attributes.DownloadCount > filtered[j].Attributes.DownloadCount
	})
	logging.Debugf("opensubtitles search done results=%d", len(filtered))
	return filtered, nil
}

type downloadRequest struct {
	FileID int `json:"file_id"`
}

type downloadResponse struct {
	Link     string `json:"link"`
	FileName string `json:"file_name"`
}

func (c *Client) Download(ctx context.Context, fileID int) (string, error) {
	logging.Debugf("opensubtitles download start fileID=%d", fileID)
	if err := c.ensureToken(ctx); err != nil {
		return "", err
	}

	body, err := json.Marshal(downloadRequest{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("opensubtitles download marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+"/download", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("opensubtitles download request: %w", err)
	}
	c.setHeaders(req, true)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("opensubtitles download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("opensubtitles download: status %d: %s", resp.StatusCode, string(raw))
	}

	var dr downloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return "", fmt.Errorf("opensubtitles download decode: %w", err)
	}
	if dr.Link == "" {
		return "", fmt.Errorf("opensubtitles download: empty link")
	}

	logging.Debugf("opensubtitles resolving file link %q", dr.Link)

	rawData, err := c.downloadFile(ctx, dr.Link)
	if err != nil {
		return "", fmt.Errorf("opensubtitles file download: %w", err)
	}

	processedData, detectedFormat := c.processSubtitleData(rawData)

	subDir := filepath.Join(os.TempDir(), "kari-subs")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		return "", fmt.Errorf("opensubtitles download mkdir: %w", err)
	}

	localPath := filepath.Join(subDir, fmt.Sprintf("sub_%d.srt", fileID))

	if err := os.WriteFile(localPath, processedData, 0o644); err != nil {
		return "", fmt.Errorf("opensubtitles file write: %w", err)
	}

	logging.Infof("opensubtitles: downloaded subtitle to %s (format: %s)", localPath, detectedFormat)
	return localPath, nil
}

func (c *Client) processSubtitleData(data []byte) ([]byte, string) {
	if len(data) < 2 {
		return data, detectFormatByContent(data)
	}

	if isGZIP(data) {
		decompressed, err := decompressGZIP(data)
		if err != nil {
			logging.Debugf("gzip decompression failed: %v", err)
			return data, "gzip-failed"
		}
		data = decompressed
		logging.Debugf("gzip decompressed, size: %d", len(data))
	}

	if isZIP(data) {
		extracted, err := extractFromZIP(data)
		if err != nil {
			logging.Debugf("zip extraction failed: %v", err)
			return data, "zip-failed"
		}
		data = extracted
		logging.Debugf("zip extracted, size: %d", len(data))
	}

	data = stripBOM(data)

	data = convertToUTF8(data)

	if isVTT(data) {
		converted, err := vttToSRT(data)
		if err != nil {
			logging.Debugf("vtt to srt conversion failed: %v", err)
			return data, "vtt-convert-failed"
		}
		logging.Debugf("converted vtt to srt")
		return converted, "srt-from-vtt"
	}

	detected := detectFormatByContent(data)
	return data, detected
}

func isGZIP(data []byte) bool {
	return len(data) >= 3 && data[0] == 0x1f && data[1] == 0x8b && data[2] == 0x08
}

func isZIP(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x50 && data[1] == 0x4b
}

func isVTT(data []byte) bool {
	if len(data) < 6 {
		return false
	}
	header := strings.TrimSpace(string(data[:min(20, len(data))]))
	return strings.HasPrefix(header, "WEBVTT")
}

func isSRT(data []byte) bool {
	if len(data) < 10 {
		return false
	}
	text := string(data[:min(100, len(data))])
	return srtTimestampPattern.MatchString(text)
}

var srtTimestampPattern = regexp.MustCompile(`^\d+[\r\n]+\d{2}:\d{2}:\d{2},\d{3}`)
var srtIndexPattern = regexp.MustCompile(`^\d+$`)

func decompressGZIP(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func extractFromZIP(data []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range reader.File {
		name := strings.ToLower(f.Name)
		if strings.HasSuffix(name, ".srt") || strings.HasSuffix(name, ".vtt") || strings.HasSuffix(name, ".ass") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				continue
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("no subtitle found in zip")
}

func stripBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		return data[3:]
	}
	return data
}

func convertToUTF8(data []byte) []byte {
	if utf8.Valid(data) {
		return data
	}

	decoded, err := charmap.ISO8859_1.NewDecoder().Bytes(data)
	if err == nil && utf8.Valid(decoded) {
		return decoded
	}

	text := string(data)
	cleaned := strings.Map(func(r rune) rune {
		if r == '\uFFFD' {
			return -1
		}
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, text)

	return []byte(cleaned)
}

func vttToSRT(data []byte) ([]byte, error) {
	text := string(data)
	text = strings.ReplaceAll(text, "WEBVTT", "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, " --> ") {
			line = strings.ReplaceAll(line, ".", ",")
		}
		lines = append(lines, line)
	}

	if len(lines) > 0 {
		lines = normalizeSRTIndices(lines)
	}

	return []byte(strings.Join(lines, "\n")), nil
}

func normalizeSRTIndices(lines []string) []string {
	var result []string
	counter := 1
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if srtIndexPattern.MatchString(line) {
			result = append(result, fmt.Sprintf("%d", counter))
			counter++
		} else {
			result = append(result, line)
		}
	}
	return result
}

func detectFormatByContent(data []byte) string {
	if isVTT(data) {
		return "vtt"
	}
	if isSRT(data) {
		return "srt"
	}
	text := string(data[:min(50, len(data))])
	if strings.Contains(text, "[Script Info]") || strings.Contains(text, "[Events]") {
		return "ass"
	}
	return "unknown"
}

func (c *Client) FetchBestSubtitle(ctx context.Context, query string, tmdbID, season, episode int) (model.SubtitleTrack, bool, error) {
	logging.Debugf("opensubtitles FetchBestSubtitle start query=%q tmdbID=%d S%dE%d", query, tmdbID, season, episode)
	results, err := c.Search(ctx, query, tmdbID, season, episode)
	if err != nil {
		return model.SubtitleTrack{}, false, err
	}
	if len(results) == 0 {
		logging.Debugf("opensubtitles FetchBestSubtitle no results found")
		return model.SubtitleTrack{}, false, nil
	}

	titleLower := strings.ToLower(query)
	var best *searchEntry
	var bestScore int

	for i := range results {
		entry := &results[i]
		if len(entry.Attributes.Files) == 0 {
			continue
		}

		score := 0

		if strings.EqualFold(entry.Attributes.Format, "srt") {
			score += 10
		}

		if strings.Contains(strings.ToLower(entry.Attributes.Release), titleLower) {
			score += 50 // Increased bonus for release title match
		}

		// Exact title match bonus
		if strings.EqualFold(entry.Attributes.Release, query) {
			score += 100
		}

		countScore := entry.Attributes.DownloadCount / 200 // More conservative download count score
		if countScore > 30 {
			countScore = 30
		}
		score += countScore

		if best == nil || score > bestScore {
			best = entry
			bestScore = score
		}
	}

	// Score threshold: reject if score is too low and we have no TMDB ID (which increases confidence)
	threshold := 40
	if tmdbID > 0 {
		threshold = 10 // Trust TMDB ID more
	}

	if best == nil || len(best.Attributes.Files) == 0 || bestScore < threshold {
		logging.Debugf("opensubtitles FetchBestSubtitle no suitable file found or score too low: bestScore=%d threshold=%d", bestScore, threshold)
		return model.SubtitleTrack{}, false, nil
	}

	logging.Debugf("opensubtitles FetchBestSubtitle selected release=%q score=%d", best.Attributes.Release, bestScore)
	fileID := best.Attributes.Files[0].FileID
	localPath, err := c.Download(ctx, fileID)
	if err != nil {
		return model.SubtitleTrack{}, false, err
	}

	track := model.SubtitleTrack{
		Label:    "English (OpenSubtitles)",
		Language: "en",
		Path:     localPath,
		Default:  true,
	}
	return track, true, nil
}

func (c *Client) setHeaders(req *http.Request, auth bool) {
	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if auth && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}
