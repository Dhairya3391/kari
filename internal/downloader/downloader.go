package downloader

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
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
	Accepts(source provider.MediaSource, resolver string) bool
}

const (
	minDownloadParallelism = 16
	maxDownloadParallelism = 64
)

var (
	sharedTransportOnce sync.Once
	sharedTransport     *http.Transport
)

var knownMediaExts = map[string]struct{}{
	".mp4":  {},
	".mkv":  {},
	".webm": {},
	".m4v":  {},
	".mov":  {},
	".avi":  {},
	".ts":   {},
}

var progressRe = regexp.MustCompile(`\s(\d+(\.\d+)?)%`)

func downloadParallelism() int {
	parallelism := runtime.NumCPU() * 2
	if parallelism < minDownloadParallelism {
		parallelism = minDownloadParallelism
	}
	if parallelism > maxDownloadParallelism {
		parallelism = maxDownloadParallelism
	}
	return parallelism
}

func downloadTransport() *http.Transport {
	sharedTransportOnce.Do(func() {
		maxConns := downloadParallelism()
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxConnsPerHost = maxConns
		transport.MaxIdleConnsPerHost = maxConns
		transport.MaxIdleConns = maxConns * 2
		transport.IdleConnTimeout = 90 * time.Second
		transport.TLSHandshakeTimeout = 10 * time.Second
		transport.ExpectContinueTimeout = 1 * time.Second
		if runtime.GOOS == "android" {
			transport.DialContext = androidDialer().DialContext
		}
		sharedTransport = transport
	})
	return sharedTransport
}

