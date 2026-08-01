//go:build windows

package player

import (
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func dialIPC(socketPath string, timeout time.Duration) (net.Conn, error) {
	pathPtr, err := windows.UTF16PtrFromString(socketPath)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		h, err := windows.CreateFile(
			pathPtr,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			0,
			0,
		)
		if err == nil {
			return &windowsPipeConn{handle: h}, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout connecting to mpv IPC pipe: %s", socketPath)
}

func DefaultMPVSocketPath() string {
	return fmt.Sprintf(`\\.\pipe\kari-mpv-%d`, os.Getpid())
}

type windowsPipeConn struct {
	handle windows.Handle
}

func (c *windowsPipeConn) Read(b []byte) (int, error) {
	var done uint32
	err := windows.ReadFile(c.handle, b, &done, nil)
	if err != nil {
		return int(done), err
	}
	return int(done), nil
}

func (c *windowsPipeConn) Write(b []byte) (int, error) {
	var done uint32
	err := windows.WriteFile(c.handle, b, &done, nil)
	if err != nil {
		return int(done), err
	}
	return int(done), nil
}

func (c *windowsPipeConn) Close() error {
	return windows.CloseHandle(c.handle)
}

func (c *windowsPipeConn) LocalAddr() net.Addr {
	return pipeAddr("pipe")
}

func (c *windowsPipeConn) RemoteAddr() net.Addr {
	return pipeAddr("pipe")
}

func (c *windowsPipeConn) SetDeadline(t time.Time) error {
	return nil
}

func (c *windowsPipeConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *windowsPipeConn) SetWriteDeadline(t time.Time) error {
	return nil
}

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }
