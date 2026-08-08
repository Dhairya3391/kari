//go:build android

package player

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

// amBinaryCandidates lists activity-manager invocations to try, in
// preference order. Plain `am` talks to Android's ActivityManager directly
// and needs nothing else running, so it's tried first. termux-am and
// termux-am-starter instead route through the Termux:API app's background
// am.sock socket server (https://github.com/termux/termux-am-socket) — more
// resilient on hardened Android builds that SELinux-block a shell-UID `am`
// call, but only work when that socket server is actually up, which fails
// with "Could not connect to socket" if the Termux:API app hasn't started
// its service yet.
var amBinaryCandidates = []string{"am", "termux-am", "termux-am-starter"}

var (
	amBinaryMu   sync.Mutex
	amBinaryGood string // sticky last-known-good binary, tried first next time
)

func stickyAmBinary() string {
	amBinaryMu.Lock()
	defer amBinaryMu.Unlock()
	return amBinaryGood
}

func setStickyAmBinary(bin string) {
	amBinaryMu.Lock()
	amBinaryGood = bin
	amBinaryMu.Unlock()
}

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
	pmPath, err := exec.LookPath("pm")
	if err != nil {
		return false
	}
	cmd := exec.Command(pmPath, "list", "packages")
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
	writeMpvConf(source, media)

	// mpv-android's intent accepts options only via extras for title, start
	// position and subtitle tracks; it cannot receive HTTP headers/UA/referrer
	// (see parseIntentExtras in the upstream app). Use -t video/any so the URL
	// is opened regardless of a missing/odd file extension, and pass title and
	// resume position as supported extras.
	args := []string{"start", "-n", mpvAndroidPackage + "/.MPVActivity", "-a", "android.intent.action.VIEW", "-t", "video/any", "-d", source.URL}

	if title := sanitizeMediaTitle(media.DisplayTitle()); title != "" {
		args = append(args, "--es", "title", title)
	}
	if media.StartTime > 5 {
		// mpv-android expects the start position in milliseconds.
		args = append(args, "--ei", "position", fmt.Sprintf("%d", int(media.StartTime*1000)))
	}

	if err := runAmStart(args); err != nil {
		return fmt.Errorf("mpv %w", err)
	}
	return nil
}

