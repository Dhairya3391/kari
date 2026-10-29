package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/downloader"
	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/service"
)

func (m *modelImpl) updateActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewSearch:
		return m.updateSearch(msg)
	case viewEpisodes:
		return m.updateEpisodes(msg)
	case viewChapters:
		return m.updateChapters(msg)
	case viewReader:
		return m.updateReader(msg)
	case viewPreview:
		return m.updatePreview(msg)
	case viewHistory:
		return m.updateHistory(msg)
	case viewSettings:
		return m.updateSettings(msg)
	case viewDownloads:
		return m.updateDownloads(msg)
	default:
		return m, nil
	}
}
func (m *modelImpl) updateEpisodes(msg tea.Msg) (tea.Model, tea.Cmd) {
	seasonEps, origIndices := m.currentSeasonEpisodes()

	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		// While a / filter is being typed, rune keys extend the query;
		// movement still works; enter applies, esc cancels (globally).
		if m.episodeFiltering {
			switch keyMsg.String() {
			case "enter":
				m.episodeFiltering = false
				m.clampEpisodeIndex()
				return m, nil
			case "backspace":
				if len(m.episodeFilter) > 0 {
					m.episodeFilter = m.episodeFilter[:len(m.episodeFilter)-1]
					m.clampEpisodeIndex()
				}
				return m, nil
			case "up", "k":
				if m.seasonEpisodeIndex > 0 {
					m.seasonEpisodeIndex--
				}
				return m, nil
			case "down", "j":
				if m.seasonEpisodeIndex < len(seasonEps)-1 {
					m.seasonEpisodeIndex++
				}
				return m, nil
			case "pgup":
				m.pageEpisodes(-m.pageStep())
				return m, nil
			case "pgdown":
				m.pageEpisodes(m.pageStep())
				return m, nil
			}
			if len(keyMsg.String()) == 1 {
				m.episodeFilter += keyMsg.String()
				m.clampEpisodeIndex()
				return m, nil
			}
			return m, nil
		}
		if m.selectMode {
			switch {
			case keyMsg.String() == " " || keyMsg.String() == "space" || key.Matches(keyMsg, m.keys.ToggleSelect):
				if len(origIndices) > 0 && m.seasonEpisodeIndex < len(origIndices) {
					origIdx := origIndices[m.seasonEpisodeIndex]
					if _, ok := m.selectedEpisodes[origIdx]; ok {
						delete(m.selectedEpisodes, origIdx)
					} else {
						m.selectedEpisodes[origIdx] = struct{}{}
					}
				}
				return m, nil
			case keyMsg.String() == "up" || keyMsg.String() == "k":
				if m.seasonEpisodeIndex > 0 {
					m.seasonEpisodeIndex--
				}
				return m, nil
			case keyMsg.String() == "down" || keyMsg.String() == "j":
				if m.seasonEpisodeIndex < len(seasonEps)-1 {
					m.seasonEpisodeIndex++
				}
				return m, nil
			case keyMsg.String() == "ctrl+a":
				m.selectAllEpisodes()
				return m, nil
			case keyMsg.String() == "ctrl+d":
				m.selectedEpisodes = make(map[int]struct{})
				return m, nil
			case keyMsg.String() == "pgup":
				m.pageEpisodes(-m.pageStep())
				return m, nil
			case keyMsg.String() == "pgdown":
				m.pageEpisodes(m.pageStep())
				return m, nil
			case keyMsg.String() == "D":
				if len(m.selectedEpisodes) == 0 {
					m.setStatus(statusWarn, "No episodes selected")
					return m, nil
				}
				if m.batchInProgress || m.downloadOpID != 0 {
					m.setStatus(statusWarn, "A download is already in progress")
					return m, nil
				}
				return m.startBatchDownload()
			case key.Matches(keyMsg, m.keys.MarkComplete) || keyMsg.String() == "m":
				if len(m.selectedEpisodes) == 0 {
					m.setStatus(statusWarn, "No episodes selected")
					return m, nil
				}
				allComplete := true
				for origIdx := range m.selectedEpisodes {
					if origIdx < 0 || origIdx >= len(m.episodeResults) {
						continue
					}
					if !m.isEpisodeComplete(m.episodeResults[origIdx]) {
						allComplete = false
						break
					}
				}
				count := 0
				var toastCmd tea.Cmd
				if allComplete {
					for origIdx := range m.selectedEpisodes {
						if origIdx < 0 || origIdx >= len(m.episodeResults) {
							continue
						}
						if err := m.unmarkEpisode(m.episodeResults[origIdx]); err == nil {
							count++
						}
					}
					toastCmd = m.setToast(fmt.Sprintf("unmarked %d episodes", count), ToastInfo)
				} else {
					for origIdx := range m.selectedEpisodes {
						if origIdx < 0 || origIdx >= len(m.episodeResults) {
							continue
						}
						if err := m.markEpisodeComplete(m.episodeResults[origIdx]); err == nil {
							count++
						}
					}
					toastCmd = m.setToast(fmt.Sprintf("marked %d episodes as complete", count), ToastSuccess)
				}
				m.selectedEpisodes = make(map[int]struct{})
				m.selectMode = false
				m.refreshEpisodeList()
				return m, toastCmd
			case keyMsg.String() == "esc":
				m.selectMode = false
				m.clearStatus()
				return m, nil
			}
		} else {
			switch {
			case keyMsg.String() == " " || keyMsg.String() == "space" || key.Matches(keyMsg, m.keys.ToggleSelect):
				m.selectMode = true
				if len(origIndices) > 0 && m.seasonEpisodeIndex < len(origIndices) {
					origIdx := origIndices[m.seasonEpisodeIndex]
					m.selectedEpisodes = make(map[int]struct{})
					m.selectedEpisodes[origIdx] = struct{}{}
				}
				return m, nil
			case key.Matches(keyMsg, m.keys.MarkComplete) || keyMsg.String() == "m":
				if len(seasonEps) > 0 && m.seasonEpisodeIndex < len(seasonEps) {
					ep := seasonEps[m.seasonEpisodeIndex]
					origIdx := origIndices[m.seasonEpisodeIndex]
					marked, err := m.toggleEpisodeComplete(ep)
					if err != nil {
						m.setStatus(statusWarn, fmt.Sprintf("Failed to update progress: %v", err))
						return m, nil
					}
					code := episodeCode(ep, origIdx, len(distinctSeasonNumbers(m.episodeResults)) > 1)
					var toastCmd tea.Cmd
					if marked {
						toastCmd = m.setToast("marked "+code+" complete", ToastSuccess)
					} else {
						toastCmd = m.setToast("unmarked "+code, ToastInfo)
					}
					m.refreshEpisodeList()
					return m, toastCmd
				}
			case key.Matches(keyMsg, m.keys.Select) || keyMsg.String() == "enter":
				if len(origIndices) > 0 && m.seasonEpisodeIndex < len(origIndices) {
					origIdx := origIndices[m.seasonEpisodeIndex]
					return m.selectEpisode(origIdx)
				}
				return m, nil
			case keyMsg.String() == "up" || keyMsg.String() == "k":
				if m.seasonEpisodeIndex > 0 {
					m.seasonEpisodeIndex--
				}
				return m, nil
			case keyMsg.String() == "down" || keyMsg.String() == "j":
				if m.seasonEpisodeIndex < len(seasonEps)-1 {
					m.seasonEpisodeIndex++
				}
				return m, nil
			case keyMsg.String() == "g" || keyMsg.String() == "home":
				m.seasonEpisodeIndex = 0
				return m, nil
			case keyMsg.String() == "G" || keyMsg.String() == "end":
				if len(seasonEps) > 0 {
					m.seasonEpisodeIndex = len(seasonEps) - 1
				}
				return m, nil
			case keyMsg.String() == "ctrl+u":
				m.pageEpisodes(-m.pageStep() / 2)
				return m, nil
			case keyMsg.String() == "ctrl+d":
				m.pageEpisodes(m.pageStep() / 2)
				return m, nil
			case keyMsg.String() == "pgup":
				m.pageEpisodes(-m.pageStep())
				return m, nil
			case keyMsg.String() == "pgdown":
				m.pageEpisodes(m.pageStep())
				return m, nil
			case keyMsg.String() == "/" || keyMsg.String() == "ctrl+f" || key.Matches(keyMsg, m.keys.Filter):
				m.episodeFiltering = true
				m.episodeFilter = ""
				m.seasonEpisodeIndex = 0
				return m, nil
			case keyMsg.String() == "[":
				if m.activeSeason > 0 {
					m.activeSeason--
					m.seasonEpisodeIndex = 0
				}
				return m, nil
			case keyMsg.String() == "]":
				seasons := distinctSeasonNumbers(m.episodeResults)
				if m.activeSeason < len(seasons)-1 {
					m.activeSeason++
					m.seasonEpisodeIndex = 0
				}
				return m, nil
			case key.Matches(keyMsg, m.keys.BatchDownload) || keyMsg.String() == "D":
				if len(m.selectedEpisodes) > 0 {
					return m.startBatchDownload()
				}
			case keyMsg.String() == "d":
				m.pushView(viewDownloads)
				return m, nil
			case keyMsg.String() == "a":
				return m.toggleAnimeAudio()
			}
		}
	}

	return m, nil
}

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
					targetIdx := m.downloadsIndex
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
						m.downloadService.CleanupPartial(m.batchActiveDir, m.batchActiveTitle)
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
					m.downloadService.CleanupPartial(m.downloadOutputDir, m.downloadTitle)
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

