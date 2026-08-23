package tui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/downloader"
	"kari/internal/model"
	"kari/internal/provider"
)

func (m *modelImpl) updateActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewSearch:
		return m.updateSearch(msg)
	case viewEpisodes:
		return m.updateEpisodes(msg)
	case viewPreview:
		return m.updatePreview(msg)
	case viewHistory:
		return m.updateHistory(msg)
	case viewSettings:
		return m.updateSettings(msg)
	default:
		return m, nil
	}
}

func (m *modelImpl) updateEpisodes(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(keyMsg, m.keys.ToggleSelect):
			if m.episodeList.SettingFilter() {
				break
			}
			if len(m.episodeResults) == 0 {
				return m, nil
			}
			idx := m.selectedEpisodeIndex()
			if _, ok := m.selectedEpisodes[idx]; ok {
				delete(m.selectedEpisodes, idx)
			} else {
				m.selectedEpisodes[idx] = struct{}{}
			}
			m.refreshEpisodeList()
			return m, nil
		case key.Matches(keyMsg, m.keys.SelectAll):
			if m.episodeList.SettingFilter() {
				break
			}
			m.selectAllEpisodes()
			return m, nil
		case key.Matches(keyMsg, m.keys.DeselectAll):
			if m.episodeList.SettingFilter() {
				break
			}
			m.selectedEpisodes = make(map[int]struct{})
			m.refreshEpisodeList()
			return m, nil
		case key.Matches(keyMsg, m.keys.BatchDownload):
			if len(m.selectedEpisodes) == 0 {
				m.setStatus(statusWarn, "No episodes selected — press space to select")
				return m, nil
			}
			if m.batchInProgress || m.downloadOpID != 0 {
				m.setStatus(statusWarn, "A download is already in progress")
				return m, nil
			}
			return m.startBatchDownload()
		case key.Matches(keyMsg, m.keys.Select):
			return m.selectEpisode(m.selectedEpisodeIndex())
		case key.Matches(keyMsg, m.keys.Audio):
			if m.selectedSeries != nil {
				if m.audioMode == "sub" {
					m.audioMode = "dub"
				} else {
					m.audioMode = "sub"
				}
				m.selectedEpisodes = make(map[int]struct{})
				opID := m.newOpID()
				m.episodesOpID = opID
				return m, tea.Batch(m.spinner.Tick, m.episodesCmd(opID, *m.selectedSeries))
			}
		case keyMsg.String() == "g":
			if m.episodeList.SettingFilter() {
				break
			}
			m.episodeList.Select(0)
			return m, nil
		case keyMsg.String() == "G":
			if m.episodeList.SettingFilter() {
				break
			}
			if visible := m.episodeList.VisibleItems(); len(visible) > 0 {
				m.episodeList.Select(len(visible) - 1)
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.episodeList, cmd = m.episodeList.Update(msg)
	return m, cmd
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
	m.loading = true
	m.loadingText = fmt.Sprintf("Downloading 0/%d...", m.batchTotal)

	opID := m.newOpID()
	m.downloadOpID = opID

	var series model.SearchResult
	var mode provider.ContentType
	var hasSeries bool
	if m.selectedSeries != nil {
		series = *m.selectedSeries
		mode = m.appMode
		hasSeries = true
	}

	cmd := m.batchDownloadCmd(opID, selected, series, mode, hasSeries)
	return m, tea.Batch(m.spinner.Tick, cmd)
}

func (m *modelImpl) orderedSelectedEpisodes() []model.EpisodeResult {
	out := make([]model.EpisodeResult, 0, len(m.selectedEpisodes))
	for i := range m.episodeResults {
		if _, ok := m.selectedEpisodes[i]; ok {
			out = append(out, m.episodeResults[i])
		}
	}
	return out
}

func (m *modelImpl) batchDownloadCmd(opID int, episodes []model.EpisodeResult, series model.SearchResult, mode provider.ContentType, hasSeries bool) tea.Cmd {
	qualityMode := m.qualityMode
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
					downloaded:      dp.Downloaded,
					eta:             dp.ETA,
				}:
				default:
				}
			}

			results := m.downloadService.BatchDownload(
				ctx,
				series,
				episodes,
				mode,
				qualityMode,
				languageFilter,
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
	if msg.totalSize != "" && msg.speed != "" && msg.downloaded != "" {
		text := fmt.Sprintf("Downloading %d/%d: %s — %.1f%% — %s / %s at %s",
			msg.current, msg.total, msg.episodeTitle, msg.episodeProgress*100,
			msg.downloaded, msg.totalSize, msg.speed)
		if msg.eta != "" {
			text += fmt.Sprintf(", ETA %s", msg.eta)
		}
		m.loadingText = text
	} else {
		m.loadingText = fmt.Sprintf("Downloading %d/%d: %s — %.0f%%",
			msg.current, msg.total, msg.episodeTitle, msg.episodeProgress*100)
	}
	return m, m.batchSubscription()
}

func (m *modelImpl) onBatchDone(msg batchDoneMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.downloadOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	m.batchInProgress = false
	m.batchCancel = nil
	m.batchCurrent = 0
	m.batchTotal = 0
	m.downloadOpID = 0

	if msg.completed == 0 {
		return m, m.setStatusTimed(statusError, "Batch download failed")
	}
	return m, m.setStatusTimed(statusSuccess, fmt.Sprintf("Downloaded %d/%d episodes", msg.completed, msg.total))
}

func (m *modelImpl) batchSubscription() tea.Cmd {
	return func() tea.Msg {
		return <-m.batchChan
	}
}
