package presence

import (
	"context"
	"errors"
	"io"
)

// dialIPC connects to the first reachable Discord IPC endpoint and completes
// the handshake. Endpoint discovery is per-platform; the trial-and-handshake
// loop is shared.
func dialIPC(ctx context.Context, appID string) (io.ReadWriteCloser, error) {
	var lastErr error
	for _, endpoint := range ipcEndpoints() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := dialEndpoint(ctx, endpoint)
		if err != nil {
			lastErr = err
			continue
		}
		if err := handshake(conn, appID); err != nil {
			_ = conn.Close()
			lastErr = err
			continue
		}
		return conn, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no discord ipc endpoint available")
	}
	return nil, lastErr
}
