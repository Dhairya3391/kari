package tui

import (
	"context"
	"fmt"
	"kari/internal/downloader"
	"kari/internal/provider"
	"kari/internal/service"

	tea "github.com/charmbracelet/bubbletea"
)

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
		return m, m.batchSubscription()
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
		return m, m.batchSubscription()
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