func writeMpvConf(source model.PlaybackSource, media model.ResolvedMedia) {
	// mpv-android loads libmpv's config only from its own internal files dir
	// (/data/user/0/is.xyz.mpv/files/), which the app sets via
	// `config-dir=<filesDir>` (see BaseMPVView.initialize upstream). That
	// directory is UNWRITABLE by Termux without root, and mpv-android never
	// auto-reads /storage/emulated/0/Android/media/.
	//
	// The one hook that works on every Android is an `include=` line in a
	// config that mpv DOES load. The user adds it once via the app
	// (Settings -> Advanced -> Edit mpv.conf):
	//
	//   include=/storage/emulated/0/Android/media/is.xyz.mpv/.mpv.conf
	//
	// Every play launch this function rewrites that target (and mpv.conf
	// alongside it for includes that point there instead) with the fresh
	// playback options: Referer/Origin/User-Agent/Cookie via
	// `http-header-fields`, title, resume position, network tuning and the
	// subtitle. Because the file is regenerated per play, per-stream tokens
	// always reach libmpv when the app next starts. Note that writing
	// `~/.config/mpv/mpv.conf` in the Termux home is pointless here: the
	// mpv-android libmpv process does not read the Termux HOME path.
	var confBuilder strings.Builder
	title := sanitizeMediaTitle(media.DisplayTitle())
	if title != "" {
		confBuilder.WriteString(fmt.Sprintf("force-media-title=%s\n", title))
	}

	confBuilder.WriteString("network-timeout=8\n")
	confBuilder.WriteString("cache=yes\n")
	confBuilder.WriteString("cache-pause-initial=no\n")
	confBuilder.WriteString("stream-buffer-size=16M\n")
	confBuilder.WriteString("demuxer-readahead-secs=2\n")

	if source.Referer != "" {
		confBuilder.WriteString(fmt.Sprintf("referrer=%s\n", source.Referer))
	}
	userAgent := source.UserAgent
	if userAgent == "" {
		userAgent = "Mozilla/5.0"
	}
	confBuilder.WriteString(fmt.Sprintf("user-agent=%s\n", userAgent))

	var headers []string
	if userAgent != "" {
		headers = append(headers, "User-Agent: "+userAgent)
	}
	if strings.TrimSpace(source.Referer) != "" {
		headers = append(headers, "Referer: "+source.Referer)
		ref := strings.TrimSuffix(source.Referer, "/")
		headers = append(headers, "Origin: "+ref)
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}
	if len(headers) > 0 {
		// mpv list-typed options (http-header-fields) are cumulative: each
		// repeated line in a config file appends one entry. Do that instead of
		// a single value joined with "\r\n" (mpv.conf does not decode that
		// escape, unlike the command line parser).
		for _, h := range headers {
			confBuilder.WriteString("http-header-fields=")
			confBuilder.WriteString(h)
			confBuilder.WriteString("\n")
		}
	}

	if media.StartTime > 5 {
		confBuilder.WriteString(fmt.Sprintf("start=%d\n", int(media.StartTime)))
	}

	// Subtitles: mpv-android's VIEW intent CANNOT carry subtitles here — its
	// `subs` extra is a Parcelable (Uri) array which `am`/termux-am has no
	// `--eua` flag capable of building. Instead copy the downloaded track next
	// to our include target and attach it with a sub-file line so it loads for
	// this session. (mpv's sub-file is a list option, so a repeated line works.)
	subPath := ""
	subtitleFiles := media.SubtitlePaths()
	if len(subtitleFiles) > 0 && subtitleFiles[0] != "" {
		target := mpvAndroidDir + "/sub.vtt"
		if err := copyFile(subtitleFiles[0], target); err == nil {
			subPath = target
			logging.Debugf("writeMpvConf: copied subtitle to %s", target)
		} else {
			logging.Debugf("writeMpvConf: failed to copy subtitle to %s: %v", target, err)
		}
	}
	if subPath != "" {
		confBuilder.WriteString("sub-file=")
		confBuilder.WriteString(subPath)
		confBuilder.WriteString("\n")
	}

	confData := confBuilder.String()

	paths := []string{
		mpvAndroidDir + "/.mpv.conf",
		mpvAndroidDir + "/mpv.conf",
	}

	wroteCount := 0
	for _, confPath := range paths {
		dir := filepath.Dir(confPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(confPath, []byte(confData), 0o644); err != nil {
			logging.Debugf("writeMpvConf: failed to write %s: %v", confPath, err)
			continue
		}
		wroteCount++
		logging.Debugf("writeMpvConf: wrote %s", confPath)
	}
	if wroteCount == 0 {
		logging.Debugf("writeMpvConf: could not write mpv.conf to any path (headers/title won't be set)")
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
	args := buildMXPlayerAndroidIntent(source, media)
	if err := runAmStart(args); err != nil {
		return fmt.Errorf("mxplayer %w", err)
	}
	return nil
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

// runAmStart fires an Android `am start` intent, trying each candidate
// activity-manager binary in turn until one actually launches successfully.
// A static "which one works" pre-check isn't reliable here — e.g. `termux-am
// help` succeeds even when the am.sock server behind `termux-am start` is
// down — so this validates against the real invocation every time, caching
// only which binary last worked to try that one first.
func runAmStart(args []string) error {
	order := make([]string, 0, len(amBinaryCandidates)+1)
	seen := make(map[string]bool, len(amBinaryCandidates)+1)
	if sticky := stickyAmBinary(); sticky != "" {
		order = append(order, sticky)
		seen[sticky] = true
	}
	for _, bin := range amBinaryCandidates {
		if !seen[bin] {
			order = append(order, bin)
			seen[bin] = true
		}
	}

	var attempts []string
	for _, bin := range order {
		path, err := exec.LookPath(bin)
		if err != nil {
			continue
		}
		if err := execAmStart(path, args); err != nil {
			attempts = append(attempts, fmt.Sprintf("%s: %v", bin, err))
			setStickyAmBinary("")
			continue
		}
		setStickyAmBinary(bin)
		return nil
	}
	if len(attempts) == 0 {
		return fmt.Errorf("no activity-manager binary found (install termux-api, or ensure am is in PATH)")
	}
	return fmt.Errorf("exited unexpectedly: %s", strings.Join(attempts, " | "))
}

func execAmStart(path string, args []string) error {
	logging.Debugf("android playback launch binary=%q args=%v", path, args)
	cmd := exec.Command(path, args...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start: %w", err)
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
		if msg := strings.TrimSpace(out.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	case <-time.After(androidStartupTimeout):
		return nil
	}
}
