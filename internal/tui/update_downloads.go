package tui

import (
	"fmt"
	"kari/internal/downloader"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *modelImpl) updateDownloads(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		data := m.collectDownloadsData(m.width, m.height, m.currentAccent())
		totalItems := len(data.Active) + len(data.Done)

		switch keyMsg.String() {
		case "up", "k":
			if m.downloadsIndex > 0 {
				m.downloadsIndex--
			}
			return m, nil
		case "down", "j":
			if totalItems > 0 && m.downloadsIndex < totalItems-1 {
				m.downloadsIndex++
			}
			return m, nil
		case "g", "home":
			m.downloadsIndex = 0
			return m, nil
		case "G", "end":
			if totalItems > 0 {
				m.downloadsIndex = totalItems - 1
			}
			return m, nil
		case "pgup", "ctrl+u":
			step := m.pageStep()
			if m.downloadsIndex > step {
				m.downloadsIndex -= step
			} else {
				m.downloadsIndex = 0
			}
			return m, nil
		case "pgdn", "pgdown", "ctrl+d":
			step := m.pageStep()
			if totalItems > 0 {
				m.downloadsIndex = min(totalItems-1, m.downloadsIndex+step)
			}
			return m, nil
		case "enter":
			if m.downloadsIndex < len(data.Active) {
				if m.downloadService != nil {
					openFolder(m.downloadService.OutputDir())
				}
				return m, nil
			}
			doneIdx := m.downloadsIndex - len(data.Active)
			if doneIdx >= 0 && doneIdx < len(data.Done) {
				item := data.Done[doneIdx]
				if item.Path != "" && m.players != nil {
					opID := m.newOpID()
					m.playOpID = opID
					toastCmd := m.setToast("launching playback...", ToastInfo)
					return m, tea.Batch(toastCmd, m.playDownloadedFileCmd(opID, item.Path, item.Title))
				}
				if m.downloadService != nil {
					openFolder(m.downloadService.OutputDir())
				}
			}
			return m, nil
		case "o", "O":
			if m.downloadService != nil {
				openFolder(m.downloadService.OutputDir())
			}
			return m, nil
		case "p":
			if !m.downloadPaused {
				// Pause: cancel the running context but keep every partial
				// file (.aria2 control file / .part fragments) on disk —
				// they are the resume checkpoints — and keep the batch
				// state so the row stays visible with its progress.
				if m.batchInProgress || m.cancelDownload != nil || m.downloadOpID != 0 {
					m.downloadPaused = true
					if m.batchCancel != nil {
						m.batchCancel()
						m.batchCancel = nil
					}
					if m.cancelDownload != nil {
						m.cancelDownload()
						m.cancelDownload = nil
					}
					return m, m.setToast("download paused — press p to resume", ToastInfo)
				}
			} else {
				if m.pendingDownload != nil && m.downloadService != nil && m.mediaService != nil {
					job := *m.pendingDownload
					m.downloadPaused = false
					m.activeDownloads = nil
					m.downloadOpID = m.newOpID()
					if len(job.Episodes) > 0 {
						remIdx := max(0, job.BatchCurrent-1)
						if remIdx < len(job.Episodes) {
							m.batchInProgress = true
							m.batchCurrent = job.BatchCurrent
							m.batchTotal = len(job.Episodes)
							m.batchEpisodes = job.Episodes
							m.batchSeries = job.Series
							m.batchMode = job.Mode
							toastCmd := m.setToast("download resumed", ToastSuccess)
							cmd := m.batchDownloadCmd(m.downloadOpID, job.Episodes[remIdx:], job.Series, job.Mode, true, max(1, job.BatchCurrent), len(job.Episodes))
							return m, tea.Batch(toastCmd, cmd)
						}
					}
					toastCmd := m.setToast("resolving interrupted download", ToastInfo)
					return m, tea.Batch(toastCmd, m.resumePendingDownloadCmd(m.downloadOpID, job))
				}
				// Resume: re-run the same sources. aria2c picks up the
				// .aria2 checkpoint, yt-dlp continues from .part files.
				m.downloadPaused = false
				if m.batchInProgress && len(m.batchEpisodes) > 0 {
					opID := m.newOpID()
					m.downloadOpID = opID
					remIdx := max(0, m.batchCurrent-1)
					if remIdx < len(m.batchEpisodes) {
						rem := m.batchEpisodes[remIdx:]
						toastCmd := m.setToast("download resumed", ToastSuccess)
						cmd := m.batchDownloadCmd(opID, rem, m.batchSeries, m.batchMode, true, max(1, m.batchCurrent), len(m.batchEpisodes))
						return m, tea.Batch(toastCmd, cmd)
					}
				} else if m.singleResolved != nil {
					opID := m.newOpID()
					m.downloadOpID = opID
					toastCmd := m.setToast("download resumed", ToastSuccess)
					return m, tea.Batch(toastCmd, m.downloadCmd(opID, *m.singleResolved))
				}
				return m, m.setToast("download resumed", ToastSuccess)
			}
		case "x", "delete", "backspace":
			// Double-press confirmation for anything destructive, matching
			// the x-stop and q-quit conventions elsewhere.
			if !m.confirmStop {
				m.confirmStop = true
				prompt := "Press x again to cancel (partials are deleted)"
				if m.downloadsIndex >= len(data.Active) {
					prompt = "Press x again to delete file from disk"
				}
				m.setStatus(statusWarn, prompt)
				return m, tea.Tick(time.Second*3, func(t time.Time) tea.Msg {
					return resetConfirmStopMsg{}
				})
			}
			m.confirmStop = false
			if len(data.Active) > 0 && m.downloadsIndex < len(data.Active) {
				item := data.Active[m.downloadsIndex]
				if item.Queued {
					// A queued episode has no files yet: just drop it.
					targetIdx := max(0, m.batchCurrent-1) + m.downloadsIndex
					if targetIdx >= 0 && targetIdx < len(m.batchEpisodes) {
						m.batchEpisodes = append(m.batchEpisodes[:targetIdx], m.batchEpisodes[targetIdx+1:]...)
						m.batchTotal = len(m.batchEpisodes)
					}
					return m, m.setToast(fmt.Sprintf("cancelled %s", item.Title), ToastInfo)
				}
				if m.batchInProgress && len(m.batchEpisodes) > 0 {
					// Skip the active episode: cancel its context, remove its
					// partial files (a cancelled download is not resumed),
					// then continue with the rest of the queue.
					if m.batchCancel != nil {
						m.batchCancel()
						m.batchCancel = nil
					}
					if m.batchActiveDir != "" {
						if m.downloadService != nil {
							m.downloadService.CleanupPartial(m.batchActiveDir, m.batchActiveTitle)
						}
					}
					curIdx := max(0, m.batchCurrent-1)
					toastCmd := m.setToast(fmt.Sprintf("skipped %s", item.Title), ToastInfo)
					if curIdx < len(m.batchEpisodes) {
						m.batchEpisodes = append(m.batchEpisodes[:curIdx], m.batchEpisodes[curIdx+1:]...)
						m.batchTotal = len(m.batchEpisodes)
					}
					if curIdx < len(m.batchEpisodes) {
						opID := m.newOpID()
						m.downloadOpID = opID
						m.batchInProgress = true
						rem := m.batchEpisodes[curIdx:]
						cmd := m.batchDownloadCmd(opID, rem, m.batchSeries, m.batchMode, true, curIdx+1, len(m.batchEpisodes))
						return m, tea.Batch(toastCmd, cmd)
					}
					m.batchInProgress = false
					return m, toastCmd
				}
				// Single download cancel: stop it and delete its partials.
				if m.cancelDownload != nil {
					m.cancelDownload()
					m.cancelDownload = nil
				}
				if m.downloadOutputDir != "" {
					if m.downloadService != nil {
						m.downloadService.CleanupPartial(m.downloadOutputDir, m.downloadTitle)
					}
				}
				m.downloadOpID = 0
				m.downloadProgress = 0
				m.downloadSpeed = ""
				m.downloadETA = ""
				m.singleResolved = nil
				m.pendingDownload = nil
				m.downloadPaused = false
				if m.downloadService != nil {
					_ = m.downloadService.ClearPendingJob()
				}
				return m, m.setToast("download cancelled", ToastInfo)
			} else if m.downloadsIndex >= len(data.Active) && len(data.Done) > 0 {
				doneIdx := m.downloadsIndex - len(data.Active)
				if doneIdx >= 0 && doneIdx < len(data.Done) {
					item := data.Done[doneIdx]
					if item.Path != "" {
						_ = os.Remove(item.Path)
						dir := filepath.Dir(item.Path)
						baseTitle, _ := downloader.SplitTitleExt(filepath.Base(item.Path))
						if entries, err := os.ReadDir(dir); err == nil {
							for _, e := range entries {
								if strings.HasPrefix(e.Name(), baseTitle+".") {
									ext := strings.ToLower(filepath.Ext(e.Name()))
									if ext == ".srt" || ext == ".ass" || ext == ".vtt" {
										_ = os.Remove(filepath.Join(dir, e.Name()))
									}
								}
							}
						}
						if m.downloadService != nil {
							m.downloadService.RemoveEmptyDirs(dir)
						}
					}
					if m.dismissedDoneDownloads == nil {
						m.dismissedDoneDownloads = make(map[string]bool)
					}
					m.dismissedDoneDownloads[item.Title] = true
					for i, d := range m.doneDownloads {
						if d.Title == item.Title {
							m.doneDownloads = append(m.doneDownloads[:i], m.doneDownloads[i+1:]...)
							break
						}
					}
					totalRemaining := len(data.Active) + len(data.Done) - 1
					if m.downloadsIndex >= totalRemaining && m.downloadsIndex > 0 {
						m.downloadsIndex--
					}
					return m, m.setToast("file deleted from disk", ToastInfo)
				}
			}
			return m, nil
		case "X":
			if !m.confirmStop {
				m.confirmStop = true
				m.setStatus(statusWarn, "Cancel ALL downloads? Press X again (partials are deleted)")
				return m, tea.Tick(time.Second*3, func(t time.Time) tea.Msg {
					return resetConfirmStopMsg{}
				})
			}
			m.confirmStop = false
			if m.batchInProgress && m.batchCancel != nil {
				m.batchCancel()
				m.batchCancel = nil
			}
			if m.cancelDownload != nil {
				m.cancelDownload()
				m.cancelDownload = nil
			}
			// X is explicit cancel-all: partials of the active episode go too.
			if m.batchActiveDir != "" {
				m.downloadService.CleanupPartial(m.batchActiveDir, m.batchActiveTitle)
			} else if m.downloadOutputDir != "" {
				m.downloadService.CleanupPartial(m.downloadOutputDir, m.downloadTitle)
			}
			m.batchInProgress = false
			m.downloadPaused = false
			m.batchEpisodes = nil
			m.batchCurrent = 0
			m.batchTotal = 0
			m.batchEpisodeProgress = 0
			m.batchActiveDir = ""
			m.batchActiveTitle = ""
			m.downloadOpID = 0
			m.downloadProgress = 0
			m.downloadSpeed = ""
			m.downloadETA = ""
			m.singleResolved = nil
			m.pendingDownload = nil
			if m.downloadService != nil {
				_ = m.downloadService.ClearPendingJob()
			}
			return m, m.setToast("all downloads cancelled", ToastInfo)
		case "esc":
			m.goBackOne()
			return m, nil
		}
	}
	return m, nil
}

