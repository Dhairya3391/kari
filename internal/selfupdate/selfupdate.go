// Package selfupdate fetches release metadata from GitHub. It's split out
// from internal/app so the TUI can check for updates (to show a "new
// version available" notice) without importing internal/app, which itself
// imports the TUI.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"kari/internal/httpclient"
)

const (
	repoOwner = "Dhairya3391"
	repoName  = "kari"
)

// Release describes one GitHub release asset relevant to self-update.
type Release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// Version strips the "v" tag prefix, giving a bare version like "2.4.0".
func (r *Release) Version() string {
	return strings.TrimPrefix(r.TagName, "v")
}

// GetLatestRelease fetches metadata for the newest published release.
func GetLatestRelease() (*Release, error) {
	// Use the shared client: on Termux/Android it swaps in a public DNS
	// resolver (Cloudflare/Google) because the system resolver can be broken
	// (e.g. "lookup api.github.com on [::1]:53: connection refused").
	client := httpclient.New()
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

// IsNewer reports whether latest differs from the running version, current
// (both bare, e.g. "2.4.0" - no "v" prefix or "-dirty" suffix).
func IsNewer(current, latest string) bool {
	return latest != "" && latest != strings.TrimSuffix(current, "-dirty")
}
