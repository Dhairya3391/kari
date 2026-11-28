package player

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"kari/internal/model"
)

// IPC connection and request timings. A redial is bounded by a short window
// with a brief pause between attempts so a dead socket cannot hang playback.
const (
	ipcConnectWindow   = 5 * time.Second
	ipcConnectRetry    = 500 * time.Millisecond
	ipcRetryDelay      = 200 * time.Millisecond
	ipcRequestDeadline = 3 * time.Second
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

type ipcRequest struct {
	Command   []any `json:"command"`
	RequestID int   `json:"request_id"`
}

type ipcResponse struct {
	RequestID json.Number     `json:"request_id"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
}

// playbackStats guards PlaybackResult fields that are updated by the
// ipcPoller goroutine and read by the caller once playback ends. Without a
// lock, the final snapshot could race with the poller's last update.
type playbackStats struct {
	mu           sync.Mutex
	result       PlaybackResult
	loaded       bool
	shortWarning bool
	shortDur     float64
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
		s.result.Completed = s.result.FinalPositionSecs/s.result.DurationSecs > model.CompletionThreshold
	} else {
		s.result.Completed = false
	}
}
func (s *playbackStats) setShortWarning(dur float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shortWarning = true
	s.shortDur = dur
}

func (s *playbackStats) isShortWarning() (bool, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shortWarning, s.shortDur
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
	return !s.shortWarning && s.hasEvidence()
}

// hasEvidence reports IPC-observed media with the caller holding the lock.
func (s *playbackStats) hasEvidence() bool {
	return s.loaded || s.result.DurationSecs > 0 || s.result.FinalPositionSecs > 0
}

// observedPlayback reports whether IPC ever saw evidence that media loaded.
// The launch result snapshots it before the poller is torn down so a clean
// mpv exit alone can never be mistaken for successful playback.
func (s *playbackStats) observedPlayback() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasEvidence()
}

func newIPCSerializer(conn net.Conn) *bufio.Scanner {
	sc := bufio.NewScanner(conn)
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
	return c.command(ipcCmdGetProperty, property)
}

// AddSubtitle sends a sub-add command over IPC to load and select an external
// subtitle track in the running player without restarting playback.
func (c *IPCClient) AddSubtitle(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	path = strings.ReplaceAll(path, `\`, `/`)
	_, err := c.command(ipcCmdSubAdd, path, ipcSubSelect)
	return err
}

// HotAddSubtitle connects to the active MPV/IINA IPC socket and side-loads
// the given subtitle file so that late-arriving subtitles appear without
// interrupting playback.
func HotAddSubtitle(path string) error {
	return HotAddSubtitleContext(context.Background(), "", path)
}

// HotAddSubtitleContext side-loads a subtitle through the given IPC endpoint
// (or the default one when empty). The context is checked before each connect
// attempt and again after connecting, so a hot-add scheduled for a playback
// session that has since ended cannot attach to a player that rebound the
// socket in the meantime.
func HotAddSubtitleContext(ctx context.Context, socketPath, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(socketPath) == "" {
		socketPath = DefaultMPVSocketPath()
	}
	client := NewIPCClient(socketPath)

	deadline := time.Now().Add(ipcConnectWindow)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := client.Connect(ipcConnectRetry)
		if err == nil {
			defer client.Close()
			if err := ctx.Err(); err != nil {
				return err
			}
			return client.AddSubtitle(path)
		}
		lastErr = err
		time.Sleep(ipcRetryDelay)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return lastErr
}

func (c *IPCClient) command(args ...any) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || c.closed {
		return nil, fmt.Errorf("ipc client not connected")
	}

	c.reqID++
	reqID := c.reqID
	data, err := json.Marshal(ipcRequest{Command: args, RequestID: reqID})
	if err != nil {
		return nil, err
	}
	if err := c.conn.SetDeadline(time.Now().Add(ipcRequestDeadline)); err != nil {
		return nil, err
	}
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}

	for c.scanner.Scan() {
		var response ipcResponse
		if err := json.Unmarshal(c.scanner.Bytes(), &response); err != nil {
			continue
		}
		id, err := response.RequestID.Int64()
		if err != nil || id != int64(reqID) {
			continue
		}
		if response.Error != "" && response.Error != ipcSuccess {
			return nil, fmt.Errorf("mpv error: %s", response.Error)
		}
		if len(response.Data) == 0 || string(response.Data) == "null" {
			return nil, nil
		}
		var value any
		if err := json.Unmarshal(response.Data, &value); err != nil {
			return nil, fmt.Errorf("decode mpv IPC response: %w", err)
		}
		return value, nil
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
