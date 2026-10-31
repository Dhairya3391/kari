package service

import (
	"context"
	"fmt"
	"kari/internal/downloader"
	"kari/internal/provider"
)

func (s *DownloadService) BatchDownload(
	ctx context.Context,
	series provider.SearchResult,
	episodes []provider.Episode,
	mode provider.ContentType,
	qualityMode int,
	languages map[string]bool,
	onProgress func(current, total int, epTitle string, dp downloader.DownloadProgress),
) []BatchDownloadResult {
	return s.BatchDownloadWithActive(ctx, series, episodes, mode, qualityMode, languages, nil, onProgress)
}

// BatchDownloadWithActive is BatchDownload plus onActive, which fires when
// each episode's output location is known — before its download starts — so
// callers can cancel or clean up exactly the files being written.
func (s *DownloadService) BatchDownloadWithActive(
	ctx context.Context,
	series provider.SearchResult,
	episodes []provider.Episode,
	mode provider.ContentType,
	qualityMode int,
	languages map[string]bool,
	onActive func(outputDir, title string),
	onProgress func(current, total int, epTitle string, dp downloader.DownloadProgress),
) []BatchDownloadResult {
	// Each episode still resolves and downloads in order, so per-file
	// parallel connections (aria2c splits / yt-dlp fragments) remain the
	// speed lever.
	results := make([]BatchDownloadResult, len(episodes))
	reportProgress := func(cur, tot int, title string, dp downloader.DownloadProgress) {
		if onProgress != nil {
			onProgress(cur, tot, title, dp)
		}
	}

	for i, ep := range episodes {
		results[i].Episode = ep
		if ctx.Err() != nil {
			results[i].Err = ctx.Err()
			continue
		}

		if onActive != nil {
			onActive("", "")
		}

		current := i + 1
		epTitle := episodeResultTitle(ep)
		batchLog.Debug("batch episode starting", "current", current, "total", len(episodes), "episode", epTitle)
		reportProgress(current, len(episodes), epTitle, downloader.DownloadProgress{Percent: 0})

		resolved, err := s.mediaService.Resolve(ctx, mode, series, ep, nil, ResolveOptions{})
		if err != nil {
			reportProgress(current, len(episodes), epTitle, downloader.DownloadProgress{Percent: 1.0})
			results[i].Episode = ep
			results[i].Err = fmt.Errorf("resolve %s: %w", epTitle, err)
			continue
		}
		filtered := FilterPlaybackSources(resolved.Playback, qualityMode, languages)
		if len(filtered) > 0 {
			resolved.Playback = filtered
		} else {
			// If no source matches requested quality tier, fall back to language-filtered or any available sources
			langOnly := FilterPlaybackSources(resolved.Playback, 0, languages)
			if len(langOnly) > 0 {
				resolved.Playback = langOnly
			}
		}
		if len(resolved.Playback) == 0 {
			reportProgress(current, len(episodes), epTitle, downloader.DownloadProgress{Percent: 1.0})
			results[i].Err = fmt.Errorf("filter %s: no playback source available", epTitle)
			continue
		}

		if onActive != nil {
			outputDir, title := s.OrganizedPath(resolved)
			onActive(outputDir, title)
		}
		if err := s.Download(ctx, resolved, func(dp downloader.DownloadProgress) {
			reportProgress(current, len(episodes), epTitle, dp)
		}); err != nil {
			reportProgress(current, len(episodes), epTitle, downloader.DownloadProgress{Percent: 1.0})
			results[i].Err = fmt.Errorf("download %s: %w", epTitle, err)
			continue
		}

		reportProgress(current, len(episodes), epTitle, downloader.DownloadProgress{Percent: 1.0})
	}

	return results
}

func episodeResultTitle(ep provider.Episode) string {
	if ep.Season > 0 && ep.Episode > 0 {
		return fmt.Sprintf("S%02dE%02d", ep.Season, ep.Episode)
	}
	if ep.Episode > 0 {
		return fmt.Sprintf("E%02d", ep.Episode)
	}
	if ep.Title != "" {
		return ep.Title
	}
	return "Unknown"
}
