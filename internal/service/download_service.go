package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"kari/internal/downloader"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/util"
)

// log scopes every line from this package/component.
var batchLog = logging.With("component", "service.download.batch")

// DownloadService resolves media through MediaService and hands the chosen
// sources to the downloader engine with organized output paths.
type DownloadService struct {
	downloadDir     string
	dl              downloader.Downloader
	mediaService    *MediaService
	subtitleService *SubtitleService
	subLangMu       sync.RWMutex
	subLang         string
	verifyMu        sync.RWMutex
	verifyDownloads bool
}

// NewDownloadService wires the service to a download directory, engine, and subtitle service.
func NewDownloadService(downloadDir string, dl downloader.Downloader, mediaService *MediaService, subtitleService *SubtitleService) *DownloadService {
	return &DownloadService{
		downloadDir:     downloadDir,
		dl:              dl,
		mediaService:    mediaService,
		subtitleService: subtitleService,
		subLang:         "en",
		verifyDownloads: true,
	}
}

// SetVerifyDownloads enables or disables integrity verification for completed downloads.
func (s *DownloadService) SetVerifyDownloads(verify bool) {
	s.verifyMu.Lock()
	s.verifyDownloads = verify
	s.verifyMu.Unlock()
}

// VerifyDownloads reports whether downloaded video files are checked for corruption.
func (s *DownloadService) VerifyDownloads() bool {
	s.verifyMu.RLock()
	defer s.verifyMu.RUnlock()
	return s.verifyDownloads
}

// SetSubtitleLanguage configures the preferred subtitle language for downloads.
func (s *DownloadService) SetSubtitleLanguage(lang string) {
	s.subLangMu.Lock()
	s.subLang = lang
	s.subLangMu.Unlock()
}

// SubtitleLanguage returns the preferred subtitle language for downloads.
func (s *DownloadService) SubtitleLanguage() string {
	s.subLangMu.RLock()
	defer s.subLangMu.RUnlock()
	if s.subLang == "" {
		return "en"
	}
	return s.subLang
}

// OutputDir returns the configured base download directory.
func (s *DownloadService) OutputDir() string {
	return s.downloadDir
}

// CompletedDownload represents a finished media file discovered on disk.
type CompletedDownload struct {
	Path    string
	Title   string
	Size    string
	ModTime time.Time
}

// CompletedDownloads scans the download directory for finished media files,
// excluding temporary staging directories (.tmp) and incomplete partial files,
// ordered newest first.
func (s *DownloadService) CompletedDownloads() []CompletedDownload {
	if s == nil || strings.TrimSpace(s.downloadDir) == "" {
		return nil
	}

	var completed []CompletedDownload
	knownExts := map[string]struct{}{
		".mp4":  {},
		".mkv":  {},
		".webm": {},
		".m4v":  {},
		".mov":  {},
		".avi":  {},
		".ts":   {},
	}

	err := filepath.WalkDir(s.downloadDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".tmp" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if _, ok := knownExts[ext]; !ok {
			return nil
		}

		// Incomplete download check: .aria2, .part, or .ytdl control/partial files
		// beside the target or staged in its temp directory.
		if _, err := os.Stat(path + ".aria2"); err == nil {
			return nil
		}
		if _, err := os.Stat(path + ".part"); err == nil {
			return nil
		}
		if _, err := os.Stat(path + ".ytdl"); err == nil {
			return nil
		}
		tempDir := s.tempDir(filepath.Dir(path))
		if _, err := os.Stat(filepath.Join(tempDir, entry.Name()+".aria2")); err == nil {
			return nil
		}
		if _, err := os.Stat(filepath.Join(tempDir, entry.Name()+".part")); err == nil {
			return nil
		}
		if _, err := os.Stat(filepath.Join(tempDir, entry.Name()+".ytdl")); err == nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() <= 0 {
			// A vanished or unreadable entry must not hide the rest of
			// the completed downloads.
			return nil //nolint:nilerr // intentional per-entry skip
		}

		rel, err := filepath.Rel(s.downloadDir, path)
		if err != nil {
			// WalkDir rooted at downloadDir should always yield relative
			// paths; skip rather than abort the whole listing on a path
			// that cannot be made relative.
			return nil //nolint:nilerr // intentional per-entry skip
		}

		slashRel := filepath.ToSlash(rel)
		parts := strings.Split(slashRel, "/")
		origExt := filepath.Ext(entry.Name())
		fileName := strings.TrimSuffix(entry.Name(), origExt)
		title := fileName
		if len(parts) > 1 {
			series := parts[0]
			if series != "." && series != "" && !strings.Contains(fileName, series) {
				title = fmt.Sprintf("%s — %s", series, fileName)
			}
		}

		completed = append(completed, CompletedDownload{
			Path:    path,
			Title:   title,
			Size:    formatDiskFileSize(info.Size()),
			ModTime: info.ModTime(),
		})
		return nil
	})

	if err != nil {
		logging.Debug("completed downloads scan error", "err", err)
	}

	slices.SortFunc(completed, func(a, b CompletedDownload) int {
		if a.ModTime.After(b.ModTime) {
			return -1
		}
		if a.ModTime.Before(b.ModTime) {
			return 1
		}
		return 0
	})

	return completed
}

func formatDiskFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
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

// OrganizedPath computes the destination directory and file title for a
// resolved media item, inserting Season folders for episodic content.
func (s *DownloadService) OrganizedPath(resolved model.ResolvedMedia) (outputDir, title string) {
	seriesDir := sanitizePathName(resolved.SeriesTitle)
	outputDir = filepath.Join(s.downloadDir, seriesDir)
	if resolved.SeasonNumber > 0 && resolved.MediaType != provider.MediaTypeMovie {
		outputDir = filepath.Join(outputDir, fmt.Sprintf("Season %02d", resolved.SeasonNumber))
	}

	epTitle := strings.TrimSpace(resolved.EpisodeTitle)
	if epTitle == resolved.SeriesTitle || epTitle == "" {
		epTitle = ""
	}

	switch {
	case resolved.SeasonNumber > 0 && resolved.EpisodeNumber > 0 && resolved.MediaType != provider.MediaTypeMovie:
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

// Download resolves and fetches one item, reporting progress through the
// given callback.
func (s *DownloadService) Download(ctx context.Context, resolved model.ResolvedMedia, progress func(downloader.DownloadProgress)) error {
	outputDir, title := s.OrganizedPath(resolved)
	return s.downloadMedia(ctx, resolved, outputDir, title, progress)
}

func (s *DownloadService) tempDir(outputDir string) string {
	rel, err := filepath.Rel(s.downloadDir, outputDir)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join(s.downloadDir, ".tmp", rel)
	}
	return filepath.Join(outputDir, ".tmp")
}

func (s *DownloadService) downloadMedia(ctx context.Context, resolved model.ResolvedMedia, outputDir, title string, progress func(downloader.DownloadProgress)) error {
	if len(resolved.Playback) == 0 || resolved.Playback[0].URL == "" {
		return fmt.Errorf("no playback URL")
	}

	tempDir := s.tempDir(outputDir)
	req := downloader.DownloadRequest{
		Title:     title,
		OutputDir: outputDir,
		TempDir:   tempDir,
		Sources:   resolved.Playback,
		Progress:  progress,
		Verify:    s.VerifyDownloads(),
	}

	logging.Debug("download service: start", "title", title, "outputDir", outputDir, "tempDir", tempDir)
	if err := s.dl.Download(ctx, req); err != nil {
		return err
	}

	removeEmptyDirs(tempDir, s.downloadDir)
	s.saveSidecarSubtitle(ctx, resolved, outputDir, title)
	return nil
}

func (s *DownloadService) saveSidecarSubtitle(ctx context.Context, resolved model.ResolvedMedia, outputDir, title string) {
	if s.subtitleService == nil {
		return
	}
	subLang := s.SubtitleLanguage()
	if strings.EqualFold(strings.TrimSpace(subLang), "off") {
		return
	}
	isAnime := resolved.MediaType == provider.MediaTypeAnime || resolved.ContentMode == provider.ModeAnime
	if isAnime && s.subtitleService.AnimeSubtitlesDisabled() {
		return
	}
	if len(resolved.Playback) == 0 {
		return
	}

	source := resolved.Playback[0]
	if isAnimeHardSub(resolved, source) {
		logging.Debug("download service: skipping sidecar subtitles for hardsub anime", "title", title, "quality", source.Quality)
		return
	}

	var track model.SubtitleTrack
	var err error

	if resolved.SelectedSubtitle != nil && usableCachedTrack(*resolved.SelectedSubtitle) {
		selectedLang := resolved.SelectedSubtitle.Language
		if selectedLang == "" || strings.EqualFold(selectedLang, subLang) {
			track = *resolved.SelectedSubtitle
		}
	}

	if track.Path == "" {
		track, err = s.subtitleService.FetchForSource(ctx, resolved, subLang, source.Resolver, source.TransportIdentity())
		if err != nil {
			logging.Warn("download service: fetch sidecar subtitle failed", "title", title, "err", err)
			return
		}
	}

	if track.Path == "" {
		return
	}

	ext := strings.ToLower(filepath.Ext(track.Path))
	if ext == "" {
		ext = ".srt"
	}
	langCode := track.Language
	if langCode == "" {
		langCode = subLang
	}
	if langCode == "" {
		langCode = "en"
	}

	baseTitle, _ := downloader.SplitTitleExt(title)
	subFilename := fmt.Sprintf("%s.%s%s", baseTitle, langCode, ext)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		logging.Warn("download service: create output dir for subtitle failed", "dir", outputDir, "err", err)
		return
	}
	destPath := filepath.Join(outputDir, subFilename)
	data, err := os.ReadFile(track.Path)
	if err != nil {
		logging.Warn("download service: read cached subtitle failed", "path", track.Path, "err", err)
		return
	}
	if err := util.AtomicWriteFile(destPath, data, 0o644); err != nil {
		logging.Warn("download service: write sidecar subtitle failed", "dest", destPath, "err", err)
		return
	}
	logging.Info("download service: saved sidecar subtitle", "path", destPath)
}

