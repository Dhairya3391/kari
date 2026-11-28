//go:build !android

// Desktop mpv playback: Player implementation, argument construction, and
// process/readiness management. The IPC layer beneath this lives in ipc.go /
// ipc_posix.go / ipc_windows.go.
package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"kari/internal/lang"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/util"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// HLS needs a manifest plus its first segment before IPC sees media.
	// --network-timeout bounds a single request, so allow one more cycle.
	mpvReadinessTimeout = 20 * time.Second

	// mpvPollInterval drives the status ticker while playback is active.
	mpvPollInterval = 1 * time.Second
	// mpvShutdownWait is the grace period for a clean mpv exit before it is
	// killed forcibly.
	mpvShutdownWait = 500 * time.Millisecond

	// mpvReadinessPollInterval is how often the readiness loop checks IPC
	// stats for the first playback frame. Halving the default interval
	// shortens the gap between mpv actually starting and the UI noticing,
	// without shortening the readiness budget itself.
	mpvReadinessPollInterval = 50 * time.Millisecond

	// ShortWarningVideoMaxDuration is the maximum duration in seconds of a fake
	// placeholder or warning clip (e.g. 45-second Cloudflare/VPN notice) served by
	// hostile CDNs. Real episodic and movie media exceeds this threshold.
	ShortWarningVideoMaxDuration = 60.0

	// playbackMark is printed to stdout by mpv when playback actually starts.
	// Unlike an exit code it cannot be produced by a URL mpv failed to open,
	// so it is the signal used when the IPC socket is unreachable.
	playbackMark = "KARI_PLAYING"
)

// playbackMarkerWriter watches mpv's stdout for playbackMark. mpv writes a
// status line throughout playback, so only the tail needed to spot the marker
// is kept; memory does not grow with the length of the video.
type playbackMarkerWriter struct {
	mu   sync.Mutex
	tail string
	seen bool
}

func (w *playbackMarkerWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen {
		return len(p), nil
	}
	data := w.tail + string(p)
	if strings.Contains(data, playbackMark) {
		w.seen = true
		w.tail = ""
		return len(p), nil
	}
	keep := len(playbackMark) - 1
	if len(data) > keep {
		data = data[len(data)-keep:]
	}
	w.tail = data
	return len(p), nil
}

func (w *playbackMarkerWriter) found() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen
}

// maxOutputTailBytes bounds the diagnostic copy of mpv's stdout. mpv writes
// its errors there, not to stderr, so failure logs would otherwise be blank.
const maxOutputTailBytes = 4096

// outputTail keeps the tail of mpv's stdout for failure reports only.
type outputTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *outputTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(p) >= maxOutputTailBytes {
		t.buf = append(t.buf[:0], p[len(p)-maxOutputTailBytes:]...)
		return len(p), nil
	}
	if overflow := len(t.buf) + len(p) - maxOutputTailBytes; overflow > 0 {
		copy(t.buf, t.buf[overflow:])
		t.buf = t.buf[:len(t.buf)-overflow]
	}
	t.buf = append(t.buf, p...)
	return len(p), nil
}

