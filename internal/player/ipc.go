package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
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

func newPlaybackStats() *playbackStats { return &playbackStats{} }

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

// GetProperty issues a get_property command and decodes the "data" field.
func (c *IPCClient) GetProperty(property string) (interface{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || c.closed {
		return nil, fmt.Errorf("ipc client not connected")
	}

	c.reqID++
	reqID := c.reqID

	req := map[string]interface{}{
		"command":    []interface{}{"get_property", property},
		"request_id": reqID,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	c.conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}

	// Keep reading lines until we find the response for our request_id
	for c.scanner.Scan() {
		var resp map[string]interface{}
		if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
			continue // Skip malformed lines
		}

		// Check if this is the response we are waiting for
		if idVal, ok := resp["request_id"].(float64); ok && int(idVal) == reqID {
			if errStr, ok := resp["error"].(string); ok && errStr != "success" {
				_ = c.conn.SetDeadline(time.Time{})
				return nil, fmt.Errorf("mpv error: %s", errStr)
			}
			// Clear the deadline so a stale one doesn't trip a later call.
			_ = c.conn.SetDeadline(time.Time{})
			return resp["data"], nil
		}
		// If it's not our request_id (e.g., it's an event or old response), we just loop and scan again
	}

	// The scanner is exhausted (EOF, deadline, or an oversized line). The
	// connection may still be usable, so clear the deadline and install a
	// fresh scanner so subsequent requests can recover instead of failing
	// forever. Responses are matched by request_id, so a dropped line here
	// self-heals on the next request.
	if err := c.scanner.Err(); err != nil {
		_ = c.conn.SetDeadline(time.Time{})
		c.scanner = newIPCSerializer(c.conn)
		return nil, err
	}
	return nil, fmt.Errorf("no response from mpv")
}
// SendCommand sends a command array to mpv without waiting for a request_id response.
func (c *IPCClient) SendCommand(command ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || c.closed {
		return fmt.Errorf("ipc client not connected")
	}

	req := map[string]any{
		"command": command,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_ = c.conn.SetDeadline(time.Now().Add(3 * time.Second))
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	_, err = c.conn.Write(append(data, '\n'))
	return err
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
