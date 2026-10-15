package presence

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"kari/internal/logging"
)

// presenceLog scopes every line emitted by this package.
var presenceLog = logging.With("component", "presence")

// Discord IPC opcodes (the 8-byte little-endian frame header's first field).
const (
	opHandshake = 0
	opFrame     = 1
	opClose     = 2
)

// maxFrameBytes bounds an inbound frame so a malformed length can't trigger
// an unbounded allocation.
const maxFrameBytes = 1 << 20

// handshakeTimeout bounds how long a connect waits for Discord's READY reply
// so a half-open socket never wedges the worker.
const handshakeTimeout = 5 * time.Second

// minUpdateInterval spaces activity pushes well under Discord's limit of five
// updates per twenty seconds.
const minUpdateInterval = 4 * time.Second

// seekDriftThreshold is how far reported position may drift from the locally
// projected position before a seek is assumed and the card is refreshed.
// Timestamps animate client-side, so no update is needed while position
// tracks the timer.
const seekDriftThreshold = 15 * time.Second

// dialFunc opens and handshakes an IPC connection. It is a field on Client
// (not a package global) so tests can inject a fake transport.
type dialFunc func(ctx context.Context, appID string) (io.ReadWriteCloser, error)

// Client publishes Kari playback as Discord Rich Presence over the local
// Discord IPC socket. Every method is safe for concurrent use and never
// blocks on Discord: updates are coalesced and delivered by a background
// worker that reconnects on demand. When Discord is not running, calls are
// silent no-ops.
type Client struct {
	appID string
	dial  dialFunc

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// signal is a one-slot mailbox: a pending update collapses with any
	// others, so bursts (e.g. rapid telemetry) cost a single send.
	signal chan struct{}

	mu   sync.Mutex
	want *Activity // nil clears the activity
	now  *NowPlaying
	last Progress
	// syncedAt/syncedPos record the last published sample so telemetry drift
	// (a seek) can be detected without resending every second.
	syncedAt  time.Time
	syncedPos float64
	lastPause bool

	// conn is owned by the worker; guarded so Close can unblock a pending
	// handshake or read.
	connMu sync.Mutex
	conn   io.ReadWriteCloser

	// lastSent is touched only by the worker goroutine.
	lastSent time.Time
}

// New builds a client for the given Discord application id. A blank id is an
// error so callers can treat presence as unconfigured and skip wiring it.
func New(appID string) (*Client, error) {
	return newClient(appID, dialIPC)
}

func newClient(appID string, dial dialFunc) (*Client, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, fmt.Errorf("presence: discord application id is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		appID:  appID,
		dial:   dial,
		ctx:    ctx,
		cancel: cancel,
		signal: make(chan struct{}, 1),
	}
	c.wg.Add(1)
	go c.worker()
	return c, nil
}

// SetNowPlaying publishes the media now being watched. The media's StartTime
// seeds the timer until live telemetry arrives.
func (c *Client) SetNowPlaying(np NowPlaying) {
	start := np.Media.StartTime
	c.mu.Lock()
	c.now = &np
	c.last = Progress{PositionSecs: start}
	c.lastPause = false
	c.syncedAt = time.Now()
	c.syncedPos = start
	c.want = buildActivity(np, c.last, c.syncedAt)
	c.mu.Unlock()
	c.notify()
}

// UpdateProgress applies a live playback sample to the current card. The card
// is republished only on a pause/resume transition or a seek; otherwise
// Discord's timestamp animation keeps the timer accurate for free.
func (c *Client) UpdateProgress(p Progress) {
	c.mu.Lock()
	if c.now == nil {
		c.mu.Unlock()
		return
	}
	changed := p.Paused != c.lastPause
	drift := 0.0
	if !c.lastPause {
		projected := c.syncedPos + time.Since(c.syncedAt).Seconds()
		drift = math.Abs(p.PositionSecs - projected)
	}
	c.last = p
	if !changed && drift < seekDriftThreshold.Seconds() {
		c.mu.Unlock()
		return
	}
	c.lastPause = p.Paused
	c.syncedAt = time.Now()
	c.syncedPos = p.PositionSecs
	c.want = buildActivity(*c.now, p, c.syncedAt)
	c.mu.Unlock()
	c.notify()
}