func androidDialer() *net.Dialer {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			conn, err := d.DialContext(ctx, "udp", "1.1.1.1:53")
			if err != nil {
				return d.DialContext(ctx, "udp", "8.8.8.8:53")
			}
			return conn, err
		},
	}
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver:  resolver,
	}
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
	if cleaned == "" {
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

func outputPathWithExt(outputDir, title, defaultExt string) (string, error) {
	if err := ensureOutputDir(outputDir); err != nil {
		return "", err
	}
	baseTitle, ext := splitTitleExt(title)
	outputPath := filepath.Join(outputDir, baseTitle)
	if ext != "" {
		return outputPath + ext, nil
	}
	if defaultExt != "" {
		outputPath += defaultExt
	}
	return outputPath, nil
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
	// Check if file already exists (any common extension)
	for ext := range knownMediaExts {
		path := filepath.Join(req.OutputDir, baseTitle+ext)
		if info, err := os.Stat(path); err == nil && info.Size() > 1024*1024 { // > 1MB
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

	baseTitle, _ = splitTitleExt(req.Title)
	outputPattern := filepath.Join(req.OutputDir, baseTitle+".%(ext)s")
	parallelism := downloadParallelism()
	args := []string{
		"-o", outputPattern,
		"--concurrent-fragments", strconv.Itoa(parallelism),
		"--retries", "10",
		"--fragment-retries", "10",
		"--buffer-size", "64K",
		"--hls-use-mpegts",
		"--newline",
	}

	if _, err := exec.LookPath("aria2c"); err == nil {
		args = append(args, "--external-downloader", "aria2c")
		args = append(args, "--external-downloader-args", fmt.Sprintf("aria2c:-x %d -s %d -k 1M", parallelism, parallelism))
	}

	if ua := strings.TrimSpace(req.Sources[0].UserAgent); ua != "" {
		args = append(args, "--user-agent", ua)
	}
	if ref := strings.TrimSpace(req.Sources[0].Referer); ref != "" {
		args = append(args, "--referer", ref)
	}
	args = append(args, req.Sources[0].URL)
	logging.Debugf("download start title=%q outputDir=%q args=%v", req.Title, req.OutputDir, args)

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ytdlp: stdout pipe failed: %w", err)
	}
	var stderrBuf bytes.Buffer
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("ytdlp: stderr pipe failed: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ytdlp: start failed: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	var maxProgress atomic.Int64 // Store as int64(percent * 100) to maintain monotonicity

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
		if ctx.Err() != nil {
			d.CleanupPartial(req.OutputDir, req.Title)
			return ctx.Err()
		}
		d.CleanupPartial(req.OutputDir, req.Title)
		return fmt.Errorf("ytdlp: failed: %w, stderr: %s", err, stderrBuf.String())
	}
	if req.Progress != nil {
		req.Progress(1.0)
	}
	logging.Infof("download complete title=%q", req.Title)
	return nil
}

func (d *YTDLPDownloader) CleanupPartial(outputDir, title string) {
	baseTitle, _ := splitTitleExt(title)
	files, err := os.ReadDir(outputDir)
	if err != nil {
		logging.Errorf("cleanup partial failed readDir: %v", err)
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
				strings.Contains(name, ".part-Frag") {
				if err := os.Remove(filepath.Join(outputDir, name)); err != nil {
					logging.Errorf("cleanup partial failed remove %s: %v", name, err)
				} else {
					logging.Infof("cleaned up partial file: %s", name)
				}
			}
		}
	}
}

func (d *YTDLPDownloader) Accepts(source provider.MediaSource, resolver string) bool {
	return true
}

// ── MiruroDownloader ─────────────────────────────────────────────────────────

type MiruroDownloader struct{}

var _ Downloader = (*MiruroDownloader)(nil)

func NewMiruroDownloader() *MiruroDownloader { return &MiruroDownloader{} }

func (d *MiruroDownloader) Download(ctx context.Context, req DownloadRequest) error {
	if len(req.Sources) == 0 {
		return fmt.Errorf("miruro: no sources provided")
	}

	baseTitle, _ := splitTitleExt(req.Title)
	// Check if file already exists (any common extension)
	for ext := range knownMediaExts {
		path := filepath.Join(req.OutputDir, baseTitle+ext)
		if info, err := os.Stat(path); err == nil && info.Size() > 1024*1024 {
			logging.Infof("miruro: file already exists, skipping: %s", path)
			if req.Progress != nil {
				req.Progress(1.0)
			}
			return nil
		}
	}

	source := req.Sources[0]
	if req.Progress != nil {
		req.Progress(0.0)
	}
	logging.Debugf("miruro download source url=%q referer=%q", source.URL, source.Referer)

	ua := source.UserAgent
	if strings.TrimSpace(ua) == "" {
		ua = config.AndroidUA()
	}
	client := httpclient.NewWithUserAgent(ua)
	client.Transport = downloadTransport()

	segments, err := d.resolveSegments(ctx, client, source.URL, source.Referer, source.UserAgent)
	if err != nil {
		return fmt.Errorf("miruro: resolve segments: %w", err)
	}
	if len(segments) == 0 {
		return fmt.Errorf("miruro: no segments found in m3u8")
	}

	outputPath, err := outputPathWithExt(req.OutputDir, req.Title, ".ts")
	if err != nil {
		return fmt.Errorf("miruro: create output path: %w", err)
	}

	tempFiles := make([]string, len(segments))
	defer func() {
		for _, tf := range tempFiles {
			if tf != "" {
				os.Remove(tf)
			}
		}
	}()

	cleanupOnError := func(err error) error {
		if err != nil {
			d.CleanupPartial(req.OutputDir, req.Title)
		}
		return err
	}

	sem := make(chan struct{}, maxDownloadParallelism)
	var wg sync.WaitGroup
	var completed atomic.Int64
	errCh := make(chan error, len(segments))

	baseTitle, _ = splitTitleExt(req.Title)

	for i, url := range segments {
		wg.Add(1)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			goto wait
		}

		go func(i int, url string) {
			defer wg.Done()
			defer func() { <-sem }()

			tmpPath := filepath.Join(req.OutputDir, fmt.Sprintf("%s.seg.%04d.tmp", baseTitle, i))

			var lastErr error
			for attempt := 0; attempt < 3; attempt++ {
				if ctx.Err() != nil {
					return
				}
				if attempt > 0 {
					time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
				}

				f, err := os.Create(tmpPath)
				if err != nil {
					lastErr = fmt.Errorf("create temp segment file %d: %w", i, err)
					continue
				}
				tempFiles[i] = tmpPath

				err = d.fetchSegment(ctx, client, url, source.Referer, source.UserAgent, f)
				f.Close()

				if err != nil {
					lastErr = fmt.Errorf("segment %d failed: %w", i, err)
					continue
				}
				lastErr = nil
				break
			}

			if lastErr != nil {
				errCh <- lastErr
				return
			}

			if req.Progress != nil {
				n := completed.Add(1)
				req.Progress(float64(n) / float64(len(segments)))
			}
		}(i, url)
	}

wait:
	wg.Wait()
	close(errCh)

	if err := ctx.Err(); err != nil {
		return cleanupOnError(err)
	}

	// Drain errCh to avoid goroutine leaks and find first error
	var firstErr error
	for e := range errCh {
		if firstErr == nil {
			firstErr = e
		}
	}
	if firstErr != nil {
		return cleanupOnError(firstErr)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("miruro: create output file: %w", err)
	}
	defer f.Close()

	bw := bufio.NewWriter(f)
	for i, tf := range tempFiles {
		sf, err := os.Open(tf)
		if err != nil {
			return cleanupOnError(fmt.Errorf("miruro: open segment %d failed: %w", i, err))
		}
		_, err = io.Copy(bw, sf)
		sf.Close()
		if err != nil {
			return cleanupOnError(fmt.Errorf("miruro: copy segment %d failed: %w", i, err))
		}
	}

	if err := bw.Flush(); err != nil {
		return cleanupOnError(fmt.Errorf("miruro: flush output failed: %w", err))
	}

	if req.Progress != nil {
		req.Progress(1.0)
	}
	logging.Infof("download complete title=%q → %s", req.Title, outputPath)
	return nil
}