func (t *outputTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func isShortWarningVideo(dur float64, rawURL string, isLive bool) bool {
	if isLive || dur <= 0 || dur > ShortWarningVideoMaxDuration {
		return false
	}
	return isHTTPURL(rawURL)
}

// isHTTPURL reports whether rawURL is fetched over HTTP(S). Network streams
// can fail without a nonzero exit code (a CDN can serve an unplayable body),
// so their launches need IPC evidence before counting as playback.
func isHTTPURL(rawURL string) bool {
	u := strings.ToLower(strings.TrimSpace(rawURL))
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// mediaLoaded reports whether IPC property reads prove media is loaded.
// idle-active is deliberately excluded: mpv reports it false as soon as it
// is launched with a file, even when opening that file fails.
func mediaLoaded(posErr, durErr error, dur float64) bool {
	return posErr == nil || (durErr == nil && dur > 0)
}

// trustedWithoutIPC reports whether a launch with no IPC evidence can still
// be trusted. mpv returns 0 both after real playback and after a file it
// could not play once initialization mostly succeeded, so the exit code alone
// proves nothing for HTTP URLs. The playback-start marker does: mpv prints it
// only after it actually begins playing the file.
func trustedWithoutIPC(marked bool, rawURL string, exitCode int) bool {
	if marked {
		// A live IPC socket is not evidence: mpv answers pause/idle-active
		// before it loads media, so those replies must not hide the marker.
		return true
	}
	if !isHTTPURL(rawURL) {
		// Local files that fail to open exit nonzero.
		return exitCode == 0
	}
	return false
}

type startupCheck struct {
	binary     string
	args       []string
	socketPath string
	sourceURL  string
	isLive     bool
	observer   ProgressObserver
	skipSink   *liveSkipSink
}

type launchResult struct {
	mpvStderr    string
	stdoutTail   string
	exitCode     int
	launched     bool
	observed     bool
	trusted      bool
	playback     PlaybackResult
	shortWarning bool
	shortDur     float64
}

// succeeded reports whether playback actually happened. IPC evidence of
// loaded media or mpv's playback-start marker decide it. A clean exit alone
// is never proof for HTTP URLs: mpv can exit 0 right after failing to open an
// unplayable URL.
func (r launchResult) succeeded() bool {
	if r.shortWarning || !r.launched {
		return false
	}
	return r.observed || r.trusted
}

// playbackReady reports whether IPC or mpv's playback marker proves media
// started. The marker matters when IPC answers properties but stays blind to
// duration and position.
func playbackReady(stats *playbackStats, marker *playbackMarkerWriter) bool {
	return marker.found() || stats.playing()
}

// snapshotLaunch copies the poller's final state into the launch result.
func snapshotLaunch(result *launchResult, stats *playbackStats, sourceURL string, marked bool, tail *outputTail) {
	result.playback = stats.snapshot()
	result.observed = stats.observedPlayback()
	result.stdoutTail = tail.String()
	result.trusted = trustedWithoutIPC(marked, sourceURL, result.exitCode)
	if isShort, dur := stats.isShortWarning(); isShort {
		result.shortWarning = true
		result.shortDur = dur
		result.launched = false
	}
}

// finalizeLaunch stops the poller, then snapshots its final state.
func finalizeLaunch(result *launchResult, stats *playbackStats, ipcDone chan struct{}, ipcWG *sync.WaitGroup, sourceURL string, marked bool, tail *outputTail) {
	close(ipcDone)
	ipcWG.Wait()
	snapshotLaunch(result, stats, sourceURL, marked, tail)
}

// MPVPlayer plays via a desktop mpv process, using JSON IPC for position
// tracking so resume/scrobble get real playback stats.
type MPVPlayer struct {
	skipClients      SkipClients
	skipSettings     SkipSettings
	skipCache        *util.BoundedCache[combinedSkipTimes]
	progressObserver ProgressObserver
}

var _ Player = (*MPVPlayer)(nil)

// Name implements Player.
func (p *MPVPlayer) Name() string { return "mpv" }

// Available implements Player. It checks for native mpv on PATH first, then
// falls back to Flatpak (io.mpv.Mpv) on Linux.
func (p *MPVPlayer) Available() bool {
	bin, _ := mpvCommand()
	return bin != ""
}

// mpvCommand returns the binary name and any prefix arguments required to
// execute mpv. On desktop Linux where mpv was installed through Flatpak, it
// returns ("flatpak", []string{"run", "io.mpv.Mpv"}). Returns empty string
// when mpv is unavailable.
func mpvCommand() (string, []string) {
	if _, err := exec.LookPath("mpv"); err == nil {
		return "mpv", nil
	}
	if isFlatpakMPVAvailable() {
		return "flatpak", []string{"run", "io.mpv.Mpv"}
	}
	return "", nil
}

// isFlatpakMPVAvailable detects whether Flatpak MPV (io.mpv.Mpv) is installed
// on Linux desktop environments.
func isFlatpakMPVAvailable() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if _, err := exec.LookPath("flatpak"); err != nil {
		return false
	}
	if _, err := exec.LookPath("io.mpv.Mpv"); err == nil {
		return true
	}

	candidates := []string{
		"/var/lib/flatpak/app/io.mpv.Mpv",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "flatpak", "app", "io.mpv.Mpv"))
	}
	for _, dir := range candidates {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return true
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), mpvPollInterval)
	defer cancel()
	return exec.CommandContext(ctx, "flatpak", "info", "io.mpv.Mpv").Run() == nil
}

