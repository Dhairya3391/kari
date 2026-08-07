//go:build !android

package player

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"kari/internal/aniskip"
	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/model"
)

const mpvStartupTimeout = 1500 * time.Millisecond
const mpvReadinessTimeout = 8 * time.Second

type MPVPlayer struct {
	aniskip *aniskip.Client
}

var _ Player = (*MPVPlayer)(nil)

func (p *MPVPlayer) Name() string {
	return "mpv"
}

func (p *MPVPlayer) Available() bool {
	return mpvAvailable()
}

func (p *MPVPlayer) Play(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return playWithMPVSources(sources, media, p.aniskip)
}

func playWithMPVSources(sources []model.PlaybackSource, media model.ResolvedMedia, aniskipClient *aniskip.Client) (PlaybackResult, error) {
	logging.Debugf("PlayWithMPVSources: media=%q sources_count=%d", media.DisplayTitle(), len(sources))
	if len(sources) == 0 {
		return PlaybackResult{}, errors.New("mpv playback failed: no playback sources available")
	}

	aniskipArgs, aniskipPath := getAniskipArgs(aniskipClient, media)
	defer cleanupAniskipScript(aniskipPath)

	errs := make([]string, 0, len(sources))
	for idx, source := range sources {
		logging.Debugf("PlayWithMPVSources: trying source %d: %s (URL=%q)", idx+1, source.Label, source.URL)
		if strings.TrimSpace(source.URL) == "" {
			continue
		}
		if result, err := playSingleSource(source, media, aniskipArgs); err == nil {
			return result, nil
		} else {
			label := strings.TrimSpace(source.Label)
			if label == "" {
				label = fmt.Sprintf("source %d", idx+1)
			}
			errs = append(errs, fmt.Sprintf("%s: %v", label, err))
		}
	}

	if len(errs) == 0 {
		return PlaybackResult{}, errors.New("mpv playback failed: no usable playback sources available")
	}
	return PlaybackResult{}, fmt.Errorf("mpv playback failed: %s", strings.Join(errs, " | "))
}

func mpvAvailable() bool {
	_, err := exec.LookPath("mpv")
	return err == nil
}

func playSingleSource(source model.PlaybackSource, media model.ResolvedMedia, aniskipArgs []string) (PlaybackResult, error) {
	logging.Debugf("playSingleSource: trying direct playback for URL=%q", source.URL)
	socketPath := DefaultMPVSocketPath()

	// 1. Direct MPV playback (primary)
	directArgs := buildMPVArgs(source, media, socketPath, aniskipArgs)
	directErr, directRC, directLaunched, stats := startMPVWithStartupCheck(directArgs, socketPath)
	if attemptSucceeded(directLaunched, directRC, stats) {
		logging.Debugf("playSingleSource: direct playback succeeded")
		return stats, nil
	}
	logging.Warnf("playSingleSource: direct playback failed (rc=%d, err=%q)", directRC, directErr)

	// 2. Curl-to-MPV pipe (fallback for streams requiring custom headers/TLS connection handling)
	userAgent := config.AndroidUA()
	if strings.TrimSpace(source.UserAgent) != "" {
		userAgent = source.UserAgent
	}
	headers := []string{
		"Accept: */*",
		"Connection: keep-alive",
	}
	if userAgent != "" {
		headers = append(headers, "User-Agent: "+userAgent)
	}
	if strings.TrimSpace(source.Referer) != "" {
		headers = append(headers, "Referer: "+source.Referer)
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}

	curlArgs := buildCurlArgs(source.URL, headers)

	pipeMpvArgs := []string{
		"--no-ytdl",
		"--really-quiet",
		"--msg-level=all=error",
		"--cache=no",
		"--demuxer-max-bytes=50M",
		"--network-timeout=8",
		"--input-ipc-server=" + socketPath,
		hwdecOptionArg(),
	}

	if media.StartTime > 5 {
		pipeMpvArgs = append(pipeMpvArgs, fmt.Sprintf("--start=%d", int(media.StartTime)))
	}

	pipeMpvArgs = appendTitleArgs(pipeMpvArgs, media.DisplayTitle())
	pipeMpvArgs = appendSubtitleArgs(pipeMpvArgs, media.SubtitlePaths())
	pipeMpvArgs = append(pipeMpvArgs, aniskipArgs...)
	pipeMpvArgs = append(pipeMpvArgs, "-")

	logging.Debugf("playSingleSource: trying curl-to-mpv pipe for URL=%q", source.URL)
	mpvErr, curlErr, mpvRC, launched, statsPipe, pipeErr := startPipeWithStartupCheck(curlArgs, pipeMpvArgs, socketPath)
	if pipeErr != nil {
		logging.Errorf("playSingleSource: pipe startup error: %v", pipeErr)
		return PlaybackResult{}, fmt.Errorf("mpv playback failed: pipe startup error: %w", pipeErr)
	}
	if attemptSucceeded(launched, mpvRC, statsPipe) {
		logging.Debugf("playSingleSource: curl-to-mpv pipe succeeded")
		return statsPipe, nil
	}
	logging.Warnf("playSingleSource: curl-to-mpv pipe failed (rc=%d, mpv_err=%q, curl_err=%q)", mpvRC, mpvErr, curlErr)

	summary := fmt.Sprintf("mpv playback failed (direct rc=%d, pipe rc=%d)", directRC, mpvRC)
	details := joinNonEmpty(
		summarizeErr("direct", directErr),
		summarizeErr("pipe-mpv", mpvErr),
		summarizeErr("pipe-curl", curlErr),
	)
	if details == "" {
		return PlaybackResult{}, errors.New(summary)
	}
	return PlaybackResult{}, fmt.Errorf("%s: %s", summary, details)
}

