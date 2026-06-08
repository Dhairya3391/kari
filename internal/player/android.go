//go:build android

package player

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"kari/internal/logging"
	"kari/internal/model"
)

const (
	androidStartupTimeout = 5000 * time.Millisecond
	mxPlayerPackage       = "com.mxtech.videoplayer.ad"
	mpvAndroidPackage     = "is.xyz.mpv"
	mpvAndroidDir         = "/storage/emulated/0/Android/media/is.xyz.mpv"
)

var (
	termuxAmPathOnce sync.Once
	termuxAmPath     string
)

type MPVPlayer struct{}

var _ Player = (*MPVPlayer)(nil)

func (p *MPVPlayer) Name() string {
	return "mpv"
}

func (p *MPVPlayer) Available() bool {
	return isPackageAvailable(mpvAndroidPackage)
}

func (p *MPVPlayer) Play(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return playWithMPVAndroid(sources, media)
}

type MXPlayer struct{}

var _ Player = (*MXPlayer)(nil)

func (p *MXPlayer) Name() string {
	return "mxplayer"
}

func (p *MXPlayer) Available() bool {
	return isPackageAvailable(mxPlayerPackage)
}

func (p *MXPlayer) Play(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return playWithMXPlayerAndroid(sources, media)
}

func isPackageAvailable(pkg string) bool {
	cmd := exec.Command("pm", "list", "packages")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(pkg) + `\b`).MatchString(string(output))
}

