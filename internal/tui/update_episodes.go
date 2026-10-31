package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
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
			case keyMsg.String() == "[" || keyMsg.String() == "left":
				if m.activeSeason > 0 {
					m.activeSeason--
					m.seasonEpisodeIndex = 0
				}
				return m, nil
			case keyMsg.String() == "]" || keyMsg.String() == "right":
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

// FilterRedundantEpisodeNavigation drops key repeats that cannot move the
// episode cursor, avoiding redraws while queued input drains at a boundary.
func FilterRedundantEpisodeNavigation(model tea.Model, msg tea.Msg) tea.Msg {
	m, ok := model.(*modelImpl)
	if !ok || m.activeView != viewEpisodes || !m.hasCurrentEpisodeProjection() {
		return msg
	}
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return msg
	}
	index := m.seasonEpisodeIndex
	count := len(m.episodeCache.filtered)
	switch keyMsg.String() {
	case "down", "j", "pgdown":
		if index >= count-1 {
			return nil
		}
	case "ctrl+d":
		if !m.selectMode && !m.episodeFiltering && index >= count-1 {
			return nil
		}
	case "up", "k", "pgup":
		if index <= 0 {
			return nil
		}
	case "ctrl+u":
		if !m.selectMode && !m.episodeFiltering && index <= 0 {
			return nil
		}
	}
	return msg
}
