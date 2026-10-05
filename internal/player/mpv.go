//go:build !android

// Desktop mpv playback: Player implementation, argument construction, and
// process/readiness management, including the curl|mpv pipe fallback for
// streams that need custom transport handling. The IPC layer beneath this
// lives in ipc.go / ipc_posix.go / ipc_windows.go.
package player

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/util"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
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

type startupCheck struct {
	binary     string
	args       []string
	socketPath string
}

type pipeStartupCheck struct {
	curlArgs   []string
	mpvArgs    []string
	socketPath string
}

type launchResult struct {
	mpvStderr  string
	curlStderr string
	exitCode   int
	launched   bool
	quickExit  bool
	curlFailed bool
	playback   PlaybackResult
}

func (r launchResult) succeeded() bool {
	return !r.curlFailed && attemptSucceeded(r.launched, r.exitCode, r.quickExit, r.playback)
}

// MPVPlayer plays via a desktop mpv process, using JSON IPC for position
// tracking so resume/scrobble get real playback stats.
type MPVPlayer struct {
	skipClients  SkipClients
	skipSettings SkipSettings
	skipCache    *util.BoundedCache[combinedSkipTimes]
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
	skipArgs, skipPath := getSkipArgs(
		p.skipClients,
		p.skipSettings,
		media,
		p.skipCache,
	)
	defer cleanupSkipScript(skipPath)

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
	direct := startPlayerWithStartupCheck(startupCheck{
		binary:     "mpv",
		args:       directArgs,
		socketPath: socketPath,
	})
	if direct.succeeded() {
		return direct.playback, nil
	}
	mpvLog.Warn(
		"direct playback failed",
		"exitCode", direct.exitCode,
		"stderr", summarizeErr("", direct.mpvStderr),
	)

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
		"--vo=gpu-next,gpu",
		"--gpu-context=auto",
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
	// The pipe carries only the playlist bytes: mpv still fetches every
	// segment itself, so it needs the same transport identity as the
	// direct path (header-gated CDNs answer 403 without Referer/Origin
	// on segment requests too).
	if strings.TrimSpace(source.UserAgent) != "" {
		pipeMpvArgs = append(pipeMpvArgs, "--user-agent="+source.UserAgent)
	}
	if strings.TrimSpace(source.Referer) != "" {
		pipeMpvArgs = append(pipeMpvArgs, "--referrer="+source.Referer)
		if !source.SuppressOrigin {
			if origin := originFromReferer(strings.TrimSpace(source.Referer)); origin != "" {
				pipeMpvArgs = append(pipeMpvArgs, "--http-header-fields=Origin: "+origin)
			}
		}
	}

	if media.StartTime > 5 {
		pipeMpvArgs = append(pipeMpvArgs, fmt.Sprintf("--start=%d", int(media.StartTime)))
	}

	pipeMpvArgs = appendTitleArgs(pipeMpvArgs, media.DisplayTitle())
	pipeMpvArgs = appendSubtitleArgs(pipeMpvArgs, media.SubtitlePath())
	pipeMpvArgs = appendAudioLangArgs(pipeMpvArgs, source.Language)
	pipeMpvArgs = append(pipeMpvArgs, aniskipArgs...)
	pipeMpvArgs = append(pipeMpvArgs, source.ExtraArgs...)
	if runtime.GOOS == "windows" {
		pipeMpvArgs = append(pipeMpvArgs, "--terminal=no")
	}
	pipeMpvArgs = append(pipeMpvArgs, "-")

	curlArgs := buildCurlArgs(source.URL, headers)
	pipe, pipeErr := startPipeWithStartupCheck(pipeStartupCheck{
		curlArgs:   curlArgs,
		mpvArgs:    pipeMpvArgs,
		socketPath: socketPath,
	})
	if pipeErr != nil {
		return PlaybackResult{}, fmt.Errorf("mpv playback failed: pipe startup error: %w", pipeErr)
	}
	if pipe.succeeded() {
		return pipe.playback, nil
	}
	mpvLog.Warn("pipe playback failed",
		"exitCode", pipe.exitCode,
		"mpvStderr", summarizeErr("", pipe.mpvStderr),
		"curlStderr", summarizeErr("", pipe.curlStderr))

	summary := fmt.Sprintf(
		"mpv playback failed (direct rc=%d, pipe rc=%d)",
		direct.exitCode,
		pipe.exitCode,
	)
	details := joinNonEmpty(
		summarizeErr("direct", direct.mpvStderr),
		summarizeErr("pipe-mpv", pipe.mpvStderr),
		summarizeErr("pipe-curl", pipe.curlStderr),
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
		"--vo=gpu-next,gpu",
		"--gpu-context=auto",
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
	if runtime.GOOS == "windows" {
		args = append(args, "--terminal=no")
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
		// scheme://host), so it's opt-in via SuppressOrigin. When sent it stays
		// derived from the referer, matching what a browser would send.
		if !source.SuppressOrigin {
			if origin := originFromReferer(strings.TrimSpace(source.Referer)); origin != "" {
				headers = append(headers, "Origin: "+origin)
			}
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
	args = appendSubtitleArgs(args, media.SubtitlePath())
	args = appendAudioLangArgs(args, source.Language)
	args = append(args, aniskipArgs...)
	args = append(args, source.ExtraArgs...)
	args = append(args, "--input-ipc-server="+socketPath)

	return append(args, source.URL)
}

// buildCurlArgs assembles the fetch side of the curl|mpv fallback pipe.
func buildCurlArgs(url string, headers []string) []string {
	args := []string{"-s", "-L", "--fail"}
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
	return []string{
		"--compressed",
		"--connect-timeout", "5",
		"--retry", "2",
	}
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

// appendSubtitleArgs uses mpv's single-file CLI option so one supplied track
// is selected by default, including when the URL is a non-file stream.
func appendSubtitleArgs(args []string, subtitlePath string) []string {
	subtitlePath = strings.TrimSpace(subtitlePath)
	if subtitlePath == "" {
		return args
	}
	subtitlePath = strings.ReplaceAll(subtitlePath, `\`, `/`)
	mpvLog.Debug("subtitle side-loaded", "path", subtitlePath)
	return append(args, "--sub-file="+subtitlePath)
}

// appendAudioLangArgs passes preferred audio-track languages (--alang) to MPV.
// The code-to-track mapping lives in lang.AudioLangs; this stays a thin
// flag formatter.
func appendAudioLangArgs(args []string, language string) []string {
	alang := lang.AudioLangs(language)
	if len(alang) == 0 {
		return args
	}
	return append(args, "--alang="+strings.Join(alang, ","))
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
func ipcPoller(
	ctx context.Context,
	client *IPCClient,
	stats *playbackStats,
	done <-chan struct{},
) {
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

// startPlayerWithStartupCheck launches binary and watches for IPC evidence of
// loaded media. Shared by mpv and IINA.
func startPlayerWithStartupCheck(check startupCheck) launchResult {
	_ = os.Remove(check.socketPath)

	cmd := exec.Command(check.binary, check.args...)
	cmd.Stdout = io.Discard
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return launchResult{mpvStderr: err.Error(), exitCode: 1}
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	return waitForPlaybackReadiness(cmd, done, check, stderr)
}

func waitForPlaybackReadiness(
	cmd *exec.Cmd,
	done <-chan error,
	check startupCheck,
	stderr *bytes.Buffer,
) (result launchResult) {
	ipcDone := make(chan struct{})
	var ipcWG sync.WaitGroup
	client := NewIPCClient(check.socketPath)
	stats := newPlaybackStats()
	ipcWG.Add(1)
	go func() {
		defer ipcWG.Done()
		ipcPoller(context.Background(), client, stats, ipcDone)
	}()
	defer func() {
		close(ipcDone)
		ipcWG.Wait()
		result.playback = stats.snapshot()
	}()

	phaseStart := time.Now()
	readinessTimer := time.NewTimer(mpvReadinessTimeout)
	defer readinessTimer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			return launchResult{
				mpvStderr: stderr.String(),
				exitCode:  exitCodeOf(err),
				launched:  true,
				quickExit: time.Since(phaseStart) < mpvQuickExitThreshold,
			}
		case <-readinessTimer.C:
			_ = cmd.Process.Kill()
			<-done
			mpvLog.Warn("readiness timeout; killed process", "stderr", summarizeErr("", stderr.String()))
			return launchResult{mpvStderr: stderr.String(), exitCode: 1, launched: true}
		case <-ticker.C:
			if stats.playing() {
				err := <-done
				return launchResult{
					mpvStderr: stderr.String(),
					exitCode:  exitCodeOf(err),
					launched:  true,
					quickExit: true,
				}
			}
		}
	}
}

func startPipeWithStartupCheck(check pipeStartupCheck) (result launchResult, err error) {
	_ = os.Remove(check.socketPath)

	curl := exec.Command("curl", check.curlArgs...)
	mpv := exec.Command("mpv", check.mpvArgs...)
	stdout, err := curl.StdoutPipe()
	if err != nil {
		return launchResult{exitCode: 1}, err
	}
	curlStderr := &bytes.Buffer{}
	curl.Stderr = curlStderr
	mpv.Stdin = stdout
	mpv.Stdout = io.Discard
	mpvStderr := &bytes.Buffer{}
	mpv.Stderr = mpvStderr

	if err := curl.Start(); err != nil {
		return launchResult{exitCode: 1}, err
	}
	if err := mpv.Start(); err != nil {
		killAndWait(curl)
		return launchResult{curlStderr: curlStderr.String(), exitCode: 1}, err
	}

	curlDone := make(chan error, 1)
	mpvDone := make(chan error, 1)
	go func() { curlDone <- curl.Wait() }()
	go func() { mpvDone <- mpv.Wait() }()

	ipcDone := make(chan struct{})
	var ipcWG sync.WaitGroup
	client := NewIPCClient(check.socketPath)
	stats := newPlaybackStats()
	ipcWG.Add(1)
	go func() {
		defer ipcWG.Done()
		ipcPoller(context.Background(), client, stats, ipcDone)
	}()
	defer func() {
		close(ipcDone)
		ipcWG.Wait()
		result.playback = stats.snapshot()
	}()

	phaseStart := time.Now()
	readinessTimer := time.NewTimer(mpvReadinessTimeout)
	defer readinessTimer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case mpvErr := <-mpvDone:
			curlErr := waitForCurlExit(curl, curlDone)
			return launchResult{
				mpvStderr:  mpvStderr.String(),
				curlStderr: curlStderr.String(),
				exitCode:   exitCodeOf(mpvErr),
				launched:   true,
				quickExit:  time.Since(phaseStart) < mpvQuickExitThreshold,
				curlFailed: exitCodeOf(curlErr) != 0 && !stats.playing(),
			}, nil
		case curlErr := <-curlDone:
			curlDone = nil
			if exitCodeOf(curlErr) == 0 {
				continue
			}
			_ = mpv.Process.Kill()
			mpvErr := <-mpvDone
			return launchResult{
				mpvStderr:  mpvStderr.String(),
				curlStderr: curlStderr.String(),
				exitCode:   exitCodeOf(mpvErr),
				launched:   true,
				curlFailed: true,
			}, nil
		case <-readinessTimer.C:
			_ = mpv.Process.Kill()
			<-mpvDone
			if curlDone != nil {
				_ = curl.Process.Kill()
				<-curlDone
			}
			return launchResult{
				mpvStderr:  mpvStderr.String(),
				curlStderr: curlStderr.String(),
				exitCode:   1,
				launched:   true,
			}, nil
		case <-ticker.C:
			if !stats.playing() {
				continue
			}
			mpvErr := <-mpvDone
			curlErr := waitForCurlExit(curl, curlDone)
			return launchResult{
				mpvStderr:  mpvStderr.String(),
				curlStderr: curlStderr.String(),
				exitCode:   exitCodeOf(mpvErr),
				launched:   true,
				quickExit:  true,
				curlFailed: exitCodeOf(curlErr) != 0 && !stats.playing(),
			}, nil
		}
	}
}

func waitForCurlExit(cmd *exec.Cmd, done <-chan error) error {
	if done == nil {
		return nil
	}
	select {
	case err := <-done:
		return err
	case <-time.After(500 * time.Millisecond):
		_ = cmd.Process.Kill()
		return <-done
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
