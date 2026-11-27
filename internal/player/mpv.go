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
	mpvReadinessTimeout = 10 * time.Second

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
func trustedWithoutIPC(connected, marked bool, rawURL string, exitCode int) bool {
	if !isHTTPURL(rawURL) {
		// Local files that fail to open exit nonzero. A marker also covers a
		// signal quit, which mpv reports with a nonzero exit code.
		return exitCode == 0 || marked
	}
	if connected {
		return false
	}
	return marked
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

type pipeStartupCheck struct {
	curlArgs   []string
	mpvArgs    []string
	socketPath string
	sourceURL  string
	isLive     bool
	observer   ProgressObserver
	skipSink   *liveSkipSink
}

type launchResult struct {
	mpvStderr    string
	curlStderr   string
	exitCode     int
	launched     bool
	observed     bool
	trusted      bool
	curlFailed   bool
	playback     PlaybackResult
	shortWarning bool
	shortDur     float64
}

// succeeded reports whether playback actually happened. IPC evidence of
// loaded media always wins. With a reachable IPC socket, a clean exit alone
// is not enough: mpv can exit 0 right after failing to open an unplayable
// URL. A playback-start marker is the fallback for launches where IPC never
// connected; the exit code alone is never treated as proof for HTTP URLs.
func (r launchResult) succeeded() bool {
	if r.shortWarning || r.curlFailed || !r.launched {
		return false
	}
	return r.observed || r.trusted
}

// finalizeLaunch copies the poller's final state into the launch result. Both
// launchers close the IPC channel and join the poller before calling it.
func finalizeLaunch(result *launchResult, stats *playbackStats, ipcDone chan struct{}, ipcWG *sync.WaitGroup, sourceURL string, marked bool) {
	close(ipcDone)
	ipcWG.Wait()
	result.playback = stats.snapshot()
	result.observed = stats.observedPlayback()
	result.trusted = trustedWithoutIPC(stats.ipcConnected(), marked, sourceURL, result.exitCode)
	if isShort, dur := stats.isShortWarning(); isShort {
		result.shortWarning = true
		result.shortDur = dur
		result.launched = false
	}
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

// playSingleSource plays one source through two strategies: direct mpv with
// native options first, then a curl|mpv pipe for streams whose TLS/header
// quirks mpv's own HTTP stack rejects.
func playSingleSource(source provider.MediaSource, media model.ResolvedMedia, aniskipArgs []string, observer ProgressObserver, skipSink *liveSkipSink) (PlaybackResult, error) {
	socketPath := DefaultMPVSocketPath()

	// Strategy 1: direct MPV playback (primary).
	bin, prefix := mpvCommand()
	if bin == "" {
		return PlaybackResult{}, fmt.Errorf("mpv player is not available")
	}
	directArgs := append(prefix, buildMPVArgs(source, media, socketPath, aniskipArgs)...)
	direct := startPlayerWithStartupCheck(startupCheck{
		binary:     bin,
		args:       directArgs,
		socketPath: socketPath,
		sourceURL:  source.URL,
		isLive:     media.IsLive(),
		observer:   observer,
		skipSink:   skipSink,
	})
	if direct.shortWarning {
		return PlaybackResult{}, fmt.Errorf("source rejected: stream is a short placeholder/warning video (%.1fs <= %.0fs)", direct.shortDur, ShortWarningVideoMaxDuration)
	}
	if direct.succeeded() {
		return direct.playback, nil
	}
	mpvLog.Warn(
		"direct playback failed",
		"exitCode", direct.exitCode,
		"stderr", summarizeErr("", direct.mpvStderr),
	)

	// Strategy 2: curl-to-MPV pipe.
	headers := []string{
		"Accept: */*",
		"Connection: keep-alive",
	}
	if ua := strings.TrimSpace(source.UserAgent); ua != "" {
		headers = append(headers, "User-Agent: "+ua)
	}
	if strings.TrimSpace(source.Referer) != "" {
		headers = append(headers, "Referer: "+source.Referer)
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}
	headers = append(headers, extraHeaderFields(source)...)

	pipeMpvArgs := []string{
		"--no-ytdl",
		"--really-quiet",
		"--msg-level=all=error,term-msg=info",
		"--term-playing-msg=" + playbackMark,
		"--vo=gpu-next,gpu",
		"--gpu-context=auto",
		"--cache=yes",
		"--demuxer-seekable-cache=yes",
		"--demuxer-max-bytes=500M",
		"--demuxer-max-back-bytes=100M",
		"--demuxer-readahead-secs=300",
		"--stream-buffer-size=8M",
		"--hls-bitrate=max",
		"--network-timeout=8",
		"--input-ipc-server=" + socketPath,
		hwdecOptionArg(),
	}
	if strings.TrimSpace(source.UserAgent) != "" {
		pipeMpvArgs = append(pipeMpvArgs, "--user-agent="+source.UserAgent)
	}
	if strings.TrimSpace(source.Referer) != "" {
		pipeMpvArgs = append(pipeMpvArgs, "--referrer="+source.Referer)
		if !source.SuppressOrigin && explicitOrigin(source) == "" {
			if origin := originFromReferer(strings.TrimSpace(source.Referer)); origin != "" {
				pipeMpvArgs = append(pipeMpvArgs, "--http-header-fields=Origin: "+origin)
			}
		}
	}
	for _, field := range mpvExtraHeaderFields(source) {
		pipeMpvArgs = append(pipeMpvArgs, "--http-header-fields="+field)
	}

	if media.StartTime > 5 {
		pipeMpvArgs = append(pipeMpvArgs, fmt.Sprintf("--start=%d", int(media.StartTime)))
	}

	pipeMpvArgs = appendTitleArgs(pipeMpvArgs, media.DisplayTitle())
	pipeMpvArgs = appendSubtitleArgs(pipeMpvArgs, media.SubtitlePath(), media.SubtitleLanguage)
	pipeMpvArgs = appendAudioLangArgs(pipeMpvArgs, audioLanguagesFor(source, media), media.DisabledAudioLanguages)
	pipeMpvArgs = appendAudioTrackArgs(pipeMpvArgs, source)
	pipeMpvArgs = append(pipeMpvArgs, aniskipArgs...)
	pipeMpvArgs = appendExtraArgsWithBracket(pipeMpvArgs, source.ExtraArgs, media.SubtitlePath())
	if runtime.GOOS == "darwin" {
		pipeMpvArgs = append(pipeMpvArgs, "--focus-on=open")
	}
	if runtime.GOOS == "windows" {
		pipeMpvArgs = append(pipeMpvArgs, "--terminal=no")
	}

	curlArgs := buildCurlArgs(source.URL, headers)
	pipe, pipeErr := startPipeWithStartupCheck(pipeStartupCheck{
		curlArgs:   curlArgs,
		mpvArgs:    pipeMpvArgs,
		socketPath: socketPath,
		sourceURL:  source.URL,
		isLive:     media.IsLive(),
		observer:   observer,
		skipSink:   skipSink,
	})
	if pipeErr != nil {
		return PlaybackResult{}, fmt.Errorf("mpv playback failed: pipe startup error: %w", pipeErr)
	}
	if pipe.shortWarning {
		return PlaybackResult{}, fmt.Errorf("source rejected: stream is a short placeholder/warning video (%.1fs <= %.0fs)", pipe.shortDur, ShortWarningVideoMaxDuration)
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
	if !isHTTPURL(finalURL) {
		return nil
	}
	return []string{
		"--compressed",
		"--connect-timeout", "4",
		"--retry", "1",
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
		_, idleErr := client.GetProperty(ipcPropIdleActive)
		pauseProp, pauseErr := client.GetProperty(ipcPropPause)
		paused := false
		if pauseErr == nil {
			if b, ok := pauseProp.(bool); ok {
				paused = b
			}
		}
		// A socket that answers get_property is usable IPC even when the
		// properties are all "unavailable" (mpv never loaded media). Mark it
		// here rather than on dial so a connect that dies before its first
		// response does not suppress the playback-marker fallback.
		if posErr == nil || durErr == nil || idleErr == nil || pauseErr == nil {
			stats.markConnected()
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

// startPlayerWithStartupCheck launches binary and watches for IPC evidence of
// loaded media. Shared by mpv and IINA.
func startPlayerWithStartupCheck(check startupCheck) launchResult {
	_ = os.Remove(check.socketPath)
	// The process has exited by the time this returns; unlink its endpoint so
	// a killed/crashed player does not leave a stale socket in /tmp.
	defer func() { _ = os.Remove(check.socketPath) }()

	cmd := exec.Command(check.binary, check.args...)
	marker := &playbackMarkerWriter{}
	cmd.Stdout = marker
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return launchResult{mpvStderr: err.Error(), exitCode: 1}
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	return waitForPlaybackReadiness(cmd, done, check, stderr, marker)
}

func waitForPlaybackReadiness(
	cmd *exec.Cmd,
	done <-chan error,
	check startupCheck,
	stderr *bytes.Buffer,
	marker *playbackMarkerWriter,
) (result launchResult) {
	ipcDone := make(chan struct{})
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
		finalizeLaunch(&result, stats, ipcDone, &ipcWG, check.sourceURL, marker.found())
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
			_ = cmd.Process.Kill()
			drainErr := <-done
			_ = drainErr
			mpvLog.Warn("readiness timeout; killed process", "stderr", summarizeErr("", stderr.String()))
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
			if stats.playing() {
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

func startPipeWithStartupCheck(check pipeStartupCheck) (result launchResult, err error) {
	_ = os.Remove(check.socketPath)
	defer func() { _ = os.Remove(check.socketPath) }()

	bin, prefix := mpvCommand()
	if bin == "" {
		return launchResult{exitCode: 1}, fmt.Errorf("mpv player is not available")
	}

	curl := exec.Command("curl", check.curlArgs...)
	mpvArgs := append(prefix, check.mpvArgs...)
	mpv := exec.Command(bin, mpvArgs...)
	stdout, err := curl.StdoutPipe()
	if err != nil {
		return launchResult{exitCode: 1}, err
	}
	curlStderr := &bytes.Buffer{}
	curl.Stderr = curlStderr
	mpv.Stdin = stdout
	marker := &playbackMarkerWriter{}
	mpv.Stdout = marker
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
		finalizeLaunch(&result, stats, ipcDone, &ipcWG, check.sourceURL, marker.found())
	}()

	readinessTimer := time.NewTimer(mpvReadinessTimeout)
	defer readinessTimer.Stop()
	ticker := time.NewTicker(mpvReadinessPollInterval)
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
			drainErr := <-mpvDone
			_ = drainErr
			if curlDone != nil {
				_ = curl.Process.Kill()
				drainCurl := <-curlDone
				_ = drainCurl
			}
			return launchResult{
				mpvStderr:  mpvStderr.String(),
				curlStderr: curlStderr.String(),
				exitCode:   1,
				launched:   true,
			}, nil
		case <-ticker.C:
			if isShort, dur := stats.isShortWarning(); isShort {
				_ = mpv.Process.Kill()
				drainMPV := <-mpvDone
				_ = drainMPV
				if curlDone != nil {
					_ = curl.Process.Kill()
					drainCurl := <-curlDone
					_ = drainCurl
				}
				return launchResult{
					mpvStderr:    fmt.Sprintf("filtered short warning video (duration: %.1fs <= %.0fs)", dur, ShortWarningVideoMaxDuration),
					exitCode:     2,
					launched:     false,
					shortWarning: true,
					shortDur:     dur,
				}, nil
			}
			if !stats.playing() {
				continue
			}
			mpvErr := <-mpvDone
			curlErr := waitForCurlExit(curl, curlDone)
			isShort, dur := stats.isShortWarning()
			return launchResult{
				mpvStderr:    mpvStderr.String(),
				curlStderr:   curlStderr.String(),
				exitCode:     exitCodeOf(mpvErr),
				launched:     !isShort,
				curlFailed:   exitCodeOf(curlErr) != 0 && !stats.playing(),
				shortWarning: isShort,
				shortDur:     dur,
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
	case <-time.After(mpvShutdownWait):
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