func ipcPoller(ctx context.Context, client *IPCClient, stats *playbackStats, done <-chan struct{}) {
	if err := client.Connect(3 * time.Second); err != nil {
		logging.Debugf("ipcPoller: connect failed: %v", err)
		return
	}
	defer client.Close()

	poll := func() {
		pos, posErr := client.GetProperty("time-pos")
		var posF float64
		if posErr == nil {
			if f, ok := pos.(float64); ok {
				posF = f
			}
		}
		dur, durErr := client.GetProperty("duration")
		var durF float64
		if durErr == nil {
			if f, ok := dur.(float64); ok {
				durF = f
			}
		}
		idle, idleErr := client.GetProperty("idle-active")
		var isIdle bool
		if idleErr == nil {
			if b, ok := idle.(bool); ok {
				isIdle = b
			}
		}

		// Media is loaded if time-pos is available (even if 0.0), duration is > 0,
		// or MPV is actively loading/playing media (not idle).
		loaded := (posErr == nil) || (durErr == nil && durF > 0) || (idleErr == nil && !isIdle)

		if loaded || posF > 0 || durF > 0 {
			stats.update(posF, durF, loaded)
		}
	}

	// Poll immediately on connection so readiness is detected without waiting for ticker delay.
	poll()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			poll()
		}
	}
}

func buildMPVArgs(source model.PlaybackSource, media model.ResolvedMedia, socketPath string, aniskipArgs []string) []string {
	args := []string{
		"--no-ytdl",
		"--msg-level=all=warn",
		hwdecOptionArg(),
		"--network-timeout=8",
		"--cache=yes",
		"--cache-pause-initial=no",
		"--demuxer-seekable-cache=yes",
		"--demuxer-max-bytes=200M",
		"--demuxer-readahead-secs=2",
		"--stream-buffer-size=16M",
		"--hls-bitrate=max",
	}

	if media.StartTime > 5 {
		args = append(args, fmt.Sprintf("--start=%d", int(media.StartTime)))
	}

	userAgent := source.UserAgent
	if strings.TrimSpace(userAgent) == "" {
		userAgent = config.AndroidUA()
	}
	if userAgent != "" {
		args = append(args, "--user-agent="+userAgent)
	}
	if strings.TrimSpace(source.Referer) != "" {
		args = append(args, "--referrer="+source.Referer)
	}

	var headers []string
	if userAgent != "" {
		headers = append(headers, "User-Agent: "+userAgent)
	}
	if strings.TrimSpace(source.Referer) != "" {
		headers = append(headers, "Referer: "+source.Referer)
		ref := strings.TrimSuffix(source.Referer, "/")
		headers = append(headers, "Origin: "+ref)
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}
	if len(headers) > 0 {
		args = append(args, "--http-header-fields="+strings.Join(headers, "\r\n"))
	}

	args = appendTitleArgs(args, media.DisplayTitle())
	args = appendSubtitleArgs(args, media.SubtitlePaths())
	args = append(args, aniskipArgs...)
	args = append(args, "--input-ipc-server="+socketPath)

	return append(args, source.URL)
}

