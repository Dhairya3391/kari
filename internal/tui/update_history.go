package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/provider"
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
				m.clearStatus()
				return m, nil
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, m.keys.Back):
			m.goBackOne()
			return m, nil
		case key.Matches(msg, m.keys.Type):
			if m.historyList.SettingFilter() {
				break
			}
			return m, m.cycleHistoryTab()
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
			return m, nil
		}
	}

	prevSel := m.historySelectedIndex()
	var cmd tea.Cmd
	m.historyList, cmd = m.historyList.Update(msg)
	cmds := []tea.Cmd{cmd}
	if newSel := m.historySelectedIndex(); newSel != prevSel {
		cmds = append(cmds, m.triggerHistoryPoster())
	}
	return m, tea.Batch(cmds...)
}

// historySelectedIndex resolves the history list cursor to a result
// index, or -1 when the list is empty.
func (m *modelImpl) historySelectedIndex() int {
	if item, ok := m.historyList.SelectedItem().(rowItem); ok {
		return item.index
	}
	return m.historyList.Index()
}

// cycleHistoryTab flips between the Continue and Finished tabs,
// restoring each tab's remembered cursor and poster.
func (m *modelImpl) cycleHistoryTab() tea.Cmd {
	m.historyTabIndex[m.historyTab] = m.historyList.Index()
	m.historyTab = (m.historyTab + 1) % 2
	m.refreshHistoryItems()
	if idx := m.historyTabIndex[m.historyTab]; idx > 0 && idx < len(m.historyList.Items()) {
		m.historyList.Select(idx)
	} else {
		m.historyList.Select(0)
	}
	return m.triggerHistoryPoster()
}

// refreshHistoryItems rebuilds the list for the active tab from the
// retained groups, without re-reading the store.
func (m *modelImpl) refreshHistoryItems() {
	continued, finished := splitHistoryGroups(m.historyGroups)
	if m.historyTab == historyTabFinished {
		m.historyList.SetItems(historyTabItems(finished, true))
	} else {
		m.historyList.SetItems(historyTabItems(continued, false))
	}
}

func (m *modelImpl) refreshHistory() (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, nil
	}
	groups := history.BuildGroups(m.historyStore.All())
	m.historyGroups = groups
	m.refreshHistoryItems()
	if idx := m.historyTabIndex[m.historyTab]; idx > 0 && idx < len(m.historyList.Items()) {
		m.historyList.Select(idx)
	} else {
		m.historyList.Select(0)
	}
	return m, m.triggerHistoryPoster()
}

func (m *modelImpl) resumeHistoryEntry(entry history.Entry) (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, nil
	}
	if !m.guardLoad() {
		return m, nil
	}
	m.appMode = modeForHistoryEntry(entry)
	if entry.AudioMode != "" {
		m.audioMode = entry.AudioMode
	} else if m.modeFeatures().AudioSelection {
		m.audioMode = provider.AudioSub
	}
	if entry.Language != "" {
		m.prevSourceLanguage = entry.Language
	}
	m.selectedSeries = nil
	m.selectedEpisode = nil
	m.resolved = nil
	m.manualPlaybackSelected = false
	m.clearPreviewPoster()
	m.pendingHistoryTarget = nil
	m.loading = true
	m.loadingText = fmt.Sprintf("Finding %s...", entry.Title)
	opID := m.newOpID()
	m.historyContinueOpID = opID

	groups := history.BuildGroups(m.historyStore.All())
	var group *history.Group
	for i := range groups {
		if strings.EqualFold(groups[i].Title, entry.Title) {
			group = &groups[i]
			break
		}
	}
	var grp history.Group
	if group != nil {
		grp = *group
	} else {
		grp = history.Group{
			Key:           history.GroupKey{Title: entry.Title, Mode: entry.Mode, MediaType: entry.MediaType},
			Title:         entry.Title,
			Mode:          entry.Mode,
			MediaType:     entry.MediaType,
			ContinueEntry: entry,
		}
	}

	tuiLog.Info("history resume: searching providers", "title", entry.Title, "mode", m.appMode)
	return m, tea.Batch(m.spinner.Tick, m.historyResolveSeriesCmd(opID, entry, &grp))
}

