package presence

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := map[string]any{"cmd": "SET_ACTIVITY", "nonce": "abc"}
	if err := writeFrame(&buf, opFrame, want); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}

	opcode, payload, err := readFrame(&buf)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if opcode != opFrame {
		t.Errorf("opcode = %d, want %d", opcode, opFrame)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["cmd"] != "SET_ACTIVITY" || got["nonce"] != "abc" {
		t.Errorf("payload = %+v", got)
	}
}

func TestReadFrameRejectsOversized(t *testing.T) {
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[0:4], opFrame)
	binary.LittleEndian.PutUint32(header[4:8], maxFrameBytes+1)
	if _, _, err := readFrame(bytes.NewReader(header)); err == nil {
		t.Fatal("expected oversized frame to be rejected")
	}
}

func TestHandshake(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		opcode, payload, err := readFrame(server)
		if err != nil {
			return
		}
		if opcode != opHandshake {
			return
		}
		var hs struct {
			V        int    `json:"v"`
			ClientID string `json:"client_id"`
		}
		_ = json.Unmarshal(payload, &hs)
		if hs.V != 1 || hs.ClientID != "12345" {
			return
		}
		_ = writeFrame(server, opFrame, map[string]any{"cmd": "DISPATCH", "evt": "READY", "data": map[string]any{}})
	}()

	if err := handshake(client, "12345"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
}

// fakeDial returns the client half of an in-memory pipe, letting tests drive
// the worker without a real Discord socket.
func fakeDial(t *testing.T) (dialFunc, func() (uint32, []byte)) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })

	read := make(chan struct {
		opcode  uint32
		payload []byte
	}, 8)
	go func() {
		for {
			opcode, payload, err := readFrame(server)
			if err != nil {
				close(read)
				return
			}
			read <- struct {
				opcode  uint32
				payload []byte
			}{opcode, payload}
		}
	}()

	dial := func(context.Context, string) (io.ReadWriteCloser, error) { return client, nil }
	recv := func() (uint32, []byte) {
		select {
		case frame, ok := <-read:
			if !ok {
				return 0, nil
			}
			return frame.opcode, frame.payload
		case <-time.After(2 * time.Second):
			return 0, nil
		}
	}
	return dial, recv
}

func TestClientPublishesActivity(t *testing.T) {
	dial, recv := fakeDial(t)
	c, err := newClient("12345", dial)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	defer c.Close()

	c.SetNowPlaying(NowPlaying{Media: mediaFixture()})

	_, payload := recv()
	var cmd struct {
		Cmd  string `json:"cmd"`
		Args struct {
			Activity *Activity `json:"activity"`
		} `json:"args"`
	}
	if err := json.Unmarshal(payload, &cmd); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cmd.Cmd != "SET_ACTIVITY" || cmd.Args.Activity == nil {
		t.Fatalf("unexpected command: %+v", cmd)
	}
	if cmd.Args.Activity.Details != "Kari Show" {
		t.Errorf("details = %q", cmd.Args.Activity.Details)
	}
}

func TestClientCloseClearsConnection(t *testing.T) {
	dial, _ := fakeDial(t)
	c, err := newClient("12345", dial)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	c.SetNowPlaying(NowPlaying{Media: mediaFixture()})
	c.Close()
	// Close must be idempotent enough to not hang; reaching here is the test.
}

func TestClientAnswersPingWithPong(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })

	ping := []byte(`{"nonce":"keepalive"}`)
	got := make(chan struct{}, 1)
	go func() {
		// The first frame the client sends is the activity; consume it so the
		// reply we read next is unambiguously the keepalive answer.
		if _, _, err := readFrame(server); err != nil {
			return
		}
		if err := writeRawFrame(server, opPing, ping); err != nil {
			return
		}
		opcode, payload, err := readFrame(server)
		if err != nil {
			return
		}
		if opcode == opPong && bytes.Equal(payload, ping) {
			got <- struct{}{}
		}
	}()

	dial := func(context.Context, string) (io.ReadWriteCloser, error) { return client, nil }
	c, err := newClient("12345", dial)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	defer c.Close()

	c.SetNowPlaying(NowPlaying{Media: mediaFixture()})

	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a PONG reply to Discord's PING")
	}
}
