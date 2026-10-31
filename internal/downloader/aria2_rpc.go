package downloader

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kari/internal/logging"
	"kari/internal/provider"
)

// log scopes every line from this package.
var ariaLog = logging.With("component", "downloader.aria2")

// ── JSON-RPC types ────────────────────────────────────────────────────────────
type aria2RPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params,omitempty"`
}

type aria2RPCResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type aria2TellStatusResult struct {
	GID             string `json:"gid"`
	Status          string `json:"status"`
	TotalLength     string `json:"totalLength"`
	CompletedLength string `json:"completedLength"`
	DownloadSpeed   string `json:"downloadSpeed"`
	Connections     string `json:"connections"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
	Dir             string `json:"dir"`
	Files           []struct {
		Path string `json:"path"`
	} `json:"files,omitempty"`
}

// ── Aria2Downloader ──────────────────────────────────────────────────────────

// Aria2Downloader downloads a single direct-media source through aria2c's
// JSON-RPC interface. It starts an aria2c subprocess per download, provides
// structured progress via DownloadProgress, and handles ctx cancellation.
type Aria2Downloader struct{}

// Download performs the aria2c RPC-based download for the given source.
// tempDir is where aria2c downloads fragments and control files. Once
// complete, the media file is relocated to outputDir/baseTitle.ext.
func (d *Aria2Downloader) Download(
	ctx context.Context,
	source provider.MediaSource,
	outputDir, tempDir, baseTitle string,
	progress func(DownloadProgress),
) error {
	// Aria2 JSON-RPC uses POST for non-idempotent commands. Keep a bounded
	// client local to this download so retries cannot duplicate submissions.
	rpcClient := &http.Client{Timeout: 15 * time.Second}
	port, err := findFreePort()
	if err != nil {
		return fmt.Errorf("aria2: find port: %w", err)
	}

	secret, err := generateRPCSecret()
	if err != nil {
		return fmt.Errorf("aria2: generate secret: %w", err)
	}

	aria2Args := []string{
		"--enable-rpc",
		"--rpc-listen-port", strconv.Itoa(port),
		"--rpc-secret", secret,
		"--rpc-listen-all=false",
		"-x16", "-s16", "-k1M",
		"--file-allocation=none",
		"--console-log-level=warn",
		// Frequent control-file checkpoints so a pause/cancel loses at
		// most ~15s of progress; the .aria2 file is the resume anchor.
		"--auto-save-interval=15",
		"--max-tries=8",
		"--retry-wait=2",
	}
	ariaLog.Debug("daemon starting", "port", port)

	cmd := exec.CommandContext(ctx, "aria2c", aria2Args...)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("aria2: start daemon: %w", err)
	}

	// Best-effort cleanup on return: kill the subprocess.
	killDaemon := func() {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	defer killDaemon()

	// Wait until RPC is ready (up to 5 seconds).
	if err := waitForAria2RPC(ctx, rpcClient, port, secret, 5*time.Second); err != nil {
		return fmt.Errorf("aria2: rpc not ready: %w", err)
	}

	headers := sourceHeaders(source)

	// Build aria2c options. A present .aria2 control file makes the re-added
	// URI resume from its checkpoint; --continue covers partial files whose
	// control file was lost (server must support range requests).
	aria2Opts := map[string]any{
		"max-connection-per-server": "16",
		"split":                     "16",
		"min-split-size":            "1M",
		"allow-overwrite":           "true",
		"auto-file-renaming":        "false",
		"continue":                  "true",
	}
	ext := "." + strings.TrimPrefix(strings.ToLower(strings.TrimSpace(source.Type)), ".")
	if _, ok := knownMediaExts[ext]; !ok {
		ext = ".mp4"
	}
	aria2Opts["out"] = baseTitle + ext
	stageDir := tempDir
	if stageDir == "" {
		stageDir = outputDir
	}
	if stageDir != "" {
		aria2Opts["dir"] = stageDir
	}
	if len(headers) > 0 {
		aria2Opts["header"] = headers
	}
	gid, err := aria2AddUri(ctx, rpcClient, port, secret, source.URL, aria2Opts)
	if err != nil {
		return fmt.Errorf("aria2: addUri: %w", err)
	}

	ariaLog.Debug("download queued", "gid", gid, "url", source.URL)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_ = aria2ForceRemove(cleanupCtx, rpcClient, port, secret, gid)
			cleanupCancel()
			// Read stderr for logging before killing.
			daemonStderr := stderrBuf.String()
			if daemonStderr != "" {
				ariaLog.Debug("daemon stderr on cancel", "stderr", daemonStderr)
			}
			return ctx.Err()

		case <-ticker.C:
			status, err := aria2TellStatus(ctx, rpcClient, port, secret, gid)
			if err != nil {
				return fmt.Errorf("aria2: tellStatus: %w", err)
			}

			switch status.Status {
			case "complete":
				_ = aria2RemoveDownloadResult(ctx, rpcClient, port, secret, gid)

				// aria2c may produce a file whose name we don't control.
				// Rename it to the canonical baseTitle + extension.
				if err := renameOutputFile(status, outputDir, baseTitle); err != nil {
					return fmt.Errorf("aria2: finalize output file: %w", err)
				}
				if tempDir != "" && tempDir != outputDir {
					_ = os.Remove(tempDir)
				}
				if progress != nil {
					total, _ := strconv.ParseInt(status.TotalLength, 10, 64)
					progress(DownloadProgress{
						Percent:    1.0,
						TotalSize:  formatFileSize(total),
						Downloaded: formatFileSize(total),
					})
				}
				return nil

			case "error":
				return fmt.Errorf("aria2: download error: %s", status.ErrorMessage)

			case "removed":
				return fmt.Errorf("aria2: download was removed")

			default: // "active", "waiting"
				if progress != nil {
					p := computeAria2Progress(status)
					progress(p)
				}
			}
		}
	}
}

// ── JSON-RPC client helpers ──────────────────────────────────────────────────
func aria2RPC(ctx context.Context, client *http.Client, port int, req *aria2RPCRequest) (json.RawMessage, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("rpc marshal: %w", err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", port)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rpc request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("rpc http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rpc read: %w", err)
	}

	var rpcResp aria2RPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("rpc decode: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("aria2 rpc error (%d): %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

func aria2AddUri(ctx context.Context, client *http.Client, port int, secret, uri string, opts map[string]any) (string, error) {
	params := []any{
		"token:" + secret,
		[]string{uri},
		opts,
	}

	req := &aria2RPCRequest{
		JSONRPC: "2.0",
		ID:      "add",
		Method:  "aria2.addUri",
		Params:  params,
	}

	result, err := aria2RPC(ctx, client, port, req)
	if err != nil {
		return "", err
	}

	var gid string
	if err := json.Unmarshal(result, &gid); err != nil {
		return "", fmt.Errorf("addUri decode gid: %w", err)
	}
	return gid, nil
}

func aria2TellStatus(ctx context.Context, client *http.Client, port int, secret, gid string) (*aria2TellStatusResult, error) {
	req := &aria2RPCRequest{
		JSONRPC: "2.0",
		ID:      "status",
		Method:  "aria2.tellStatus",
		Params:  []any{"token:" + secret, gid},
	}

	result, err := aria2RPC(ctx, client, port, req)
	if err != nil {
		return nil, err
	}

	var status aria2TellStatusResult
	if err := json.Unmarshal(result, &status); err != nil {
		return nil, fmt.Errorf("tellStatus decode: %w", err)
	}
	return &status, nil
}

func aria2ForceRemove(ctx context.Context, client *http.Client, port int, secret, gid string) error {
	req := &aria2RPCRequest{
		JSONRPC: "2.0",
		ID:      "remove",
		Method:  "aria2.forceRemove",
		Params:  []any{"token:" + secret, gid},
	}
	_, err := aria2RPC(ctx, client, port, req)
	return err
}

func aria2RemoveDownloadResult(ctx context.Context, client *http.Client, port int, secret, gid string) error {
	req := &aria2RPCRequest{
		JSONRPC: "2.0",
		ID:      "rmresult",
		Method:  "aria2.removeDownloadResult",
		Params:  []any{"token:" + secret, gid},
	}
	_, err := aria2RPC(ctx, client, port, req)
	return err
}

func aria2GetVersion(ctx context.Context, client *http.Client, port int, secret string) error {
	req := &aria2RPCRequest{
		JSONRPC: "2.0",
		ID:      "ver",
		Method:  "aria2.getVersion",
		Params:  []any{"token:" + secret},
	}
	_, err := aria2RPC(ctx, client, port, req)
	return err
}

// ── Daemon lifecycle ─────────────────────────────────────────────────────────
func findFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("listen: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func generateRPCSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// waitForAria2RPC polls aria2.getVersion until it responds, context cancels, or timeout elapses.
func waitForAria2RPC(ctx context.Context, client *http.Client, port int, secret string, timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		if err := aria2GetVersion(ctx, client, port, secret); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout after %v", timeout)
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// ── Progress computation ─────────────────────────────────────────────────────
func computeAria2Progress(status *aria2TellStatusResult) DownloadProgress {
	total, _ := strconv.ParseInt(status.TotalLength, 10, 64)
	completed, _ := strconv.ParseInt(status.CompletedLength, 10, 64)
	speed, _ := strconv.ParseInt(status.DownloadSpeed, 10, 64)

	var pct float64
	var totalSize, downloaded, speedStr, etaStr string

	if total > 0 {
		pct = float64(completed) / float64(total)
	}

	totalSize = formatFileSize(total)
	downloaded = formatFileSize(completed)

	if speed > 0 {
		// Use the same format as formatFileSize, just append "/s".
		speedStr = formatFileSize(speed) + "/s"

		if total > 0 && completed < total {
			remaining := total - completed
			etaSecs := remaining / speed
			if etaSecs < 3600 {
				etaStr = fmt.Sprintf("%02d:%02d", etaSecs/60, etaSecs%60)
			} else {
				etaStr = fmt.Sprintf("%d:%02d:%02d", etaSecs/3600, (etaSecs%3600)/60, etaSecs%60)
			}
		}
	}

	return DownloadProgress{
		Percent:    pct,
		TotalSize:  totalSize,
		Speed:      speedStr,
		Downloaded: downloaded,
		ETA:        etaStr,
	}
}

// ── Output file post-processing ──────────────────────────────────────────────

// renameOutputFile moves the file produced by aria2c to the canonical
// outputDir/baseTitle.ext path if it differs.
func renameOutputFile(status *aria2TellStatusResult, outputDir, baseTitle string) error {
	if len(status.Files) == 0 || status.Files[0].Path == "" {
		return fmt.Errorf("no output file reported by aria2")
	}
	actualPath := status.Files[0].Path
	actualExt := filepath.Ext(actualPath)
	if idx := strings.IndexAny(actualExt, "?#"); idx >= 0 {
		actualExt = actualExt[:idx]
	}
	actualExt = strings.ToLower(actualExt)
	if _, ok := knownMediaExts[actualExt]; !ok {
		actualExt = ".mp4"
	}
	actualPath = filepath.Clean(actualPath)
	desiredPath := filepath.Clean(filepath.Join(outputDir, baseTitle+actualExt))

	if actualPath == desiredPath {
		return nil
	}
	if aInfo, err1 := os.Stat(actualPath); err1 == nil {
		if dInfo, err2 := os.Stat(desiredPath); err2 == nil && os.SameFile(aInfo, dInfo) {
			return nil
		}
	}

	_ = os.MkdirAll(outputDir, 0o755)

	if err := os.Rename(actualPath, desiredPath); err != nil {
		if _, statErr := os.Stat(desiredPath); statErr == nil {
			_ = os.Remove(desiredPath)
			if renameErr := os.Rename(actualPath, desiredPath); renameErr == nil {
				ariaLog.Debug("renamed (overwrote existing)", "from", actualPath, "to", desiredPath)
				return nil
			}
		}
		if copyErr := moveFileCrossFS(actualPath, desiredPath); copyErr != nil {
			ariaLog.Warn("rename failed", "from", actualPath, "to", desiredPath, "err", err, "copyErr", copyErr)
			return fmt.Errorf("move %s to %s: %w", actualPath, desiredPath, copyErr)
		}
	}
	ariaLog.Debug("renamed", "from", actualPath, "to", desiredPath)
	return nil
}

func moveFileCrossFS(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("sync %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("close %s: %w", dst, err)
	}
	_ = in.Close()
	_ = os.Remove(src)
	return nil
}