func (m *modelImpl) playHistoryGroup(keyStr string) (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, nil
	}
	if !m.guardLoad() {
		return m, nil
	}
	all := m.historyStore.All()
	groups := history.BuildGroups(all)
	var group *history.Group
	for i := range groups {
		if groups[i].Key.String() == keyStr || strings.EqualFold(groups[i].Title, keyStr) {
			group = &groups[i]
			break
		}
	}
	if group == nil {
		for _, e := range all {
			if e.Key.String() == keyStr || strings.EqualFold(e.Title, keyStr) {
				return m.resumeHistoryEntry(e)
			}
		}
		return m, nil
	}
	return m.resumeHistoryEntry(group.ContinueEntry)
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
		if len(results) == 0 {
			// Fallback 1: try title with subtitle split (e.g. before " - ")
			if strings.Contains(entry.Title, " - ") {
				shortTitle := strings.TrimSpace(strings.Split(entry.Title, " - ")[0])
				if shortTitle != "" {
					results, _, _, _ = m.mediaService.Search(m.appCtx, mode, shortTitle)
				}
			}
		}
		if len(results) == 0 {
			// Fallback 2: try title with colon split (e.g. before ":")
			if strings.Contains(entry.Title, ":") {
				shortTitle := strings.TrimSpace(strings.Split(entry.Title, ":")[0])
				if shortTitle != "" {
					results, _, _, _ = m.mediaService.Search(m.appCtx, mode, shortTitle)
				}
			}
		}
		if len(results) == 0 {
			if err == nil {
				err = fmt.Errorf("no provider currently has %q", entry.Title)
			}
			return historyResolveSeriesMsg{entry: entry, group: group, opID: opID, err: err}
		}
		return historyResolveSeriesMsg{entry: entry, group: group, series: bestHistorySeriesMatch(results, entry), opID: opID}
	}
}

// bestHistorySeriesMatch picks the live search result that most likely
// corresponds to a history entry: an exact TMDBID match first (most
// reliable, provider-independent), falling back to an exact title match,
// then a fuzzy title contains match, and finally just the top result.
func bestHistorySeriesMatch(results []provider.SearchResult, entry history.Entry) provider.SearchResult {
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
	for _, r := range results {
		rTitle := strings.ToLower(strings.TrimSpace(r.Title))
		if strings.Contains(target, rTitle) || strings.Contains(rTitle, target) {
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
		tuiLog.Warn("history resume failed", "err", msg.err)
		m.setStatus(statusWarn, fmt.Sprintf("%q not found on any current provider", msg.entry.Title))
		return m, nil
	}

	series := msg.series
	m.selectedSeries = &series
	// Manga resume skips the episode/preview pipeline entirely:
	// chapters screen, then straight into the saved chapter and page.
	if m.appMode == provider.ModeManga {
		tuiLog.Info("history resume: opening manga chapters", "title", series.Title)
		return m.resumeMangaSeries(msg.entry, series)
	}
	m.selectedEpisode = nil
	m.resolved = nil
	m.manualPlaybackSelected = false
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
		tuiLog.Info("history resume: fetching next episodes", "title", msg.group.Title, "season", msg.group.FarthestComplete.Season, "episode", msg.group.FarthestComplete.Episode)
		return m, m.historyContinueEpisodesCmd(opID, *msg.group, series, m.appMode)
	}

	target := msg.entry
	m.pendingHistoryTarget = &target
	m.loadingText = "Loading episodes..."
	opID := m.newOpID()
	m.episodesOpID = opID
	tuiLog.Info("history resume: re-resolving for preview", "title", target.Title, "season", target.Season, "episode", target.Episode)
	return m, m.episodesCmd(opID, series)
}

// episodeIndexForEntry finds the live episode matching a history entry's
// season/episode. Movies have no season/episode numbering, so any single
// live "episode" result is treated as the match.
func episodeIndexForEntry(episodes []provider.Episode, entry history.Entry) (int, bool) {
	if entry.Season == 0 && entry.Episode == 0 && len(episodes) > 0 {
		return 0, true
	}
	for i, ep := range episodes {
		if ep.Season == entry.Season && ep.Episode == entry.Episode {
			return i, true
		}
	}
	if entry.Episode > 0 {
		for i, ep := range episodes {
			if ep.Episode == entry.Episode {
				return i, true
			}
		}
	}
	return 0, false
}
