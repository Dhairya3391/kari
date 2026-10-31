package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/provider"
)

func (m *modelImpl) updateHistory(msg tea.Msg) (tea.Model, tea.Cmd) {

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.historyFiltering {
			switch msg.String() {
			case "enter":
				m.historyFiltering = false
				m.historyTerminalInputPrefix = ""
				m.historyDroppingTerminalResponse = false
				return m, nil
			case "backspace", "ctrl+h":
				m.historyTerminalInputPrefix = ""
				if m.setHistoryFilter(removeLastRune(m.historyFilter)) {
					return m, m.triggerHistoryPoster()
				}
				return m, nil
			case "ctrl+u":
				m.historyTerminalInputPrefix = ""
				m.historyDroppingTerminalResponse = false
				if m.setHistoryFilter("") {
					return m, m.triggerHistoryPoster()
				}
				return m, nil
			case "ctrl+w", "alt+backspace":
				m.historyTerminalInputPrefix = ""
				if m.setHistoryFilter(removeLastWord(m.historyFilter)) {
					return m, m.triggerHistoryPoster()
				}
				return m, nil
			case "up":
				previous := m.selectedHistoryKey()
				m.historyList.CursorUp()
				if previous != m.selectedHistoryKey() {
					return m, m.triggerHistoryPoster()
				}
				return m, nil
			case "down":
				previous := m.selectedHistoryKey()
				m.historyList.CursorDown()
				if previous != m.selectedHistoryKey() {
					return m, m.triggerHistoryPoster()
				}
				return m, nil
			}
			if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
				input := m.historyFilterInput(string(msg.Runes))
				if input != "" && m.setHistoryFilter(m.historyFilter+input) {
					return m, m.triggerHistoryPoster()
				}
				return m, nil
			}
			if msg.Type == tea.KeySpace {
				if m.setHistoryFilter(m.historyFilter + " ") {
					return m, m.triggerHistoryPoster()
				}
				return m, nil
			}
			return m, nil
		}
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
		case msg.String() == "left":
			return m, m.cycleHistoryMode(-1)
		case msg.String() == "right":
			return m, m.cycleHistoryMode(1)
		case key.Matches(msg, m.keys.Type):
			return m, m.cycleHistoryTab(msg.Type == tea.KeyShiftTab)
		case msg.String() == "]":
			return m, m.cycleHistoryMode(1)
		case msg.String() == "[":
			return m, m.cycleHistoryMode(-1)
		case msg.String() == "/" || msg.String() == "ctrl+f" || key.Matches(msg, m.keys.Filter):
			m.historyFiltering = true
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
			return m, nil
		}
	}

	previousSelection := m.historySelectedIndex()
	var cmd tea.Cmd
	m.historyList, cmd = m.historyList.Update(msg)
	if m.historySelectedIndex() != previousSelection {
		cmd = tea.Batch(cmd, m.triggerHistoryPoster())
	}
	return m, cmd
}

func (m *modelImpl) setHistoryFilter(query string) bool {
	selectedKey := ""
	if item, ok := m.historyList.SelectedItem().(rowItem); ok {
		selectedKey = item.key
	}
	m.historyFilter = query
	m.refreshHistoryItems()
	if selectedKey == "" {
		m.historyList.Select(0)
		return m.selectedHistoryKey() != selectedKey
	}
	for i, item := range m.historyList.Items() {
		if row, ok := item.(rowItem); ok && row.key == selectedKey {
			m.historyList.Select(i)
			return m.selectedHistoryKey() != selectedKey
		}
	}
	m.historyList.Select(0)
	return m.selectedHistoryKey() != selectedKey
}

