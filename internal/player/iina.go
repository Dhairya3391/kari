//go:build darwin && !android

package player

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	if len(sources) == 0 {
		return PlaybackResult{}, errors.New("iina playback failed: no playback sources available")
	}

	bin := iinaBinary()
	if bin == "" {
		return PlaybackResult{}, errors.New("iina playback failed: iina-cli not found")
	}

	errs := make([]string, 0, len(sources))
	for idx, source := range sources {
		if strings.TrimSpace(source.URL) == "" {
			continue
		}
		if result, err := playSingleSourceWithIINA(bin, source, media); err == nil {
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
		return PlaybackResult{}, errors.New("iina playback failed: no usable playback sources available")
	}
	return PlaybackResult{}, fmt.Errorf("iina playback failed: %s", strings.Join(errs, " | "))
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
	stderr, exitCode, launched, stats := startPlayerWithStartupCheck(binary, args, iinaStartupTimeout, socketPath)
	if attemptSucceeded(launched, exitCode, stats) {
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
		"--network-timeout=10",
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
	if strings.TrimSpace(source.CookieHeader) != "" {
		args = append(args, "--http-header-fields=Cookie: "+source.CookieHeader)
	}
	args = appendTitleArgs(args, media.DisplayTitle())
	args = appendSubtitleArgs(args, media.SubtitlePaths())
	return args
}

func startPlayerWithStartupCheck(binary string, args []string, timeout time.Duration, socketPath string) (stderr string, exitCode int, launched bool, stats PlaybackResult) {
	// Clean up any stale socket from a previous run
	os.Remove(socketPath)

	cmd := exec.Command(binary, args...)
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
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return buf.String(), exitCode, false, PlaybackResult{}
	case <-time.After(timeout):
		// Launched successfully, start IPC polling
		ipcDone := make(chan struct{})
		client := NewIPCClient(socketPath)
		go ipcPoller(context.Background(), client, &stats, ipcDone)

		err := <-done
		close(ipcDone)

		exitCode = 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}
		return "", exitCode, true, stats
	}
}
