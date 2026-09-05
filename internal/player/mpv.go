//go:build !android

// Desktop mpv playback: Player implementation, argument construction, and
// process/readiness management, including the curl|mpv pipe fallback for
// streams that need custom transport handling. The IPC layer beneath this
// lives in ipc.go / socket_posix.go / ipc_windows.go.
package player

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"kari/internal/animeskip"
	"kari/internal/aniskip"
	"kari/internal/config"
	"kari/internal/model"
	"kari/internal/provider"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	mpvStartupTimeout   = 1500 * time.Millisecond
	mpvReadinessTimeout = 20 * time.Second

	// mpvQuickExitThreshold bounds how long into the readiness phase mpv can
	// exit cleanly (code 0) with no IPC evidence that media ever loaded before
	// we stop trusting that as "the user quit on purpose" and instead treat it
	// as a silent playback failure (e.g. a dead URL mpv gives up on without a
	// nonzero exit code). A real user quitting reacts within a second or two of
	// seeing the window; a stream failing to open typically takes longer,
	// bounded by --network-timeout=15 in the mpv args below.
	mpvQuickExitThreshold = 2 * time.Second
)

// MPVPlayer plays via a desktop mpv process, using JSON IPC for position
// tracking so resume/scrobble get real playback stats.
type MPVPlayer struct {
	aniskip      *aniskip.Client
	animeskip    *animeskip.Client
	skipSettings SkipSettings
}

var _ Player = (*MPVPlayer)(nil)

// Name implements Player.
func (p *MPVPlayer) Name() string { return "mpv" }

// Available implements Player.
func (p *MPVPlayer) Available() bool {
	_, err := exec.LookPath("mpv")
	return err == nil
}

func (p *MPVPlayer) setSkipSettings(s SkipSettings) {
	p.skipSettings = s
}

// Play implements Player.
func (p *MPVPlayer) Play(sources []provider.MediaSource, media model.ResolvedMedia) (PlaybackResult, error) {
	mpvLog.Debug("playback starting", "media", media.DisplayTitle(), "sources", len(sources))
	if len(sources) == 0 {
		return PlaybackResult{}, errors.New("mpv playback failed: no sources available")
	}

	skipArgs, skipPath := getSkipArgs(p.aniskip, p.animeskip, p.skipSettings, media)
	defer cleanupAniskipScript(skipPath)

	return attemptSources("mpv", sources, func(source provider.MediaSource) (PlaybackResult, error) {
		return playSingleSource(source, media, skipArgs)
	})
}

