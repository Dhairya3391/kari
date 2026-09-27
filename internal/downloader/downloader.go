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
	"kari/internal/model"
	"kari/internal/provider"
)

// log scopes every line from this package.
var ytdlpLog = logging.With("component", "downloader.ytdlp")

// DownloadProgress is a snapshot reported during an active download.
type DownloadProgress struct {
	Percent    float64 // 0.0 to 1.0
	TotalSize  string  // e.g. "1.35GiB"
	Speed      string  // e.g. "3.47MiB/s"
	Downloaded string  // e.g. "612MiB"
	ETA        string  // e.g. "00:36"
}

// DownloadRequest describes one download job: ordered fallback sources,
// display title, destination, and the progress sink.
type DownloadRequest struct {
	Sources   []provider.MediaSource
	Title     string
	OutputDir string
	Progress  func(p DownloadProgress)
}

// Downloader is the engine contract for fetching media to disk.
// CleanupPartial removes incomplete artifacts after a failed/cancelled run.
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

var extendedProgressRe = regexp.MustCompile(
	`^KARI_PROGRESS:\s*(\S+?)\|\s*FRAG:\s*(\S+?)\|\s*TOTAL:\s*(.*?)\|\s*TOTAL_EST:\s*(.+?)\|\s*SPEED:\s*(.+?)\|\s*ETA:\s*(.+?)\|\s*DOWNLOADED:\s*(.+)$`,
)

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
	var b strings.Builder
	b.Grow(len(title))
	inSpace := false
	for _, r := range title {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteByte('-')
			inSpace = false
		case r == '\n' || r == '\r' || r == '\t' || r == ' ':
			if !inSpace && b.Len() > 0 {
				b.WriteByte(' ')
				inSpace = true
			}
		case r >= 32:
			b.WriteRune(r)
			inSpace = false
		}
	}
	cleaned := strings.TrimSpace(b.String())
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

// YTDLPDownloader fetches media by shelling out to yt-dlp, parsing its
// progress output into DownloadProgress callbacks. It tries the request's
// sources in order until one succeeds.
type YTDLPDownloader struct{}

var _ Downloader = (*YTDLPDownloader)(nil)

// NewYTDLPDownloader constructs the yt-dlp engine.
func NewYTDLPDownloader() *YTDLPDownloader { return &YTDLPDownloader{} }

// Download fetches req.Sources in order until one succeeds, streaming
// parsed progress to req.Progress.
func (d *YTDLPDownloader) Download(ctx context.Context, req DownloadRequest) error {
	if len(req.Sources) == 0 {
		return fmt.Errorf("ytdlp: no sources provided")
	}

	baseTitle, _ := splitTitleExt(req.Title)
	existingSize := d.findOutputSize(req.OutputDir, baseTitle)
	if existingSize != "" {
		ytdlpLog.Info("output exists; skipping download")
		if req.Progress != nil {
			req.Progress(DownloadProgress{Percent: 1.0, TotalSize: existingSize})
		}
		return nil
	}

	if err := ensureOutputDir(req.OutputDir); err != nil {
		return fmt.Errorf("ytdlp: create output directory: %w", err)
	}
	if req.Progress != nil {
		req.Progress(DownloadProgress{Percent: 0.0})
	}

	var errs []error
	seenSources := make(map[string]struct{}, len(req.Sources))
	for i, source := range req.Sources {
		if err := ctx.Err(); err != nil {
			// Pause/cancel keeps partial data on disk: the .aria2 control
			// file and .part fragments are the resume checkpoints. Only
			// CleanupPartial (explicit delete from the UI) removes them.
			return err
		}

		source.URL = strings.TrimSpace(source.URL)
		if source.URL == "" {
			continue
		}
		key := source.TransportIdentity()
		if _, ok := seenSources[key]; ok {
			continue
		}
		seenSources[key] = struct{}{}

		ytdlpLog.Info("trying source",
			"index", i+1, "total", len(req.Sources),
			"resolver", source.Resolver, "quality", source.Quality,
			"strategy", downloadStrategy(source))
		if err := d.downloadSource(ctx, req, source); err == nil {
			if req.Progress != nil {
				finalSize := d.findOutputSize(req.OutputDir, baseTitle)
				req.Progress(DownloadProgress{Percent: 1.0, TotalSize: finalSize})
			}
			ytdlpLog.Info("download complete", "title", req.Title, "source", i+1)
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else {
			// Preserve .aria2 control files so the next source can resume.
			d.CleanupPartial(req.OutputDir, req.Title)
			errs = append(errs, fmt.Errorf("source %d (%s): %w", i+1, source.Quality, err))
			ytdlpLog.Warn("source failed; trying next", "index", i+1, "total", len(req.Sources), "err", err)
		}
	}

	if len(errs) == 0 {
		return fmt.Errorf("ytdlp: no usable sources provided")
	}
	// .aria2 control files survive a full failure so a later retry of the
	// same source resumes instead of restarting from zero.
	return fmt.Errorf("ytdlp: all %d usable sources failed: %w", len(errs), errors.Join(errs...))
}

func (d *YTDLPDownloader) findOutputSize(outputDir, baseTitle string) string {
	for ext := range knownMediaExts {
		path := filepath.Join(outputDir, baseTitle+ext)
		if info, err := os.Stat(path); err == nil && info.Size() > 1024*1024 {
			// A .aria2 control file next to the media file means the
			// download was interrupted and the file is incomplete.
			if _, err := os.Stat(path + ".aria2"); err == nil {
				continue
			}
			return formatFileSize(info.Size())
		}
	}
	return ""
}

func formatFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
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
	ytdlpLog.Debug("aria2c strategy failed; retrying natively", "resolver", source.Resolver)
	d.CleanupPartial(req.OutputDir, req.Title)
	return d.downloadWithStrategy(ctx, req, source, "native")
}

