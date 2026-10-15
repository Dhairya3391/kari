//go:build windows

package presence

import (
	"context"
	"fmt"
	"io"
	"os"
)

// ipcEndpoints lists the Discord IPC named pipes to try on Windows. Both the
// NT and Win32 pipe prefixes are probed since client builds differ.
func ipcEndpoints() []string {
	prefixes := []string{`\\.\pipe\`, `\\?\pipe\`}
	endpoints := make([]string, 0, 20)
	for i := 0; i < 10; i++ {
		for _, prefix := range prefixes {
			endpoints = append(endpoints, fmt.Sprintf("%sdiscord-ipc-%d", prefix, i))
		}
	}
	return endpoints
}

// dialEndpoint opens a Windows named pipe as a plain file. Deadlines are not
// supported; the worker closes the handle to abort a stuck operation. The
// pipe exists only while Discord runs, so a missing pipe fails immediately.
func dialEndpoint(_ context.Context, endpoint string) (io.ReadWriteCloser, error) {
	f, err := os.OpenFile(endpoint, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return f, nil
}