func playWithMPVAndroid(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	if len(sources) == 0 {
		return PlaybackResult{}, fmt.Errorf("mpv playback failed: no playback sources available")
	}

	errs := make([]string, 0, len(sources))
	for idx, source := range sources {
		if strings.TrimSpace(source.URL) == "" {
			continue
		}
		if err := playSingleSourceWithMPVAndroid(source, media); err == nil {
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
		return PlaybackResult{}, fmt.Errorf("mpv playback failed: no usable playback sources available")
	}
	return PlaybackResult{}, fmt.Errorf("mpv playback failed: %s", strings.Join(errs, " | "))
}

func playSingleSourceWithMPVAndroid(source model.PlaybackSource, media model.ResolvedMedia) error {
	if !termuxAmAvailable() {
		return fmt.Errorf("mpv playback failed: termux-am not found (install termux-am)")
	}

	// Prepare mpv.conf for title, headers and subtitles
	if err := os.MkdirAll(mpvAndroidDir, 0o755); err != nil {
		logging.Errorf("failed to create mpv dir: %v", err)
	}

	var confBuilder strings.Builder
	title := sanitizeMediaTitle(media.DisplayTitle())
	if title != "" {
		confBuilder.WriteString(fmt.Sprintf("force-media-title=%s\n", title))
	}

	if source.Referer != "" {
		confBuilder.WriteString(fmt.Sprintf("referrer=%s\n", source.Referer))
	}
	userAgent := source.UserAgent
	if userAgent == "" {
		userAgent = "Mozilla/5.0"
	}
	confBuilder.WriteString(fmt.Sprintf("user-agent=%s\n", userAgent))

	if source.CookieHeader != "" {
		confBuilder.WriteString(fmt.Sprintf("http-header-fields=Cookie: %s\n", source.CookieHeader))
	}

	if media.StartTime > 5 {
		confBuilder.WriteString(fmt.Sprintf("start=%d\n", int(media.StartTime)))
	}

	subtitleFiles := media.SubtitlePaths()
	if len(subtitleFiles) > 0 && subtitleFiles[0] != "" {
		subPath := subtitleFiles[0]
		targetSubPath := mpvAndroidDir + "/sub.vtt"
		if err := copyFile(subPath, targetSubPath); err == nil {
			confBuilder.WriteString(fmt.Sprintf("sub-file=%s\n", targetSubPath))
			confBuilder.WriteString("sid=1\n")
		} else {
			logging.Errorf("failed to copy subtitle to %s: %v", targetSubPath, err)
		}
	}

	confPath := mpvAndroidDir + "/mpv.conf"
	if err := os.WriteFile(confPath, []byte(confBuilder.String()), 0o644); err != nil {
		logging.Errorf("failed to write mpv.conf: %v", err)
	}

	args := []string{"start", "-n", mpvAndroidPackage + "/.MPVActivity", "-a", "android.intent.action.VIEW", "-d", source.URL}

	binary := termuxAmBinary()
	logging.Debugf("android playback launch player=mpv-android binary=%q args=%v", binary, args)
	cmd := exec.Command(binary, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start mpv: %w", err)
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
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 0 {
			return nil
		}
		return fmt.Errorf("mpv exited unexpectedly")
	case <-time.After(androidStartupTimeout):
		return nil
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func playWithMXPlayerAndroid(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	if len(sources) == 0 {
		return PlaybackResult{}, fmt.Errorf("mxplayer playback failed: no playback sources available")
	}
	errs := make([]string, 0, len(sources))
	for idx, source := range sources {
		if strings.TrimSpace(source.URL) == "" {
			continue
		}
		if err := playSingleSourceWithMXPlayer(source, media); err == nil {
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
		return PlaybackResult{}, fmt.Errorf("mxplayer playback failed: no usable playback sources available")
	}
	return PlaybackResult{}, fmt.Errorf("mxplayer playback failed: %s", strings.Join(errs, " | "))
}

func playSingleSourceWithMXPlayer(source model.PlaybackSource, media model.ResolvedMedia) error {
	if !termuxAmAvailable() {
		return fmt.Errorf("mxplayer playback failed: termux-am not found (install termux-am)")
	}
	args := buildMXPlayerAndroidIntent(source, media)
	binary := termuxAmBinary()
	logging.Debugf("android playback launch player=mxplayer binary=%q args=%v", binary, args)
	cmd := exec.Command(binary, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start mxplayer: %w", err)
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
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 0 {
			return nil
		}
		return fmt.Errorf("mxplayer exited unexpectedly")
	case <-time.After(androidStartupTimeout):
		return nil
	}
}

func buildMXPlayerAndroidIntent(source model.PlaybackSource, media model.ResolvedMedia) []string {
	args := []string{"start", "-n", mxPlayerPackage + "/com.mxtech.videoplayer.ad.ActivityScreen", "-a", "android.intent.action.VIEW", "-t", "video/*", "-d", source.URL}

	title := sanitizeMediaTitle(media.DisplayTitle())
	if title != "" {
		args = append(args, "--es", "title", title)
	}

	subtitleFiles := media.SubtitlePaths()
	if len(subtitleFiles) > 0 && subtitleFiles[0] != "" {
		args = append(args, "--es", "subs", strings.Join(subtitleFiles, ","))
	}

	var headers []string
	if source.Referer != "" {
		headers = append(headers, "Referer", strings.ReplaceAll(source.Referer, ",", "\\,"))
	}
	if source.CookieHeader != "" {
		headers = append(headers, "Cookie", strings.ReplaceAll(url.QueryEscape(source.CookieHeader), ",", "\\,"))
	}
	if source.UserAgent != "" {
		headers = append(headers, "User-Agent", strings.ReplaceAll(source.UserAgent, ",", "\\,"))
	}
	if len(headers) > 0 {
		args = append(args, "--esa", "headers", strings.Join(headers, ","))
	}

	if media.StartTime > 5 {
		// MX Player expects position in milliseconds
		args = append(args, "--ei", "position", fmt.Sprintf("%d", int(media.StartTime*1000)))
	}

	return args
}

func termuxAmAvailable() bool {
	termuxAmPathOnce.Do(func() {
		if path, err := exec.LookPath("am"); err == nil {
			termuxAmPath = path
			return
		}
		if path, err := exec.LookPath("termux-am"); err == nil {
			termuxAmPath = path
		}
	})
	return termuxAmPath != ""
}

func termuxAmBinary() string {
	if termuxAmPath != "" {
		return termuxAmPath
	}
	return "am"
}
