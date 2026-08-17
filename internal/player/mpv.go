//go:build !android

package player

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"kari/internal/aniskip"
	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/model"
)

const mpvStartupTimeout = 1500 * time.Millisecond
const mpvReadinessTimeout = 8 * time.Second

// mpvQuickExitThreshold bounds how long into the readiness phase mpv can
// exit cleanly (code 0) with no IPC evidence that media ever loaded before
// we stop trusting that as "the user quit on purpose" and instead treat it
// as a silent playback failure (e.g. a dead URL mpv gives up on without a
// nonzero exit code). A real user quitting reacts within a second or two of
// seeing the window; a stream failing to open typically takes longer,
// bounded by --network-timeout=8 in the mpv args below.
const mpvQuickExitThreshold = 2 * time.Second

type MPVPlayer struct {
	aniskip *aniskip.Client
}

var _ Player = (*MPVPlayer)(nil)

func (p *MPVPlayer) Name() string {
	return "mpv"
}

func (p *MPVPlayer) Available() bool {
	return mpvAvailable()
}

func (p *MPVPlayer) Play(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error) {
	return playWithMPVSources(sources, media, p.aniskip)
}

func playWithMPVSources(sources []model.PlaybackSource, media model.ResolvedMedia, aniskipClient *aniskip.Client) (PlaybackResult, error) {
	logging.Debugf("PlayWithMPVSources: media=%q sources_count=%d", media.DisplayTitle(), len(sources))
	if len(sources) == 0 {
		return PlaybackResult{}, errors.New("mpv playback failed: no playback sources available")
	}

	aniskipArgs, aniskipPath := getAniskipArgs(aniskipClient, media)
	defer cleanupAniskipScript(aniskipPath)

	return attemptSources("mpv", sources, func(source model.PlaybackSource) (PlaybackResult, error) {
		return playSingleSource(source, media, aniskipArgs)
	})
}

func mpvAvailable() bool {
	_, err := exec.LookPath("mpv")
	return err == nil
}

func playSingleSource(source model.PlaybackSource, media model.ResolvedMedia, aniskipArgs []string) (PlaybackResult, error) {
	logging.Debugf("playSingleSource: trying direct playback for URL=%q", source.URL)
	socketPath := DefaultMPVSocketPath()

	// 1. Direct MPV playback (primary)
	directArgs := buildMPVArgs(source, media, socketPath, aniskipArgs)
	directErr, directRC, directLaunched, directQuickExit, stats := startPlayerWithStartupCheck("mpv", directArgs, mpvStartupTimeout, socketPath)
	if attemptSucceeded(directLaunched, directRC, directQuickExit, stats) {
		logging.Debugf("playSingleSource: direct playback succeeded")
		return stats, nil
	}
	logging.Warnf("playSingleSource: direct playback failed (rc=%d, err=%q)", directRC, directErr)

	// 2. Curl-to-MPV pipe (fallback for streams requiring custom headers/TLS connection handling)
	userAgent := config.AndroidUA()
	if strings.TrimSpace(source.UserAgent) != "" {
		userAgent = source.UserAgent
	}
	headers := []string{
		"Accept: */*",
		"Connection: keep-alive",
	}
	if userAgent != "" {
		headers = append(headers, "User-Agent: "+userAgent)
	}
	if strings.TrimSpace(source.Referer) != "" {
		headers = append(headers, "Referer: "+source.Referer)
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		headers = append(headers, "Cookie: "+source.CookieHeader)
	}

	curlArgs := buildCurlArgs(source.URL, headers)

	pipeMpvArgs := []string{
		"--no-ytdl",
		"--really-quiet",
		"--msg-level=all=error",
		"--vo=gpu-next",
		"--cache=no",
		"--demuxer-max-bytes=50M",
		"--network-timeout=8",
		"--input-ipc-server=" + socketPath,
		hwdecOptionArg(),
	}

	if media.StartTime > 5 {
		pipeMpvArgs = append(pipeMpvArgs, fmt.Sprintf("--start=%d", int(media.StartTime)))
	}

	pipeMpvArgs = appendTitleArgs(pipeMpvArgs, media.DisplayTitle())
	pipeMpvArgs = appendSubtitleArgs(pipeMpvArgs, media.SubtitlePaths())
	pipeMpvArgs = append(pipeMpvArgs, aniskipArgs...)
	pipeMpvArgs = append(pipeMpvArgs, source.ExtraArgs...)
	pipeMpvArgs = append(pipeMpvArgs, "-")

	logging.Debugf("playSingleSource: trying curl-to-mpv pipe for URL=%q", source.URL)
	mpvErr, curlErr, mpvRC, launched, pipeQuickExit, statsPipe, pipeErr := startPipeWithStartupCheck(curlArgs, pipeMpvArgs, socketPath)
	if pipeErr != nil {
		logging.Errorf("playSingleSource: pipe startup error: %v", pipeErr)
		return PlaybackResult{}, fmt.Errorf("mpv playback failed: pipe startup error: %w", pipeErr)
	}
	if attemptSucceeded(launched, mpvRC, pipeQuickExit, statsPipe) {
		logging.Debugf("playSingleSource: curl-to-mpv pipe succeeded")
		return statsPipe, nil
	}
	logging.Warnf("playSingleSource: curl-to-mpv pipe failed (rc=%d, mpv_err=%q, curl_err=%q)", mpvRC, mpvErr, curlErr)

	summary := fmt.Sprintf("mpv playback failed (direct rc=%d, pipe rc=%d)", directRC, mpvRC)
	details := joinNonEmpty(
		summarizeErr("direct", directErr),
		summarizeErr("pipe-mpv", mpvErr),
		summarizeErr("pipe-curl", curlErr),
	)
	if details == "" {
		return PlaybackResult{}, errors.New(summary)
	}
	return PlaybackResult{}, fmt.Errorf("%s: %s", summary, details)
}
