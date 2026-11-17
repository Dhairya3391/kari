package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLiveSkipInjectionSendsScriptMessage verifies that a late skip lookup is
// pushed into the running mpv through its IPC socket rather than being lost
// for the current playback.
func TestLiveSkipInjectionSendsScriptMessage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping unix socket test on windows")
	}
	// macOS caps unix socket paths at ~104 bytes, so keep this short.
	sockPath := fmt.Sprintf("%s/ks-%d.sock", os.TempDir(), os.Getpid())
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(sockPath)

	type message struct {
		Command []any `json:"command"`
		ID      int   `json:"request_id"`
	}
	messages := make(chan message, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req message
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						continue
					}
					messages <- req
					_, _ = c.Write([]byte(`{"request_id":1,"error":"success","data":null}` + "\n"))
				}
			}(conn)
		}
	}()

	client := NewIPCClient(sockPath)
	if err := client.Connect(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	sink := newLiveSkipSink(SkipSettings{AutoSkipIntro: true, Provider: "hybrid"})
	sink.attach(client)

	times := combinedSkipTimes{
		OpStart: 10, OpEnd: 90, EdStart: 1200, EdEnd: 1300,
		PreviewStart: -1, PreviewEnd: -1,
	}
	if err := sink.ApplySkipTimes(times); err != nil {
		t.Fatalf("ApplySkipTimes: %v", err)
	}

	select {
	case msg := <-messages:
		if len(msg.Command) < 3 || msg.Command[0] != "script-message" || msg.Command[1] != "kari-skip-update" {
			t.Fatalf("unexpected mpv command: %+v", msg.Command)
		}
		payload, ok := msg.Command[2].(string)
		if !ok {
			t.Fatalf("payload is not a string: %T", msg.Command[2])
		}
		var fields map[string]any
		if json.Unmarshal([]byte(payload), &fields) != nil {
			t.Fatalf("payload is not JSON: %q", payload)
		}
		if fields["op_start"] != float64(10) || fields["ed_end"] != float64(1300) {
			t.Fatalf("payload = %v", fields)
		}
		if fields["auto_intro"] != float64(1) {
			t.Fatalf("auto_intro = %v, want 1", fields["auto_intro"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no script-message was sent to mpv")
	}
}

func TestLiveSkipInjectionWithoutConnection(t *testing.T) {
	sink := newLiveSkipSink(SkipSettings{})
	if err := sink.ApplySkipTimes(combinedSkipTimes{OpStart: 1, OpEnd: 2}); err == nil {
		t.Fatal("expected an error when mpv is not connected")
	}
}

// A late lookup can finish before the player's IPC client is attached. The
// result must be staged and delivered once the client appears, not dropped on
// a nil client.
func TestLiveSkipLateResultWaitsForAttach(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping unix socket test on windows")
	}
	sockPath := fmt.Sprintf("%s/ks-late-%d.sock", os.TempDir(), os.Getpid())
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(sockPath)

	messages := make(chan []byte, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					line := append([]byte(nil), sc.Bytes()...)
					messages <- line
					var req struct {
						ID int `json:"request_id"`
					}
					_ = json.Unmarshal(line, &req)
					resp, _ := json.Marshal(map[string]any{"request_id": req.ID, "error": "success", "data": nil})
					_, _ = c.Write(append(resp, '\n'))
				}
			}(conn)
		}
	}()

	sink := newLiveSkipSink(SkipSettings{AutoSkipIntro: true})
	results := make(chan combinedSkipTimes, 1)
	results <- combinedSkipTimes{
		OpStart: 10, OpEnd: 90, EdStart: -1, EdEnd: -1,
		RecapStart: -1, RecapEnd: -1, PreviewStart: -1, PreviewEnd: -1,
	}
	done := make(chan struct{})
	go func() {
		sink.applyWhenReady(results)
		close(done)
	}()

	// The lookup is already done but no client is attached yet: the result
	// must still be pending rather than dropped.
	select {
	case <-done:
		t.Fatal("late result was dropped before the player attached")
	case <-time.After(100 * time.Millisecond):
	}
	if sink.pendingDone() {
		t.Fatal("late result was not staged for delivery")
	}

	// Attach and connect like the real player does just after launch.
	client := NewIPCClient(sockPath)
	sink.attach(client)
	if err := client.Connect(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	select {
	case line := <-messages:
		if !strings.Contains(string(line), "kari-skip-update") {
			t.Fatalf("unexpected message: %s", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late skip result was never delivered after attach")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("applyWhenReady did not finish after delivery")
	}
}
