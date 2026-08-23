//go:build darwin && !android

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
)

const iinaStartupTimeout = 4500 * time.Millisecond

type IINAPlayer struct{}

var _ Player = (*IINAPlayer)(nil)

func (p *IINAPlayer) Name() string {
	return "iina"
}

func (p *IINAPlayer) Available() bool {
	return iinaAvailable()
}

func (p *IINAPlayer) Play(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return PlayWithIINASources(sources, media)
}

func PlayWithIINASources(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	bin := iinaBinary()
	if bin == "" {
		return PlaybackResult{}, errors.New("iina playback failed: iina-cli not found")
	}

	return attemptSources("iina", sources, func(source model.PlaybackSource) (PlaybackResult, error) {
		return playSingleSourceWithIINA(bin, source, media)
	})
}

func iinaAvailable() bool {
	return iinaBinary() != ""
}

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

func playSingleSourceWithIINA(binary string, source model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	socketPath := DefaultMPVSocketPath()
	args := buildIINAArgs(source, media, socketPath)
	stderr, exitCode, launched, quickExit, stats := startPlayerWithStartupCheck(binary, args, iinaStartupTimeout, socketPath)
	if attemptSucceeded(launched, exitCode, quickExit, stats) {
		return stats, nil
	}
	if stderr == "" {
		return PlaybackResult{}, fmt.Errorf("process exited with code %d", exitCode)
	}
	return PlaybackResult{}, fmt.Errorf("process exited with code %d: %s", exitCode, stderr)
}

func buildIINAArgs(source model.PlaybackSource, media model.ResolvedMedia, socketPath string) []string {
	args := []string{"--no-stdin", "--keep-running", source.URL, "--"}
	args = append(args,
		"--no-ytdl",
		"--network-timeout=8",
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

	// UA and Referer are sent via the dedicated --user-agent/--referrer mpv
	// options, not repeated in --http-header-fields (a comma-split list that a
	// UA containing commas would corrupt into a 400 from strict CDNs). Only
	// add what mpv has no native option for: Origin and the Cookie.
	var headers []string
	if strings.TrimSpace(source.Referer) != "" {
		if !source.SuppressOrigin {
			ref := strings.TrimSuffix(source.Referer, "/")
			headers = append(headers, "Origin: "+ref)
		}
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}
	if len(headers) > 0 {
		args = append(args, "--http-header-fields="+strings.Join(headers, ","))
	}

	args = appendTitleArgs(args, media.DisplayTitle())
	args = appendSubtitleArgs(args, media.SubtitlePaths())
	args = append(args, source.ExtraArgs...)
	return args
}
