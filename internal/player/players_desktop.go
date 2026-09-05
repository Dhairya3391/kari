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
)

const (
	// vlcStartupTimeout bounds how long a VLC launch is watched before
	// assuming it started fine.
	vlcStartupTimeout = 3 * time.Second
	// iinaStartupTimeout matches IINA's mpv-derived startup behavior.
	iinaStartupTimeout = 4500 * time.Millisecond
)

// VLCPlayer launches VLC with per-source args.
type VLCPlayer struct{}

var _ Player = (*VLCPlayer)(nil)

// Name implements Player.
func (p *VLCPlayer) Name() string { return "vlc" }

// Available implements Player.
func (p *VLCPlayer) Available() bool {
	_, err := exec.LookPath("vlc")
	return err == nil
}

// Play implements Player.
func (p *VLCPlayer) Play(sources []provider.MediaSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return attemptSources("vlc", sources, func(source provider.MediaSource) (PlaybackResult, error) {
		if err := playSingleSourceWithVLC(source, media); err != nil {
			return PlaybackResult{}, err
		}
		return PlaybackResult{}, &NeedsCompletionConfirmError{Media: media}
	})
}

// playSingleSourceWithVLC starts VLC and treats a launch that survives the
// startup window as success; VLC gives no usable exit semantics for streams.
func playSingleSourceWithVLC(source provider.MediaSource, media model.ResolvedMedia) error {
	args := buildVLCArgs(source, media)
	playerLog.Debug("vlc launching", "args", len(args))

	cmd := exec.Command("vlc", args...)
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
	for _, sub := range media.SubtitlePaths() {
		if strings.TrimSpace(sub) != "" {
			sub = strings.ReplaceAll(sub, `\`, `/`)
			args = append(args, "--sub-file="+sub)
		}
	}

	return append(args, source.URL)
}

// IINAPlayer launches the macOS IINA player (mpv-derived), reusing the
// shared process/readiness machinery from mpv.go.
type IINAPlayer struct{}

var _ Player = (*IINAPlayer)(nil)

// Name implements Player.
func (p *IINAPlayer) Name() string { return "iina" }

// Available implements Player.
func (p *IINAPlayer) Available() bool { return iinaBinary() != "" }

// Play implements Player.
func (p *IINAPlayer) Play(sources []provider.MediaSource, media model.ResolvedMedia) (PlaybackResult, error) {
	bin := iinaBinary()
	if bin == "" {
		return PlaybackResult{}, errors.New("iina playback failed: iina-cli not found")
	}
	return attemptSources("iina", sources, func(source provider.MediaSource) (PlaybackResult, error) {
		return playSingleSourceWithIINA(bin, source, media)
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
func playSingleSourceWithIINA(binary string, source provider.MediaSource, media model.ResolvedMedia) (PlaybackResult, error) {
	socketPath := DefaultMPVSocketPath()
	args := buildIINAArgs(source, media, socketPath)
	stderr, exitCode, launched, quickExit, stats :=
		startPlayerWithStartupCheck(binary, args, iinaStartupTimeout, socketPath)
	if attemptSucceeded(launched, exitCode, quickExit, stats) {
		return stats, nil
	}
	if stderr == "" {
		return PlaybackResult{}, fmt.Errorf("process exited with code %d", exitCode)
	}
	return PlaybackResult{}, fmt.Errorf("process exited with code %d: %s", exitCode, stderr)
}

// buildIINAArgs mirrors buildMPVArgs minus mpv-only options IINA rejects;
// keep both in sync when adding stream options.
func buildIINAArgs(source provider.MediaSource, media model.ResolvedMedia, socketPath string) []string {
	args := []string{"--no-stdin", "--keep-running", source.URL, "--"}
	args = append(args,
		"--no-ytdl",
		"--network-timeout=15",
		"--cache=yes",
		"--cache-pause-initial=no",
		"--stream-buffer-size=8M",
		"--demuxer-seekable-cache=yes",
		"--demuxer-max-bytes=150M",
		"--demuxer-max-back-bytes=30M",
		"--demuxer-readahead-secs=60",
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
		ref := strings.TrimSuffix(source.Referer, "/")
		headers = append(headers, "Origin: "+ref)
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}
	if len(headers) > 0 {
		args = append(args, "--http-header-fields="+strings.Join(headers, ","))
	}

	args = appendTitleArgs(args, media.DisplayTitle())
	args = appendSubtitleArgs(args, media.SubtitlePaths())
	return append(args, source.ExtraArgs...)
}
