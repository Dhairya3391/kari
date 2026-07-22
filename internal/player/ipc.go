package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

func NewIPCClient(socketPath string) *IPCClient {
	return &IPCClient{
		socketPath: socketPath,
	}
}

func (c *IPCClient) Connect(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", c.socketPath, 1*time.Second)
		if err == nil {
			c.mu.Lock()
			c.conn = conn
			c.scanner = bufio.NewScanner(conn)
			c.closed = false
			c.mu.Unlock()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout connecting to mpv IPC socket: %s", c.socketPath)
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

	c.conn.SetDeadline(time.Now().Add(2 * time.Second))
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
			return resp["data"], nil
		}
		// If it's not our request_id (e.g., it's an event or old response), we just loop and scan again
	}

	if err := c.scanner.Err(); err != nil {
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

func DefaultMPVSocketPath() string {
	cleanupStaleSockets()
	return fmt.Sprintf("/tmp/kari-mpv-%d.sock", os.Getpid())
}

// cleanupStaleSockets removes /tmp/kari-mpv-*.sock files whose owning
// process is no longer running (e.g. after a crash or SIGKILL).
func cleanupStaleSockets() {
	matches, err := filepath.Glob("/tmp/kari-mpv-*.sock")
	if err != nil {
		return
	}
	for _, path := range matches {
		base := filepath.Base(path)
		// Extract PID from "kari-mpv-<pid>.sock"
		name := strings.TrimPrefix(base, "kari-mpv-")
		name = strings.TrimSuffix(name, ".sock")
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			os.Remove(path)
			continue
		}
		// Signal 0 checks if the process exists without actually signaling it.
		err = proc.Signal(syscall.Signal(0))
		if err != nil {
			os.Remove(path)
		}
	}
}
