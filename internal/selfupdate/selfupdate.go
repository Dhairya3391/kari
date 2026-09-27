// Package selfupdate fetches release metadata from GitHub. It's split out
// from internal/app so the TUI can check for updates (to show a "new
// version available" notice) without importing internal/app, which itself
// imports the TUI.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"kari/internal/httpclient"
)

const (
	repoOwner = "Dhairya3391"
	repoName  = "kari"
)

// Release describes one GitHub release asset relevant to self-update.
type Release struct {
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish"`
	Assets          []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`

	commitSHA string
}

// Version returns the bare version without "v" prefix or "-dirty" suffix.
func (r *Release) Version() string {
	v := strings.TrimPrefix(r.TagName, "v")
	v = strings.TrimPrefix(v, "V")
	return strings.TrimSuffix(v, "-dirty")
}

// CommitSHA resolves the commit SHA for this release. If TargetCommitish is
// already a 40-character hex SHA, it is returned directly. Otherwise, it queries
// GitHub's commits endpoint for the release's TagName.
func (r *Release) CommitSHA() (string, error) {
	if r.commitSHA != "" {
		return r.commitSHA, nil
	}
	if isHexSHA(r.TargetCommitish) {
		r.commitSHA = r.TargetCommitish
		return r.commitSHA, nil
	}
	if r.TagName == "" {
		return "", fmt.Errorf("release has no tag name")
	}

	client := httpclient.New()
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", repoOwner, repoName, r.TagName)
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch release commit: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api commit status %d", resp.StatusCode)
	}

	var commitResp struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&commitResp); err != nil {
		return "", fmt.Errorf("decode commit response: %w", err)
	}
	r.commitSHA = commitResp.SHA
	return r.commitSHA, nil
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
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}

	return &release, nil
}

type semver struct {
	major      int
	minor      int
	patch      int
	prerelease string
	valid      bool
	raw        string
}

func parseSemver(s string) semver {
	clean := strings.TrimSpace(s)
	clean = strings.TrimPrefix(clean, "v")
	clean = strings.TrimPrefix(clean, "V")
	clean = strings.TrimSuffix(clean, "-dirty")

	raw := clean
	// Remove build metadata (+...)
	if idx := strings.IndexByte(clean, '+'); idx != -1 {
		clean = clean[:idx]
	}

	var prerelease string
	if idx := strings.IndexByte(clean, '-'); idx != -1 {
		prerelease = clean[idx+1:]
		clean = clean[:idx]
	}

	parts := strings.Split(clean, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return semver{raw: raw, valid: false}
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return semver{raw: raw, valid: false}
	}

	minor := 0
	if len(parts) > 1 {
		minor, err = strconv.Atoi(parts[1])
		if err != nil || minor < 0 {
			return semver{raw: raw, valid: false}
		}
	}

	patch := 0
	if len(parts) > 2 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil || patch < 0 {
			return semver{raw: raw, valid: false}
		}
	}

	return semver{
		major:      major,
		minor:      minor,
		patch:      patch,
		prerelease: prerelease,
		valid:      true,
		raw:        raw,
	}
}

func comparePrereleases(p1, p2 string) int {
	parts1 := strings.Split(p1, ".")
	parts2 := strings.Split(p2, ".")
	minLen := min(len(parts1), len(parts2))

	for i := range minLen {
		num1, err1 := strconv.Atoi(parts1[i])
		num2, err2 := strconv.Atoi(parts2[i])

		if err1 == nil && err2 == nil {
			if num1 != num2 {
				if num1 < num2 {
					return -1
				}
				return 1
			}
		} else if err1 == nil && err2 != nil {
			return -1
		} else if err1 != nil && err2 == nil {
			return 1
		} else {
			if parts1[i] != parts2[i] {
				if parts1[i] < parts2[i] {
					return -1
				}
				return 1
			}
		}
	}

	if len(parts1) < len(parts2) {
		return -1
	}
	if len(parts1) > len(parts2) {
		return 1
	}
	return 0
}

// CompareVersions compares two version strings (e.g. "v5.0.0" and "5.0.1").
// Returns:
//
//	-1 if v1 < v2 (v2 is newer)
//	 0 if v1 == v2
//	 1 if v1 > v2 (v1 is newer)
func CompareVersions(v1, v2 string) int {
	sv1 := parseSemver(v1)
	sv2 := parseSemver(v2)

	if !sv1.valid || !sv2.valid {
		if sv1.raw == sv2.raw {
			return 0
		}
		if !sv1.valid && sv2.valid {
			return -1
		}
		if sv1.valid && !sv2.valid {
			return 1
		}
		return strings.Compare(sv1.raw, sv2.raw)
	}

	if sv1.major != sv2.major {
		if sv1.major < sv2.major {
			return -1
		}
		return 1
	}

	if sv1.minor != sv2.minor {
		if sv1.minor < sv2.minor {
			return -1
		}
		return 1
	}

	if sv1.patch != sv2.patch {
		if sv1.patch < sv2.patch {
			return -1
		}
		return 1
	}

	if sv1.prerelease == "" && sv2.prerelease != "" {
		return 1
	}
	if sv1.prerelease != "" && sv2.prerelease == "" {
		return -1
	}
	if sv1.prerelease == sv2.prerelease {
		return 0
	}
	return comparePrereleases(sv1.prerelease, sv2.prerelease)
}

// CommitsMatch reports whether two commit SHAs match. It supports matching
// git short SHAs (e.g. 7 characters) against full 40-character SHAs.
func CommitsMatch(a, b string) bool {
	a = strings.TrimSpace(strings.ToLower(a))
	b = strings.TrimSpace(strings.ToLower(b))
	if a == "" || b == "" {
		return false
	}
	if a == "dev" || b == "dev" {
		return a == b
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// ShouldUpdate reports whether Kari should update based on current and latest
// version and commit metadata. It returns true if the upstream version is newer
// (semver), or if the version is the same but the commit hash is different.
func ShouldUpdate(currentVersion, currentCommit, latestVersion, latestCommit string) bool {
	cmp := CompareVersions(currentVersion, latestVersion)
	if cmp < 0 {
		return true
	}
	if cmp > 0 {
		return false
	}
	if currentCommit != "" && latestCommit != "" {
		return !CommitsMatch(currentCommit, latestCommit)
	}
	return false
}

// IsNewer reports whether latest is strictly newer than current.
func IsNewer(current, latest string) bool {
	return CompareVersions(current, latest) < 0
}

// ShortSHA returns a 7-character short commit hash if the input is longer.
func ShortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func isHexSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := range s {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
