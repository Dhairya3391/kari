//go:build android

package player

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"kari/internal/logging"
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
	logging.Debug("android playback launch", "binary", path, "args", args)
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
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return fmt.Errorf("launch timed out after %v", androidStartupTimeout)
	}
}