func appendTitleArgs(args []string, title string) []string {
	if strings.TrimSpace(title) == "" {
		return args
	}
	title = sanitizeMediaTitle(title)
	return append(args, "--title="+title, "--force-media-title="+title)
}

func appendSubtitleArgs(args []string, subtitleFiles []string) []string {
	if len(subtitleFiles) == 0 {
		logging.Debugf("appendSubtitleArgs: no subtitle files to append")
		return args
	}
	for _, sub := range subtitleFiles {
		if strings.TrimSpace(sub) != "" {
			sub = strings.ReplaceAll(sub, `\`, `/`)
			logging.Debugf("appendSubtitleArgs: adding sub-file=%q", sub)
			args = append(args, "--sub-files="+sub)
		}
	}
	return args
}

func buildCurlArgs(url string, headers []string) []string {
	args := []string{"-s", "-L"}
	for _, h := range headers {
		args = append(args, "-H", h)
	}
	args = append(args, optionalCurlFlags(url)...)
	args = append(args, url)
	return args
}

func optionalCurlFlags(finalURL string) []string {
	isHTTP := strings.HasPrefix(strings.ToLower(strings.TrimSpace(finalURL)), "http://") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(finalURL)), "https://")
	if !isHTTP {
		return nil
	}
	return append([]string{}, curlOptionalFlags...)
}

var curlOptionalFlags = []string{
	"--compressed",
	"--connect-timeout", "5",
	"--retry", "2",
}

func startMPVWithStartupCheck(args []string, socketPath string) (stderr string, exitCode int, launched bool, stats PlaybackResult) {
	// Clean up any stale socket from a previous run
	os.Remove(socketPath)

	cmd := exec.Command("mpv", args...)
	cmd.Stdout = io.Discard
	buf := &bytes.Buffer{}
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		return err.Error(), 1, false, PlaybackResult{}
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err == nil {
			return buf.String(), 0, false, PlaybackResult{}
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return buf.String(), ee.ExitCode(), false, PlaybackResult{}
		}
		return buf.String(), 1, false, PlaybackResult{}
	case <-time.After(mpvStartupTimeout):
		// Process stayed alive — now check actual playback readiness
		return waitForMPVReadiness(cmd, done, socketPath, buf)
	}
}

func waitForMPVReadiness(cmd *exec.Cmd, done <-chan error, socketPath string, buf *bytes.Buffer) (stderr string, exitCode int, launched bool, stats PlaybackResult) {
	ipcDone := make(chan struct{})
	client := NewIPCClient(socketPath)
	ps := newPlaybackStats()
	go ipcPoller(context.Background(), client, ps, ipcDone)

	readinessTimeout := time.After(mpvReadinessTimeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Phase 1: Wait for playback readiness or exit
	for {
		select {
		case err := <-done:
			// MPV exited before becoming ready
			close(ipcDone)
			exitCode = 0
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					exitCode = ee.ExitCode()
				} else {
					exitCode = 1
				}
			}
			return buf.String(), exitCode, true, ps.snapshot()

		case <-readinessTimeout:
			// Playback didn't start — kill MPV and fall through
			_ = cmd.Process.Kill()
			<-done
			close(ipcDone)
			logging.Warnf("mpv readiness timeout — killed process (stderr: %s)", summarizeErr("", buf.String()))
			return buf.String(), 1, true, ps.snapshot()

		case <-ticker.C:
			if ps.playing() {
				goto phase2
			}
		}
	}

phase2:
	// Phase 2: Playback is active — wait for MPV to exit normally
	err := <-done
	close(ipcDone)

	exitCode = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return buf.String(), exitCode, true, ps.snapshot()
}

func startPipeWithStartupCheck(curlArgs, mpvArgs []string, socketPath string) (mpvStderr string, curlStderr string, exitCode int, launched bool, stats PlaybackResult, err error) {
	os.Remove(socketPath)

	p1 := exec.Command("curl", curlArgs...)
	p2 := exec.Command("mpv", mpvArgs...)

	stdout, err := p1.StdoutPipe()
	if err != nil {
		return "", "", 1, false, PlaybackResult{}, err
	}
	curlBuf := &bytes.Buffer{}
	p1.Stderr = curlBuf
	p2.Stdin = stdout
	p2.Stdout = io.Discard
	mpvBuf := &bytes.Buffer{}
	p2.Stderr = mpvBuf

	if err := p1.Start(); err != nil {
		return "", "", 1, false, PlaybackResult{}, err
	}
	if err := p2.Start(); err != nil {
		killAndWait(p1)
		return "", curlBuf.String(), 1, false, PlaybackResult{}, err
	}

	done := make(chan error, 1)
	go func() {
		err := p2.Wait()
		killAndWait(p1)
		done <- err
	}()

	exitFromErr := func(waitErr error) int {
		if waitErr == nil {
			return 0
		}
		if ee, ok := waitErr.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return 1
	}

	select {
	case waitErr := <-done:
		return mpvBuf.String(), curlBuf.String(), exitFromErr(waitErr), false, PlaybackResult{}, nil
	case <-time.After(mpvStartupTimeout):
		// Launched successfully, start IPC polling
		ipcDone := make(chan struct{})
		client := NewIPCClient(socketPath)
		ps := newPlaybackStats()
		go ipcPoller(context.Background(), client, ps, ipcDone)

		// Wait for playback readiness with timeout
		pipeReadinessTimeout := time.After(mpvReadinessTimeout)
		pipeTick := time.NewTicker(500 * time.Millisecond)
		defer pipeTick.Stop()

	pipeCheck:
		for {
			select {
			case waitErr := <-done:
				close(ipcDone)
				return mpvBuf.String(), curlBuf.String(), exitFromErr(waitErr), true, ps.snapshot(), nil
			case <-pipeReadinessTimeout:
				_ = p2.Process.Kill()
				<-done
				close(ipcDone)
				return mpvBuf.String(), curlBuf.String(), 1, true, ps.snapshot(), nil
			case <-pipeTick.C:
				if ps.playing() {
					break pipeCheck
				}
			}
		}

		waitErr := <-done
		close(ipcDone)

		return mpvBuf.String(), curlBuf.String(), exitFromErr(waitErr), true, ps.snapshot(), nil
	}
}

func killAndWait(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func summarizeErr(label, stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	// just use first line or first 100 chars
	lines := strings.Split(stderr, "\n")
	if len(lines) > 0 {
		msg := strings.TrimSpace(lines[0])
		if len(msg) > 100 {
			msg = msg[:100] + "..."
		}
		return fmt.Sprintf("%s: %s", label, msg)
	}
	return ""
}

func attemptSucceeded(launched bool, exitCode int, stats PlaybackResult) bool {
	// Evidence that the stream actually started playing (position or
	// duration observed over IPC) always counts as a success, even if mpv
	// crashed or was quit abnormally afterwards.
	if stats.DurationSecs > 0 || stats.FinalPositionSecs > 0 {
		return true
	}
	// A process that exits before the startup window elapses — even with a 0
	// exit code — means playback never started (e.g. mpv fails to open the
	// URL and quits cleanly). Treat that as a failure so the caller falls
	// through to the next strategy instead of reporting a bogus success.
	if !launched {
		return false
	}
	// Survived the startup window but exited cleanly before we could observe
	// playback (e.g. the user quit quickly): accept it so we don't relaunch
	// another window for nothing.
	return exitCode == 0 || exitCode == 4
}

func hwdecOptionArg() string {
	if runtime.GOOS == "darwin" {
		return "--hwdec=auto"
	}
	return "--hwdec=auto-safe"
}

func joinNonEmpty(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ; ")
}