// playSingleSource plays one source through two strategies: direct mpv with
// native options first, then a curl|mpv pipe for streams whose TLS/header
// quirks mpv's own HTTP stack rejects.
func playSingleSource(source provider.MediaSource, media model.ResolvedMedia, aniskipArgs []string) (PlaybackResult, error) {
	socketPath := DefaultMPVSocketPath()

	// Strategy 1: direct MPV playback (primary).
	directArgs := buildMPVArgs(source, media, socketPath, aniskipArgs)
	directErr, directRC, directLaunched, directQuickExit, stats :=
		startPlayerWithStartupCheck("mpv", directArgs, mpvStartupTimeout, socketPath)
	if attemptSucceeded(directLaunched, directRC, directQuickExit, stats) {
		return stats, nil
	}
	mpvLog.Warn("direct playback failed", "exitCode", directRC, "stderr", summarizeErr("", directErr))

	// Strategy 2: curl-to-MPV pipe.
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

	pipeMpvArgs := []string{
		"--no-ytdl",
		"--really-quiet",
		"--msg-level=all=error",
		"--vo=gpu-next",
		"--cache=yes",
		"--demuxer-seekable-cache=yes",
		"--demuxer-max-bytes=150M",
		"--demuxer-max-back-bytes=30M",
		"--demuxer-readahead-secs=60",
		"--stream-buffer-size=8M",
		"--network-timeout=15",
		"--input-ipc-server=" + socketPath,
		hwdecOptionArg(),
	}

	if media.StartTime > 5 {
		pipeMpvArgs = append(pipeMpvArgs, fmt.Sprintf("--start=%d", int(media.StartTime)))
	}

	pipeMpvArgs = appendTitleArgs(pipeMpvArgs, media.DisplayTitle())
	pipeMpvArgs = appendSubtitleArgs(pipeMpvArgs, media.SubtitlePaths())
	pipeMpvArgs = append(pipeMpvArgs, aniskipArgs...)
	pipeMpvArgs = append(pipeMpvArgs, source.ExtraArgs...)
	pipeMpvArgs = append(pipeMpvArgs, "-")

	curlArgs := buildCurlArgs(source.URL, headers)
	mpvErr, curlErr, mpvRC, launched, pipeQuickExit, statsPipe, pipeErr :=
		startPipeWithStartupCheck(curlArgs, pipeMpvArgs, socketPath)
	if pipeErr != nil {
		return PlaybackResult{}, fmt.Errorf("mpv playback failed: pipe startup error: %w", pipeErr)
	}
	if attemptSucceeded(launched, mpvRC, pipeQuickExit, statsPipe) {
		return statsPipe, nil
	}
	mpvLog.Warn("pipe playback failed",
		"exitCode", mpvRC, "mpvStderr", summarizeErr("", mpvErr), "curlStderr", summarizeErr("", curlErr))

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

// buildMPVArgs assembles the full mpv command line for one source:
// buffering/cache tuning, resume position, transport identity (UA/referer/
// cookies/origin), title, subtitles, aniskip hooks, and the IPC socket.
func buildMPVArgs(source provider.MediaSource, media model.ResolvedMedia, socketPath string, aniskipArgs []string) []string {
	args := []string{
		"--no-ytdl",
		"--msg-level=all=warn",
		"--vo=gpu-next",
		hwdecOptionArg(),
		"--network-timeout=15",
		"--cache=yes",
		"--cache-pause-initial=no",
		"--demuxer-seekable-cache=yes",
		"--demuxer-max-bytes=150M",
		"--demuxer-max-back-bytes=30M",
		"--demuxer-readahead-secs=60",
		"--stream-buffer-size=8M",
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

	// UA and Referer are sent via the dedicated --user-agent/--referrer mpv
	// options (mpv applies them to ffmpeg streams too), so they are NOT
	// repeated here: --http-header-fields is a comma-split list and real UAs
	// contain commas ("(KHTML, like Gecko)"), which would corrupt the header
	// block and make strict CDNs answer 400. Only add what mpv has no native
	// option for: Origin and the provider's Cookie.
	var headers []string
	if strings.TrimSpace(source.Referer) != "" {
		// Some CDNs reject an Origin header outright (or only accept a bare
		// scheme://host), so it's opt-in via SuppressOrigin (e.g. PirateX,
		// whose CDN validates Referer only). When sent it stays derived from
		// the referer, matching what a browser would send.
		if !source.SuppressOrigin {
			ref := strings.TrimSuffix(source.Referer, "/")
			headers = append(headers, "Origin: "+ref)
		}
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}
	if len(headers) > 0 {
		// mpv's list-typed options split list values on commas; joining with
		// anything else (like CR/LF) corrupts the header block. Plain
		// comma-join is correct here precisely because UA/Referer never go
		// through this path.
		args = append(args, "--http-header-fields="+strings.Join(headers, ","))
	}

	args = appendTitleArgs(args, media.DisplayTitle())
	args = appendSubtitleArgs(args, media.SubtitlePaths())
	args = append(args, aniskipArgs...)
	args = append(args, source.ExtraArgs...)
	args = append(args, "--input-ipc-server="+socketPath)

	return append(args, source.URL)
}

// buildCurlArgs assembles the fetch side of the curl|mpv fallback pipe.
func buildCurlArgs(url string, headers []string) []string {
	args := []string{"-s", "-L"}
	for _, h := range headers {
		args = append(args, "-H", h)
	}
	args = append(args, optionalCurlFlags(url)...)
	return append(args, url)
}

// optionalCurlFlags adds resilience flags only for real HTTP URLs; other
// schemes would reject them outright.
func optionalCurlFlags(finalURL string) []string {
	isHTTP := strings.HasPrefix(strings.ToLower(strings.TrimSpace(finalURL)), "http://") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(finalURL)), "https://")
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

// hwdecOptionArg picks the hardware-decode flag per platform: darwin's
// VideoToolbox is reliable with auto, elsewhere auto-safe avoids black
// screens on broken VA-API/VDPAU stacks.
func hwdecOptionArg() string {
	if runtime.GOOS == "darwin" {
		return "--hwdec=auto"
	}
	return "--hwdec=auto-safe"
}

// appendTitleArgs sets both the window title and the media-title metadata
// override when a display title is known.
func appendTitleArgs(args []string, title string) []string {
	if strings.TrimSpace(title) == "" {
		return args
	}
	title = sanitizeMediaTitle(title)
	return append(args, "--title="+title, "--force-media-title="+title)
}

// appendSubtitleArgs side-loads downloaded subtitle files.
func appendSubtitleArgs(args []string, subtitleFiles []string) []string {
	for _, sub := range subtitleFiles {
		if strings.TrimSpace(sub) == "" {
			continue
		}
		sub = strings.ReplaceAll(sub, `\`, `/`)
		mpvLog.Debug("subtitle side-loaded", "path", sub)
		args = append(args, "--sub-file="+sub)
	}
	return args
}

// joinNonEmpty concatenates parts with " ; ", skipping blanks.
func joinNonEmpty(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ; ")
}

// ipcPoller samples mpv's IPC properties once per second, folding observed
// position/duration into the shared playbackStats until done closes. It
// reconnects when the connection goes bad (e.g. a read deadline poisoned by
// an early "property unavailable" answer), so transient failures during HLS
// load never permanently blind playback tracking.
func ipcPoller(ctx context.Context, client *IPCClient, stats *playbackStats, done <-chan struct{}) {
	defer client.Close()

	poll := func() bool {
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

		// Media is loaded if time-pos is available (even if 0.0), duration is
		// > 0, or MPV is actively loading/playing media (not idle). Early in
		// an HLS load every property may legitimately answer "unavailable";
		// that's not a connection failure.
		loaded := (posErr == nil) || (durErr == nil && durF > 0) || (idleErr == nil && !isIdle)
		failed := posErr != nil && durErr != nil && idleErr != nil

		if loaded || posF > 0 || durF > 0 {
			stats.update(posF, durF, loaded)
		}
		return !failed
	}

	for {
		if err := client.Connect(3 * time.Second); err != nil {
			mpvLog.Debug("ipc connect failed", "err", err)
		} else {
			// Poll immediately on connection so readiness is detected without
			// waiting for the first tick.
			if healthy := poll(); healthy {
				ticker := time.NewTicker(1 * time.Second)
			sample:
				for {
					select {
					case <-ctx.Done():
						ticker.Stop()
						return
					case <-done:
						ticker.Stop()
						return
					case <-ticker.C:
						if !poll() {
							ticker.Stop()
							break sample // connection went bad; dial afresh
						}
					}
				}
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-time.After(500 * time.Millisecond):
			// retry loop: redial
		}
	}
}

// startPlayerWithStartupCheck launches binary and waits up to timeout for it
// to either exit or show IPC evidence of loaded media on socketPath. Shared
// by every single-process player (mpv direct, IINA) so there's exactly one
// launch/readiness implementation to keep correct across all of them.
func startPlayerWithStartupCheck(binary string, args []string, timeout time.Duration, socketPath string) (stderr string, exitCode int, launched bool, quickExit bool, stats PlaybackResult) {
	// Clean up any stale socket from a previous run.
	os.Remove(socketPath)

	cmd := exec.Command(binary, args...)
	cmd.Stdout = io.Discard
	buf := &bytes.Buffer{}
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		return err.Error(), 1, false, false, PlaybackResult{}
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return buf.String(), exitCodeOf(err), false, false, PlaybackResult{}
	case <-time.After(timeout):
		// Process stayed alive — now check actual playback readiness.
		return waitForPlaybackReadiness(cmd, done, socketPath, buf)
	}
}

// waitForPlaybackReadiness waits for an already-launched, single-process
// player (mpv direct, or IINA — both expose --input-ipc-server on
// socketPath) to show IPC evidence of loaded media, killing it if it never
// becomes ready within mpvReadinessTimeout instead of blocking on it
// indefinitely.
func waitForPlaybackReadiness(cmd *exec.Cmd, done <-chan error, socketPath string, buf *bytes.Buffer) (stderr string, exitCode int, launched bool, quickExit bool, stats PlaybackResult) {
	ipcDone := make(chan struct{})
	client := NewIPCClient(socketPath)
	ps := newPlaybackStats()
	go ipcPoller(context.Background(), client, ps, ipcDone)

	phaseStart := time.Now()
	readinessTimeout := time.After(mpvReadinessTimeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Phase 1: wait for playback readiness or exit.
	for {
		select {
		case err := <-done:
			close(ipcDone)
			quickExit = time.Since(phaseStart) < mpvQuickExitThreshold
			return buf.String(), exitCodeOf(err), true, quickExit, ps.snapshot()

		case <-readinessTimeout:
			_ = cmd.Process.Kill()
			<-done
			close(ipcDone)
			mpvLog.Warn("readiness timeout; killed process", "stderr", summarizeErr("", buf.String()))
			return buf.String(), 1, true, false, ps.snapshot()

		case <-ticker.C:
			if ps.playing() {
				goto phase2
			}
		}
	}

phase2:
	// Phase 2: playback is active — wait for normal exit.
	err := <-done
	close(ipcDone)
	return buf.String(), exitCodeOf(err), true, true, ps.snapshot()
}

// startPipeWithStartupCheck runs curl piped into mpv with the same two-phase
// startup/readiness contract as startPlayerWithStartupCheck. Returns both
// processes' stderr for error reporting; err is only set when a process
// could not be started at all.
func startPipeWithStartupCheck(curlArgs, mpvArgs []string, socketPath string) (mpvStderr string, curlStderr string, exitCode int, launched bool, quickExit bool, stats PlaybackResult, err error) {
	os.Remove(socketPath)

	p1 := exec.Command("curl", curlArgs...)
	p2 := exec.Command("mpv", mpvArgs...)

	stdout, err := p1.StdoutPipe()
	if err != nil {
		return "", "", 1, false, false, PlaybackResult{}, err
	}
	curlBuf := &bytes.Buffer{}
	p1.Stderr = curlBuf
	p2.Stdin = stdout
	p2.Stdout = io.Discard
	mpvBuf := &bytes.Buffer{}
	p2.Stderr = mpvBuf

	if err := p1.Start(); err != nil {
		return "", "", 1, false, false, PlaybackResult{}, err
	}
	if err := p2.Start(); err != nil {
		killAndWait(p1)
		return "", curlBuf.String(), 1, false, false, PlaybackResult{}, err
	}

	done := make(chan error, 1)
	go func() {
		err := p2.Wait()
		killAndWait(p1)
		done <- err
	}()

	select {
	case waitErr := <-done:
		return mpvBuf.String(), curlBuf.String(), exitCodeOf(waitErr), false, false, PlaybackResult{}, nil
	case <-time.After(mpvStartupTimeout):
		ipcDone := make(chan struct{})
		client := NewIPCClient(socketPath)
		ps := newPlaybackStats()
		go ipcPoller(context.Background(), client, ps, ipcDone)

		phaseStart := time.Now()
		pipeReadinessTimeout := time.After(mpvReadinessTimeout)
		pipeTick := time.NewTicker(500 * time.Millisecond)
		defer pipeTick.Stop()

	pipeCheck:
		for {
			select {
			case waitErr := <-done:
				close(ipcDone)
				return mpvBuf.String(), curlBuf.String(), exitCodeOf(waitErr),
					true, time.Since(phaseStart) < mpvQuickExitThreshold, ps.snapshot(), nil

			case <-pipeReadinessTimeout:
				_ = p2.Process.Kill()
				<-done
				close(ipcDone)
				return mpvBuf.String(), curlBuf.String(), 1, true, false, ps.snapshot(), nil

			case <-pipeTick.C:
				if ps.playing() {
					break pipeCheck
				}
			}
		}

		waitErr := <-done
		close(ipcDone)
		return mpvBuf.String(), curlBuf.String(), exitCodeOf(waitErr), true, true, ps.snapshot(), nil
	}
}

// killAndWait stops cmd and reaps it, tolerating nils.
func killAndWait(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// summarizeErr condenses process stderr into one short labeled line for
// error aggregation; empty when there's nothing to report.
func summarizeErr(label, stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	msg := strings.TrimSpace(strings.SplitN(stderr, "\n", 2)[0])
	if len(msg) > 100 {
		msg = msg[:100] + "..."
	}
	if label == "" {
		return msg
	}
	return label + ": " + msg
}

// attemptSucceeded decides whether a launch attempt counts as playback that
// actually happened. Evidence that the stream started (position or duration
// over IPC) always wins, even if mpv crashed or was quit abnormally after.
func attemptSucceeded(launched bool, exitCode int, quickExit bool, stats PlaybackResult) bool {
	if stats.DurationSecs > 0 || stats.FinalPositionSecs > 0 {
		return true
	}
	// A process that exits before the startup window elapses — even with a 0
	// exit code — means playback never started (e.g. mpv fails to open the
	// URL and quits cleanly). Treat that as failure so the caller falls
	// through to the next strategy instead of reporting bogus success.
	if !launched {
		return false
	}
	// Exit code 4 is mpv's own "quit" signal and unambiguous either way.
	if exitCode == 4 {
		return true
	}
	// Exit 0 with no IPC evidence is ambiguous: user quitting instantly vs
	// silent failure on a dead URL. quickExit narrows it down — a real user
	// reacts within a second or two, while a failing open typically takes
	// longer (bounded by --network-timeout=8). Only trust exit 0 inside that
	// window, otherwise dead sources get reported as "played successfully"
	// and fallback sources never get tried.
	return exitCode == 0 && quickExit
}

// exitCodeOf maps a cmd.Wait() error to an integer exit code.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}