func (m *modelImpl) episodeHistoryKey(ep provider.Episode) history.EntryKey {
	seriesTitle, mediaType := "", ""
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
		mediaType = m.selectedSeries.MediaType
	}
	if seriesTitle == "" && m.resolved != nil {
		seriesTitle = m.resolved.SeriesTitle
	}
	if mediaType == "" && m.resolved != nil {
		mediaType = m.resolved.MediaType
	}
	if mediaType == "" {
		mediaType = string(m.appMode)
	}
	return history.EntryKey{
		Title:     seriesTitle,
		Mode:      string(m.appMode),
		MediaType: mediaType,
		Season:    ep.Season,
		Episode:   ep.Episode,
	}
}

func (m *modelImpl) isEpisodeComplete(ep provider.Episode) bool {
	if m.historyStore == nil {
		return false
	}
	entry, ok := m.historyStore.Get(m.episodeHistoryKey(ep))
	return ok && (entry.Complete || entry.PercentComplete >= 0.95 || (entry.DurationSecs > 0 && (entry.PositionSecs/entry.DurationSecs) >= 0.95))
}

func (m *modelImpl) markEpisodeComplete(ep provider.Episode) error {
	if m.historyStore == nil {
		return nil
	}
	key := m.episodeHistoryKey(ep)
	existing, found := m.historyStore.Get(key)

	seriesTitle, mediaType := "", ""
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
		mediaType = m.selectedSeries.MediaType
	}
	if seriesTitle == "" && m.resolved != nil {
		seriesTitle = m.resolved.SeriesTitle
	}
	if mediaType == "" && m.resolved != nil {
		mediaType = m.resolved.MediaType
	}
	if mediaType == "" {
		mediaType = string(m.appMode)
	}

	tmdbID := 0
	if m.selectedSeries != nil && m.selectedSeries.TMDBID != 0 {
		tmdbID = m.selectedSeries.TMDBID
	} else if ep.TMDBID != 0 {
		tmdbID = ep.TMDBID
	} else if m.resolved != nil {
		tmdbID = m.resolved.TMDBID
	}

	epTitle := ep.Title
	if epTitle == "" && m.resolved != nil {
		epTitle = m.resolved.EpisodeTitle
	}

	entry := history.Entry{
		Key:                  key,
		Title:                seriesTitle,
		EpisodeTitle:         epTitle,
		Season:               ep.Season,
		Episode:              ep.Episode,
		WatchedAt:            time.Now(),
		PercentComplete:      1.0,
		Complete:             true,
		LastScrobbledPercent: 1.0,
		Mode:                 string(m.appMode),
		MediaType:            mediaType,
		TMDBID:               tmdbID,
		AniListID:            anilistIDFor(m.appMode, m.selectedSeries),
		AudioMode:            m.audioMode,
	}

	if found {
		entry.DurationSecs = existing.DurationSecs
		if existing.DurationSecs > 0 {
			entry.PositionSecs = existing.DurationSecs
		}
		if existing.EpisodeTitle != "" && entry.EpisodeTitle == "" {
			entry.EpisodeTitle = existing.EpisodeTitle
		}
	}

	if err := m.historyStore.Upsert(entry); err != nil {
		return err
	}

	media := &model.ResolvedMedia{
		SeriesTitle:   seriesTitle,
		EpisodeTitle:  entry.EpisodeTitle,
		MediaType:     mediaType,
		SeasonNumber:  ep.Season,
		EpisodeNumber: ep.Episode,
		TMDBID:        tmdbID,
	}
	m.triggerScrobble(entry, media)
	return nil
}