func (p *MPVPlayer) setSkipSettings(s SkipSettings) {
	p.skipSettings = s
}

func (p *MPVPlayer) setProgressObserver(obs ProgressObserver) {
	p.progressObserver = obs
}

// Play implements Player.
func (p *MPVPlayer) Play(sources []provider.MediaSource, media model.ResolvedMedia) (PlaybackResult, error) {
	mpvLog.Debug("playback starting", "media", media.DisplayTitle(), "sources", len(sources))
	var defaultSkipArgs []string
	var defaultSkipScript string
	var defaultResolved bool
	skipSink := newLiveSkipSink(p.skipSettings)
	skipClients := p.skipClients
	skipClients.SkipSink = skipSink
	getDefaultSkipArgs := func() ([]string, string) {
		if !defaultResolved {
			defaultSkipArgs, defaultSkipScript = getSkipArgs(
				skipClients,
				p.skipSettings,
				media,
				p.skipCache,
			)
			defaultResolved = true
		}
		return defaultSkipArgs, defaultSkipScript
	}
	defer func() {
		if defaultSkipScript != "" {
			cleanupSkipScript(defaultSkipScript)
		}
	}()

	return attemptSources("mpv", media.DisplayTitle(), sources, func(source provider.MediaSource) (PlaybackResult, error) {
		skipArgs, skipPath, isMiruro := resolveSourceSkipArgs(source, p.skipSettings, media)
		if isMiruro {
			defer cleanupSkipScript(skipPath)
		} else {
			skipArgs, _ = getDefaultSkipArgs()
		}
		return playSingleSource(source, media, skipArgs, p.progressObserver, skipSink)
	})
}

// playSingleSource launches mpv once, keeping the first source that proves
// playback actually started. A failure is reported with mpv's stdout tail,
// since that is where mpv writes its diagnostics.
func playSingleSource(source provider.MediaSource, media model.ResolvedMedia, aniskipArgs []string, observer ProgressObserver, skipSink *liveSkipSink) (PlaybackResult, error) {
	socketPath := DefaultMPVSocketPath()

	bin, prefix := mpvCommand()
	if bin == "" {
		return PlaybackResult{}, fmt.Errorf("mpv player is not available")
	}
	args := append(prefix, buildMPVArgs(source, media, socketPath, aniskipArgs)...)
	result := startPlayerWithStartupCheck(startupCheck{
		binary:     bin,
		args:       args,
		socketPath: socketPath,
		sourceURL:  source.URL,
		isLive:     media.IsLive(),
		observer:   observer,
		skipSink:   skipSink,
	})
	if result.shortWarning {
		return PlaybackResult{}, fmt.Errorf("source rejected: stream is a short placeholder/warning video (%.1fs <= %.0fs)", result.shortDur, ShortWarningVideoMaxDuration)
	}
	if result.succeeded() {
		return result.playback, nil
	}

	stdout := summarizeTail(result.stdoutTail)
	mpvLog.Warn("direct playback failed",
		"exitCode", result.exitCode,
		"stderr", summarizeErr("", result.mpvStderr),
		"stdout", stdout)
	details := joinNonEmpty(summarizeErr("stderr", result.mpvStderr), stdout)
	if details == "" {
		return PlaybackResult{}, fmt.Errorf("mpv playback failed (exit code %d)", result.exitCode)
	}
	return PlaybackResult{}, fmt.Errorf("mpv playback failed (exit code %d): %s", result.exitCode, details)
}

