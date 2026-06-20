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
	// Download all provider-supplied subtitles locally to avoid MPV remote URL issues
	downloadedProvider := s.downloadProviderSubtitles(ctx, &media)

	cacheKey := fmt.Sprintf("%d:%d:%d:%s", media.TMDBID, media.SeasonNumber, media.EpisodeNumber, media.SeriesTitle)
	s.mu.Lock()
	if tracks, ok := s.cache[cacheKey]; ok {
		s.mu.Unlock()
		return tracks, nil
	}
	s.mu.Unlock()

	// Priority 1: Use a single provider subtitle (VidKing > Cineby > first available)
	if downloadedProvider && len(media.Subtitles) > 0 {
		logging.Debugf("subtitle fetch: %d provider subs available after download, trying pickBestSubtitle", len(media.Subtitles))
		for i, t := range media.Subtitles {
			logging.Debugf("subtitle fetch:   sub[%d] label=%q path=%q url=%q", i, t.Label, t.Path, t.URL)
		}
		if track, ok := s.pickBestSubtitle(media.Subtitles); ok {
			track.Label = "English"
			tracks := []model.SubtitleTrack{track}
			logging.Debugf("subtitle fetch: selected provider sub path=%q", track.Path)
			s.mu.Lock()
			s.cache[cacheKey] = tracks
			s.mu.Unlock()
			return tracks, nil
		}
		logging.Debugf("subtitle fetch: pickBestSubtitle found nothing")
	}

	query := strings.TrimSpace(media.SeriesTitle)
	if query == "" {
		query = strings.TrimSpace(media.EpisodeTitle)
	}

	// Priority 2: Try OpenSubtitles
	if s.openSubtitles != nil && s.openSubtitles.Configured() {
		track, found, err := s.openSubtitles.FetchBestSubtitle(ctx, query, media.TMDBID, media.SeasonNumber, media.EpisodeNumber)
		if err == nil && found {
			track.Label = "English"
			tracks := []model.SubtitleTrack{track}
			s.mu.Lock()
			s.cache[cacheKey] = tracks
			s.mu.Unlock()
			return tracks, nil
		}
		if err != nil {
			logging.Debugf("opensubtitles: %v", err)
		}
	}

	// Priority 3: Try YIFY
	{
		tracks, err := s.fetchYify(ctx, media)
		if err == nil && len(tracks) > 0 {
			tracks[0].Label = "English"
			s.mu.Lock()
			s.cache[cacheKey] = tracks
			s.mu.Unlock()
			return tracks, nil
		}
		if err != nil {
			logging.Debugf("yify: %v", err)
		}
	}

	// Fallback to provider subtitles if downloaded
	if downloadedProvider && len(media.Subtitles) > 0 {
		if track, ok := s.pickBestSubtitle(media.Subtitles); ok {
			track.Label = "English"
			tracks := []model.SubtitleTrack{track}
			s.mu.Lock()
			s.cache[cacheKey] = tracks
			s.mu.Unlock()
			return tracks, nil
		}
	}

	return nil, fmt.Errorf("no subtitles found")
}

func (s *SubtitleService) pickBestSubtitle(tracks []model.SubtitleTrack) (model.SubtitleTrack, bool) {
	for _, t := range tracks {
		if t.Path != "" {
			return t, true
		}
	}
	return model.SubtitleTrack{}, false
}

func (s *SubtitleService) downloadProviderSubtitles(ctx context.Context, media *model.ResolvedMedia) bool {
	downloaded := false
	for i, sub := range media.Subtitles {
		if sub.URL != "" && sub.Path == "" {
			if localPath, err := s.downloadProviderSubtitle(ctx, sub.URL, sub.Referer); err == nil {
				media.Subtitles[i].Path = localPath
				media.Subtitles[i].URL = ""
				downloaded = true
			} else {
				logging.Warnf("failed to download provider subtitle %s: %v", sub.URL, err)
			}
		}
	}
	return downloaded
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

func (s *SubtitleService) downloadProviderSubtitle(ctx context.Context, subURL string, referer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, subURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

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
	if len(data) == 0 {
		return "", fmt.Errorf("empty subtitle file")
	}

	subDir := filepath.Join(os.TempDir(), "kari-subs")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		return "", err
	}

	ext := detectSubtitleExt(subURL)

	filename := fmt.Sprintf("provider_sub_%d%s", time.Now().UnixNano(), ext)
	localPath := filepath.Join(subDir, filename)

	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		return "", err
	}

	return localPath, nil
}

func detectSubtitleExt(subURL string) string {
	parsed, err := url.Parse(subURL)
	if err != nil {
		return ".vtt"
	}
	path := strings.ToLower(parsed.Path)
	if strings.HasSuffix(path, ".srt") {
		return ".srt"
	}
	if strings.HasSuffix(path, ".ass") {
		return ".ass"
	}
	return ".vtt"
}
