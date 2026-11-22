package tui

import (
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/provider"
	"kari/internal/service"
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
	height := m.height - 7
	return max(1, height)
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
	// The chapter list is sized per frame in collectChaptersData from the
	// frame body height; a resize-time value here would be overwritten and
	// disagree by a few rows.
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
	m.toastID++
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

func (m *modelImpl) setToast(msg string, ttype ToastType) tea.Cmd {
	m.toastID++
	id := m.toastID
	m.activeToast = NewToast(msg, ttype, 3*time.Second)
	return tea.Tick(2650*time.Millisecond, func(t time.Time) tea.Msg {
		return toastFadeTickMsg{id: id}
	})
}

func (m *modelImpl) pushView(next viewState) {
	if m.activeView == next {
		return
	}

	m.clearStatus()
	m.resetScrollGate()

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
	m.resetScrollGate()
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
		m.cancelSubtitleAdds()
		m.loadedSubtitlePaths = nil
		m.hotAddPendingPath = ""
		m.resolveOpID = 0
		m.cancelResolve()
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
	m.toggleAnimeAudioTrack()
	m.saveSettings()

	if m.selectedSeries == nil {
		return m, nil
	}

	if m.activeView == viewPreview && m.selectedEpisode != nil {
		m.selectedEpisode.Audio = normalizeAudioMode(m.audioMode)
		m.loading = true
		m.loadingText = "Reloading streams (" + m.audioMode + ")..."
		m.resolved = nil
		m.rawSubtitles = nil
		m.cancelSubtitleAdds()
		m.loadedSubtitlePaths = nil
		m.hotAddPendingPath = ""
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
	if series.AniListID > 0 {
		return series.AniListID
	}
	// Providers that do not populate AniListID key anime catalogs by the
	// numeric id in ID, so keep that path for backward compatibility.
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
		if unavailable {
			return protocol.Cleanup(imageID) + posterPlaceholder("no image found")
		}
		return protocol.Cleanup(imageID) + posterPlaceholder("loading image…")
	}
	return protocol.Cleanup(imageID)
}

// scrubTerminalResponses strips stray Kitty graphics-protocol responses
// (e.g. `_Gi=1;OK\`) that some terminals emit asynchronously after an
// image transmit: they arrive on stdin as ordinary runes and a focused
// text input would otherwise eat them as typed text. The `i=<id>` key
// requirement keeps human typing safe.
func scrubTerminalResponses(s string) string {
	cleaned, _ := scrubTerminalResponseChunk(s)
	return cleaned
}

func scrubTerminalResponseChunk(s string) (string, bool) {
	var b strings.Builder
	discardTerminator := false
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
		terminated := end < len(rest) && rest[end] == '\\'
		if terminated {
			end++
		}
		token := rest[:end]
		if !strings.Contains(token, "i=") {
			b.WriteString(token)
		} else if !terminated && end == len(rest) {
			discardTerminator = true
		}
		s = rest[end:]
	}
	return b.String(), discardTerminator
}

// posterPlaceholder renders the loading/absent stand-in text. It does not pad
// to a fixed width: the caller's fixed-slot helper owns the slot geometry, and
// pre-padding here gets truncated into an ellipsis in narrower panes.
func posterPlaceholder(text string) string {
	return lipgloss.NewStyle().Faint(true).Render(text)
}

// languageEnabled reports whether an audio language is enabled according to
// the authoritative typed scope. Unknown and absent codes stay enabled; only
// an explicit disabled canonical code removes a language.
func (m *modelImpl) languageEnabled(code string) bool {
	return m.filterScope.Defaults.Audio.Allows(code)
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
	return service.EpisodeSeasons(episodes)
}

func (m *modelImpl) episodeSeasons() []int {
	m.ensureEpisodeListCache()
	return m.episodeCache.seasons
}

func (m *modelImpl) currentSeasonEpisodes() ([]provider.Episode, []int) {
	m.ensureEpisodeListCache()
	cache := &m.episodeCache
	if cache.season != m.activeSeason || cache.filter != m.episodeFilter {
		cache.filtered, cache.indices = service.FilterEpisodeList(m.episodeResults, m.activeSeason, m.episodeFilter)
		cache.season = m.activeSeason
		cache.filter = m.episodeFilter
	}
	return cache.filtered, cache.indices
}

func (m *modelImpl) hasCurrentEpisodeProjection() bool {
	cache := &m.episodeCache
	return cache.valid && episodeSlicesShareSource(cache.source, m.episodeResults) &&
		cache.season == m.activeSeason && cache.filter == m.episodeFilter
}

func (m *modelImpl) ensureEpisodeListCache() {
	cache := &m.episodeCache
	if cache.valid && episodeSlicesShareSource(cache.source, m.episodeResults) {
		return
	}
	*cache = episodeListCache{
		source:  m.episodeResults,
		seasons: service.EpisodeSeasons(m.episodeResults),
		season:  m.activeSeason,
		filter:  m.episodeFilter,
		valid:   true,
	}
	cache.filtered, cache.indices = service.FilterEpisodeList(m.episodeResults, m.activeSeason, m.episodeFilter)
}

func episodeSlicesShareSource(left, right []provider.Episode) bool {
	if len(left) != len(right) {
		return false
	}
	return len(left) == 0 || &left[0] == &right[0]
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