// buildMPVArgs assembles the full mpv command line for one source:
// buffering/cache tuning, resume position, transport identity (UA/referer/
// cookies/origin), title, subtitles, aniskip hooks, and the IPC socket.
func buildMPVArgs(source provider.MediaSource, media model.ResolvedMedia, socketPath string, aniskipArgs []string) []string {
	args := []string{
		"--no-ytdl",
		"--msg-level=all=warn,term-msg=info",
		"--term-playing-msg=" + playbackMark,
		"--vo=gpu-next,gpu",
		"--gpu-context=auto",
		hwdecOptionArg(),
		"--network-timeout=8",
		"--cache=yes",
		"--cache-pause-initial=no",
		"--demuxer-seekable-cache=yes",
		"--demuxer-max-bytes=500M",
		"--demuxer-max-back-bytes=100M",
		"--demuxer-readahead-secs=300",
		"--stream-buffer-size=8M",
		"--hls-bitrate=max",
	}
	if runtime.GOOS == "windows" {
		args = append(args, "--terminal=no")
	}
	if runtime.GOOS == "darwin" {
		args = append(args, "--focus-on=open")
	}

	if media.StartTime > 5 {
		args = append(args, fmt.Sprintf("--start=%d", int(media.StartTime)))
	}

	if ua := strings.TrimSpace(source.UserAgent); ua != "" {
		args = append(args, "--user-agent="+ua)
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
	if strings.TrimSpace(source.Referer) != "" && explicitOrigin(source) == "" {
		if !source.SuppressOrigin {
			if origin := originFromReferer(strings.TrimSpace(source.Referer)); origin != "" {
				headers = append(headers, "Origin: "+origin)
			}
		}
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}
	headers = append(headers, mpvExtraHeaderFields(source)...)
	if len(headers) > 0 {
		// mpv's list-typed options split list values on commas; joining with
		// anything else (like CR/LF) corrupts the header block. Plain
		// comma-join is correct here precisely because UA/Referer never go
		// through this path.
		args = append(args, "--http-header-fields="+strings.Join(headers, ","))
	}

	args = appendTitleArgs(args, media.DisplayTitle())
	args = appendSubtitleArgs(args, media.SubtitlePath(), media.SubtitleLanguage)
	args = appendAudioLangArgs(args, audioLanguagesFor(source, media), media.DisabledAudioLanguages)
	args = appendAudioTrackArgs(args, source)
	args = append(args, aniskipArgs...)
	// ExtraArgs may contain --demuxer-lavf-format=hls (miruro). When
	// a subtitle file is present, that format override bleeds onto the subtitle
	// file open and makes mpv fail with "Can not open external file". Scope
	// ExtraArgs to the video file only via per-file brackets so the global
	// subtitle open is not affected. Brackets are a no-op when ExtraArgs is
	// empty or contains no format override.
	args = appendExtraArgsWithBracket(args, source.ExtraArgs, media.SubtitlePath())
	args = append(args, "--input-ipc-server="+socketPath)

	return append(args, source.URL)
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
func appendSubtitleArgs(args []string, subtitlePath, subtitleLanguage string) []string {
	if slang := lang.SubtitleLangs(subtitleLanguage); len(slang) > 0 {
		args = append(args, "--slang="+strings.Join(slang, ","))
	}
	subtitlePath = strings.TrimSpace(subtitlePath)
	if subtitlePath == "" {
		return args
	}
	subtitlePath = strings.ReplaceAll(subtitlePath, `\`, `/`)
	mpvLog.Debug("subtitle side-loaded", "path", subtitlePath)
	return append(args, "--sub-file="+subtitlePath)
}

// appendAudioLangArgs passes preferred audio-track languages (--alang) to MPV.
// The code-to-track mapping and off-list filtering live in lang; this stays a
// thin flag formatter.
func appendAudioLangArgs(args []string, languages, disabled []string) []string {
	alang := lang.AudioLangsForExcept(languages, disabled)
	if len(alang) == 0 {
		return args
	}
	return append(args, "--alang="+strings.Join(alang, ","))
}

// appendAudioTrackArgs loads every manifest rendition as an extra audio input
// so mpv lists all advertised tracks, not just the one muxed into a video
// variant. Common transport headers are global mpv options and apply here too.
func appendAudioTrackArgs(args []string, source provider.MediaSource) []string {
	for _, track := range source.AudioTracks {
		if address := strings.TrimSpace(track.URL); address != "" {
			args = append(args, "--audio-file="+address)
		}
	}
	return args
}

// hasLavfFormatOverride reports whether any of the given ExtraArgs sets
// --demuxer-lavf-format, which causes mpv to force that format on every
// file it opens — including external subtitle files — breaking sub loading.
func hasLavfFormatOverride(extraArgs []string) bool {
	for _, a := range extraArgs {
		if strings.HasPrefix(a, "--demuxer-lavf-format=") {
			return true
		}
	}
	return false
}

// appendExtraArgsWithBracket appends provider ExtraArgs to args. When a
// subtitle path is present and ExtraArgs contain --demuxer-lavf-format=, the
// args are wrapped in mpv per-file brackets (--{ … --}) so the demuxer format
// override only applies to the main video URL and not to the external subtitle
// file that mpv opens separately. The URL must follow immediately after this
// call for the bracket to take effect.
func appendExtraArgsWithBracket(args, extraArgs []string, subtitlePath string) []string {
	if len(extraArgs) == 0 {
		return args
	}
	if strings.TrimSpace(subtitlePath) != "" && hasLavfFormatOverride(extraArgs) {
		args = append(args, "--{")
		args = append(args, extraArgs...)
		return append(args, "--}")
	}
	return append(args, extraArgs...)
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
	sourceURL string,
	isLive bool,
	observer ProgressObserver,
	done <-chan struct{},
) {
	defer client.Close()

	poll := func() bool {
		pos, posErr := client.GetProperty(ipcPropTimePos)
		var posF float64
		if posErr == nil {
			if f, ok := pos.(float64); ok {
				posF = f
			}
		}
		dur, durErr := client.GetProperty(ipcPropDuration)
		var durF float64
		if durErr == nil {
			if f, ok := dur.(float64); ok {
				durF = f
			}
		}
		paused := false
		if observer != nil && (posErr == nil || durErr == nil) {
			pauseProp, pauseErr := client.GetProperty(ipcPropPause)
			if pauseErr == nil {
				if b, ok := pauseProp.(bool); ok {
					paused = b
				}
			}
		}
		var idleErr error
		if posErr != nil && durErr != nil {
			// idle-active distinguishes a live IPC socket from a player that
			// never loaded media, so it is only needed when both fail.
			_, idleErr = client.GetProperty(ipcPropIdleActive)
		}
		if isShortWarningVideo(durF, sourceURL, isLive) {
			mpvLog.Warn("rejected short placeholder/warning stream", "duration", durF, "url", sourceURL)
			stats.setShortWarning(durF)
			_, _ = client.command("quit", "2")
			return false
		}

		loaded := mediaLoaded(posErr, durErr, durF)
		failed := posErr != nil && durErr != nil && idleErr != nil

		if loaded || posF > 0 || durF > 0 {
			stats.update(posF, durF, loaded)
		}
		if observer != nil && (loaded || posF > 0 || durF > 0) {
			observer(PlaybackProgress{PositionSecs: posF, DurationSecs: durF, Paused: paused})
		}
		return !failed
	}

	for {
		if err := client.Connect(3 * time.Second); err != nil {
			mpvLog.Debug("ipc connect failed", "err", err)
		} else {
			if healthy := poll(); healthy {
				ticker := time.NewTicker(mpvPollInterval)
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
		case <-time.After(mpvShutdownWait):
			// retry loop: redial
		}
	}
}

// startPlayerWithStartupCheck launches binary and watches for IPC evidence or
// mpv's playback marker. Shared by mpv and IINA.
func startPlayerWithStartupCheck(check startupCheck) launchResult {
	_ = os.Remove(check.socketPath)

	cmd := exec.Command(check.binary, check.args...)
	marker := &playbackMarkerWriter{}
	tail := &outputTail{}
	cmd.Stdout = io.MultiWriter(marker, tail)
	// exec copies stderr in its own goroutine; a locked tail lets readiness
	// polling read it without racing the write.
	stderr := &outputTail{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return launchResult{mpvStderr: err.Error(), stdoutTail: tail.String(), exitCode: 1}
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	return waitForPlaybackReadiness(cmd, done, check, stderr, marker, tail)
}

func waitForPlaybackReadiness(
	cmd *exec.Cmd,
	done <-chan error,
	check startupCheck,
	stderr *outputTail,
	marker *playbackMarkerWriter,
	tail *outputTail,
) (result launchResult) {
	ipcDone := make(chan struct{})
	keepPoller := false
	var ipcWG sync.WaitGroup
	client := NewIPCClient(check.socketPath)
	if check.skipSink != nil {
		check.skipSink.attach(client)
	}
	stats := newPlaybackStats()
	ipcWG.Add(1)
	go func() {
		defer ipcWG.Done()
		ipcPoller(context.Background(), client, stats, check.sourceURL, check.isLive, check.observer, ipcDone)
	}()
	defer func() {
		if keepPoller {
			// The player is still running: snapshot without stopping IPC. A
			// goroutine releases the socket and poller when it exits.
			snapshotLaunch(&result, stats, check.sourceURL, true, tail)
			return
		}
		finalizeLaunch(&result, stats, ipcDone, &ipcWG, check.sourceURL, marker.found(), tail)
		_ = os.Remove(check.socketPath)
	}()

	readinessTimer := time.NewTimer(mpvReadinessTimeout)
	defer readinessTimer.Stop()
	ticker := time.NewTicker(mpvReadinessPollInterval)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			return launchResult{
				mpvStderr: stderr.String(),
				exitCode:  exitCodeOf(err),
				launched:  true,
			}
		case <-readinessTimer.C:
			if marker.found() {
				// Media started, so do not kill mpv. A short VOD file exits on
				// its own and still yields an exit code; a live stream never
				// exits, so bound the wait instead of blocking readiness.
				select {
				case err := <-done:
					return launchResult{
						mpvStderr: stderr.String(),
						exitCode:  exitCodeOf(err),
						launched:  true,
					}
				case <-time.After(mpvShutdownWait):
					keepPoller = true
					go func() {
						if err := <-done; err != nil {
							mpvLog.Debug("mpv exited after readiness timeout", "err", err)
						}
						close(ipcDone)
						ipcWG.Wait()
						_ = os.Remove(check.socketPath)
					}()
					return launchResult{launched: true}
				}
			}
			_ = cmd.Process.Kill()
			drainErr := <-done
			_ = drainErr
			mpvLog.Warn("readiness timeout; killed process",
				"stderr", summarizeErr("", stderr.String()), "stdout", summarizeTail(tail.String()))
			return launchResult{mpvStderr: stderr.String(), exitCode: 1, launched: true}
		case <-ticker.C:
			if isShort, dur := stats.isShortWarning(); isShort {
				_ = cmd.Process.Kill()
				drainErr := <-done
				_ = drainErr
				return launchResult{
					mpvStderr:    fmt.Sprintf("filtered short warning video (duration: %.1fs <= %.0fs)", dur, ShortWarningVideoMaxDuration),
					exitCode:     2,
					launched:     false,
					shortWarning: true,
					shortDur:     dur,
				}
			}
			if playbackReady(stats, marker) {
				err := <-done
				isShort, dur := stats.isShortWarning()
				return launchResult{
					mpvStderr:    stderr.String(),
					exitCode:     exitCodeOf(err),
					launched:     !isShort,
					shortWarning: isShort,
					shortDur:     dur,
				}
			}
		}
	}
}

// summarizeTail returns the last nonblank line of mpv's stdout, truncated.
func summarizeTail(output string) string {
	// mpv writes progress with \r, so a raw split can leave one long blob
	// whose truncated prefix hides the final error.
	normalized := strings.ReplaceAll(strings.TrimSpace(output), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if len(line) > 160 {
			line = line[:160] + "..."
		}
		return line
	}
	return ""
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
