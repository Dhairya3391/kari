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
		"--demuxer-readahead-secs=10",
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

	// UA and Referer are sent via the dedicated --user-agent/--referrer mpv
	// options (mpv applies them to ffmpeg streams too), so they are NOT
	// repeated here: --http-header-fields is a comma-split list and real UAs
	// contain commas ("(KHTML, like Gecko)"), which would corrupt the header
	// block and make strict CDNs answer 400. Only add what mpv has no native
	// option for: Origin and the provider's Cookie.
	var headers []string
	if strings.TrimSpace(source.Referer) != "" {
		// Some CDNs reject an Origin header outright (or only accept a bare
		// scheme://host), so it's opt-in via SuppressOrigin (e.g. PirateX,
		// whose CDN validates Referer only). When sent it stays derived from
		// the referer, matching what a browser would send.
		if !source.SuppressOrigin {
			ref := strings.TrimSuffix(source.Referer, "/")
			headers = append(headers, "Origin: "+ref)
		}
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}
	if len(headers) > 0 {
		// mpv's list-typed options (http-header-fields) split on commas on the
		// command line. Joining with "\r\n" makes mpv receive a single value
		// containing literal CR/LF bytes, which corrupts the header block and
		// can make strict CDNs answer with 500 — plain comma-join is correct.
		args = append(args, "--http-header-fields="+strings.Join(headers, ","))
	}

	args = appendTitleArgs(args, media.DisplayTitle())
	args = appendSubtitleArgs(args, media.SubtitlePaths())
	args = append(args, aniskipArgs...)
	args = append(args, source.ExtraArgs...)
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
