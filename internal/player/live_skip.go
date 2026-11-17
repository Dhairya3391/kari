package player

import (
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const (
	// liveSkipAttachWait caps how long a late result waits for the player IPC
	// client to be attached. Attach happens right after mpv starts, so this
	// only needs to cover process-launch latency, not a full lookup.
	liveSkipAttachWait = 10 * time.Second
	// liveSkipConnectWait bounds the retry loop that delivers a staged result
	// while the IPC poller establishes the connection.
	liveSkipConnectWait = 5 * time.Second
	// liveSkipRetryInterval is the delay between delivery attempts before the
	// IPC socket is connected.
	liveSkipRetryInterval = 50 * time.Millisecond
)

// liveSkipSink delivers skip timings to an mpv process that is already
// playing. The skip lookup can outlast the foreground wait; when it finishes
// the sink pushes the segment boundaries into the running player through its
// IPC socket so the current playback gets chapters and auto-skip without a
// restart.
type liveSkipSink struct {
	mu       sync.Mutex
	client   *IPCClient
	settings SkipSettings
	// pending is a late result awaiting a confirmed delivery. It is kept
	// across attach calls so a result that resolved before the player started,
	// or that was sent to a direct launch that then failed, is retried on the
	// next client (e.g. the curl|mpv pipe fallback).
	pending  *combinedSkipTimes
	attached chan struct{}
	once     sync.Once
}

// newLiveSkipSink builds a sink carrying the playback's skip preferences.
func newLiveSkipSink(settings SkipSettings) *liveSkipSink {
	return &liveSkipSink{settings: settings, attached: make(chan struct{})}
}

// attach binds a live mpv IPC client to the sink and immediately tries to
// deliver any staged late result.
func (s *liveSkipSink) attach(client *IPCClient) {
	s.mu.Lock()
	s.client = client
	s.mu.Unlock()
	s.once.Do(func() { close(s.attached) })
	s.deliverPending()
}

// stageLateResult caches a late lookup result for delivery once a player
// client is attached and connected.
func (s *liveSkipSink) stageLateResult(times combinedSkipTimes) {
	s.mu.Lock()
	s.pending = &times
	s.mu.Unlock()
}

// pendingDone reports whether the staged result has been delivered.
func (s *liveSkipSink) pendingDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending == nil
}

// ApplySkipTimes sends a skip-update message to the running mpv. A missing
// connection is an error so callers can fall back to the cached result for
// the next play.
func (s *liveSkipSink) ApplySkipTimes(times combinedSkipTimes) error {
	s.mu.Lock()
	client, settings := s.client, s.settings
	s.mu.Unlock()
	if client == nil {
		return errors.New("mpv ipc not connected")
	}
	return sendSkipTimes(client, times, settings)
}

// deliverPending sends the staged result, if any, to the currently attached
// client. It clears the result only after a successful send, so a failed
// direct launch can hand it to the next client.
func (s *liveSkipSink) deliverPending() {
	s.mu.Lock()
	client, settings, pending := s.client, s.settings, s.pending
	s.mu.Unlock()
	if client == nil || pending == nil {
		return
	}
	if err := sendSkipTimes(client, *pending, settings); err != nil {
		skipLog.Debug("live skip injection failed", "err", err)
		return
	}
	s.mu.Lock()
	s.pending = nil
	s.mu.Unlock()
}

// sendSkipTimes builds and sends the skip-update payload over one IPC client.
func sendSkipTimes(client *IPCClient, times combinedSkipTimes, settings SkipSettings) error {
	if times.PreviewStart >= 0 && times.EdStart >= 0 && times.EdStart < times.PreviewStart && times.EdEnd > times.PreviewStart {
		times.EdEnd = times.PreviewStart
	}
	payload := map[string]any{
		"recap_start":   times.RecapStart,
		"recap_end":     times.RecapEnd,
		"op_start":      times.OpStart,
		"op_end":        times.OpEnd,
		"ed_start":      times.EdStart,
		"ed_end":        times.EdEnd,
		"preview_start": times.PreviewStart,
		"preview_end":   times.PreviewEnd,
		"auto_recap":    boolInt(settings.SkipRecap),
		"auto_intro":    boolInt(settings.AutoSkipIntro && times.OpStart >= 0),
		"auto_ending":   boolInt(settings.AutoSkipEnding && times.EdStart >= 0),
		"auto_preview":  boolInt(settings.SkipPreview && times.PreviewStart >= 0),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = client.command("script-message", "kari-skip-update", string(data))
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
