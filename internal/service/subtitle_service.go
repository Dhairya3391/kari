package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/subtitles"
	"kari/internal/tmdb"
)

type SubtitleService struct {
	openSubtitles *subtitles.Client
	yify          *subtitles.YifyClient
	httpClient    *http.Client
	keyPool       *tmdb.KeyPool
	downloadDir   string
	cache         map[string][]model.SubtitleTrack
	mu            sync.Mutex
}

func NewSubtitleService(cfg *config.Config) *SubtitleService {
	if cfg == nil {
		cfg = &config.Config{DownloadDir: "./downloads"}
	}
	var openSubtitles *subtitles.Client
	if strings.TrimSpace(cfg.OpenSubtitlesKey) != "" && strings.TrimSpace(cfg.OpenSubtitlesUser) != "" && strings.TrimSpace(cfg.OpenSubtitlesPass) != "" {
		openSubtitles = subtitles.NewClient(cfg.OpenSubtitlesKey, cfg.OpenSubtitlesUser, cfg.OpenSubtitlesPass)
	}
	downloadDir := strings.TrimSpace(cfg.DownloadDir)
	if downloadDir == "" {
		downloadDir = "./downloads"
	}
	return &SubtitleService{
		openSubtitles: openSubtitles,
		yify:          subtitles.NewYifyClient(),
		httpClient:    httpclient.New(),
		keyPool:       tmdb.NewKeyPool(cfg.TMDBAPIKeys),
		downloadDir:   downloadDir,
		cache:         make(map[string][]model.SubtitleTrack),
	}
}

func (s *SubtitleService) Fetch(ctx context.Context, media model.ResolvedMedia) ([]model.SubtitleTrack, error) {
	// Priority 1: Use provider-supplied subtitles for anime/cartoon
	if (media.MediaType == "anime" || media.MediaType == "cartoon") && len(media.Subtitles) > 0 {
		// Download them locally to prevent MPV remote URL issues
		for i, sub := range media.Subtitles {
			if sub.URL != "" && sub.Path == "" {
				if localPath, err := s.downloadProviderSubtitle(ctx, sub.URL); err == nil {
					media.Subtitles[i].Path = localPath
					media.Subtitles[i].URL = "" // Clear URL to avoid confusion
				} else {
					logging.Warnf("failed to download provider subtitle %s: %v", sub.URL, err)
				}
			}
		}
		return nil, nil // TUI will use existing media.Subtitles
	}

	cacheKey := fmt.Sprintf("%d:%d:%d:%s", media.TMDBID, media.SeasonNumber, media.EpisodeNumber, media.SeriesTitle)
	s.mu.Lock()
	if tracks, ok := s.cache[cacheKey]; ok {
		s.mu.Unlock()
		return tracks, nil
	}
	s.mu.Unlock()

	query := strings.TrimSpace(media.SeriesTitle)
	if query == "" {
		query = strings.TrimSpace(media.EpisodeTitle)
	}

	var openErr error
	var finalTracks []model.SubtitleTrack
	if s.openSubtitles != nil && s.openSubtitles.Configured() {
		track, found, err := s.openSubtitles.FetchBestSubtitle(ctx, query, media.TMDBID, media.SeasonNumber, media.EpisodeNumber)
		if err == nil && found {
			finalTracks = []model.SubtitleTrack{track}
			goto done
		}
		openErr = err
	}

	{
		tracks, err := s.fetchYify(ctx, media)
		if err == nil && len(tracks) > 0 {
			finalTracks = tracks
			goto done
		}
		if err != nil {
			if openErr != nil {
				return nil, fmt.Errorf("opensubtitles: %w; yify: %w", openErr, err)
			}
			return nil, err
		}
	}

	if openErr != nil {
		return nil, openErr
	}

done:
	s.mu.Lock()
	s.cache[cacheKey] = finalTracks
	s.mu.Unlock()
	return finalTracks, nil
}

func (s *SubtitleService) fetchYify(ctx context.Context, media model.ResolvedMedia) ([]model.SubtitleTrack, error) {
	imdbID, err := s.getIMDBID(ctx, media.TMDBID, media.MediaType)
	if err != nil {
		return nil, err
	}
	if imdbID == "" {
		return nil, nil
	}

	data, err := s.yify.GetEnglishSubtitle(ctx, imdbID)
	if err != nil {
		if isYifyNoResult(err) {
			return nil, nil
		}
		return nil, err
	}

	path, err := s.yify.SaveSubtitle(data, media.SeriesTitle, s.downloadDir)
	if err != nil {
		return nil, err
	}

	return []model.SubtitleTrack{{
		Label:    "English (Yify)",
		Language: "en",
		Path:     path,
	}}, nil
}

type tmdbExternalIDs struct {
	IMDBID string `json:"imdb_id"`
}

func (s *SubtitleService) getIMDBID(ctx context.Context, tmdbID int, mediaType string) (string, error) {
	if s.keyPool == nil {
		return "", fmt.Errorf("tmdb key pool is required")
	}
	media := "tv"
	switch mediaType {
	case "movie":
		media = "movie"
	case "anime":
		media = "tv"
	case "cartoon":
		media = "tv"
	}

	var lastAuthErr error
	for {
		apiKey, err := s.keyPool.NextKey()
		if err != nil {
			if lastAuthErr != nil {
				return "", fmt.Errorf("get imdb id: %w", lastAuthErr)
			}
			return "", err
		}
		target := fmt.Sprintf("%s/%s/%d/external_ids?api_key=%s", config.TMDBAPIBase, media, tmdbID, url.QueryEscape(apiKey))
		ids, err := s.fetchTMDBExternalIDs(ctx, target)
		if err == nil {
			return ids.IMDBID, nil
		}
		var httpErr *provider.HTTPError
		if errors.As(err, &httpErr) && (httpErr.Code == http.StatusUnauthorized || httpErr.Code == http.StatusForbidden || httpErr.Code == http.StatusTooManyRequests) {
			s.keyPool.MarkFailed(apiKey)
			lastAuthErr = err
			continue
		}
		return "", err
	}
}

func (s *SubtitleService) fetchTMDBExternalIDs(ctx context.Context, target string) (tmdbExternalIDs, error) {
	var ids tmdbExternalIDs
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ids, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return ids, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return ids, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return ids, err
	}
	if err := json.Unmarshal(raw, &ids); err != nil {
		return ids, err
	}
	return ids, nil
}

func isYifyNoResult(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no english subtitle found") || strings.Contains(msg, "subtitle file not found") || strings.Contains(msg, "no srt file found")
}

func (s *SubtitleService) downloadProviderSubtitle(ctx context.Context, subURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, subURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	subDir := filepath.Join(os.TempDir(), "kari-subs")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		return "", err
	}

	ext := ".vtt"
	if strings.HasSuffix(strings.ToLower(subURL), ".srt") {
		ext = ".srt"
	} else if strings.HasSuffix(strings.ToLower(subURL), ".ass") {
		ext = ".ass"
	}

	// Create a unique filename
	filename := fmt.Sprintf("provider_sub_%d%s", time.Now().UnixNano(), ext)
	localPath := filepath.Join(subDir, filename)

	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		return "", err
	}

	return localPath, nil
}
