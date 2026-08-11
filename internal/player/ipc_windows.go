//go:build windows

package player

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
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
			windows.FILE_FLAG_OVERLAPPED,
			0,
		)
		if err == nil {
			conn, err := newWindowsPipeConn(h)
			if err != nil {
				windows.CloseHandle(h)
				return nil, err
			}
			return conn, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout connecting to mpv IPC pipe: %s", socketPath)
}

func DefaultMPVSocketPath() string {
	return fmt.Sprintf(`\\.\pipe\kari-mpv-%d`, os.Getpid())
}

// windowsPipeConn implements net.Conn over a Windows named pipe opened with
// FILE_FLAG_OVERLAPPED so SetDeadline/SetReadDeadline/SetWriteDeadline can
// be honored for real.
//
// This deliberately does NOT use a synchronous (non-overlapped) handle plus
// a timer that calls CancelIoEx from another goroutine to fake a deadline —
// that pattern is a documented hang hazard on Windows: the kernel disables
// APC delivery for a thread blocked in synchronous I/O, so the canceling
// call can itself block forever waiting for an APC that will never be
// delivered (see "Canceling Pending I/O Operations" in the Win32 docs, and
// https://www.ntkernel.com/a-rare-cancelioex-hang-in-go-on-windows/ for the
// same failure mode hit from Go specifically). CancelIoEx is only reliable
// against overlapped (asynchronous) I/O, which is what this implementation
// uses throughout.
type windowsPipeConn struct {
	handle windows.Handle

	// readEvent/writeEvent are dedicated manual-reset events for each
	// direction's OVERLAPPED request. IPCClient only ever has one
	// goroutine driving a given conn (Read via bufio.Scanner, Write from
	// GetProperty, never concurrently), but keeping them separate avoids
	// any cross-talk if that ever changes.
	readEvent  windows.Handle
	writeEvent windows.Handle

	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
}

func newWindowsPipeConn(h windows.Handle) (*windowsPipeConn, error) {
	readEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	writeEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(readEvent)
		return nil, err
	}
	return &windowsPipeConn{handle: h, readEvent: readEvent, writeEvent: writeEvent}, nil
}

// doOverlappedIO issues an overlapped ReadFile/WriteFile via issue, then
// waits for it to complete or for deadline to elapse. On timeout it cancels
// only this specific request (CancelIoEx targeted with ov, not a nil-scoped
// cancel of everything pending on the handle) and blocks briefly on
// GetOverlappedResult to let the kernel acknowledge the cancellation before
// returning — that wait is expected to be short precisely because this is
// overlapped I/O, unlike the synchronous case this replaces.
func (c *windowsPipeConn) doOverlappedIO(event windows.Handle, deadline time.Time, issue func(ov *windows.Overlapped) error) (int, error) {
	if err := windows.ResetEvent(event); err != nil {
		return 0, err
	}
	ov := &windows.Overlapped{HEvent: event}

	if err := issue(ov); err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return 0, err
	}

	waitMs := uint32(windows.INFINITE)
	if !deadline.IsZero() {
		d := max(time.Until(deadline), 0)
		ms := d.Milliseconds()
		if ms >= int64(windows.INFINITE) {
			ms = int64(windows.INFINITE) - 1
		}
		waitMs = uint32(ms)
	}

	waitResult, waitErr := windows.WaitForSingleObject(event, waitMs)
	if waitErr != nil {
		return 0, waitErr
	}

	var done uint32
	if waitResult == uint32(windows.WAIT_TIMEOUT) {
		_ = windows.CancelIoEx(c.handle, ov)
		_ = windows.GetOverlappedResult(c.handle, ov, &done, true)
		return int(done), os.ErrDeadlineExceeded
	}

	err := windows.GetOverlappedResult(c.handle, ov, &done, false)
	return int(done), err
}

func (c *windowsPipeConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	deadline := c.readDeadline
	c.mu.Unlock()

	return c.doOverlappedIO(c.readEvent, deadline, func(ov *windows.Overlapped) error {
		var scratch uint32
		return windows.ReadFile(c.handle, b, &scratch, ov)
	})
}

func (c *windowsPipeConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	deadline := c.writeDeadline
	c.mu.Unlock()

	return c.doOverlappedIO(c.writeEvent, deadline, func(ov *windows.Overlapped) error {
		var scratch uint32
		return windows.WriteFile(c.handle, b, &scratch, ov)
	})
}

func (c *windowsPipeConn) Close() error {
	err := windows.CloseHandle(c.handle)
	_ = windows.CloseHandle(c.readEvent)
	_ = windows.CloseHandle(c.writeEvent)
	return err
}

func (c *windowsPipeConn) LocalAddr() net.Addr {
	return pipeAddr("pipe")
}

func (c *windowsPipeConn) RemoteAddr() net.Addr {
	return pipeAddr("pipe")
}

func (c *windowsPipeConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

func (c *windowsPipeConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()
	return nil
}

func (c *windowsPipeConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }
