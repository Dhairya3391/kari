//go:build android

package player

import (
	"fmt"
	"net/url"
	"strings"

	"kari/internal/model"
)

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

func playWithMXPlayerAndroid(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return attemptSources("mxplayer", sources, func(source model.PlaybackSource) (PlaybackResult, error) {
		if err := playSingleSourceWithMXPlayer(source, media); err != nil {
			return PlaybackResult{}, err
		}
		return PlaybackResult{}, &NeedsCompletionConfirmError{Media: media}
	})
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
