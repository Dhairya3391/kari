package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
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

	ps.update(0.0, 0.0, true)
	if !ps.playing() {
		t.Fatalf("expected playing() to be true when loaded is true, even at position 0.0s")
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
