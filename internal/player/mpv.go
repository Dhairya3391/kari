//go:build !android

package player

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

	buildArgsWithSocket := func(lite bool) []string {
		args := buildMPVArgs(source, media, lite)
		// Pop the URL
		url := args[len(args)-1]
		args = args[:len(args)-1]
		// Append extra args
		args = append(args, aniskipArgs...)
		args = append(args, "--input-ipc-server="+socketPath)
		// Push the URL back
		return append(args, url)
	}

	directArgs := buildArgsWithSocket(false)
	directErr, directRC, directLaunched, stats := startMPVWithStartupCheck(directArgs, socketPath)
	if attemptSucceeded(directLaunched, directRC, stats) {
		logging.Debugf("playSingleSource: direct playback succeeded")
		return stats, nil
	}
	logging.Warnf("playSingleSource: direct playback failed (rc=%d, err=%q)", directRC, directErr)

	logging.Debugf("playSingleSource: trying direct-lite playback for URL=%q", source.URL)
	directLiteArgs := buildArgsWithSocket(true)
	directLiteErr, directLiteRC, directLiteLaunched, statsLite := startMPVWithStartupCheck(directLiteArgs, socketPath)
	if attemptSucceeded(directLiteLaunched, directLiteRC, statsLite) {
		logging.Debugf("playSingleSource: direct-lite playback succeeded")
		return statsLite, nil
	}
	logging.Warnf("playSingleSource: direct-lite playback failed (rc=%d, err=%q)", directLiteRC, directLiteErr)

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
		"--network-timeout=10",
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

	summary := fmt.Sprintf(
		"mpv playback failed (direct rc=%d, direct-lite rc=%d, pipe rc=%d)",
		directRC,
		directLiteRC,
		mpvRC,
	)
	details := joinNonEmpty(
		summarizeErr("direct", directErr),
		summarizeErr("direct-lite", directLiteErr),
		summarizeErr("pipe-mpv", mpvErr),
		summarizeErr("pipe-curl", curlErr),
	)
	if details == "" {
		return PlaybackResult{}, errors.New(summary)
	}
	return PlaybackResult{}, fmt.Errorf("%s: %s", summary, details)
}

func ipcPoller(ctx context.Context, client *IPCClient, stats *PlaybackResult, done <-chan struct{}) {
	if err := client.Connect(3 * time.Second); err != nil {
		logging.Debugf("ipcPoller: connect failed: %v", err)
		return
	}
	defer client.Close()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			pos, err := client.GetProperty("time-pos")
			if err == nil {
				if f, ok := pos.(float64); ok {
					stats.FinalPositionSecs = f
				}
			}
			dur, err := client.GetProperty("duration")
			if err == nil {
				if f, ok := dur.(float64); ok {
					stats.DurationSecs = f
				}
			}
			if stats.DurationSecs > 0 {
				if stats.FinalPositionSecs/stats.DurationSecs > 0.85 {
					stats.Completed = true
				} else {
					stats.Completed = false
				}
			}
		}
	}
}

func buildMPVArgs(source model.PlaybackSource, media model.ResolvedMedia, lite bool) []string {
	args := []string{
		"--no-ytdl",
		"--msg-level=all=error",
		hwdecOptionArg(),
		"--network-timeout=10",
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
	if lite {
		args = append(args, "--profile=low-latency")
	} else {
		args = append(args,
			"--cache=auto",
			"--demuxer-seekable-cache=yes",
			"--demuxer-max-bytes=200M",
			"--demuxer-readahead-secs=5",
		)
	}

	if strings.TrimSpace(source.CookieHeader) != "" {
		args = append(args, "--http-header-fields=Cookie: "+source.CookieHeader)
	}
	args = appendTitleArgs(args, media.DisplayTitle())
	args = appendSubtitleArgs(args, media.SubtitlePaths())

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
		return args
	}
	for _, sub := range subtitleFiles {
		if strings.TrimSpace(sub) != "" {
			args = append(args, "--sub-file="+sub)
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
	"--connect-timeout", "10",
	"--retry", "3",
}

func startMPVWithStartupCheck(args []string, socketPath string) (stderr string, exitCode int, launched bool, stats PlaybackResult) {
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
		// Launched successfully, start IPC polling
		ipcDone := make(chan struct{})
		client := NewIPCClient(socketPath)
		go ipcPoller(context.Background(), client, &stats, ipcDone)

		err := <-done // Wait for the background goroutine to finish
		close(ipcDone)

		exitCode = 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				exitCode = 1
			}
		}
		return buf.String(), exitCode, true, stats
	}
}

func startPipeWithStartupCheck(curlArgs, mpvArgs []string, socketPath string) (mpvStderr string, curlStderr string, exitCode int, launched bool, stats PlaybackResult, err error) {
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

	select {
	case waitErr := <-done:
		exitCode = 0
		if waitErr != nil {
			if ee, ok := waitErr.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				exitCode = 1
			}
		}
		return mpvBuf.String(), curlBuf.String(), exitCode, false, PlaybackResult{}, nil
	case <-time.After(mpvStartupTimeout):
		// Launched successfully, start IPC polling
		ipcDone := make(chan struct{})
		client := NewIPCClient(socketPath)
		go ipcPoller(context.Background(), client, &stats, ipcDone)

		waitErr := <-done
		close(ipcDone)

		exitCode = 0
		if waitErr != nil {
			if ee, ok := waitErr.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				exitCode = 1
			}
		}
		return mpvBuf.String(), curlBuf.String(), exitCode, true, stats, nil
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
	if exitCode == 0 || exitCode == 4 {
		return true
	}
	// If it exited with an error code, but we managed to play some video, we can consider it a "success"
	// in the sense that the stream worked but maybe it crashed later, or user quit abnormally.
	if stats.DurationSecs > 0 || stats.FinalPositionSecs > 0 {
		return true
	}
	return false
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
