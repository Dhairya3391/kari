package downloader

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"kari/internal/logging"
	"kari/internal/provider"
)

type DownloadRequest struct {
	Sources   []provider.MediaSource
	Title     string
	OutputDir string
	Progress  func(progress float64)
}

type Downloader interface {
	Download(ctx context.Context, req DownloadRequest) error
	CleanupPartial(outputDir, title string)
}

var knownMediaExts = map[string]struct{}{
	".mp4":  {},
	".mkv":  {},
	".webm": {},
	".m4v":  {},
	".mov":  {},
	".avi":  {},
	".ts":   {},
}

var progressRe = regexp.MustCompile(`^KARI_PROGRESS:\s*(\d+(?:\.\d+)?)%$`)

func downloadParallelism() int {
	n := runtime.NumCPU() * 2
	if n < 16 {
		n = 16
	}
	if n > 64 {
		n = 64
	}
	return n
}

func sanitizeDownloadTitle(title string) string {
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
	}, title)
	cleaned = strings.TrimSpace(strings.Join(strings.Fields(cleaned), " "))
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "download"
	}
	return cleaned
}

func splitTitleExt(title string) (string, string) {
	safeTitle := sanitizeDownloadTitle(title)
	ext := strings.ToLower(filepath.Ext(safeTitle))
	if _, ok := knownMediaExts[ext]; !ok {
		ext = ""
	}
	baseTitle := strings.TrimSuffix(safeTitle, ext)
	if strings.TrimSpace(baseTitle) == "" {
		baseTitle = "download"
	}
	return baseTitle, ext
}

func ensureOutputDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("output directory is empty")
	}
	return os.MkdirAll(dir, 0o755)
}

// ── YTDLPDownloader ──────────────────────────────────────────────────────────

type YTDLPDownloader struct{}

var _ Downloader = (*YTDLPDownloader)(nil)

func NewYTDLPDownloader() *YTDLPDownloader { return &YTDLPDownloader{} }

func (d *YTDLPDownloader) Download(ctx context.Context, req DownloadRequest) error {
	if len(req.Sources) == 0 {
		return fmt.Errorf("ytdlp: no sources provided")
	}

	baseTitle, _ := splitTitleExt(req.Title)
	for ext := range knownMediaExts {
		path := filepath.Join(req.OutputDir, baseTitle+ext)
		if info, err := os.Stat(path); err == nil && info.Size() > 1024*1024 {
			logging.Infof("ytdlp: file already exists, skipping: %s", path)
			if req.Progress != nil {
				req.Progress(1.0)
			}
			return nil
		}
	}

	if err := ensureOutputDir(req.OutputDir); err != nil {
		return fmt.Errorf("ytdlp: create output directory: %w", err)
	}
	if req.Progress != nil {
		req.Progress(0.0)
	}

	var errs []error
	seenSources := make(map[string]struct{}, len(req.Sources))
	for i, source := range req.Sources {
		if err := ctx.Err(); err != nil {
			d.CleanupPartial(req.OutputDir, req.Title)
			return err
		}

		source.URL = strings.TrimSpace(source.URL)
		if source.URL == "" {
			continue
		}
		key := source.URL + "\x00" + source.Referer + "\x00" + source.CookieHeader
		if _, ok := seenSources[key]; ok {
			continue
		}
		seenSources[key] = struct{}{}

		logging.Infof(
			"ytdlp: trying source %d/%d provider=%q quality=%q strategy=%s url=%q",
			i+1,
			len(req.Sources),
			source.Resolver,
			source.Quality,
			downloadStrategy(source),
			source.URL,
		)
		if err := d.downloadSource(ctx, req, source); err == nil {
			if req.Progress != nil {
				req.Progress(1.0)
			}
			logging.Infof("ytdlp: download complete title=%q source=%d", req.Title, i+1)
			return nil
		} else if ctx.Err() != nil {
			d.CleanupPartial(req.OutputDir, req.Title)
			return ctx.Err()
		} else {
			d.CleanupPartial(req.OutputDir, req.Title)
			errs = append(errs, fmt.Errorf("source %d (%s): %w", i+1, source.Quality, err))
			logging.Warnf("ytdlp: source %d/%d failed: %v", i+1, len(req.Sources), err)
		}
	}

	if len(errs) == 0 {
		return fmt.Errorf("ytdlp: no usable sources provided")
	}
	return fmt.Errorf("ytdlp: all %d usable sources failed: %w", len(errs), errors.Join(errs...))
}

func (d *YTDLPDownloader) downloadSource(ctx context.Context, req DownloadRequest, source provider.MediaSource) error {
	strategy := downloadStrategy(source)
	err := d.downloadWithStrategy(ctx, req, source, strategy)
	if err == nil || strategy != "aria2c" || ctx.Err() != nil {
		return err
	}

	// Some providers label redirecting or signed URLs as MP4 even though they
	// need yt-dlp's native request handling. Retry that same source natively
	// before moving to the next provider URL.
	logging.Debugf("ytdlp: aria2c failed for provider=%q; retrying source natively", source.Resolver)
	d.CleanupPartial(req.OutputDir, req.Title)
	return d.downloadWithStrategy(ctx, req, source, "native")
}

