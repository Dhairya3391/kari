package subtitles

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
)

const yifyBase = config.YifyBase

type YifyClient struct {
	http *http.Client
}

func NewYifyClient() *YifyClient {
	return &YifyClient{
		http: httpclient.New(),
	}
}

type yifySubtitle struct {
	href   string
	lang   string
	rating string
}

func (c *YifyClient) FetchSubtitles(ctx context.Context, imdbID string) ([]yifySubtitle, error) {
	searchURL := fmt.Sprintf("%s/movie-imdb/%s", yifyBase, imdbID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("yify request: %w", err)
	}
	req.Header.Set("User-Agent", config.YifyUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yify fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("yify status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("yify read: %w", err)
	}

	return parseYifyTable(string(body)), nil
}

func parseYifyTable(content string) []yifySubtitle {
	var subs []yifySubtitle

	startIdx := strings.Index(content, `class="table other-subs"`)
	if startIdx == -1 {
		return subs
	}
	tableEnd := strings.Index(content[startIdx:], "</table>")
	if tableEnd == -1 {
		return subs
	}
	table := content[startIdx : startIdx+tableEnd]

	rowStart := 0
	for {
		rowIdx := strings.Index(table[rowStart:], `<tr data-id="`)
		if rowIdx == -1 {
			break
		}
		rowStart += rowIdx

		rowEnd := strings.Index(table[rowStart:], "</tr>")
		if rowEnd == -1 {
			break
		}
		row := table[rowStart : rowStart+rowEnd]

		lang := extractYifyField(row, "sub-lang\">", "</span>")
		href := extractYifyLink(row)
		rating := extractYifyField(row, `class="label">`, "</span>")

		if lang != "" && href != "" {
			if rating == "" {
				rating = "0"
			}
			subs = append(subs, yifySubtitle{href: href, lang: lang, rating: rating})
		}
		rowStart += rowEnd + 5
	}

	return subs
}

func extractYifyField(s, start, end string) string {
	idx := strings.Index(s, start)
	if idx == -1 {
		return ""
	}
	startPos := idx + len(start)
	endPos := strings.Index(s[startPos:], end)
	if endPos == -1 {
		return ""
	}
	if startPos+endPos > len(s) {
		return ""
	}
	return s[startPos : startPos+endPos]
}

func extractYifyLink(s string) string {
	idx := strings.Index(s, `href="/subtitles/`)
	if idx == -1 {
		return ""
	}
	startPos := idx + len(`href="/subtitles/`)
	endPos := strings.Index(s[startPos:], `"`)
	if endPos == -1 {
		return ""
	}
	if startPos+endPos > len(s) {
		return ""
	}
	return s[startPos : startPos+endPos]
}

func (c *YifyClient) GetEnglishSubtitle(ctx context.Context, imdbID string) ([]byte, error) {
	subs, err := c.FetchSubtitles(ctx, imdbID)
	if err != nil {
		return nil, err
	}

	var englishSub string
	for _, s := range subs {
		if s.lang == "English" {
			englishSub = s.href
			break
		}
	}

	if englishSub == "" {
		return nil, fmt.Errorf("no English subtitle found")
	}

	downloadURL := fmt.Sprintf("%s/subtitle/%s.zip", yifyBase, englishSub)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("yify download request: %w", err)
	}
	req.Header.Set("User-Agent", config.YifyUserAgent)
	req.Header.Set("Referer", fmt.Sprintf("%s/movie-imdb/%s", yifyBase, imdbID))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yify download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("yify download status: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func (c *YifyClient) SaveSubtitle(data []byte, movieTitle, targetDir string) (string, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("yify zip open: %w", err)
	}

	var srtFile string
	for _, f := range zipReader.File {
		name := filepath.Base(f.Name)
		if strings.HasSuffix(strings.ToLower(name), ".srt") {
			srtFile = name
			break
		}
	}

	if srtFile == "" {
		return "", fmt.Errorf("no srt file found in zip")
	}

	for _, f := range zipReader.File {
		if filepath.Base(f.Name) != srtFile {
			continue
		}
		src, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("yify zip open file: %w", err)
		}
		defer src.Close()

		safeTitle := strings.TrimSpace(movieTitle)
		safeTitle = strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(safeTitle)
		outPath := filepath.Join(targetDir, safeTitle+".srt")

		dst, err := os.Create(outPath)
		if err != nil {
			return "", fmt.Errorf("yify write: %w", err)
		}
		defer dst.Close()

		_, err = io.Copy(dst, src)
		if err != nil {
			return "", fmt.Errorf("yify copy: %w", err)
		}

		logging.Debugf("yify: saved subtitle to %s", outPath)
		return outPath, nil
	}

	return "", fmt.Errorf("subtitle file not found")
}
