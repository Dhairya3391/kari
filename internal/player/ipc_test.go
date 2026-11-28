package player

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestPlaybackStats_LoadedState(t *testing.T) {
	ps := newPlaybackStats()

	if ps.playing() {
		t.Fatalf("expected playing() to be false initially")
	}
	if ps.observedPlayback() {
		t.Fatal("a fresh stats object has no media evidence")
	}

	ps.update(0.0, 0.0, true)
	if !ps.playing() || !ps.observedPlayback() {
		t.Fatalf("expected playing() to be true when loaded is true, even at position 0.0s")
	}
}

func TestMediaLoaded(t *testing.T) {
	tests := []struct {
		name   string
		posErr error
		durErr error
		dur    float64
		want   bool
	}{
		{"time-pos available", nil, errors.New("unavailable"), 0, true},
		{"duration available", errors.New("unavailable"), nil, 100, true},
		{"duration zero", errors.New("unavailable"), nil, 0, false},
		{"both unavailable", errors.New("unavailable"), errors.New("unavailable"), 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mediaLoaded(tc.posErr, tc.durErr, tc.dur); got != tc.want {
				t.Fatalf("mediaLoaded() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIPCClientCommandDecodesTypedResponse(t *testing.T) {
	tests := []struct {
		name    string
		reply   string
		want    any
		wantErr bool
	}{
		{"numeric data", `{"request_id":%d,"error":"success","data":123.5}`, float64(123.5), false},
		{"boolean data", `{"request_id":%d,"error":"success","data":false}`, false, false},
		{"string data", `{"request_id":%d,"error":"success","data":"idle"}`, "idle", false},
		{"null data", `{"request_id":%d,"error":"success","data":null}`, nil, false},
		{"missing data", `{"request_id":%d,"error":"success"}`, nil, false},
		{
			"skips events and other request ids",
			`{"event":"file-loaded"}` + "\n" +
				`{"request_id":999,"error":"success","data":"wrong"}` + "\n" +
				`{"request_id":%d,"error":"success","data":123.5}`,
			float64(123.5), false,
		},
		{"error response", `{"request_id":%d,"error":"property unavailable"}`, nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()
			client := &IPCClient{conn: clientConn, scanner: newIPCSerializer(clientConn)}

			go func() {
				defer serverConn.Close()
				var req ipcRequest
				if err := json.NewDecoder(serverConn).Decode(&req); err != nil {
					return
				}
				_, _ = fmt.Fprintf(serverConn, tc.reply+"\n", req.RequestID)
			}()

			got, err := client.command(ipcCmdGetProperty, ipcPropTimePos)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an mpv error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("command() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// Unavailable media properties on a reachable socket (an unplayable URL mpv
// never loaded) must never count as playback evidence. That is what makes the
// exit-code fallback unavailable for the false "Playback finished" case.
func TestIPCPollerUnavailablePropertiesAreNotPlayback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket test")
	}
	sockPath := fmt.Sprintf("/tmp/kari-ipc-poll-%d.sock", os.Getpid())
	_ = os.Remove(sockPath)
	defer os.Remove(sockPath)
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req ipcRequest
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						continue
					}
					prop := ""
					if len(req.Command) > 1 {
						prop, _ = req.Command[1].(string)
					}
					if prop == ipcPropTimePos || prop == ipcPropDuration {
						fmt.Fprintf(c, `{"request_id":%d,"error":"property unavailable"}`+"\n", req.RequestID)
					} else {
						fmt.Fprintf(c, `{"request_id":%d,"error":"success","data":false}`+"\n", req.RequestID)
					}
				}
			}(conn)
		}
	}()

	stats := newPlaybackStats()
	stop := make(chan struct{})
	go ipcPoller(context.Background(), NewIPCClient(sockPath), stats,
		"https://cdn.example.com/v.m3u8", false, nil, stop)
	defer close(stop)

	time.Sleep(100 * time.Millisecond)
	if stats.observedPlayback() {
		t.Fatal("unavailable media properties must not count as playback evidence")
	}
}

func TestAddSubtitleEmptyPath(t *testing.T) {
	c := NewIPCClient("/tmp/fake.sock")
	if err := c.AddSubtitle(""); err != nil {
		t.Fatalf("empty subtitle path should be no-op, got %v", err)
	}
	if err := HotAddSubtitle("   "); err != nil {
		t.Fatalf("whitespace subtitle path should be no-op, got %v", err)
	}
}

func TestAddSubtitleDisconnectedClient(t *testing.T) {
	c := NewIPCClient("/tmp/fake.sock")
	if err := c.AddSubtitle("/path/to/sub.vtt"); err == nil {
		t.Fatal("expected error on disconnected client")
	}
}

func TestCleanupStaleSocketsRemovesDeadOwnerFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket cleanup is platform-specific")
	}
	stale := "/tmp/kari-mpv-99999999.sock"
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(stale)

	cleanupStaleSockets()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale socket still present after cleanup: %v", err)
	}
}

func TestAddSubtitleMockSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping unix socket test on windows")
	}
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req map[string]any
					if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
						return
					}
					reqID := req["request_id"]
					resp := fmt.Sprintf(`{"request_id":%v,"error":"success","data":null}`+"\n", reqID)
					_, _ = c.Write([]byte(resp))
				}
			}(conn)
		}
	}()

	client := NewIPCClient(sockPath)
	if err := client.Connect(2 * time.Second); err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	defer client.Close()

	if err := client.AddSubtitle("/tmp/subs/eng.vtt"); err != nil {
		t.Fatalf("AddSubtitle failed: %v", err)
	}

	t.Setenv("MPV_SOCKET", sockPath)
	if err := HotAddSubtitle("/tmp/subs/eng.vtt"); err != nil {
		t.Fatalf("HotAddSubtitle failed: %v", err)
	}
}

// A hot-add scheduled for a playback that has since ended must not attach to
// a player that binds the same socket afterwards (the next episode). Before
// the session-scoped API existed this test failed: the add reached the new
// player. Verified against the old code with a diagnostic variant.
func TestHotAddSubtitleContextStopsWhenCanceled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping unix socket test on windows")
	}
	sockPath := fmt.Sprintf("/tmp/kari-ipc-test-%d.sock", os.Getpid())
	_ = os.Remove(sockPath)
	defer os.Remove(sockPath)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- HotAddSubtitleContext(ctx, sockPath, "/tmp/subs/episodeN.srt")
	}()

	// No listener yet; the side-load sits in its connect retry loop. End the
	// playback session before the replacement player appears.
	time.Sleep(300 * time.Millisecond)
	cancel()

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	got := make(chan string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			var req ipcRequest
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				continue
			}
			if len(req.Command) > 0 && req.Command[0] == "sub-add" {
				got <- fmt.Sprint(req.Command[1])
			}
			resp := fmt.Sprintf(`{"request_id":%d,"error":"success","data":null}`+"\n", req.RequestID)
			_, _ = conn.Write([]byte(resp))
		}
	}()

	select {
	case path := <-got:
		t.Fatalf("cancelled hot-add still reached the new player: %q", path)
	case <-time.After(500 * time.Millisecond):
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled hot-add did not return")
	}
}
