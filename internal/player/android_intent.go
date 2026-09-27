//go:build android

package player

import (
	"errors"
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
	sharedSubtitleDir     = "/storage/emulated/0/Download/kari"
)

// androidLauncher remembers which activity-manager invocation works on the
// current device without leaking signed intent arguments into logs.
type androidLauncher struct {
	mu       sync.Mutex
	lastGood string
}

func newAndroidLauncher() *androidLauncher {
	return &androidLauncher{}
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

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

func (l *androidLauncher) run(args []string) error {
	candidates := []string{"am", "termux-am", "termux-am-starter"}
	l.mu.Lock()
	lastGood := l.lastGood
	l.mu.Unlock()

	order := make([]string, 0, len(candidates)+1)
	seen := make(map[string]struct{}, len(candidates)+1)
	if lastGood != "" {
		order = append(order, lastGood)
		seen[lastGood] = struct{}{}
	}
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			continue
		}
		order = append(order, candidate)
		seen[candidate] = struct{}{}
	}

	var attempts []error
	for _, candidate := range order {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		if err := execAmStart(path, args); err != nil {
			attempts = append(attempts, fmt.Errorf("%s: %w", candidate, err))
			l.setLastGood("")
			continue
		}
		l.setLastGood(candidate)
		return nil
	}
	if len(attempts) == 0 {
		return fmt.Errorf("no activity-manager binary found (install termux-api, or ensure am is in PATH)")
	}
	return fmt.Errorf("activity-manager launch failed: %w", errors.Join(attempts...))
}

func (l *androidLauncher) setLastGood(candidate string) {
	l.mu.Lock()
	l.lastGood = candidate
	l.mu.Unlock()
}

func execAmStart(path string, args []string) error {
	logging.Debug("android playback launch", "binary", path, "argCount", len(args))
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
