package app

import (
	"fmt"

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

	latestVersion := latest.TagName
	var latestCommit string
	if selfupdate.CompareVersions(Version, latestVersion) == 0 {
		latestCommit, err = latest.CommitSHA()
		if err != nil {
			updateLog.Debug("failed to fetch release commit sha", "err", err)
		}
	}

	if !selfupdate.ShouldUpdate(Version, Commit, latestVersion, latestCommit) {
		if !quiet {
			fmt.Printf("Kari is already up to date (version %s).\n", Version)
		}
		return nil
	}

	if !quiet {
		if selfupdate.CompareVersions(Version, latestVersion) == 0 && Commit != "" && latestCommit != "" {
			fmt.Printf("Updating Kari from %s (%s) to %s (%s)...\n", Version, selfupdate.ShortSHA(Commit), latestVersion, selfupdate.ShortSHA(latestCommit))
		} else {
			fmt.Printf("Updating Kari from %s to %s...\n", Version, latestVersion)
		}
	} else {
		updateLog.Info("new version found", "latest", latestVersion, "current", Version)
	}

	var onProgress selfupdate.ProgressFunc
	if !quiet {
		onProgress = func(pct int) {
			fmt.Printf("\r  Downloading... %d%%", pct)
		}
	}

	if err := latest.Apply(onProgress); err != nil {
		if !quiet {
			return fmt.Errorf("failed to apply update: %w", err)
		}
		return nil
	}

	if !quiet {
		fmt.Print("\r  Downloading... done\n")
		fmt.Printf("Successfully updated to %s! Please restart Kari.\n", latest.TagName)
	} else {
		updateLog.Info("update downloaded; active on next restart", "version", latest.TagName)
	}
	return nil
}
