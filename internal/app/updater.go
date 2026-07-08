package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"kari/internal/logging"
)

const (
	repoOwner = "Dhairya3391"
	repoName  = "kari"
)

type GitHubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func Update() error {
	return update(false)
}

func update(quiet bool) error {
	if !quiet {
		fmt.Printf("Checking for updates...\n")
	}
	latest, err := getLatestRelease()
	if err != nil {
		if !quiet {
			return fmt.Errorf("failed to fetch latest release: %w", err)
		}
		return nil
	}

	latestVersion := strings.TrimPrefix(latest.TagName, "v")
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
		logging.Infof("Background update: found new version %s (current: %s)", latestVersion, Version)
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
		logging.Infof("Background update: successfully downloaded %s. Will be active on next restart.", latest.TagName)
	}
	return nil
}

func getLatestRelease() (*GitHubRelease, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

func applyUpdate(url string) error {
	client := &http.Client{Timeout: 120 * time.Second}
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

	var written int64
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				pct := 0
				if total > 0 {
					pct = int(written * 100 / int64(total))
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
		logging.Warnf("failed to close temp file: %v", err)
	}

	oldPath := exePath + ".old"
	if err := os.Rename(exePath, oldPath); err != nil {
		logging.Errorf("failed to rename current binary: %v", err)
		return err
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		if e := os.Rename(oldPath, exePath); e != nil {
			logging.Errorf("rollback rename failed: %v", e)
		}
		return err
	}

	err = os.Remove(oldPath)
	if err != nil {
		logging.Warnf("could not remove old binary: %v (it will be removed on next run or manually)", err)
	}

	return nil
}

type progressWriter struct {
	written *int64
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	*pw.written += int64(n)
	return n, nil
}