// Clear removes the presence while keeping the connection open, e.g. between
// episodes or when the user stops watching.
func (c *Client) Clear() {
	c.mu.Lock()
	c.now = nil
	c.last = Progress{}
	c.want = nil
	c.mu.Unlock()
	c.notify()
}

// Close stops the worker and drops the IPC connection. Disconnecting removes
// the presence from Discord, so no explicit clear is required.
func (c *Client) Close() {
	c.cancel()
	c.closeConn()
	c.wg.Wait()
}

func (c *Client) notify() {
	select {
	case c.signal <- struct{}{}:
	default:
	}
}

func (c *Client) worker() {
	defer c.wg.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.signal:
			c.flush()
		}
	}
}

// flush delivers the latest desired activity, throttled to respect Discord's
// rate limit. The newest value always wins: anything superseded while waiting
// is re-read just before sending.
func (c *Client) flush() {
	c.mu.Lock()
	want := c.want
	c.mu.Unlock()

	if since := time.Since(c.lastSent); since < minUpdateInterval {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(minUpdateInterval - since):
		}
		c.mu.Lock()
		want = c.want
		c.mu.Unlock()
	}

	conn := c.connection()
	if conn == nil {
		return
	}
	if err := writeActivity(conn, want); err != nil {
		presenceLog.Debug("discord activity update failed", "err", err)
		c.closeConn()
		return
	}
	c.lastSent = time.Now()
}

// connection returns the live IPC connection, dialing and handshaking on
// first use. It returns nil when Discord is unreachable.
func (c *Client) connection() io.ReadWriteCloser {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn != nil {
		return c.conn
	}
	conn, err := c.dial(c.ctx, c.appID)
	if err != nil {
		presenceLog.Debug("discord ipc unavailable", "err", err)
		return nil
	}
	c.conn = conn
	return conn
}

func (c *Client) closeConn() {
	c.connMu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
}

// writeActivity sends a SET_ACTIVITY command; a nil activity clears the
// presence.
func writeActivity(w io.Writer, act *Activity) error {
	args := map[string]any{"pid": os.Getpid()}
	if act != nil {
		args["activity"] = act
	} else {
		args["activity"] = nil
	}
	return writeFrame(w, opFrame, map[string]any{
		"cmd":   "SET_ACTIVITY",
		"args":  args,
		"nonce": nonce(),
	})
}

func writeFrame(w io.Writer, opcode uint32, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[0:4], opcode)
	binary.LittleEndian.PutUint32(header[4:8], uint32(len(data)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func readFrame(r io.Reader) (uint32, []byte, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	opcode := binary.LittleEndian.Uint32(header[0:4])
	length := binary.LittleEndian.Uint32(header[4:8])
	if length > maxFrameBytes {
		return 0, nil, fmt.Errorf("presence: frame too large (%d bytes)", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}

// handshake performs the IPC connect handshake and waits for Discord's READY
// reply. It bounds the exchange with a read deadline where the transport
// supports one (unix sockets; Windows named pipes do not).
func handshake(conn io.ReadWriteCloser, appID string) error {
	if d, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = d.SetDeadline(time.Now().Add(handshakeTimeout))
		defer func() { _ = d.SetDeadline(time.Time{}) }()
	}
	if err := writeFrame(conn, opHandshake, map[string]any{"v": 1, "client_id": appID}); err != nil {
		return fmt.Errorf("presence: handshake write: %w", err)
	}
	for i := 0; i < 3; i++ {
		opcode, payload, err := readFrame(conn)
		if err != nil {
			return fmt.Errorf("presence: handshake read: %w", err)
		}
		if opcode == opClose {
			return fmt.Errorf("presence: handshake rejected: %s", strings.TrimSpace(string(payload)))
		}
		if opcode != opFrame {
			continue
		}
		var ready struct {
			Evt string `json:"evt"`
		}
		if err := json.Unmarshal(payload, &ready); err != nil {
			continue
		}
		if ready.Evt == "READY" {
			return nil
		}
	}
	return fmt.Errorf("presence: no READY reply from discord")
}

// nonce returns a unique id for each command; Discord echoes it back and
// only uses it to correlate replies.
func nonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("kari-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