func (m *modelImpl) unmarkEpisode(ep provider.Episode) error {
	if m.historyStore == nil {
		return nil
	}
	key := m.episodeHistoryKey(ep)
	return m.historyStore.Delete(key)
}

func (m *modelImpl) toggleEpisodeComplete(ep provider.Episode) (bool, error) {
	if m.isEpisodeComplete(ep) {
		if err := m.unmarkEpisode(ep); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := m.markEpisodeComplete(ep); err != nil {
		return false, err
	}
	return true, nil
}

func (m *modelImpl) startBatchDownload() (tea.Model, tea.Cmd) {
	selected := m.orderedSelectedEpisodes()
	if len(selected) == 0 {
		m.setStatus(statusWarn, "No episodes selected")
		return m, nil
	}

	m.batchInProgress = true
	m.batchCurrent = 0
	m.batchTotal = len(selected)
	m.batchEpisodeProgress = 0
	m.batchEpisodes = selected
	m.downloadPaused = false
	m.selectMode = false
	m.selectedEpisodes = make(map[int]struct{})
	m.refreshEpisodeList()
	toastCmd := m.setToast(fmt.Sprintf("batch download started (%d episodes)", m.batchTotal), ToastSuccess)

	opID := m.newOpID()
	m.downloadOpID = opID

	var series provider.SearchResult
	var mode provider.ContentType
	var hasSeries bool
	if m.selectedSeries != nil {
		series = *m.selectedSeries
		mode = m.appMode
		hasSeries = true
	}
	m.batchSeries = series
	m.batchMode = mode

	if m.downloadService != nil && hasSeries {
		job := service.DownloadJob{
			Series:       series,
			Episodes:     selected,
			BatchCurrent: 1,
			Mode:         mode,
		}
		if err := m.downloadService.SavePendingJob(job); err != nil {
			tuiLog.Warn("save pending batch download failed", "err", err)
		} else {
			m.pendingDownload = &job
		}
	}

	cmd := m.batchDownloadCmd(opID, selected, series, mode, hasSeries, 1, len(selected))
	return m, tea.Batch(toastCmd, cmd)
}
func (m *modelImpl) orderedSelectedEpisodes() []provider.Episode {
	out := make([]provider.Episode, 0, len(m.selectedEpisodes))
	for i := range m.episodeResults {
		if _, ok := m.selectedEpisodes[i]; ok {
			out = append(out, m.episodeResults[i])
		}
	}
	return out
}

func (m *modelImpl) effectiveDownloadQuality() int {
	switch m.downloadQuality {
	case downloadQuality4K:
		return 10 // 4K (2160p)
	case downloadQualityFHD:
		return 11 // FHD (1080p)
	case downloadQualityHD:
		return 12 // HD (720p)
	case downloadQualitySD:
		return 13 // SD (480p / 360p)
	default:
		return m.qualityMode
	}
}
func downloadQualityLabel(effectiveQuality int) string {
	switch effectiveQuality {
	case 10:
		return "4K"
	case 11:
		return "1080p"
	case 12:
		return "720p"
	case 13:
		return "480p"
	case qualityHighest:
		return "1080p"
	case qualityDataSaver:
		return "720p"
	case qualityLowest:
		return "480p"
	default:
		return "Auto"
	}
}


func (m *modelImpl) batchDownloadCmd(
	opID int,
	episodes []provider.Episode,
	series provider.SearchResult,
	mode provider.ContentType,
	hasSeries bool,
	startIndex, totalBatchCount int,
) tea.Cmd {
	if startIndex <= 0 {
		startIndex = 1
	}
	if totalBatchCount <= 0 {
		totalBatchCount = len(episodes)
	}
	qualityMode := m.effectiveDownloadQuality()
	languageFilter := make(map[string]bool, len(m.languageFilter))
	for lang, enabled := range m.languageFilter {
		languageFilter[lang] = enabled
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(m.appCtx)

		if !hasSeries {
			cancel()
			return batchDoneMsg{opID: opID, completed: 0, total: totalBatchCount}
		}

		go func() {
			defer cancel()

			// Track where the active episode is being written so x (cancel
			// single) can clean up exactly those partial files.
			var activeDir, activeTitle string
			onActive := func(outputDir, title string) {
				activeDir, activeTitle = outputDir, title
			}

			onProgress := func(current, total int, epTitle string, dp downloader.DownloadProgress) {
				actualCurrent := (startIndex - 1) + current
				select {
				case m.batchChan <- batchProgressMsg{
					opID:            opID,
					current:         actualCurrent,
					total:           totalBatchCount,
					episodeTitle:    epTitle,
					episodeProgress: dp.Percent,
					totalSize:       dp.TotalSize,
					speed:           dp.Speed,
					outputDir:       activeDir,
					fileTitle:       activeTitle,
					downloaded:      dp.Downloaded,
					eta:             dp.ETA,
				}:
				default:
				}
			}

			results := m.downloadService.BatchDownloadWithActive(
				ctx,
				series,
				episodes,
				mode,
				qualityMode,
				languageFilter,
				onActive,
				onProgress,
			)

			completed := 0
			for _, r := range results {
				if r.Err == nil {
					completed++
				}
			}

			// See the equivalent comment in downloadCmd: the completion
			// message must always be delivered or the batch UI never
			// clears its loading state, so this send is intentionally
			// blocking while progress ticks above stay droppable. The
			// ctx.Done() case escapes a blocked send if the user
			// explicitly cancelled the batch (UI already reset itself).
			select {
			case m.batchChan <- batchDoneMsg{
				opID:      opID,
				completed: (startIndex - 1) + completed,
				total:     totalBatchCount,
			}:
			case <-ctx.Done():
			}
		}()

		return batchStartedMsg{opID: opID, cancel: cancel, total: totalBatchCount}
	}
}

func (m *modelImpl) onBatchProgress(msg batchProgressMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, nil
	}
	m.batchCurrent = msg.current
	m.batchTotal = msg.total
	m.batchEpisodeProgress = msg.episodeProgress
	m.downloadSpeed = msg.speed
	m.downloadDownloaded = msg.downloaded
	m.downloadTotalSize = msg.totalSize
	m.downloadETA = msg.eta
	if msg.outputDir != "" {
		m.batchActiveDir = msg.outputDir
		m.batchActiveTitle = msg.fileTitle
	}
	if m.pendingDownload != nil && m.downloadService != nil && len(m.pendingDownload.Episodes) > 0 {
		if m.pendingDownload.BatchCurrent != msg.current {
			m.pendingDownload.BatchCurrent = msg.current
			_ = m.downloadService.SavePendingJob(*m.pendingDownload)
		}
	}
	return m, m.batchSubscription()
}
func (m *modelImpl) onBatchDone(msg batchDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, nil
	}
	// A user pause surfaces as a batch completion with the pause flag set:
	// keep ALL batch state (progress, remaining episodes, active files) so
	// the p handler can resume, and keep the paused row visible.
	if m.downloadPaused {
		m.batchCancel = nil
		m.downloadOpID = 0
		return m, nil
	}
	if m.downloadService != nil {
		_ = m.downloadService.ClearPendingJob()
	}
	m.pendingDownload = nil
	m.batchInProgress = false
	m.batchCancel = nil
	m.batchCurrent = 0
	m.batchTotal = 0
	m.batchEpisodeProgress = 0
	m.batchActiveDir = ""
	m.batchActiveTitle = ""
	m.downloadSpeed = ""
	m.downloadDownloaded = ""
	m.downloadTotalSize = ""
	m.downloadETA = ""
	m.downloadOpID = 0

	if msg.completed == 0 {
		return m, m.setStatusTimed(statusError, "Batch download failed")
	}
	return m, m.setToast(fmt.Sprintf("downloaded %d/%d episodes", msg.completed, msg.total), ToastSuccess)
}

func (m *modelImpl) batchSubscription() tea.Cmd {
	return func() tea.Msg {
		return <-m.batchChan
	}
}