// isAnimeHardSub reports whether media is anime and the primary playback source
// carries burned-in subtitles, making a sidecar track redundant.
func isAnimeHardSub(resolved model.ResolvedMedia, source provider.MediaSource) bool {
	isAnime := resolved.MediaType == provider.MediaTypeAnime || resolved.ContentMode == provider.ModeAnime
	if !isAnime {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(source.SubType), provider.SubTypeHard) {
		return true
	}
	lowerQuality := strings.ToLower(source.Quality)
	return strings.Contains(lowerQuality, "hard sub") ||
		strings.Contains(lowerQuality, "hardsub") ||
		strings.Contains(lowerQuality, "hsub")
}

// CleanupPartial removes partial artifacts of an interrupted download and
// prunes now-empty directories under the download root.
func (s *DownloadService) CleanupPartial(outputDir, title string) {
	tempDir := s.tempDir(outputDir)
	s.dl.CleanupPartial(outputDir, title, tempDir)
	s.cleanupSidecarSubtitles(outputDir, title)
	removeEmptyDirs(outputDir, s.downloadDir)
	removeEmptyDirs(tempDir, s.downloadDir)
}

func (s *DownloadService) cleanupSidecarSubtitles(outputDir, title string) {
	baseTitle, _ := downloader.SplitTitleExt(title)
	for _, ext := range []string{".mp4", ".mkv", ".webm", ".m4v", ".mov", ".avi", ".ts"} {
		if _, err := os.Stat(filepath.Join(outputDir, baseTitle+ext)); err == nil {
			return
		}
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return
	}
	prefix := baseTitle + "."
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".srt" || ext == ".ass" || ext == ".vtt" {
			_ = os.Remove(filepath.Join(outputDir, entry.Name()))
		}
	}
}

// RemoveEmptyDirs removes dir and any parent directories up to (but not
// including) the download directory if they are empty after cleanup.
func (s *DownloadService) RemoveEmptyDirs(dir string) {
	removeEmptyDirs(dir, s.downloadDir)
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

// BatchDownloadResult is the per-episode outcome of a batch download.
type BatchDownloadResult struct {
	Episode provider.Episode
	Err     error
}

// BatchDownload downloads episodes sequentially (respecting ctx), applying
// quality/language filters before each download and reporting progress via
// onProgress. One failed episode never aborts the rest.
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
