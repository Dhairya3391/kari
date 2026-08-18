//go:build !android

package player

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"kari/internal/logging"
)

func ipcPoller(ctx context.Context, client *IPCClient, stats *playbackStats, done <-chan struct{}) {
	defer client.Close()

	if err := client.Connect(3 * time.Second); err != nil {
		logging.Debugf("ipcPoller: connect failed: %v", err)
		return
	}

	poll := func() {
		pos, posErr := client.GetProperty("time-pos")
		var posF float64
		if posErr == nil {
			if f, ok := pos.(float64); ok {
				posF = f
			}
		}
		dur, durErr := client.GetProperty("duration")
		var durF float64
		if durErr == nil {
			if f, ok := dur.(float64); ok {
				durF = f
			}
		}
		idle, idleErr := client.GetProperty("idle-active")
		var isIdle bool
		if idleErr == nil {
			if b, ok := idle.(bool); ok {
				isIdle = b
			}
		}

		// Media is loaded if time-pos is available (even if 0.0), duration is > 0,
		// or MPV is actively loading/playing media (not idle).
		loaded := (posErr == nil) || (durErr == nil && durF > 0) || (idleErr == nil && !isIdle)

		if loaded || posF > 0 || durF > 0 {
			stats.update(posF, durF, loaded)
		}
	}

	// Poll immediately on connection so readiness is detected without waiting for ticker delay.
	poll()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			poll()
		}
	}
}

// startPlayerWithStartupCheck launches binary and waits up to timeout for it
// to either exit or show IPC evidence of loaded media on socketPath. Shared
// by every single-process player (mpv direct, IINA) so there's exactly one
// launch/readiness implementation to keep correct across all of them.
func startPlayerWithStartupCheck(binary string, args []string, timeout time.Duration, socketPath string) (stderr string, exitCode int, launched bool, quickExit bool, stats PlaybackResult) {
	// Clean up any stale socket from a previous run
	os.Remove(socketPath)

	cmd := exec.Command(binary, args...)
	cmd.Stdout = io.Discard
	buf := &bytes.Buffer{}
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		return err.Error(), 1, false, false, PlaybackResult{}
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err == nil {
			return buf.String(), 0, false, false, PlaybackResult{}
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return buf.String(), ee.ExitCode(), false, false, PlaybackResult{}
		}
		return buf.String(), 1, false, false, PlaybackResult{}
	case <-time.After(timeout):
		// Process stayed alive — now check actual playback readiness
		return waitForPlaybackReadiness(cmd, done, socketPath, buf)
	}
}

// waitForPlaybackReadiness waits for an already-launched, single-process
// player (mpv direct, or IINA — both expose --input-ipc-server on
// socketPath) to show IPC evidence of loaded media, killing it if it never
// becomes ready within mpvReadinessTimeout instead of blocking on it
// indefinitely. Shared by mpv.go and iina.go so there's exactly one
// readiness/timeout implementation to keep correct across both players.
func waitForPlaybackReadiness(cmd *exec.Cmd, done <-chan error, socketPath string, buf *bytes.Buffer) (stderr string, exitCode int, launched bool, quickExit bool, stats PlaybackResult) {
	ipcDone := make(chan struct{})
	client := NewIPCClient(socketPath)
	ps := newPlaybackStats()
	go ipcPoller(context.Background(), client, ps, ipcDone)

	phaseStart := time.Now()
	readinessTimeout := time.After(mpvReadinessTimeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Phase 1: Wait for playback readiness or exit
	for {
		select {
		case err := <-done:
			// MPV exited before becoming ready
			close(ipcDone)
			exitCode = 0
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					exitCode = ee.ExitCode()
				} else {
					exitCode = 1
				}
			}
			quickExit = time.Since(phaseStart) < mpvQuickExitThreshold
			return buf.String(), exitCode, true, quickExit, ps.snapshot()

		case <-readinessTimeout:
			// Playback didn't start — kill MPV and fall through
			_ = cmd.Process.Kill()
			<-done
			close(ipcDone)
			logging.Warnf("mpv readiness timeout — killed process (stderr: %s)", summarizeErr("", buf.String()))
			return buf.String(), 1, true, false, ps.snapshot()

		case <-ticker.C:
			if ps.playing() {
				goto phase2
			}
		}
	}

phase2:
	// Phase 2: Playback is active — wait for MPV to exit normally
	err := <-done
	close(ipcDone)

	exitCode = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return buf.String(), exitCode, true, true, ps.snapshot()
}