func (d *MiruroDownloader) resolveSegments(ctx context.Context, client *http.Client, m3u8URL, referer, userAgent string) ([]string, error) {
	body, err := d.fetchText(ctx, client, m3u8URL, referer, userAgent)
	if err != nil {
		return nil, fmt.Errorf("m3u8 fetch failed: %w", err)
	}
	var segments []string
	var playlists []string
	baseURL, _ := url.Parse(m3u8URL)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, ".m3u8") {
			if baseURL != nil {
				if resolved, err := baseURL.Parse(line); err == nil {
					playlists = append(playlists, resolved.String())
					continue
				}
			}
			playlists = append(playlists, line)
			continue
		}
		if baseURL != nil {
			if resolved, err := baseURL.Parse(line); err == nil {
				segments = append(segments, resolved.String())
				continue
			}
		}
		segments = append(segments, line)
	}
	if len(segments) > 0 {
		return segments, nil
	}
	if len(playlists) > 0 {
		return d.resolveSegments(ctx, client, playlists[0], referer, userAgent)
	}
	return nil, nil
}

func (d *MiruroDownloader) fetchText(ctx context.Context, client *http.Client, url, referer, userAgent string) (string, error) {
	logging.Debugf("fetching m3u8 url=%q referer=%q", url, referer)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	ua := userAgent
	if strings.TrimSpace(ua) == "" {
		ua = config.AndroidUA()
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Origin", config.MiruroOrigin)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func (d *MiruroDownloader) fetchSegment(ctx context.Context, client *http.Client, url, referer, userAgent string, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	ua := userAgent
	if strings.TrimSpace(ua) == "" {
		ua = config.AndroidUA()
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Origin", config.MiruroOrigin)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("segment fetch status %d", resp.StatusCode)
	}
	_, err = io.Copy(out, resp.Body)
	return err
}

func (d *MiruroDownloader) CleanupPartial(outputDir, title string) {
	baseTitle, _ := splitTitleExt(title)
	files, err := os.ReadDir(outputDir)
	if err != nil {
		return
	}
	for _, f := range files {
		if !f.IsDir() && strings.HasPrefix(f.Name(), baseTitle) {
			os.Remove(filepath.Join(outputDir, f.Name()))
		}
	}
}

func (d *MiruroDownloader) Accepts(source provider.MediaSource, resolver string) bool {
	r := strings.ToLower(strings.TrimSpace(resolver))
	t := strings.ToLower(strings.TrimSpace(source.Type))
	if r == "miruro" || t == "m3u8" || t == "hls" {
		return true
	}
	// Detect from URL
	u := strings.ToLower(source.URL)
	return strings.Contains(u, ".m3u8") || strings.Contains(u, "index.m3u8") || strings.Contains(u, "playlist.m3u8") || strings.Contains(u, "/hls/") || strings.Contains(u, "master.m3u8")
}

// ── WCODownloader ─────────────────────────────────────────────────────────────

const (
	wcoUserAgent = "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Mobile Safari/537.36"
	wcoChunkSize = 8 * 1024 * 1024
)

type WCODownloader struct{}

var _ Downloader = (*WCODownloader)(nil)

func NewWCODownloader() *WCODownloader { return &WCODownloader{} }

func (d *WCODownloader) newClient(referer string) *http.Client {
	client := httpclient.NewWithUserAgent(wcoUserAgent)
	client.Transport = downloadTransport()
	return client
}

func (d *WCODownloader) baseHeaders(req *http.Request, referer, cookieHeader string) {
	req.Header.Set("User-Agent", wcoUserAgent)
	req.Header.Set("Accept", "*/*")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
}

func (d *WCODownloader) probe(ctx context.Context, mediaURL, referer, cookieHeader string) (finalURL string, size int64, rangeOK bool, err error) {
	client := d.newClient(referer)
	// Some WCO servers block/reset HEAD, use GET with Range: 0-0 instead
	req, err := http.NewRequestWithContext(ctx, "GET", mediaURL, nil)
	if err != nil {
		return "", 0, false, err
	}
	d.baseHeaders(req, referer, cookieHeader)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return "", 0, false, fmt.Errorf("probe returned status %d", resp.StatusCode)
	}

	contentRange := resp.Header.Get("Content-Range")
	if contentRange != "" {
		parts := strings.Split(contentRange, "/")
		if len(parts) == 2 {
			if s, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				size = s
			}
		}
	}
	if size <= 0 {
		size = resp.ContentLength
	}

	rangeOK = resp.Header.Get("Accept-Ranges") == "bytes" || contentRange != ""
	return resp.Request.URL.String(), size, rangeOK, nil
}

