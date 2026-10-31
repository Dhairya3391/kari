package tui

import (
	"context"
	"errors"
	"fmt"
	"kari/internal/downloader"
	"kari/internal/logging"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *modelImpl) onDownloadProgress(msg downloadProgressMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, m.downloadSubscription()
	}
	m.downloadProgress = msg.progress * 100
	m.downloadTotalSize = msg.totalSize
	m.downloadSpeed = msg.speed
	m.downloadDownloaded = msg.downloaded
	m.downloadETA = msg.eta
	return m, m.downloadSubscription()
}

func (m *modelImpl) onDownloadDone(msg downloadDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, m.downloadSubscription()
	}
	// A user-initiated pause cancels the context. Keep the progress readout
	// and partial files so resume continues from the checkpoint.
	if errors.Is(msg.err, context.Canceled) {
		logging.Info("download paused", "opID", msg.opID)
		m.cancelDownload = nil
		m.downloadOpID = 0
		return m, nil
	}
	statusMsg := "download complete"
	if m.downloadTotalSize != "" {
		statusMsg = fmt.Sprintf("downloaded %s", m.downloadTotalSize)
	}

	completedTitle := m.downloadTitle
	completedSize := m.downloadTotalSize
	qualityLabel := downloadQualityLabel(m.effectiveDownloadQuality())
	outputDir := m.downloadOutputDir

	m.downloadProgress = 0
	m.downloadTotalSize = ""
	m.downloadSpeed = ""
	m.downloadDownloaded = ""
	m.downloadETA = ""
	m.cancelDownload = nil
	m.downloadOpID = 0
	m.downloadTitle = ""
	m.downloadOutputDir = ""
	if msg.err != nil {
		logging.Error("download failed", "opID", msg.opID, "err", msg.err)
		errMsg := fmt.Sprintf("Download failed: %v", msg.err)
		if errors.Is(msg.err, exec.ErrNotFound) {
			errMsg = "Download failed: yt-dlp is not installed"
		}
		if m.pendingDownload != nil {
			m.downloadPaused = true
		}
		return m, m.setStatusTimed(statusError, errMsg)
	}

	if completedTitle != "" {
		displayTitle := completedTitle
		if m.downloadService != nil && outputDir != "" {
			rel, err := filepath.Rel(m.downloadService.OutputDir(), outputDir)
			if err == nil {
				parts := strings.Split(filepath.ToSlash(rel), "/")
				if len(parts) > 0 && parts[0] != "." && parts[0] != "" && !strings.Contains(completedTitle, parts[0]) {
					displayTitle = fmt.Sprintf("%s — %s", parts[0], completedTitle)
				}
			}
		} else if m.selectedSeries != nil && m.selectedSeries.Title != "" && !strings.Contains(completedTitle, m.selectedSeries.Title) {
			displayTitle = fmt.Sprintf("%s — %s", m.selectedSeries.Title, completedTitle)
		}

		if m.dismissedDoneDownloads != nil {
			delete(m.dismissedDoneDownloads, displayTitle)
		}

		var filePath string
		if outputDir != "" {
			baseTitle, _ := downloader.SplitTitleExt(completedTitle)
			for _, ext := range []string{".mp4", ".mkv", ".webm", ".m4v", ".mov", ".avi", ".ts"} {
				candidate := filepath.Join(outputDir, baseTitle+ext)
				if _, err := os.Stat(candidate); err == nil {
					filePath = candidate
					break
				}
			}
		}

		m.doneDownloads = append([]DoneDownload{{
			Title:   displayTitle,
			Quality: qualityLabel,
			Size:    completedSize,
			Path:    filePath,
		}}, m.doneDownloads...)
	}
	if m.downloadService != nil {
		if err := m.downloadService.ClearPendingJob(); err != nil {
			tuiLog.Warn("clear pending download failed", "err", err)
		}
	}
	m.pendingDownload = nil
	m.downloadPaused = false
	return m, m.setToast(statusMsg, ToastSuccess)
}

func (m *modelImpl) downloadSubscription() tea.Cmd {
	return func() tea.Msg {
		return <-m.downloadChan
	}
}

func (m *modelImpl) drainDownloadChan() {
	for {
		select {
		case <-m.downloadChan:
		default:
			return
		}
	}
}

func (m *modelImpl) resolveSubscription() tea.Cmd {
	return func() tea.Msg {
		return <-m.resolveChan
	}
}
