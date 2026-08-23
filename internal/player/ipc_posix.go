//go:build !windows

package player

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func dialIPC(socketPath string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 1*time.Second)
		if err == nil {
			return conn, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout connecting to mpv IPC socket: %s", socketPath)
}

// DefaultMPVSocketPath returns the mpv IPC socket path used on unix
// platforms, honoring the MPV_SOCKET override.
func DefaultMPVSocketPath() string {
	if sock := os.Getenv("MPV_SOCKET"); sock != "" {
		return sock
	}
	cleanupStaleSockets()
	return fmt.Sprintf("/tmp/kari-mpv-%d.sock", os.Getpid())
}

// cleanupStaleSockets removes /tmp/kari-mpv-*.sock files whose owning
// process is no longer running (e.g. after a crash or SIGKILL).
func cleanupStaleSockets() {
	matches, err := filepath.Glob("/tmp/kari-mpv-*.sock")
	if err != nil {
		return
	}
	for _, path := range matches {
		base := filepath.Base(path)
		name := strings.TrimPrefix(base, "kari-mpv-")
		name = strings.TrimSuffix(name, ".sock")
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			os.Remove(path)
			continue
		}
		err = proc.Signal(syscall.Signal(0))
		if err != nil {
			os.Remove(path)
		}
	}
}