func (d *WCODownloader) downloadChunk(ctx context.Context, client *http.Client, url, referer, cookieHeader string, start, end int64, out *os.File, downloaded *atomic.Int64) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		d.baseHeaders(req, referer, cookieHeader)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("server returned status %d", resp.StatusCode)
			continue
		}
		buf := make([]byte, 32*1024)
		offset := start
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := out.WriteAt(buf[:n], offset); werr != nil {
					return werr
				}
				offset += int64(n)
				downloaded.Add(int64(n))
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				lastErr = err
				break
			}
		}
	}
	return fmt.Errorf("wco: chunk %d-%d failed after 3 attempts: %w", start, end, lastErr)
}

func (d *WCODownloader) downloadSingle(ctx context.Context, url, referer, cookieHeader, outputPath string, progress func(float64), total int64) error {
	client := d.newClient(referer)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("wco: single download request: %w", err)
	}
	d.baseHeaders(req, referer, cookieHeader)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("wco: single download execute: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wco: server returned %d", resp.StatusCode)
	}
	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("wco: create output file: %w", err)
	}
	defer out.Close()
	var downloaded int64
	buf := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return fmt.Errorf("wco: write error: %w", werr)
			}
			downloaded += int64(n)
			if progress != nil && total > 0 {
				progress(float64(downloaded) / float64(total))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("wco: read error: %w", err)
		}
	}
	if progress != nil {
		progress(1.0)
	}
	return nil
}

