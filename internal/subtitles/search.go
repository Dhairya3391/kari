package subtitles

import (
	"bytes"
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

	"kari/internal/lang"
	"kari/internal/model"
)

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

// Search queries OpenSubtitles for subtitle entries matching the title or
// TMDB ID and preferred language.
func (c *Client) Search(ctx context.Context, query, language string, tmdbID, season, episode int) ([]searchEntry, error) {
	if language == "" {
		language = "en"
	}
	osLog.Debug("search start", "query", query, "language", language, "tmdbID", tmdbID, "season", season, "episode", episode)
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("languages", language)
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
		if strings.EqualFold(r.Attributes.Language, language) {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) == 0 {
		filtered = results
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Attributes.DownloadCount > filtered[j].Attributes.DownloadCount
	})
	osLog.Debug("search done", "results", len(filtered))
	return filtered, nil
}

type downloadRequest struct {
	FileID int `json:"file_id"`
}

type downloadResponse struct {
	Link     string `json:"link"`
	FileName string `json:"file_name"`
}

// Download materializes a subtitle file into the cache dir and returns its
// local path.
func (c *Client) Download(ctx context.Context, fileID int) (string, error) {
	osLog.Debug("download start", "fileID", fileID)
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

	osLog.Debug("resolving file link", "link", dr.Link)

	rawData, err := c.downloadFile(ctx, dr.Link)
	if err != nil {
		return "", fmt.Errorf("opensubtitles file download: %w", err)
	}

	processedData, detectedFormat := ProcessSubtitleData(rawData)

	subDir, err := CacheDir()
	if err != nil {
		return "", fmt.Errorf("opensubtitles download mkdir: %w", err)
	}

	localPath := filepath.Join(subDir, fmt.Sprintf("sub_%d.srt", fileID))

	if err := os.WriteFile(localPath, processedData, 0o644); err != nil {
		return "", fmt.Errorf("opensubtitles file write: %w", err)
	}

	osLog.Info("subtitle downloaded", "path", localPath, "format", detectedFormat)
	return localPath, nil
}

// FetchBestSubtitle combines Search + scoring + Download and returns the
// highest-scoring subtitle track materialized on disk.
func (c *Client) FetchBestSubtitle(ctx context.Context, query, language string, tmdbID, season, episode int) (model.SubtitleTrack, bool, error) {
	if language == "" {
		language = "en"
	}
	osLog.Debug("best-subtitle search start", "query", query, "language", language, "tmdbID", tmdbID, "season", season, "episode", episode)
	results, err := c.Search(ctx, query, language, tmdbID, season, episode)
	if err != nil {
		return model.SubtitleTrack{}, false, err
	}
	if len(results) == 0 {
		osLog.Debug("best-subtitle search found no results")
		return model.SubtitleTrack{}, false, nil
	}

	normalizedTitle := normalizeForSearch(query)
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

		releaseLower := strings.ToLower(entry.Attributes.Release)
		normalizedRelease := normalizeForSearch(entry.Attributes.Release)

		if strings.Contains(normalizedRelease, normalizedTitle) {
			score += 50
		}

		if strings.EqualFold(releaseLower, strings.ToLower(query)) {
			score += 100
		}

		if episode > 0 && season > 0 {
			releaseEp := parseEpisodeFromRelease(entry.Attributes.Release)
			if releaseEp == episode {
				score += 30
			} else if releaseEp > 0 && releaseEp != episode {
				score -= 40
			}
		}

		countScore := entry.Attributes.DownloadCount / 200
		if countScore > 30 {
			countScore = 30
		}
		score += countScore

		if best == nil || score > bestScore {
			best = entry
			bestScore = score
		}
	}

	threshold := 40
	if tmdbID > 0 {
		threshold = 10
	}

	if best == nil || len(best.Attributes.Files) == 0 || bestScore < threshold {
		osLog.Debug("no suitable subtitle above score threshold", "bestScore", bestScore, "threshold", threshold)
		return model.SubtitleTrack{}, false, nil
	}

	osLog.Debug("best subtitle selected", "release", best.Attributes.Release, "score", bestScore)
	fileID := best.Attributes.Files[0].FileID
	localPath, err := c.Download(ctx, fileID)
	if err != nil {
		return model.SubtitleTrack{}, false, err
	}

	track := model.SubtitleTrack{
		Label:    fmt.Sprintf("%s (OpenSubtitles)", lang.Name(language)),
		Language: lang.Normalize(language),
		Path:     localPath,
		Default:  true,
	}
	return track, true, nil
}

func normalizeForSearch(s string) string {
	s = strings.ToLower(s)
	replacer := strings.NewReplacer(
		".", " ", "-", " ", "_", " ", "[", " ", "]", " ",
		"!", " ", "?", " ", "(", " ", ")", " ", "'", " ", "\"", " ",
	)
	s = replacer.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

var episodePattern = regexp.MustCompile(`(?i)(?:s\d+[._-]?e|e(?:p)?)(\d+)`)
var seasonEpisodePattern = regexp.MustCompile(`(?i)S(\d+)[._-]E(\d+)`)
var bareEpisodePattern = regexp.MustCompile(`(?i)(?:^|[\s._-])E(\d+)(?:[\s._-]|$)`)

func parseEpisodeFromRelease(release string) int {
	if m := seasonEpisodePattern.FindStringSubmatch(release); len(m) == 3 {
		if ep, err := strconv.Atoi(m[2]); err == nil {
			return ep
		}
	}
	if m := episodePattern.FindStringSubmatch(release); len(m) >= 2 {
		if ep, err := strconv.Atoi(m[1]); err == nil {
			return ep
		}
	}
	if m := bareEpisodePattern.FindStringSubmatch(release); len(m) >= 2 {
		if ep, err := strconv.Atoi(m[1]); err == nil {
			return ep
		}
	}
	return 0
}
