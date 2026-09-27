package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/downloader"
	"kari/internal/provider"
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
			case keyMsg.String() == "esc":
				m.selectMode = false
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
			case keyMsg.String() == "g":
				m.seasonEpisodeIndex = 0
				return m, nil
			case keyMsg.String() == "G":
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
			case keyMsg.String() == "/":
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
				maxSeason := 1
				for _, ep := range m.episodeResults {
					if ep.Season > maxSeason {
						maxSeason = ep.Season
					}
				}
				if m.activeSeason < maxSeason-1 {
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
		case "enter":
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
					m.setToast("download paused — press p to resume", ToastInfo)
					return m, nil
				}
			} else {
				// Resume: re-run the same sources. aria2c picks up the
				// .aria2 checkpoint, yt-dlp continues from .part files.
				m.downloadPaused = false
				if m.batchInProgress && len(m.batchEpisodes) > 0 {
					opID := m.newOpID()
					m.downloadOpID = opID
					remIdx := max(0, m.batchCurrent-1)
					if remIdx < len(m.batchEpisodes) {
						rem := m.batchEpisodes[remIdx:]
						m.setToast("download resumed", ToastSuccess)
						cmd := m.batchDownloadCmd(opID, rem, m.batchSeries, m.batchMode, true)
						return m, cmd
					}
				} else if m.singleResolved != nil {
					opID := m.newOpID()
					m.downloadOpID = opID
					m.setToast("download resumed", ToastSuccess)
					return m, m.downloadCmd(opID, *m.singleResolved)
				}
				m.setToast("download resumed", ToastSuccess)
				return m, nil
			}
		case "x":
			// Double-press confirmation for anything destructive, matching
			// the x-stop and q-quit conventions elsewhere.
			if !m.confirmStop {
				m.confirmStop = true
				m.setStatus(statusWarn, "Press x again to cancel (partials are deleted)")
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
					m.setToast(fmt.Sprintf("cancelled %s", item.Title), ToastInfo)
					return m, nil
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
					m.setToast(fmt.Sprintf("skipped %s", item.Title), ToastInfo)
					curIdx := max(0, m.batchCurrent-1)
					if curIdx < len(m.batchEpisodes) {
						m.batchEpisodes = append(m.batchEpisodes[:curIdx], m.batchEpisodes[curIdx+1:]...)
						m.batchTotal = len(m.batchEpisodes)
					}
					if curIdx < len(m.batchEpisodes) {
						opID := m.newOpID()
						m.downloadOpID = opID
						m.batchInProgress = true
						rem := m.batchEpisodes[curIdx:]
						cmd := m.batchDownloadCmd(opID, rem, m.batchSeries, m.batchMode, true)
						return m, cmd
					}
					m.batchInProgress = false
					return m, nil
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
				m.setToast("download cancelled", ToastInfo)
				return m, nil
			} else if m.downloadsIndex >= len(data.Active) && len(data.Done) > 0 {
				doneIdx := m.downloadsIndex - len(data.Active)
				if doneIdx >= 0 && doneIdx < len(m.doneDownloads) {
					m.doneDownloads = append(m.doneDownloads[:doneIdx], m.doneDownloads[doneIdx+1:]...)
					m.setToast("removed from history", ToastInfo)
					return m, nil
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
			m.setToast("all downloads cancelled", ToastInfo)
			return m, nil
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
	seriesTitle, mediaType := "", ""
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
		mediaType = m.selectedSeries.MediaType
	}
	m.episodeList.SetItems(episodesToItems(m.episodeResults, m.historyStore, seriesTitle, m.appMode, mediaType, m.selectedEpisodes))
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
	m.setToast(fmt.Sprintf("batch download started (%d episodes)", m.batchTotal), ToastSuccess)

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

	cmd := m.batchDownloadCmd(opID, selected, series, mode, hasSeries)
	return m, cmd
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

func (m *modelImpl) batchDownloadCmd(opID int, episodes []provider.Episode, series provider.SearchResult, mode provider.ContentType, hasSeries bool) tea.Cmd {
	qualityMode := m.effectiveDownloadQuality()
	languageFilter := make(map[string]bool, len(m.languageFilter))
	for lang, enabled := range m.languageFilter {
		languageFilter[lang] = enabled
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(m.appCtx)

		if !hasSeries {
			cancel()
			return batchDoneMsg{opID: opID, completed: 0, total: len(episodes)}
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
				select {
				case m.batchChan <- batchProgressMsg{
					opID:            opID,
					current:         current,
					total:           total,
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
				completed: completed,
				total:     len(episodes),
			}:
			case <-ctx.Done():
			}
		}()

		return batchStartedMsg{opID: opID, cancel: cancel, total: len(episodes)}
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
	m.setToast(fmt.Sprintf("downloaded %d/%d episodes", msg.completed, msg.total), ToastSuccess)
	return m, nil
}

func (m *modelImpl) batchSubscription() tea.Cmd {
	return func() tea.Msg {
		return <-m.batchChan
	}
}
