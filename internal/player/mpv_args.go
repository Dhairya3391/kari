//go:build !android

package player

import (
	"fmt"
	"runtime"
	"strings"

	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/model"
)

func buildMPVArgs(source model.PlaybackSource, media model.ResolvedMedia, socketPath string, aniskipArgs []string) []string {
	args := []string{
		"--no-ytdl",
		"--msg-level=all=warn",
		"--vo=gpu-next",
		hwdecOptionArg(),
		"--network-timeout=8",
		"--cache=yes",
		"--cache-pause-initial=no",
		"--demuxer-seekable-cache=yes",
		"--demuxer-max-bytes=200M",
		"--demuxer-readahead-secs=2",
		"--stream-buffer-size=16M",
		"--hls-bitrate=max",
	}

	if media.StartTime > 5 {
		args = append(args, fmt.Sprintf("--start=%d", int(media.StartTime)))
	}

	userAgent := source.UserAgent
	if strings.TrimSpace(userAgent) == "" {
		userAgent = config.AndroidUA()
	}
	if userAgent != "" {
		args = append(args, "--user-agent="+userAgent)
	}
	if strings.TrimSpace(source.Referer) != "" {
		args = append(args, "--referrer="+source.Referer)
	}

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
		args = append(args, "--http-header-fields="+strings.Join(headers, "\r\n"))
	}

	args = appendTitleArgs(args, media.DisplayTitle())
	args = appendSubtitleArgs(args, media.SubtitlePaths())
	args = append(args, aniskipArgs...)
	args = append(args, "--input-ipc-server="+socketPath)

	return append(args, source.URL)
}

func appendTitleArgs(args []string, title string) []string {
	if strings.TrimSpace(title) == "" {
		return args
	}
	title = sanitizeMediaTitle(title)
	return append(args, "--title="+title, "--force-media-title="+title)
}

func appendSubtitleArgs(args []string, subtitleFiles []string) []string {
	if len(subtitleFiles) == 0 {
		logging.Debugf("appendSubtitleArgs: no subtitle files to append")
		return args
	}
	for _, sub := range subtitleFiles {
		if strings.TrimSpace(sub) != "" {
			sub = strings.ReplaceAll(sub, `\`, `/`)
			logging.Debugf("appendSubtitleArgs: adding sub-file=%q", sub)
			args = append(args, "--sub-files="+sub)
		}
	}
	return args
}

func buildCurlArgs(url string, headers []string) []string {
	args := []string{"-s", "-L"}
	for _, h := range headers {
		args = append(args, "-H", h)
	}
	args = append(args, optionalCurlFlags(url)...)
	args = append(args, url)
	return args
}

func optionalCurlFlags(finalURL string) []string {
	isHTTP := strings.HasPrefix(strings.ToLower(strings.TrimSpace(finalURL)), "http://") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(finalURL)), "https://")
	if !isHTTP {
		return nil
	}
	return append([]string{}, curlOptionalFlags...)
}

var curlOptionalFlags = []string{
	"--compressed",
	"--connect-timeout", "5",
	"--retry", "2",
}

func hwdecOptionArg() string {
	if runtime.GOOS == "darwin" {
		return "--hwdec=auto"
	}
	return "--hwdec=auto-safe"
}

func joinNonEmpty(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ; ")
}
