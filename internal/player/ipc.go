package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// IPCClient speaks mpv's JSON IPC protocol over a unix socket / named
// pipe to query playback state and issue commands.
type IPCClient struct {
	socketPath string
	conn       net.Conn
	scanner    *bufio.Scanner
	mu         sync.Mutex
	closed     bool
	reqID      int
}

// playbackStats guards PlaybackResult fields that are updated by the
// ipcPoller goroutine and read by the caller once playback ends. Without a
// lock, the final snapshot could race with the poller's last update.
type playbackStats struct {
	mu     sync.Mutex
	result PlaybackResult
	loaded bool
}

func newPlaybackStats() *playbackStats {
	return &playbackStats{}
}

func (s *playbackStats) update(pos, dur float64, loaded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result.FinalPositionSecs = pos
	s.result.DurationSecs = dur
	if loaded {
		s.loaded = true
	}
	if s.result.DurationSecs > 0 {
		s.result.Completed = s.result.FinalPositionSecs/s.result.DurationSecs > 0.85
	} else {
		s.result.Completed = false
	}
}

// (mpv.go, !android build); the android target genuinely excludes it.
//
//lint:ignore U1000 snapshot is used by the desktop mpv backend
func (s *playbackStats) snapshot() PlaybackResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result
}

func (s *playbackStats) playing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loaded || s.result.DurationSecs > 0 || s.result.FinalPositionSecs > 0
}

func newIPCSerializer(conn net.Conn) *bufio.Scanner {
	sc := bufio.NewScanner(conn)
	// mpv can emit events/large payloads on the same socket; use a generous
	// buffer so a single oversized line doesn't permanently kill the scanner.
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	return sc
}

// NewIPCClient constructs a client for the socket at socketPath.
func NewIPCClient(socketPath string) *IPCClient {
	return &IPCClient{
		socketPath: socketPath,
	}
}

// Connect dials the mpv IPC socket, replacing any prior connection.
func (c *IPCClient) Connect(timeout time.Duration) error {
	conn, err := dialIPC(c.socketPath, timeout)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.conn != nil {
		_ = c.conn.Close() // never leak the previous connection on redial
	}
	c.conn = conn
	c.scanner = newIPCSerializer(conn)
	c.closed = false
	c.mu.Unlock()
	return nil
}

// GetProperty issues a get_property command and decodes its data field.
func (c *IPCClient) GetProperty(property string) (any, error) {
	return c.command("get_property", property)
}

// AddSubtitle sends a sub-add command over IPC to load and select an external
// subtitle track in the running player without restarting playback.
func (c *IPCClient) AddSubtitle(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	path = strings.ReplaceAll(path, `\`, `/`)
	_, err := c.command("sub-add", path, "select")
	return err
}

// HotAddSubtitle connects to the active MPV/IINA IPC socket and side-loads
// the given subtitle file so that late-arriving subtitles appear without
// interrupting playback.
func HotAddSubtitle(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	socketPath := DefaultMPVSocketPath()
	client := NewIPCClient(socketPath)
	if err := client.Connect(500 * time.Millisecond); err != nil {
		return err
	}
	defer client.Close()
	return client.AddSubtitle(path)
}

func (c *IPCClient) command(args ...any) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || c.closed {
		return nil, fmt.Errorf("ipc client not connected")
	}

	c.reqID++
	reqID := c.reqID
	data, err := json.Marshal(map[string]any{
		"command":    args,
		"request_id": reqID,
	})
	if err != nil {
		return nil, err
	}
	if err := c.conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return nil, err
	}
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}

	for c.scanner.Scan() {
		var response map[string]any
		if err := json.Unmarshal(c.scanner.Bytes(), &response); err != nil {
			continue
		}
		id, ok := response["request_id"].(float64)
		if !ok || int(id) != reqID {
			continue
		}
		if errText, ok := response["error"].(string); ok && errText != "success" {
			return nil, fmt.Errorf("mpv error: %s", errText)
		}
		return response["data"], nil
	}

	err = c.scanner.Err()
	c.scanner = newIPCSerializer(c.conn)
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no response from mpv")
}

// Close releases the connection; safe when already closed. It deliberately
// does NOT remove the socket file: the path is owned by the launched mpv
// process (which may still be playing and accepting other clients), and
// startPlayerWithStartupCheck clears stale files before each launch.
func (c *IPCClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.closed = true
		return c.conn.Close()
	}
	return nil
}
