package tui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/provider"
	"kari/internal/termimg"
)

const (
	// maxContentWidth is declared in column.go alongside ComputeDims so the
	// frame layout and legacy list sizing always agree.
	narrowTerminalThreshold = 90
)

type layoutDims struct {
	contentW int
	bodyW    int
	bodyH    int
}

func (m *modelImpl) computeLayoutDims() layoutDims {
	contentW := m.width - 4
	if contentW < 36 {
		contentW = max(20, m.width-2)
	}
	if contentW > maxContentWidth {
		contentW = maxContentWidth
	}
	return layoutDims{
		contentW: contentW,
		bodyW:    contentW,
		bodyH:    m.bodyHeight(),
	}
}

func (m *modelImpl) bodyHeight() int {
	// Stable layout height: header (1), rule (1), body spacer (1), bottom spacer (1),
	// status/loading reserved slot (2), and footer (1) = 7 rows.
	// Kept constant so loading states and status banners never resize lists or shift
	height := m.height - 7
	return max(1, height)
}

func (m *modelImpl) scrollBody(delta int) {
	m.bodyScroll = max(0, m.bodyScroll+delta)
}

func (m *modelImpl) resizeLists() {
	dims := m.computeLayoutDims()
	w := max(20, dims.bodyW-4)
	seriesH := max(1, dims.bodyH-6)
	episodeH := max(1, dims.bodyH-5)
	historyH := max(1, dims.bodyH-5)

	seriesW := max(20, searchLeftWidth(dims.bodyW)-4)
	m.seriesList.SetSize(seriesW, seriesH)
	m.episodeList.SetSize(w, episodeH)
	m.chapterList.SetSize(w, episodeH)
	m.historyList.SetSize(w, historyH)

	inputW := max(15, dims.contentW-12)
	m.queryInput.Width = inputW
	m.authInput.Width = inputW
}

func (m *modelImpl) setStatus(level statusLevel, text string) {
	m.statusType = level
	m.statusID++
	if text == "" {
		m.statusText = ""
		m.statusExpiresAt = time.Time{}
		return
	}
	m.statusText = text
	m.statusExpiresAt = time.Now().Add(statusClearDuration(level))
}

func (m *modelImpl) clearStatus() {
	m.statusText = ""
	m.statusType = statusInfo
	m.statusID++
	m.statusExpiresAt = time.Time{}
	m.activeToast = nil
	m.confirmQuit = false
	m.confirmStop = false
}

// statusDuration* give every auto-clearing status message a consistent
// lifetime by severity, instead of the ad hoc 3s/5s/7s/8s literals that
// used to be picked independently at each call site — errors/warnings
// linger longer since they're more likely to need re-reading.
const (
	statusDurationInfo    = 3 * time.Second
	statusDurationSuccess = 4 * time.Second
	statusDurationWarn    = 5 * time.Second
	statusDurationError   = 6 * time.Second
)

func statusClearDuration(level statusLevel) time.Duration {
	switch level {
	case statusError:
		return statusDurationError
	case statusWarn:
		return statusDurationWarn
	case statusSuccess:
		return statusDurationSuccess
	default:
		return statusDurationInfo
	}
}

// setStatusTimed sets the status line and returns a Cmd that clears it
// after statusClearDuration(level), so callers that want an auto-clearing
// status don't each pick their own duration.
func (m *modelImpl) setStatusTimed(level statusLevel, text string) tea.Cmd {
	m.setStatus(level, text)
	return m.clearStatusAfter(statusClearDuration(level))
}

func (m *modelImpl) clearStatusAfter(d time.Duration) tea.Cmd {
	id := m.statusID
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return resetStatusMsg{id: id}
	})
}

func (m *modelImpl) setToast(msg string, ttype ToastType) {
	m.activeToast = NewToast(msg, ttype, 3*time.Second)
}

func (m *modelImpl) pushView(next viewState) {
	if m.activeView == next {
		return
	}

	m.clearStatus()

	// Prevent duplicate entries in backstack (e.g. going from preview to preview)
	if len(m.backStack) > 0 && m.backStack[len(m.backStack)-1] == next {
		// If we are "going back" but used pushView, just pop instead
		m.activeView = next
		m.backStack = m.backStack[:len(m.backStack)-1]
		return
	}

	m.backStack = append(m.backStack, m.activeView)
	m.activeView = next
}

func (m *modelImpl) goBackOne() bool {
	if len(m.backStack) == 0 {
		return false
	}
	m.clearStatus()
	if m.activeView == viewPreview {
		m.clearPreviewPoster()
	}
	if m.activeView == viewReader {
		m.readerOpID = m.newOpID()
		m.readerRender = make(map[int]string)
	}
	prev := m.backStack[len(m.backStack)-1]
	m.backStack = m.backStack[:len(m.backStack)-1]
	m.activeView = prev
	if m.activeView == viewSearch {
		m.playOpID = 0
		m.resolveOpID = 0
		m.subtitleOpID = 0
		m.autoPlayAfterResolve = false
		m.loading = false
		m.loadingText = ""
	}
	return true
}

