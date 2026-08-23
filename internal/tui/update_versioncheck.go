package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/selfupdate"
)

type updateCheckMsg struct {
	latestVersion string
	err           error
}

// checkForUpdateCmd asks GitHub for the latest release in the background so
// a "new version available" notice can be surfaced without auto-updating —
// see https://github.com/Dhairya3391/kari/issues/18. It never blocks
// startup and never applies anything; `kari -u` remains the only way to
// actually update.
func (m *modelImpl) checkForUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		release, err := selfupdate.GetLatestRelease()
		if err != nil {
			return updateCheckMsg{err: err}
		}
		return updateCheckMsg{latestVersion: release.Version()}
	}
}

func (m *modelImpl) onUpdateCheck(msg updateCheckMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		tuiLog.Warn("version check failed", "err", msg.err)
		return m, nil
	}
	if !selfupdate.IsNewer(m.appVersion, msg.latestVersion) {
		return m, nil
	}
	// Don't stomp on a status the user is actively reading (an error,
	// a download result, etc.) — this notice can wait for the status
	// line to be free rather than interrupt something more relevant.
	if m.statusText != "" {
		return m, nil
	}
	return m, m.setStatusTimed(statusInfo, fmt.Sprintf("Update available: v%s (run `kari -u` to update)", msg.latestVersion))
}