func openFolder(dir string) {
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	_ = cmd.Start()
}

// pageStep is one full screen of episode rows.
func (m *modelImpl) pageStep() int {
	step := m.height - 8
	if step < 5 {
		step = 5
	}
	return step
}

// pageEpisodes moves the cursor by delta rows, clamped to the list.
func (m *modelImpl) pageEpisodes(delta int) {
	eps, _ := m.currentSeasonEpisodes()
	if len(eps) == 0 {
		m.seasonEpisodeIndex = 0
		return
	}
	next := m.seasonEpisodeIndex + delta
	if next < 0 {
		next = 0
	}
	if next >= len(eps) {
		next = len(eps) - 1
	}
	m.seasonEpisodeIndex = next
}

func (m *modelImpl) selectAllEpisodes() {
	m.selectedEpisodes = make(map[int]struct{})
	for i := range m.episodeResults {
		m.selectedEpisodes[i] = struct{}{}
	}
	m.refreshEpisodeList()
}

func (m *modelImpl) refreshEpisodeList() {
	if len(m.episodeResults) == 0 {
		return
	}
	seriesTitle, mediaType := "", ""
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
		mediaType = m.selectedSeries.MediaType
	}
	m.episodeList.SetItems(episodesToItems(m.episodeResults, m.historyStore, seriesTitle, m.appMode, mediaType, m.selectedEpisodes))
}