// currentResumePosition returns the saved playback position for the
// episode currently in Preview, formatted mm:ss; empty when none.
func (m *modelImpl) currentResumePosition() string {
	if m.historyStore == nil || m.resolved == nil {
		return ""
	}
	entry, ok := m.historyStore.Get(history.EntryKey{
		Title:     m.resolved.SeriesTitle,
		Mode:      string(m.appMode),
		MediaType: m.resolved.MediaType,
		Season:    m.resolved.SeasonNumber,
		Episode:   m.resolved.EpisodeNumber,
	})
	if !ok || entry.PositionSecs <= 0 || entry.Complete {
		return ""
	}
	return formatTimeMMSS(entry.PositionSecs)
}

// hasResumePosition reports whether the episode in Preview has a
// unfinished resume point, which gates the restart key in the footer.
func (m *modelImpl) hasResumePosition() bool {
	return m.currentResumePosition() != ""
}

// toggleAnimeAudio flips the preferred anime track (sub/dub) and re-runs
// the episode listing so the selection takes effect. Non-anime modes have
// nothing to toggle.
func (m *modelImpl) toggleAnimeAudio() (tea.Model, tea.Cmd) {
	if m.appMode != provider.ModeAnime {
		m.setStatus(statusWarn, "Sub/dub toggle applies to anime only")
		return m, nil
	}
	if strings.EqualFold(m.audioMode, provider.AudioDub) {
		m.audioMode = provider.AudioSub
	} else {
		m.audioMode = provider.AudioDub
	}
	m.saveSettings()
	m.setToast("audio track: "+m.audioMode, ToastInfo)

	if m.selectedSeries == nil {
		return m, nil
	}

	// If on Preview screen, reload streams for current episode with new audio track.
	// The previous track's resolved state is fully cleared first: merging
	// the fresh resolve into stale Playback would leave the old audio's
	// rows in the list.
	if m.activeView == viewPreview && m.selectedEpisode != nil {
		m.selectedEpisode.Audio = m.audioMode
		m.loading = true
		m.loadingText = "Reloading streams (" + m.audioMode + ")..."
		m.resolved = nil
		m.rawSubtitles = nil
		m.manualPlaybackSelected = false
		m.resolveAttempts = 0
		m.preferredRepairAttempts = 0
		m.rankedSources = nil
		m.previewSelectedIndex = 0
		m.subtitleResolverUsed = ""
		m.subtitleLangUsed = ""
		m.subtitleSourceUsed = ""
		m.beginProviderWait(m.appMode)
		opID := m.newOpID()
		m.resolveOpID = opID
		return m, tea.Batch(m.spinner.Tick, m.resolveCmd(opID, *m.selectedSeries, *m.selectedEpisode, nil, nil))
	}

	m.loading = true
	m.loadingText = "Reloading episodes..."
	opID := m.newOpID()
	m.episodesOpID = opID
	return m, tea.Batch(m.spinner.Tick, m.episodesCmd(opID, *m.selectedSeries))
}

func (m *modelImpl) nextEpisodeIndex() (int, bool) {
	if m.selectedSeries == nil || len(m.episodeResults) == 0 {
		return 0, false
	}
	idx := m.episodeIndex + 1
	if idx >= len(m.episodeResults) {
		return 0, false
	}
	return idx, true
}

func (m *modelImpl) newOpID() int {
	m.nextOpID++
	return m.nextOpID
}

func shorten(text string, maxWidth int) string {
	if text == "" || maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= maxWidth {
		return text
	}
	r := []rune(text)
	if len(r) <= 3 {
		return text
	}
	cut := maxWidth - 3
	if cut < 1 {
		cut = 1
	}
	if cut > len(r) {
		cut = len(r)
	}
	return string(r[:cut]) + "..."
}

func searchLeftWidth(contentW int) int {
	if contentW <= narrowTerminalThreshold {
		return contentW
	}
	return contentW * 65 / 100
}

