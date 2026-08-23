package app

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/selfupdate"
)

// updateLog scopes self-update logging.
var updateLog = logging.With("component", "updater")

// Update self-updates the binary in place: fetch the latest GitHub release
// for this platform, download the asset with progress, swap atomically, and
// back up the old binary. Intended for `kari --update`.
func Update() error {
	return update(false)
}

func update(quiet bool) error {
	if !quiet {
		fmt.Printf("Checking for updates...\n")
	}
	latest, err := selfupdate.GetLatestRelease()
	if err != nil {
		if !quiet {
			return fmt.Errorf("failed to fetch latest release: %w", err)
		}
		return nil
	}

	latestVersion := latest.Version()
	currentVersion := strings.TrimSuffix(Version, "-dirty")
	if latestVersion == currentVersion {
		if !quiet {
			fmt.Printf("Kari is already up to date (version %s).\n", Version)
		}
		return nil
	}

	if !quiet {
		fmt.Printf("Updating Kari from %s to %s...\n", Version, latestVersion)
	} else {
		updateLog.Info("new version found", "latest", latestVersion, "current", Version)
	}

	assetName := fmt.Sprintf("kari-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		assetName += ".exe"
	}

	var downloadURL string
	for _, asset := range latest.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}

	if downloadURL == "" {
		if !quiet {
			return fmt.Errorf("could not find binary for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, latest.TagName)
		}
		return nil
	}

	if err := applyUpdate(downloadURL); err != nil {
		if !quiet {
			return fmt.Errorf("failed to apply update: %w", err)
		}
		return nil
	}

	if !quiet {
		fmt.Printf("Successfully updated to %s! Please restart Kari.\n", latest.TagName)
	} else {
		updateLog.Info("update downloaded; active on next restart", "version", latest.TagName)
	}
	return nil
}

func applyUpdate(url string) error {
	// Shared client: gets Android DNS fallback + retries on the binary download too.
	client := httpclient.NewWithTimeout(120 * time.Second)
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download binary: %d", resp.StatusCode)
	}

	total, _ := strconv.Atoi(resp.Header.Get("Content-Length"))

	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	tmpPath := exePath + ".tmp"
	if runtime.GOOS == "windows" {
		tmpPath += ".exe"
	}

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}

	var written atomic.Int64
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				pct := 0
				if total > 0 {
					pct = int(written.Load() * 100 / int64(total))
				}
				fmt.Printf("\r  Downloading... %d%%", pct)
			case <-done:
				return
			}
		}
	}()

	_, err = io.Copy(io.MultiWriter(f, &progressWriter{written: &written}), resp.Body)
	close(done)
	fmt.Print("\r  Downloading... done\n")

	if err != nil {
		_ = f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		updateLog.Warn("temp file close failed", "err", err)
	}

	oldPath := exePath + ".old"
	if err := os.Rename(exePath, oldPath); err != nil {
		updateLog.Error("current binary rename failed", "err", err)
		_ = os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		if e := os.Rename(oldPath, exePath); e != nil {
			updateLog.Error("rollback rename failed", "err", e)
		}
		_ = os.Remove(tmpPath)
		return err
	}

	err = os.Remove(oldPath)
	if err != nil {
		updateLog.Warn("old binary removal deferred to next run", "err", err)
	}

	return nil
}

type progressWriter struct {
	written *atomic.Int64
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written.Add(int64(n))
	return n, nil
}