func (d *YTDLPDownloader) downloadWithStrategy(
	ctx context.Context,
	req DownloadRequest,
	source provider.MediaSource,
	strategy string,
) error {
	if req.Progress != nil && strategy == "aria2c" {
		// yt-dlp does not expose structured progress from external downloaders.
		// Signal an indeterminate state instead of reporting a stale percentage.
		req.Progress(-1)
	}

	baseTitle, _ := splitTitleExt(req.Title)
	outputPattern := filepath.Join(req.OutputDir, baseTitle+".%(ext)s")
	args := []string{
		"-o", outputPattern,
		"--concurrent-fragments", strconv.Itoa(downloadParallelism()),
		"--retries", "10",
		"--fragment-retries", "10",
		"--retry-sleep", "http:exp=1:10",
		"--retry-sleep", "fragment:exp=1:10",
		"--buffer-size", "1M",
		"--socket-timeout", "30",
		"--hls-use-mpegts",
		"--newline",
		"--progress-template", "download:KARI_PROGRESS:%(progress._percent_str)s",
		"--progress-delta", "0.5",
	}

	// Native yt-dlp handles HLS manifests, encrypted streams, and provider
	// URLs whose media type cannot be inferred reliably. aria2c is only used
	// for explicit direct-media types declared by the provider.
	if strategy == "aria2c" {
		if _, err := exec.LookPath("aria2c"); err == nil {
			args = append(args,
				"--external-downloader", "aria2c",
				"--external-downloader-args", "aria2c:-x 16 -s 16 -k 1M -j 5 --file-allocation=none",
			)
		}
	}

	// Pass headers from the source.
	if ua := strings.TrimSpace(source.UserAgent); ua != "" {
		args = append(args, "--user-agent", ua)
	}
	if ref := strings.TrimSpace(source.Referer); ref != "" {
		args = append(args, "--referer", ref)
		if origin := originFromReferer(ref); origin != "" {
			args = append(args, "--add-headers", "Origin: "+origin)
		}
	}
	if cookie := strings.TrimSpace(source.CookieHeader); cookie != "" {
		args = append(args, "--add-headers", "Cookie: "+cookie)
	}

	args = append(args, source.URL)
	logging.Debugf("ytdlp: start title=%q source=%q args=%v", req.Title, source.Quality, args)

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	// Force unbuffered Python stdout so progress lines arrive immediately.
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ytdlp: stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("ytdlp: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ytdlp: start: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	var maxProgress atomic.Int64

	scanPipe := func(pipe io.Reader, isStderr bool) {
		defer wg.Done()
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			line := scanner.Text()
			if isStderr {
				stderrBuf.Write(append([]byte(line), '\n'))
			}
			if matches := progressRe.FindStringSubmatch(line); len(matches) > 1 && req.Progress != nil {
				if val, err := strconv.ParseFloat(matches[1], 64); err == nil {
					p := val / 100.0
					current := int64(p * 10000)
					for {
						old := maxProgress.Load()
						if current <= old {
							break
						}
						if maxProgress.CompareAndSwap(old, current) {
							req.Progress(p)
							break
						}
					}
				}
			}
		}
	}
	go scanPipe(stdout, false)
	go scanPipe(stderr, true)

	err = cmd.Wait()
	wg.Wait()

	if err != nil {
		return fmt.Errorf("ytdlp: failed: %w, stderr: %s", err, stderrBuf.String())
	}
	return nil
}

func (d *YTDLPDownloader) CleanupPartial(outputDir, title string) {
	baseTitle, _ := splitTitleExt(title)
	files, err := os.ReadDir(outputDir)
	if err != nil {
		return
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		if strings.HasPrefix(name, baseTitle) {
			if strings.HasSuffix(name, ".part") ||
				strings.HasSuffix(name, ".ytdl") ||
				strings.HasSuffix(name, ".aria2") ||
				strings.Contains(name, ".part-Frag") {
				if err := os.Remove(filepath.Join(outputDir, name)); err != nil {
					logging.Debugf("ytdlp: cleanup failed remove %s: %v", name, err)
				}
			}
		}
	}
}

func originFromReferer(referer string) string {
	// Simple extraction: scheme + "://" + host.
	for i := 0; i < len(referer); i++ {
		if referer[i] == ':' && i+3 <= len(referer) && referer[i+1] == '/' && referer[i+2] == '/' {
			end := strings.IndexAny(referer[i+3:], "/?#")
			if end == -1 {
				return referer
			}
			return referer[:i+3+end]
		}
	}
	return ""
}

func isHLSSource(source provider.MediaSource) bool {
	sourceType := strings.ToLower(strings.TrimSpace(source.Type))
	if sourceType == "hls" || sourceType == "m3u8" {
		return true
	}

	url := strings.ToLower(source.URL)
	pathEnd := strings.IndexAny(url, "?#")
	if pathEnd >= 0 {
		url = url[:pathEnd]
	}
	return strings.HasSuffix(url, ".m3u8")
}

func downloadStrategy(source provider.MediaSource) string {
	if isHLSSource(source) {
		return "native-hls"
	}

	sourceType := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(source.Type)), ".")
	if _, ok := knownMediaExts["."+sourceType]; ok {
		return "aria2c"
	}

	return "native"
}