// anilistIDFor extracts the AniList catalog id for an anime result:
// anime providers already key their catalogs by it, so persisting it on
// history entries lets tracker sync match by id instead of title.
// Anything else (other modes, non-numeric ids) reports zero.
func anilistIDFor(mode provider.ContentType, series *provider.SearchResult) int {
	if mode != provider.ModeAnime || series == nil {
		return 0
	}
	id, err := strconv.Atoi(strings.TrimSpace(series.ID))
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// posterPlaceholderWidth reserves the poster column so the header never
// collapses while artwork loads or when none exists.
const posterPlaceholderWidth = 30

func posterBlock(rendered string, unavailable bool, protocol termimg.Protocol, imageID uint32, imagesEnabled bool) string {
	if imagesEnabled && rendered != "" {
		return rendered
	}
	if imagesEnabled {
		// Reserve the column with explicit feedback instead of
		// collapsing the header: loading artwork vs confirmed absent.
		// The slot cleanup still runs first so a stale placement from
		// the previous title never lingers behind the placeholder.
		if unavailable {
			return protocol.Cleanup(imageID) + posterPlaceholder("no image found")
		}
		return protocol.Cleanup(imageID) + posterPlaceholder("loading image…")
	}
	return protocol.Cleanup(imageID)
}

// posterPlaceholder renders a fixed-width dim stand-in keeping the
// poster column stable across loading → loaded → absent states.
// scrubTerminalResponses strips stray Kitty graphics-protocol responses
// (e.g. `_Gi=1;OK\`) that some terminals emit asynchronously after an
// image transmit: they arrive on stdin as ordinary runes and a focused
// text input would otherwise eat them as typed text. The `i=<id>` key
// requirement keeps human typing safe.
func scrubTerminalResponses(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		i := strings.Index(s, "_G")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		rest := s[i:]
		end := 2
		for end < len(rest) && end < 64 && rest[end] != '\\' {
			end++
		}
		if end < len(rest) && rest[end] == '\\' {
			end++
		}
		token := rest[:end]
		if !strings.Contains(token, "i=") {
			b.WriteString(token)
		}
		s = rest[end:]
	}
	return b.String()
}

func posterPlaceholder(text string) string {
	dim := lipgloss.NewStyle().Faint(true)
	padded := text + strings.Repeat(" ", max(0, posterPlaceholderWidth-lipgloss.Width(text)))
	return dim.Render(padded)
}

// languageEnabled reports whether an audio language is enabled: a nil
// map, an absent key, or an explicit true all mean enabled; only an
// explicit false disables. (Committed-code scheme: the filter records
// overrides, never a full set.)
func (m *modelImpl) languageEnabled(lang string) bool {
	if m.languageFilter == nil {
		return true
	}
	enabled, ok := m.languageFilter[lang]
	if !ok {
		return true
	}
	return enabled
}

// hasEnabledLanguage guards the loaded filter against a degenerate
// "everything disabled" state. It deliberately checks the full movies/TV
// language pool — not the active mode's slice — because at startup the
// active mode may be one with no audio languages at all (anime), where
// checking locally would wrongly conclude every language was disabled and
// wipe the user's saved filter.
func (m *modelImpl) hasEnabledLanguage() bool {
	for _, l := range m.registry.AudioLanguages(provider.ModeMovies, provider.ModeTV) {
		if m.languageEnabled(l.Code) {
			return true
		}
	}
	return false
}

func distinctSeasonNumbers(episodes []provider.Episode) []int {
	seen := make(map[int]bool)
	var seasons []int
	for _, ep := range episodes {
		s := ep.Season
		if s <= 0 {
			s = 1
		}
		if !seen[s] {
			seen[s] = true
			seasons = append(seasons, s)
		}
	}
	slices.Sort(seasons)
	return seasons
}

func (m *modelImpl) currentSeasonEpisodes() ([]provider.Episode, []int) {
	if len(m.episodeResults) == 0 {
		return nil, nil
	}
	seasons := distinctSeasonNumbers(m.episodeResults)
	if len(seasons) <= 1 {
		indices := make([]int, len(m.episodeResults))
		for i := range indices {
			indices[i] = i
		}
		return FilterEpisodesByText(m.episodeResults, indices, m.episodeFilter)
	}

	activeIdx := m.activeSeason
	if activeIdx < 0 {
		activeIdx = 0
	}
	if activeIdx >= len(seasons) {
		activeIdx = len(seasons) - 1
	}
	targetSeason := seasons[activeIdx]

	var eps []provider.Episode
	var indices []int
	for origIdx, ep := range m.episodeResults {
		s := ep.Season
		if s <= 0 {
			s = 1
		}
		if s == targetSeason {
			eps = append(eps, ep)
			indices = append(indices, origIdx)
		}
	}
	// Text filter shares the screens helper with the renderer so the
	// cursor index always addresses the same rows on screen.
	return FilterEpisodesByText(eps, indices, m.episodeFilter)
}

// clampEpisodeIndex keeps the cursor inside the visible (season- and
// text-filtered) list.
func (m *modelImpl) clampEpisodeIndex() {
	eps, _ := m.currentSeasonEpisodes()
	if m.seasonEpisodeIndex < 0 || len(eps) == 0 {
		m.seasonEpisodeIndex = 0
		return
	}
	if m.seasonEpisodeIndex >= len(eps) {
		m.seasonEpisodeIndex = len(eps) - 1
	}
}