func startPipeWithStartupCheck(curlArgs, mpvArgs []string, socketPath string) (mpvStderr string, curlStderr string, exitCode int, launched bool, quickExit bool, stats PlaybackResult, err error) {
	os.Remove(socketPath)

	p1 := exec.Command("curl", curlArgs...)
	p2 := exec.Command("mpv", mpvArgs...)

	stdout, err := p1.StdoutPipe()
	if err != nil {
		return "", "", 1, false, false, PlaybackResult{}, err
	}
	curlBuf := &bytes.Buffer{}
	p1.Stderr = curlBuf
	p2.Stdin = stdout
	p2.Stdout = io.Discard
	mpvBuf := &bytes.Buffer{}
	p2.Stderr = mpvBuf

	if err := p1.Start(); err != nil {
		return "", "", 1, false, false, PlaybackResult{}, err
	}
	if err := p2.Start(); err != nil {
		killAndWait(p1)
		return "", curlBuf.String(), 1, false, false, PlaybackResult{}, err
	}

	done := make(chan error, 1)
	go func() {
		err := p2.Wait()
		killAndWait(p1)
		done <- err
	}()

	exitFromErr := func(waitErr error) int {
		if waitErr == nil {
			return 0
		}
		if ee, ok := waitErr.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return 1
	}

	select {
	case waitErr := <-done:
		return mpvBuf.String(), curlBuf.String(), exitFromErr(waitErr), false, false, PlaybackResult{}, nil
	case <-time.After(mpvStartupTimeout):
		// Launched successfully, start IPC polling
		ipcDone := make(chan struct{})
		client := NewIPCClient(socketPath)
		ps := newPlaybackStats()
		go ipcPoller(context.Background(), client, ps, ipcDone)

		// Wait for playback readiness with timeout
		phaseStart := time.Now()
		pipeReadinessTimeout := time.After(mpvReadinessTimeout)
		pipeTick := time.NewTicker(500 * time.Millisecond)
		defer pipeTick.Stop()

	pipeCheck:
		for {
			select {
			case waitErr := <-done:
				close(ipcDone)
				quickExit := time.Since(phaseStart) < mpvQuickExitThreshold
				return mpvBuf.String(), curlBuf.String(), exitFromErr(waitErr), true, quickExit, ps.snapshot(), nil
			case <-pipeReadinessTimeout:
				_ = p2.Process.Kill()
				<-done
				close(ipcDone)
				return mpvBuf.String(), curlBuf.String(), 1, true, false, ps.snapshot(), nil
			case <-pipeTick.C:
				if ps.playing() {
					break pipeCheck
				}
			}
		}

		waitErr := <-done
		close(ipcDone)

		return mpvBuf.String(), curlBuf.String(), exitFromErr(waitErr), true, true, ps.snapshot(), nil
	}
}

func killAndWait(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func summarizeErr(label, stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	// just use first line or first 100 chars
	lines := strings.Split(stderr, "\n")
	if len(lines) > 0 {
		msg := strings.TrimSpace(lines[0])
		if len(msg) > 100 {
			msg = msg[:100] + "..."
		}
		return fmt.Sprintf("%s: %s", label, msg)
	}
	return ""
}

func attemptSucceeded(launched bool, exitCode int, quickExit bool, stats PlaybackResult) bool {
	// Evidence that the stream actually started playing (position or
	// duration observed over IPC) always counts as a success, even if mpv
	// crashed or was quit abnormally afterwards.
	if stats.DurationSecs > 0 || stats.FinalPositionSecs > 0 {
		return true
	}
	// A process that exits before the startup window elapses — even with a 0
	// exit code — means playback never started (e.g. mpv fails to open the
	// URL and quits cleanly). Treat that as a failure so the caller falls
	// through to the next strategy instead of reporting a bogus success.
	if !launched {
		return false
	}
	// Exit code 4 is mpv's own "quit" signal and unambiguous either way.
	// Exit code 0 with no IPC evidence of loaded media is ambiguous — it's
	// both what a user quitting almost instantly looks like AND what mpv
	// silently failing to open a bad URL looks like. quickExit narrows that
	// down using how long mpv survived: a real user reacts within a second
	// or two, while a stream that's failing to open typically takes longer
	// (bounded by --network-timeout=8), so only trust exit 0 within that
	// window instead of unconditionally — otherwise a dead source gets
	// reported as "played successfully" and the caller stops trying
	// fallback sources that might have actually worked.
	if exitCode == 4 {
		return true
	}
	return exitCode == 0 && quickExit
}