// historyFilterInput removes Kitty image responses even when Bubble Tea splits
// one response across several key events. Holding a possible prefix prevents
// terminal protocol bytes from changing the query or causing another redraw.
func (m *modelImpl) historyFilterInput(input string) string {
	const responsePrefix = "_Gi="
	var output strings.Builder
	for _, current := range input {
		if m.historyDroppingTerminalResponse {
			if current == '\\' {
				m.historyDroppingTerminalResponse = false
			}
			continue
		}

		candidate := m.historyTerminalInputPrefix + string(current)
		if candidate == responsePrefix {
			m.historyTerminalInputPrefix = ""
			m.historyDroppingTerminalResponse = true
			continue
		}
		if strings.HasPrefix(responsePrefix, candidate) {
			m.historyTerminalInputPrefix = candidate
			continue
		}

		if m.historyTerminalInputPrefix != "" {
			output.WriteString(m.historyTerminalInputPrefix)
			m.historyTerminalInputPrefix = ""
		}
		if current == '_' {
			m.historyTerminalInputPrefix = "_"
			continue
		}
		output.WriteRune(current)
	}
	return output.String()
}

func (m *modelImpl) selectedHistoryKey() string {
	if item, ok := m.historyList.SelectedItem().(rowItem); ok {
		return item.key
	}
	return ""
}

func removeLastRune(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}
	return string(runes[:len(runes)-1])
}

func removeLastWord(value string) string {
	trimmed := strings.TrimRight(value, " \t\n")
	lastSpace := strings.LastIndexAny(trimmed, " \t\n")
	if lastSpace < 0 {
		return ""
	}
	return strings.TrimRight(trimmed[:lastSpace], " \t\n")
}

func (m *modelImpl) cycleHistoryMode(direction int) tea.Cmd {
	filters := historyModeFilters(m.historyGroups, m.historyGroups)
	if len(filters) <= 1 {
		return nil
	}
	active := 0
	for i, filter := range filters {
		if filter.Mode == m.historyModeFilter {
			active = i
			break
		}
	}
	next := (active + direction + len(filters)) % len(filters)
	m.historyModeFilter = filters[next].Mode
	m.historyTabIndex[m.historyTab] = 0
	m.refreshHistoryItems()
	m.historyList.Select(0)
	m.clearStatus()
	return m.triggerHistoryPoster()
}

// historySelectedIndex resolves the history list cursor to a result
// index, or -1 when the list is empty.
func (m *modelImpl) historySelectedIndex() int {
	if item, ok := m.historyList.SelectedItem().(rowItem); ok {
		return item.index
	}
	return m.historyList.Index()
}

// cycleHistoryTab moves between Continue and Finished, restoring the
// destination tab's remembered cursor.
func (m *modelImpl) cycleHistoryTab(reverse bool) tea.Cmd {
	m.historyTabIndex[m.historyTab] = m.historyList.Index()
	direction := 1
	if reverse {
		direction = -1
	}
	m.historyTab = (m.historyTab + direction + 2) % 2
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
	if m.historyList.Width() == 0 && m.historyList.Height() == 0 {
		return
	}
	m.normalizeHistoryModeFilter()
	continued, finished := splitHistoryGroups(m.historyGroups)
	if m.historyTab == historyTabFinished {
		groups := filterHistoryGroups(sortGroupsByDateBucket(finished, true), m.historyModeFilter)
		groups = searchHistoryGroups(groups, m.historyFilter)
		m.historyList.SetItems(historyTabItems(groups, true))
	} else {
		groups := filterHistoryGroups(sortGroupsByDateBucket(continued, false), m.historyModeFilter)
		groups = searchHistoryGroups(groups, m.historyFilter)
		m.historyList.SetItems(historyTabItems(groups, false))
	}
}

func (m *modelImpl) normalizeHistoryModeFilter() {
	if m.historyModeFilter == "" {
		return
	}
	for _, filter := range historyModeFilters(m.historyGroups, m.historyGroups) {
		if filter.Mode == m.historyModeFilter {
			return
		}
	}
	m.historyModeFilter = ""
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
	if entry.AniListID > 0 {
		aniStr := strconv.Itoa(entry.AniListID)
		for _, r := range results {
			if strings.TrimSpace(r.ID) == aniStr {
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
