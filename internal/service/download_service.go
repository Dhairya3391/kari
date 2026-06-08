package service

import (
	"context"
	"fmt"
	"strings"

	"kari/internal/downloader"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
)

// DownloadService orchestrates download operations.
type DownloadService struct {
	downloadDir string
	downloaders []downloader.Downloader
}

// NewDownloadService constructs a DownloadService.
func NewDownloadService(downloadDir string, downloaders []downloader.Downloader) *DownloadService {
	return &DownloadService{downloadDir: downloadDir, downloaders: append([]downloader.Downloader(nil), downloaders...)}
}

// Download starts a download for the resolved media.
func (s *DownloadService) Download(ctx context.Context, resolved model.ResolvedMedia, progress func(float64)) error {
	title := resolved.SeriesTitle
	if ep := strings.TrimSpace(resolved.EpisodeTitle); ep != "" && ep != title {
		title += " - " + ep
	}

	var firstSource provider.MediaSource
	var firstResolver string
	if len(resolved.Playback) > 0 {
		p := resolved.Playback[0]
		firstSource = provider.MediaSource{
			URL:          p.URL,
			Quality:      p.Label,
			Referer:      p.Referer,
			Type:         p.Type,
			UserAgent:    p.UserAgent,
			CookieHeader: p.CookieHeader,
		}
		firstResolver = p.Resolver
		if firstSource.URL == "" {
			return fmt.Errorf("no playback URL")
		}
	}

	req := downloader.DownloadRequest{
		Title:     title,
		OutputDir: s.downloadDir,
		Sources:   make([]provider.MediaSource, 0, len(resolved.Playback)),
		Progress:  progress,
	}
	for _, p := range resolved.Playback {
		req.Sources = append(req.Sources, provider.MediaSource{
			URL:          p.URL,
			Quality:      p.Label,
			Referer:      p.Referer,
			Type:         p.Type,
			UserAgent:    p.UserAgent,
			CookieHeader: p.CookieHeader,
		})
	}

	logging.Debugf("download service: start download title=%q", title)
	dl := s.selectDownloader(firstSource, firstResolver)
	return dl.Download(ctx, req)
}

// CleanupPartial cleans up partial download files.
func (s *DownloadService) CleanupPartial(resolver, title string) {
	dl := s.selectDownloader(provider.MediaSource{}, resolver)
	dl.CleanupPartial(s.downloadDir, title)
}

func (s *DownloadService) selectDownloader(source provider.MediaSource, resolver string) downloader.Downloader {
	for _, dl := range s.downloaders {
		if dl != nil && dl.Accepts(source, resolver) {
			return dl
		}
	}
	return downloader.NewYTDLPDownloader()
}
