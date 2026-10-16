//go:build !android

// Desktop launchers for external players: VLC everywhere, and IINA wherever
// its binary can be found (in practice macOS). IINA supports mpv-compatible
// IPC telemetry, while VLC reports NeedsCompletionConfirm.
package player

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/util"
)

const vlcStartupTimeout = 3 * time.Second

// VLCPlayer launches VLC with per-source args.
type VLCPlayer struct{}

var _ Player = (*VLCPlayer)(nil)

// Name implements Player.
func (p *VLCPlayer) Name() string { return "vlc" }

// Available implements Player.
func (p *VLCPlayer) Available() bool { return vlcBinary() != "" }

// Play implements Player.
func (p *VLCPlayer) Play(sources []provider.MediaSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return attemptSources("vlc", sources, func(source provider.MediaSource) (PlaybackResult, error) {
		if err := playSingleSourceWithVLC(source, media); err != nil {
			return PlaybackResult{}, err
		}
		return PlaybackResult{}, &NeedsCompletionConfirmError{}
	})
}

// vlcBinary locates VLC across PATH and the standard macOS app bundle,
// which is never on PATH after a drag-install.
func vlcBinary() string {
	if path, err := exec.LookPath("vlc"); err == nil {
		return path
	}
	path := "/Applications/VLC.app/Contents/MacOS/VLC"
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// playSingleSourceWithVLC starts VLC and treats a launch that survives the
// startup window as success; VLC gives no usable exit semantics for streams.
func playSingleSourceWithVLC(source provider.MediaSource, media model.ResolvedMedia) error {
	args := buildVLCArgs(source, media)
	playerLog.Debug("vlc launching", "args", len(args))

	cmd := exec.Command(vlcBinary(), args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start vlc: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		return errors.New("vlc exited unexpectedly")
	case <-time.After(vlcStartupTimeout):
		return nil
	}
}

// buildVLCArgs maps transport identity and resume position onto VLC's
// option spellings.
func buildVLCArgs(source provider.MediaSource, media model.ResolvedMedia) []string {
	args := []string{
		"--play-and-exit",
		"--no-video-title-show",
	}

	ua := strings.TrimSpace(source.UserAgent)
	if ua == "" {
		ua = strings.TrimSpace(config.AndroidUA())
	}
	if ua != "" {
		args = append(args, "--http-user-agent="+ua)
	}
	if strings.TrimSpace(source.Referer) != "" {
		args = append(args, "--http-referrer="+source.Referer)
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		args = append(args, "--http-set-cookie="+strings.TrimSpace(source.CookieHeader))
	}
	if media.StartTime > 5 {
		args = append(args, fmt.Sprintf("--start-time=%d", int(media.StartTime)))
	}
	if title := media.DisplayTitle(); title != "" {
		args = append(args, "--meta-title="+sanitizeMediaTitle(title))
	}
	if lang := strings.ToLower(strings.TrimSpace(source.Language)); lang != "" {
		args = append(args, "--audio-language="+lang)
	}
	if subtitlePath := media.SubtitlePath(); subtitlePath != "" {
		args = append(args, "--sub-file="+strings.ReplaceAll(subtitlePath, `\`, `/`))
	}

	return append(args, source.URL)
}

// IINAPlayer launches the macOS IINA player (mpv-derived), reusing the
// shared process/readiness machinery from mpv.go.
type IINAPlayer struct {
	skipClients      SkipClients
	skipSettings     SkipSettings
	skipCache        *util.BoundedCache[combinedSkipTimes]
	progressObserver ProgressObserver
}

var _ Player = (*IINAPlayer)(nil)

// Name implements Player.
func (p *IINAPlayer) Name() string { return "iina" }

// Available implements Player.
func (p *IINAPlayer) Available() bool { return iinaBinary() != "" }

func (p *IINAPlayer) setSkipSettings(s SkipSettings) {
	p.skipSettings = s
}

func (p *IINAPlayer) setProgressObserver(obs ProgressObserver) {
	p.progressObserver = obs
}

// Play implements Player.
func (p *IINAPlayer) Play(sources []provider.MediaSource, media model.ResolvedMedia) (PlaybackResult, error) {
	bin := iinaBinary()
	if bin == "" {
		return PlaybackResult{}, errors.New("iina playback failed: iina-cli not found")
	}
	skipArgs, skipPath := getSkipArgs(
		p.skipClients,
		p.skipSettings,
		media,
		p.skipCache,
	)
	defer cleanupSkipScript(skipPath)

	return attemptSources("iina", sources, func(source provider.MediaSource) (PlaybackResult, error) {
		return playSingleSourceWithIINA(bin, source, media, skipArgs, p.progressObserver)
	})
}

// iinaBinary locates IINA's CLI wrapper across common install shapes.
func iinaBinary() string {
	if path, err := exec.LookPath("iina-cli"); err == nil {
		return path
	}
	if path, err := exec.LookPath("iina"); err == nil {
		return path
	}
	path := "/Applications/IINA.app/Contents/MacOS/iina-cli"
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// playSingleSourceWithIINA plays one source through the shared readiness
// check; IINA speaks mpv's IPC protocol so real stats come back.
func playSingleSourceWithIINA(binary string, source provider.MediaSource, media model.ResolvedMedia, aniskipArgs []string, observer ProgressObserver) (PlaybackResult, error) {
	socketPath := DefaultMPVSocketPath()
	args := buildIINAArgs(source, media, socketPath, aniskipArgs)
	result := startPlayerWithStartupCheck(startupCheck{
		binary:     binary,
		args:       args,
		socketPath: socketPath,
		sourceURL:  source.URL,
		isLive:     media.IsLive(),
		observer:   observer,
	})
	if result.succeeded() {
		return result.playback, nil
	}
	if result.shortWarning {
		return PlaybackResult{}, fmt.Errorf("source rejected: stream is a short placeholder/warning video (%.1fs <= %.0fs)", result.shortDur, ShortWarningVideoMaxDuration)
	}
	if result.mpvStderr == "" {
		return PlaybackResult{}, fmt.Errorf("process exited with code %d", result.exitCode)
	}
	return PlaybackResult{}, fmt.Errorf(
		"process exited with code %d: %s",
		result.exitCode,
		result.mpvStderr,
	)
}

// buildIINAArgs mirrors buildMPVArgs minus mpv-only options IINA rejects;
// keep both in sync when adding stream options.
func buildIINAArgs(source provider.MediaSource, media model.ResolvedMedia, socketPath string, aniskipArgs []string) []string {
	args := []string{"--no-stdin", "--keep-running", source.URL, "--"}
	args = append(args,
		"--no-ytdl",
		"--network-timeout=15",
		"--cache=yes",
		"--cache-pause-initial=no",
		"--demuxer-seekable-cache=yes",
		"--demuxer-max-bytes=150M",
		"--demuxer-max-back-bytes=30M",
		"--demuxer-readahead-secs=60",
		"--stream-buffer-size=8M",
		"--hls-bitrate=max",
		"--input-ipc-server="+socketPath,
	)

	if media.StartTime > 5 {
		args = append(args, fmt.Sprintf("--start=%d", int(media.StartTime)))
	}

	userAgent := config.AndroidUA()
	if strings.TrimSpace(source.UserAgent) != "" {
		userAgent = source.UserAgent
	}
	if userAgent != "" {
		args = append(args, "--user-agent="+userAgent)
	}
	if strings.TrimSpace(source.Referer) != "" {
		args = append(args, "--referrer="+source.Referer)
	}

	// Same header rules as buildMPVArgs: UA/Referer via native options only.
	var headers []string
	if strings.TrimSpace(source.Referer) != "" && !source.SuppressOrigin {
		if origin := originFromReferer(strings.TrimSpace(source.Referer)); origin != "" {
			headers = append(headers, "Origin: "+origin)
		}
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}
	if len(headers) > 0 {
		args = append(args, "--http-header-fields="+strings.Join(headers, ","))
	}

	args = appendTitleArgs(args, media.DisplayTitle())
	args = appendIINASubtitleArgs(args, media.SubtitlePath())
	args = appendAudioLangArgs(args, source.Language)
	args = append(args, aniskipArgs...)
	// IINA puts the URL in [files] before "--", so mpv per-file brackets
	// (--{ … --}) inside the "--" section cannot scope options to that URL.
	// When a subtitle is loaded and ExtraArgs carry --demuxer-lavf-format=,
	// that override bleeds onto the subtitle file open and silently drops it.
	// Strip the format override in that case: miruro CDNs serve
	// proper Content-Type headers so lavf auto-detects HLS by content probe.
	return append(args, iinaExtraArgs(source.ExtraArgs, media.SubtitlePath())...)
}

// appendIINASubtitleArgs side-loads subtitle inputs using mpv's canonical
// list option. iina-cli forwards everything after "--" as --mpv-* into
// libmpv, where the --sub-file CLI alias is never resolved (iina/iina#1991),
// so --sub-file silently loads nothing. Verified against IINA 1.4.4:
// --sub-files=<path> loads the track, --sub-files-append does not.
func appendIINASubtitleArgs(args []string, subtitlePath string) []string {
	subtitlePath = strings.TrimSpace(subtitlePath)
	if subtitlePath == "" {
		return args
	}
	subtitlePath = strings.ReplaceAll(subtitlePath, `\`, `/`)
	playerLog.Debug("subtitle side-loaded", "player", "iina", "path", subtitlePath)
	return append(args, "--sub-files="+subtitlePath)
}

// iinaExtraArgs returns ExtraArgs safe for IINA's "--" passthrough. Because
// IINA places the URL before "--", per-file brackets cannot scope demuxer
// overrides to the video only. Strip --demuxer-lavf-format= when a subtitle
// path is present so the format override does not bleed onto the subtitle
// file open. Providers that set this option target remote HLS streams whose
// CDN serves Content-Type headers, so lavf auto-detects the format anyway.
func iinaExtraArgs(extraArgs []string, subtitlePath string) []string {
	if strings.TrimSpace(subtitlePath) == "" || !hasLavfFormatOverride(extraArgs) {
		return extraArgs
	}
	out := make([]string, 0, len(extraArgs))
	for _, a := range extraArgs {
		if !strings.HasPrefix(a, "--demuxer-lavf-format=") {
			out = append(out, a)
		}
	}
	return out
}
