//go:build !windows

package presence

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ipcEndpoints lists the Discord IPC socket paths to try, in priority order.
// Discord places discord-ipc-N under a per-user runtime directory whose
// location varies by platform (TMPDIR on macOS, XDG_RUNTIME_DIR on Linux) and
// by packaging (snap/flatpak nest it under an app subdirectory).
func ipcEndpoints() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}

	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		add(runtimeDir)
		add(filepath.Join(runtimeDir, "app", "com.discordapp.Discord"))
		add(filepath.Join(runtimeDir, "snap.discord"))
		add(filepath.Join(runtimeDir, ".flatpak", "com.discordapp.Discord"))
		add(filepath.Join(runtimeDir, "com.discordapp.Discord"))
	}
	add(os.Getenv("TMPDIR"))
	add(os.Getenv("TMP"))
	add(os.Getenv("TEMP"))
	add("/tmp")

	endpoints := make([]string, 0, len(dirs)*10)
	for _, dir := range dirs {
		for i := 0; i < 10; i++ {
			endpoints = append(endpoints, filepath.Join(dir, fmt.Sprintf("discord-ipc-%d", i)))
		}
	}
	return endpoints
}

// dialEndpoint connects to one unix socket endpoint. Non-existent paths fail
// immediately, so probing the whole candidate list stays cheap.
func dialEndpoint(ctx context.Context, endpoint string) (io.ReadWriteCloser, error) {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", endpoint)
	if err != nil {
		return nil, err
	}
	return conn, nil
}
