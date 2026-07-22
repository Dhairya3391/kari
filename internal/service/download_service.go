package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kari/internal/downloader"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
)

type DownloadService struct {
	downloadDir  string
	dl           downloader.Downloader
	mediaService *MediaService
}

func NewDownloadService(downloadDir string, dl downloader.Downloader, mediaService *MediaService) *DownloadService {
	return &DownloadService{
		downloadDir:  downloadDir,
		dl:           dl,
		mediaService: mediaService,
	}
}

func sanitizePathName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		case '\n', '\r', '\t':
			return ' '
		}
		if r < 32 {
			return -1
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(strings.Join(strings.Fields(cleaned), " "))
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "Unknown"
	}
	return cleaned
}

func (s *DownloadService) OrganizedPath(resolved model.ResolvedMedia) (outputDir, title string) {
	seriesDir := sanitizePathName(resolved.SeriesTitle)
	outputDir = filepath.Join(s.downloadDir, seriesDir)
	if resolved.SeasonNumber > 0 && resolved.MediaType != "movie" {
		outputDir = filepath.Join(outputDir, fmt.Sprintf("Season %02d", resolved.SeasonNumber))
	}

	epTitle := strings.TrimSpace(resolved.EpisodeTitle)
	if epTitle == resolved.SeriesTitle || epTitle == "" {
		epTitle = ""
	}

	switch {
	case resolved.SeasonNumber > 0 && resolved.EpisodeNumber > 0 && resolved.MediaType != "movie":
		tag := fmt.Sprintf("S%02dE%02d", resolved.SeasonNumber, resolved.EpisodeNumber)
		title = tag
		if epTitle != "" {
			title += " - " + epTitle
		}
	case resolved.EpisodeNumber > 0:
		title = fmt.Sprintf("E%02d", resolved.EpisodeNumber)
		if epTitle != "" {
			title += " - " + epTitle
		}
	default:
		title = epTitle
		if title == "" {
			title = resolved.SeriesTitle
		}
		if title == "" {
			title = "download"
		}
	}
	return
}

func (s *DownloadService) Download(ctx context.Context, resolved model.ResolvedMedia, progress func(downloader.DownloadProgress)) error {
	outputDir, title := s.OrganizedPath(resolved)
	return s.downloadMedia(ctx, resolved, outputDir, title, progress)
}

func (s *DownloadService) downloadMedia(ctx context.Context, resolved model.ResolvedMedia, outputDir, title string, progress func(downloader.DownloadProgress)) error {
	if len(resolved.Playback) == 0 || resolved.Playback[0].URL == "" {
		return fmt.Errorf("no playback URL")
	}

	req := downloader.DownloadRequest{
		Title:     title,
		OutputDir: outputDir,
		Sources:   make([]provider.MediaSource, 0, len(resolved.Playback)),
		Progress:  progress,
	}
	for _, p := range resolved.Playback {
		req.Sources = append(req.Sources, provider.MediaSource{
			URL:          p.URL,
			Quality:      p.Label,
			Resolver:     p.Resolver,
			Referer:      p.Referer,
			Type:         p.Type,
			UserAgent:    p.UserAgent,
			CookieHeader: p.CookieHeader,
		})
	}

	logging.Debugf("download service: start title=%q outputDir=%q", title, outputDir)
	return s.dl.Download(ctx, req)
}

func (s *DownloadService) CleanupPartial(outputDir, title string) {
	s.dl.CleanupPartial(outputDir, title)
	removeEmptyDirs(outputDir, s.downloadDir)
}

// removeEmptyDirs removes outputDir and any parent directories up to (but not
// including) rootDir if they are empty after cleanup.
func removeEmptyDirs(dir, rootDir string) {
	if dir == "" || rootDir == "" {
		return
	}
	dir = filepath.Clean(dir)
	rootDir = filepath.Clean(rootDir)
	for dir != rootDir && strings.HasPrefix(dir, rootDir) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		parent := filepath.Dir(dir)
		os.Remove(dir)
		dir = parent
	}
}

type BatchDownloadResult struct {
	Episode model.EpisodeResult
	Err     error
}

func (s *DownloadService) BatchDownload(
	ctx context.Context,
	series model.SearchResult,
	episodes []model.EpisodeResult,
	mode provider.ContentType,
	qualityMode int,
	languages map[string]bool,
	onProgress func(current, total int, epTitle string, epProgress float64),
) []BatchDownloadResult {
	results := make([]BatchDownloadResult, len(episodes))

	for i, ep := range episodes {
		if ctx.Err() != nil {
			results[i].Episode = ep
			results[i].Err = ctx.Err()
			continue
		}

		current := i + 1
		epTitle := episodeResultTitle(ep)
		logging.Debugf("batch download: starting episode %d/%d: %s", current, len(episodes), epTitle)
		onProgress(current, len(episodes), epTitle, 0)

		resolved, err := s.mediaService.Resolve(ctx, mode, series, ep, nil)
		if err != nil {
			onProgress(current, len(episodes), epTitle, 1.0)
			results[i].Episode = ep
			results[i].Err = fmt.Errorf("resolve %s: %w", epTitle, err)
			continue
		}
		resolved.Playback = FilterPlaybackSources(resolved.Playback, qualityMode, languages)
		if len(resolved.Playback) == 0 {
			onProgress(current, len(episodes), epTitle, 1.0)
			results[i].Episode = ep
			results[i].Err = fmt.Errorf("filter %s: no playback source matches the current filters", epTitle)
			continue
		}

		if err := s.Download(ctx, resolved, func(dp downloader.DownloadProgress) {
			onProgress(current, len(episodes), epTitle, dp.Percent)
		}); err != nil {
			onProgress(current, len(episodes), epTitle, 1.0)
			results[i].Episode = ep
			results[i].Err = fmt.Errorf("download %s: %w", epTitle, err)
			continue
		}

		onProgress(current, len(episodes), epTitle, 1.0)
		results[i].Episode = ep
	}

	return results
}

func episodeResultTitle(ep model.EpisodeResult) string {
	if ep.Season > 0 && ep.Number > 0 {
		return fmt.Sprintf("S%02dE%02d", ep.Season, ep.Number)
	}
	if ep.Number > 0 {
		return fmt.Sprintf("E%02d", ep.Number)
	}
	if ep.Title != "" {
		return ep.Title
	}
	return "Unknown"
}
