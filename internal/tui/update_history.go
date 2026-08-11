package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/model"
)

func (m *modelImpl) updateHistory(msg tea.Msg) (tea.Model, tea.Cmd) {

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.confirmDelete || m.confirmClearHistory {
			switch msg.String() {
			case "y", "Y":
				if m.confirmDelete {
					if item, ok := m.historyList.SelectedItem().(rowItem); ok {
						_ = m.historyStore.DeleteGroup(historyGroupKeyByString(m.historyStore.All(), item.key))
					}
					m.confirmDelete = false
					return m.refreshHistory()
				}
				if m.confirmClearHistory {
					_ = m.historyStore.Clear()
					m.confirmClearHistory = false
					return m.refreshHistory()
				}
			case "n", "N", "esc":
				m.confirmDelete = false
				m.confirmClearHistory = false
				return m, nil
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, m.keys.Back):
			m.goBackOne()
			return m, nil
		case key.Matches(msg, m.keys.Delete):
			if len(m.historyList.Items()) > 0 {
				m.confirmDelete = true
			}
			return m, nil
		case key.Matches(msg, m.keys.ClearHistory):
			if len(m.historyList.Items()) > 0 {
				m.confirmClearHistory = true
			}
			return m, nil
		case key.Matches(msg, m.keys.Select):
			if item, ok := m.historyList.SelectedItem().(rowItem); ok {
				return m.playHistoryGroup(item.key)
			}
		}
	}

	var cmd tea.Cmd
	m.historyList, cmd = m.historyList.Update(msg)
	return m, cmd
}

func (m *modelImpl) refreshHistory() (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, nil
	}
	groups := history.BuildGroups(m.historyStore.All())
	items := historyGroupsToItems(groups)
	m.historyList.SetItems(items)
	return m, nil
}

func (m *modelImpl) playHistoryGroup(keyStr string) (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, nil
	}
	groups := history.BuildGroups(m.historyStore.All())
	var group *history.Group
	for i := range groups {
		if groups[i].Key.String() == keyStr {
			group = &groups[i]
			break
		}
	}
	if group == nil {
		return m, nil
	}

	entry := group.ContinueEntry
	m.appMode = modeForHistoryEntry(entry)
	m.selectedSeries = nil
	m.selectedEpisode = nil
	m.resolved = nil
	m.clearPreviewPoster()
	m.pendingHistoryTarget = nil
	m.loading = true
	m.loadingText = fmt.Sprintf("Finding %s...", entry.Title)
	opID := m.newOpID()
	m.historyContinueOpID = opID
	grp := *group
	logging.Infof("playHistoryGroup: searching current providers for %q (mode=%s)", entry.Title, m.appMode)
	return m, tea.Batch(m.spinner.Tick, m.historyResolveSeriesCmd(opID, entry, &grp))
}

// historyResolveSeriesCmd re-searches whichever providers are CURRENTLY
// registered for the entry's mode, rather than trusting a provider name/URL
// that may have been saved from a provider that's since been removed or
// renamed. This is what lets watch history keep working across provider
// changes.
func (m *modelImpl) historyResolveSeriesCmd(opID int, entry history.Entry, group *history.Group) tea.Cmd {
	mode := modeForHistoryEntry(entry)
	return func() tea.Msg {
		results, _, _, err := m.mediaService.Search(m.appCtx, mode, entry.Title)
		if err == nil && len(results) == 0 {
			err = fmt.Errorf("no provider currently has %q", entry.Title)
		}
		if err != nil {
			return historyResolveSeriesMsg{entry: entry, group: group, opID: opID, err: err}
		}
		return historyResolveSeriesMsg{entry: entry, group: group, series: bestHistorySeriesMatch(results, entry), opID: opID}
	}
}

// bestHistorySeriesMatch picks the live search result that most likely
// corresponds to a history entry: an exact TMDBID match first (most
// reliable, provider-independent), falling back to an exact title match,
// and finally just the top result.
func bestHistorySeriesMatch(results []model.SearchResult, entry history.Entry) model.SearchResult {
	if entry.TMDBID > 0 {
		for _, r := range results {
			if r.TMDBID == entry.TMDBID {
				return r
			}
		}
	}
	target := strings.ToLower(strings.TrimSpace(entry.Title))
	for _, r := range results {
		if strings.ToLower(strings.TrimSpace(r.Title)) == target {
			return r
		}
	}
	return results[0]
}

func (m *modelImpl) onHistoryResolveSeries(msg historyResolveSeriesMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.historyContinueOpID {
		return m, nil
	}
	if msg.err != nil {
		m.loading = false
		m.loadingText = ""
		logging.Warnf("history resume: %v", msg.err)
		m.setStatus(statusWarn, fmt.Sprintf("%q not found on any current provider", msg.entry.Title))
		return m, nil
	}

	series := msg.series
	m.selectedSeries = &series
	m.selectedEpisode = nil
	m.resolved = nil
	m.clearPreviewPoster()
	m.selectedPlayback = 0
	m.episodeResults = nil
	m.episodeIndex = -1
	m.autoPlayAfterResolve = false
	m.pushView(viewPreview)

	if msg.group != nil && shouldFetchNextEpisode(*msg.group) {
		m.loadingText = "Finding next episode..."
		opID := m.newOpID()
		m.historyContinueOpID = opID
		logging.Infof("onHistoryResolveSeries: loading episodes for %q after S%dE%d", msg.group.Title, msg.group.FarthestComplete.Season, msg.group.FarthestComplete.Episode)
		return m, m.historyContinueEpisodesCmd(opID, *msg.group, series, m.appMode)
	}

	target := msg.entry
	m.pendingHistoryTarget = &target
	m.loadingText = "Loading episodes..."
	opID := m.newOpID()
	m.episodesOpID = opID
	logging.Infof("onHistoryResolveSeries: re-resolving %q S%dE%d from history for preview", target.Title, target.Season, target.Episode)
	return m, m.episodesCmd(opID, series)
}

// episodeIndexForEntry finds the live episode matching a history entry's
// season/episode. Movies have no season/episode numbering, so any single
// live "episode" result is treated as the match.
func episodeIndexForEntry(episodes []model.EpisodeResult, entry history.Entry) (int, bool) {
	if entry.Season == 0 && entry.Episode == 0 && len(episodes) > 0 {
		return 0, true
	}
	for i, ep := range episodes {
		if ep.Season == entry.Season && ep.Number == entry.Episode {
			return i, true
		}
	}
	if entry.Episode > 0 {
		for i, ep := range episodes {
			if ep.Number == entry.Episode {
				return i, true
			}
		}
	}
	return 0, false
}
