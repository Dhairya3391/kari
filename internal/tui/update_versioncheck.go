package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/selfupdate"
)

type updateCheckMsg struct {
	release       *selfupdate.Release
	latestVersion string
	latestCommit  string
	err           error
}

type autoUpdateDoneMsg struct {
	version string
	err     error
}

// checkForUpdateCmd asks GitHub for the latest release in the background.
// If auto-update is enabled, it automatically downloads and applies the release.
// Otherwise, it surfaces a status notice informing the user an update is available.
func (m *modelImpl) checkForUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		release, err := selfupdate.GetLatestRelease()
		if err != nil {
			return updateCheckMsg{err: err}
		}
		var latestCommit string
		if selfupdate.CompareVersions(m.appVersion, release.TagName) == 0 {
			latestCommit, _ = release.CommitSHA()
		}
		return updateCheckMsg{
			release:       release,
			latestVersion: release.TagName,
			latestCommit:  latestCommit,
		}
	}
}

func (m *modelImpl) autoUpdateCmd(release *selfupdate.Release) tea.Cmd {
	return func() tea.Msg {
		err := release.Apply(nil)
		return autoUpdateDoneMsg{
			version: release.TagName,
			err:     err,
		}
	}
}

func (m *modelImpl) onUpdateCheck(msg updateCheckMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		tuiLog.Warn("version check failed", "err", msg.err)
		return m, nil
	}
	if !selfupdate.ShouldUpdate(m.appVersion, m.appCommit, msg.latestVersion, msg.latestCommit) {
		return m, nil
	}
	if m.autoUpdate && msg.release != nil {
		tuiLog.Info("auto-update enabled; downloading in background", "version", msg.latestVersion)
		return m, m.autoUpdateCmd(msg.release)
	}
	// Don't stomp on a status the user is actively reading (an error,
	// a download result, etc.) — this notice can wait for the status
	// line to be free rather than interrupt something more relevant.
	if m.statusText != "" {
		return m, nil
	}
	return m, m.setStatusTimed(statusInfo, fmt.Sprintf("Update available: %s (run `kari -u` to update)", msg.latestVersion))
}

func (m *modelImpl) onAutoUpdateDone(msg autoUpdateDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		tuiLog.Warn("auto-update failed", "err", msg.err)
		return m, nil
	}
	tuiLog.Info("auto-update completed successfully", "version", msg.version)
	return m, m.setStatusTimed(statusSuccess, fmt.Sprintf("Updated to %s — active on next restart", msg.version))
}