func (d *YTDLPDownloader) downloadWithStrategy(
	ctx context.Context,
	req DownloadRequest,
	source provider.MediaSource,
	strategy string,
) error {
	// Direct-media downloads use aria2c through its JSON-RPC interface,
	// providing structured progress (speed, ETA, downloaded size) instead
	// of yt-dlp's opaque external-downloader passthrough.
	if strategy == "aria2c" {
		if _, err := exec.LookPath("aria2c"); err != nil {
			return fmt.Errorf("aria2c: binary not found: %w", err)
		}
		baseTitle, _ := splitTitleExt(req.Title)
		adl := &Aria2Downloader{}
		return adl.Download(ctx, source, req.OutputDir, baseTitle, req.Progress)
	}

	baseTitle, _ := splitTitleExt(req.Title)
	outputPattern := filepath.Join(req.OutputDir, baseTitle+".%(ext)s")
	args := []string{
		"-o", outputPattern,
		"--concurrent-fragments", strconv.Itoa(downloadParallelism()),
		// Resume from .part files when a paused download restarts.
		"--continue",
		"--retries", "10",
		"--fragment-retries", "10",
		"--retry-sleep", "http:exp=1:10",
		"--retry-sleep", "fragment:exp=1:10",
		"--buffer-size", "1M",
		"--socket-timeout", "30",
		"--hls-use-mpegts",
		"--newline",
		"--progress-template", "download:KARI_PROGRESS:%(progress._percent_str)s|FRAG:%(progress.fragment_index)s/%(progress.fragment_count)s|TOTAL:%(progress._total_bytes_str)s|TOTAL_EST:%(progress._total_bytes_estimate_str)s|SPEED:%(progress._speed_str)s|ETA:%(progress._eta_str)s|DOWNLOADED:%(progress._downloaded_bytes_str)s",
		"--progress-delta", "0.5",
	}

	if ua := strings.TrimSpace(source.UserAgent); ua != "" {
		args = append(args, "--user-agent", ua)
	}
	if ref := strings.TrimSpace(source.Referer); ref != "" {
		args = append(args, "--referer", ref)
	}
	for _, header := range sourceHeaders(source) {
		if strings.HasPrefix(header, "User-Agent: ") {
			continue
		}
		args = append(args, "--add-headers", header)
	}

	args = append(args, source.URL)
	ytdlpLog.Debug("download start", "title", req.Title, "quality", source.Quality, "strategy", strategy)

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
			if req.Progress != nil {
				if ematches := extendedProgressRe.FindStringSubmatch(line); len(ematches) > 1 {
					pctStr := ematches[1]
					fragStr := ematches[2]
					totalSize := strings.TrimSpace(ematches[3])
					totalSizeEst := strings.TrimSpace(ematches[4])
					speed := strings.TrimSpace(ematches[5])
					eta := strings.TrimSpace(ematches[6])
					downloaded := strings.TrimSpace(ematches[7])

					var p float64
					var parsed bool

					// 1. If fragmented HLS/DASH, fragment count gives exact overall percentage
					if strings.Contains(fragStr, "/") {
						parts := strings.Split(fragStr, "/")
						if len(parts) == 2 {
							cur, err1 := strconv.Atoi(parts[0])
							total, err2 := strconv.Atoi(parts[1])
							if err1 == nil && err2 == nil && total > 0 && cur >= 0 {
								p = float64(cur) / float64(total)
								parsed = true
							}
						}
					}

					// 2. Direct file percentage fallback
					if !parsed {
						cleanPct := strings.TrimSpace(strings.TrimSuffix(pctStr, "%"))
						if val, err := strconv.ParseFloat(cleanPct, 64); err == nil {
							p = val / 100.0
							parsed = true
						}
					}

					if parsed {
						if totalSize == "N/A" || totalSize == "Unknown" || totalSize == "" {
							totalSize = totalSizeEst
						}
						if totalSize == "N/A" || totalSize == "Unknown" {
							totalSize = ""
						}
						if speed == "Unknown B/s" || speed == "N/A" || speed == "Unknown" || speed == "" {
							speed = ""
						}
						if eta == "Unknown" || eta == "N/A" || eta == "" {
							eta = ""
						}
						if downloaded == "N/A" || downloaded == "Unknown" {
							downloaded = ""
						}
						current := int64(p * 10000)
						for {
							old := maxProgress.Load()
							if current <= old {
								break
							}
							if maxProgress.CompareAndSwap(old, current) {
								req.Progress(DownloadProgress{Percent: p, TotalSize: totalSize, Speed: speed, Downloaded: downloaded, ETA: eta})
								break
							}
						}
					}
				} else if matches := progressRe.FindStringSubmatch(line); len(matches) > 1 {
					if val, err := strconv.ParseFloat(matches[1], 64); err == nil {
						p := val / 100.0
						current := int64(p * 10000)
						for {
							old := maxProgress.Load()
							if current <= old {
								break
							}
							if maxProgress.CompareAndSwap(old, current) {
								req.Progress(DownloadProgress{Percent: p})
								break
							}
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

// CleanupPartial removes ALL partial artifacts of a cancelled or failed
// download: yt-dlp resume files (.part, .ytdl, .part-Frag) always, and for
// aria2 the .aria2 control file plus the media file it checkpoints — a media
// file with a control file beside it is by definition incomplete. A pause
// must NOT call this: the control file is the resume anchor.
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
		if !strings.HasPrefix(name, baseTitle) {
			continue
		}
		full := filepath.Join(outputDir, name)
		switch {
		case strings.HasSuffix(name, ".aria2"):
			// Control file + the incomplete media it belongs to.
			if err := os.Remove(full); err != nil {
				ytdlpLog.Debug("cleanup aria2 control remove failed", "file", name, "err", err)
			}
			if err := os.Remove(strings.TrimSuffix(full, ".aria2")); err != nil && !os.IsNotExist(err) {
				ytdlpLog.Debug("cleanup aria2 partial media remove failed", "file", name, "err", err)
			}
		case strings.HasSuffix(name, ".part"),
			strings.HasSuffix(name, ".ytdl"),
			strings.Contains(name, ".part-Frag"):
			if err := os.Remove(full); err != nil {
				ytdlpLog.Debug("cleanup remove failed", "file", name, "err", err)
			}
		}
	}
}

// toMediaSource converts provider fields once so header derivation stays in
// Source.Headers, the single place Referer/Origin/User-Agent/Cookie
// are derived.
func toMediaSource(source provider.MediaSource) model.Source {
	return model.Source{
		URL:            source.URL,
		Quality:        source.Quality,
		Provider:       source.Resolver,
		Referer:        source.Referer,
		UserAgent:      source.UserAgent,
		Cookie:         source.CookieHeader,
		Language:       source.Language,
		SuppressOrigin: source.SuppressOrigin,
	}
}

func sourceHeaders(source provider.MediaSource) []string {
	h := toMediaSource(source).Headers()
	headers := []string{}
	// Fixed order keeps yt-dlp args deterministic for logs and tests.
	for _, name := range []string{"User-Agent", "Referer", "Origin", "Cookie"} {
		for _, value := range h.Values(name) {
			headers = append(headers, name+": "+value)
		}
	}
	return headers
}

func isHLSSource(source provider.MediaSource) bool {
	sourceType := strings.ToLower(strings.TrimSpace(source.Type))
	if sourceType == provider.SourceTypeHLS || sourceType == provider.SourceTypeM3U8 {
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
