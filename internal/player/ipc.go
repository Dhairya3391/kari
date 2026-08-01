package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

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

func NewIPCClient(socketPath string) *IPCClient {
	return &IPCClient{
		socketPath: socketPath,
	}
}

func (c *IPCClient) Connect(timeout time.Duration) error {
	conn, err := dialIPC(c.socketPath, timeout)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.scanner = newIPCSerializer(conn)
	c.closed = false
	c.mu.Unlock()
	return nil
}

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

func (c *IPCClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.closed = true
		err := c.conn.Close()
		// Remove the socket file if it belongs to our process.
		if c.socketPath != "" {
			os.Remove(c.socketPath)
		}
		return err
	}
	return nil
}