func (d *WCODownloader) Download(ctx context.Context, req DownloadRequest) error {
	if len(req.Sources) == 0 {
		return fmt.Errorf("wco: no sources provided")
	}

	baseTitle, _ := splitTitleExt(req.Title)
	// Check if file already exists (any common extension)
	for ext := range knownMediaExts {
		path := filepath.Join(req.OutputDir, baseTitle+ext)
		if info, err := os.Stat(path); err == nil && info.Size() > 1024*1024 {
			logging.Infof("wco: file already exists, skipping: %s", path)
			if req.Progress != nil {
				req.Progress(1.0)
			}
			return nil
		}
	}

	source := req.Sources[0]
	if req.Progress != nil {
		req.Progress(0.0)
	}

	finalURL, size, rangeOK, err := d.probe(ctx, source.URL, source.Referer, source.CookieHeader)
	if err != nil {
		return fmt.Errorf("wco: probe failed: %w", err)
	}
	outputPath, err := outputPathWithExt(req.OutputDir, req.Title, ".mp4")
	if err != nil {
		return fmt.Errorf("wco: output path: %w", err)
	}

	cleanupOnError := func(err error) error {
		if err != nil {
			d.CleanupPartial(req.OutputDir, req.Title)
		}
		return err
	}

	if !rangeOK || size <= 0 {
		return cleanupOnError(d.downloadSingle(ctx, finalURL, source.Referer, source.CookieHeader, outputPath, req.Progress, size))
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("wco: create file: %w", err)
	}
	defer out.Close()

	if err := out.Truncate(size); err != nil {
		return cleanupOnError(fmt.Errorf("wco: truncate failed: %w", err))
	}

	// Adaptive chunk size based on line speed probe (simplistic: measure probe time or use first chunk)
	// We'll use wcoChunkSize as base and adjust after first chunks if needed,
	// but for now let's implement the static adaptive sizing from plan.
	// Since we don't have a separate probe, we'll start with 8MB and can refine.
	chunkSize := int64(wcoChunkSize)

	numChunks := int((size + chunkSize - 1) / chunkSize)
	var downloaded atomic.Int64
	var wg sync.WaitGroup
	errCh := make(chan error, numChunks)

	// Start with 4, increase if we see good throughput
	parallelism := 4
	if parallelism > numChunks {
		parallelism = numChunks
	}
	sem := make(chan struct{}, maxDownloadParallelism)
	client := d.newClient(source.Referer)

	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				if req.Progress != nil {
					p := float64(downloaded.Load()) / float64(size)
					if p > 1.0 {
						p = 1.0
					}
					req.Progress(p)
				}
			}
		}
	}()

	for i := 0; i < numChunks; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if end >= size {
			end = size - 1
		}
		wg.Add(1)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			goto wait
		}

		go func(s, e int64) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := d.downloadChunk(ctx, client, finalURL, source.Referer, source.CookieHeader, s, e, out, &downloaded); err != nil {
				errCh <- err
			}

			// Adaptive parallelism: if we've done some chunks and everything is fine,
			// we can potentially increase the sem buffer, but here it's fixed by sem capacity.
			// We'll just stick to maxDownloadParallelism as the limit.
		}(start, end)
	}

wait:
	wg.Wait()
	close(stopProgress)
	<-progressDone
	close(errCh)

	if err := ctx.Err(); err != nil {
		return cleanupOnError(err)
	}

	if err := <-errCh; err != nil {
		return cleanupOnError(err)
	}

	if req.Progress != nil {
		req.Progress(1.0)
	}
	logging.Infof("download complete title=%q", req.Title)
	return nil
}

func (d *WCODownloader) CleanupPartial(outputDir, title string) {
	baseTitle, _ := splitTitleExt(title)
	files, err := os.ReadDir(outputDir)
	if err != nil {
		logging.Errorf("cleanup partial failed readDir: %v", err)
		return
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if strings.HasPrefix(f.Name(), baseTitle) {
			os.Remove(filepath.Join(outputDir, f.Name()))
		}
	}
}

func (d *WCODownloader) Accepts(source provider.MediaSource, resolver string) bool {
	r := strings.ToLower(strings.TrimSpace(resolver))
	t := strings.ToLower(strings.TrimSpace(source.Type))
	u := strings.ToLower(source.URL)
	if t == "hls" || t == "m3u8" || strings.Contains(u, ".m3u8") || strings.Contains(u, "/hls/") {
		return false
	}
	if r == "wco" || t == "mp4" {
		return true
	}
	// Detect from URL
	return strings.Contains(u, ".mp4") || strings.Contains(u, "video.mp4") || strings.Contains(u, "/mp4/")
}
