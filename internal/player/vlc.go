//go:build windows

package player

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/model"
)

const vlcStartupTimeout = 3 * time.Second

type VLCPlayer struct{}

var _ Player = (*VLCPlayer)(nil)

func (p *VLCPlayer) Name() string {
	return "vlc"
}

func (p *VLCPlayer) Available() bool {
	_, err := exec.LookPath("vlc")
	return err == nil
}

func (p *VLCPlayer) Play(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return PlayWithVLCSources(sources, media)
}

func PlayWithVLCSources(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	if len(sources) == 0 {
		return PlaybackResult{}, errors.New("vlc playback failed: no playback sources available")
	}

	errs := make([]string, 0, len(sources))
	for idx, source := range sources {
		if strings.TrimSpace(source.URL) == "" {
			continue
		}
		if err := playSingleSourceWithVLC(source, media); err == nil {
			return PlaybackResult{}, &NeedsCompletionConfirmError{Media: media}
		} else {
			label := strings.TrimSpace(source.Label)
			if label == "" {
				label = fmt.Sprintf("source %d", idx+1)
			}
			errs = append(errs, fmt.Sprintf("%s: %v", label, err))
		}
	}

	if len(errs) == 0 {
		return PlaybackResult{}, errors.New("vlc playback failed: no usable playback sources available")
	}
	return PlaybackResult{}, fmt.Errorf("vlc playback failed: %s", strings.Join(errs, " | "))
}

func playSingleSourceWithVLC(source model.PlaybackSource, media model.ResolvedMedia) error {
	args := buildVLCArgs(source, media)
	logging.Debugf("vlc: launching: vlc %v", args)

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
		return fmt.Errorf("vlc exited unexpectedly")
	case <-time.After(vlcStartupTimeout):
		return nil
	}
}

func buildVLCArgs(source model.PlaybackSource, media model.ResolvedMedia) []string {
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
