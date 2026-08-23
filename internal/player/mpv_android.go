//go:build android

package player

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
)

// MPVPlayer (android build) launches mpv through an Android intent; the
// mpv.conf include bridge carries stream headers that intents cannot.
type MPVPlayer struct{}

var _ Player = (*MPVPlayer)(nil)

// Name implements Player.
func (p *MPVPlayer) Name() string {
	return "mpv"
}

// Available implements Player.
func (p *MPVPlayer) Available() bool {
	return isPackageAvailable(mpvAndroidPackage)
}

// Play implements Player.
func (p *MPVPlayer) Play(sources []provider.MediaSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return playWithMPVAndroid(sources, media)
}

func playWithMPVAndroid(sources []provider.MediaSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return attemptSources("mpv", sources, func(source provider.MediaSource) (PlaybackResult, error) {
		if err := playSingleSourceWithMPVAndroid(source, media); err != nil {
			return PlaybackResult{}, err
		}
		return PlaybackResult{}, &NeedsCompletionConfirmError{Media: media}
	})
}

func playSingleSourceWithMPVAndroid(source provider.MediaSource, media model.ResolvedMedia) error {
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

func writeMpvConf(source provider.MediaSource, media model.ResolvedMedia) {
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
	confBuilder.WriteString("stream-buffer-size=8M\n")
	confBuilder.WriteString("demuxer-seekable-cache=yes\n")
	confBuilder.WriteString("demuxer-max-bytes=100M\n")
	confBuilder.WriteString("demuxer-max-back-bytes=20M\n")
	confBuilder.WriteString("demuxer-readahead-secs=60\n")
	confBuilder.WriteString("hls-bitrate=max\n")

	if source.Referer != "" {
		confBuilder.WriteString(fmt.Sprintf("referrer=%s\n", source.Referer))
	}
	userAgent := source.UserAgent
	if userAgent == "" {
		userAgent = "Mozilla/5.0"
	}
	confBuilder.WriteString(fmt.Sprintf("user-agent=%s\n", userAgent))

	// UA and Referer arrive via the user-agent=/referrer= conf lines; do NOT
	// repeat them in http-header-fields — that is a comma-split mpv list and
	// a UA containing commas ("(KHTML, like Gecko)") would split into corrupt
	// header entries that strict CDNs answer with 400. Only Origin and Cookie
	// have no native mpv option and belong here.
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

	for _, extra := range source.ExtraArgs {
		confBuilder.WriteString(strings.TrimPrefix(extra, "--"))
		confBuilder.WriteString("\n")
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
		if err := os.MkdirAll(mpvAndroidDir, 0o755); err != nil {
			mpvLog.Debug("config dir create failed", "dir", mpvAndroidDir, "err", err)
		}
		ext := filepath.Ext(subtitleFiles[0])
		if ext == "" {
			ext = ".vtt"
		}
		target := filepath.Join(mpvAndroidDir, "sub"+ext)
		if err := copyFile(subtitleFiles[0], target); err == nil {
			subPath = target
			mpvLog.Debug("subtitle copied for config bridge", "target", target)
		} else {
			mpvLog.Debug("subtitle copy failed", "target", target, "err", err)
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
			mpvLog.Debug("config write failed", "path", confPath, "err", err)
			continue
		}
		wroteCount++
		mpvLog.Debug("config written", "path", confPath)
	}
	if wroteCount == 0 {
		mpvLog.Debug("could not write mpv.conf to any path; headers and title will not be set")
	}
}
