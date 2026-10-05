package tui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/manga"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/poster"
	"kari/internal/provider"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/settings"
	"kari/internal/termimg"
	"kari/internal/util"
)

// tuiLog scopes every log line emitted by the TUI layer.
var tuiLog = logging.With("component", "tui")

// NewModel wires every dependency into the TUI root model. This signature
// is intentionally explicit: all components arrive pre-constructed from
// app.Run, so nothing inside tui constructs I/O collaborators.
func NewModel(ctx context.Context, initialQuery string, registry *provider.Registry, players *player.Registry, downloadDir string, mediaService *service.MediaService, mangaService *service.MangaService, mangaClient *manga.Client, downloadService *service.DownloadService, subtitleService *service.SubtitleService, historyStore *history.Store, historyLoadErr error, traktClient *scrobble.TraktClient, anilistClient *scrobble.AniListClient, posterClient *poster.Client, appVersion string, appCommit string) tea.Model {
	// Loaded up front (rather than where settings used to be applied,
	// further down) so the accent color is in effect before any of the
	// list delegates or the download bar below are built — those cache
	// colorPrimary at construction time, not at render time.
	savedSettings := settings.Load()
	if savedSettings != nil {
		if normalized, ok := normalizeHexColor(savedSettings.AccentColor); ok {
			SetAccentColor(normalized)
		}
	}

	ti := textinput.New()
	ti.CharLimit = 150
	ti.Width = 70
	ti.SetValue(strings.TrimSpace(initialQuery))
	ti.Placeholder = "search…"
	ti.Prompt = "› "
	ti.Focus()

	seriesDelegate := list.NewDefaultDelegate()
	seriesDelegate.ShowDescription = true
	seriesDelegate.Styles.SelectedTitle = seriesDelegate.Styles.SelectedTitle.
		Foreground(colorPrimary).
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(colorPrimary)
	seriesDelegate.Styles.NormalTitle = seriesDelegate.Styles.NormalTitle.
		Foreground(colorText)
	seriesDelegate.Styles.SelectedDesc = seriesDelegate.Styles.SelectedDesc.Foreground(colorMuted)
	seriesDelegate.Styles.NormalDesc = seriesDelegate.Styles.NormalDesc.Foreground(colorMuted)

	episodeDelegate := list.NewDefaultDelegate()
	episodeDelegate.ShowDescription = false
	episodeDelegate.SetHeight(1)
	episodeDelegate.Styles.SelectedTitle = episodeDelegate.Styles.SelectedTitle.
		Foreground(colorPrimary).
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(colorPrimary).
		PaddingLeft(1)
	episodeDelegate.Styles.NormalTitle = episodeDelegate.Styles.NormalTitle.
		Foreground(colorText).
		BorderLeft(true).
		BorderStyle(lipgloss.HiddenBorder()).
		PaddingLeft(1)

	seriesList := list.New([]list.Item{}, seriesDelegate, 80, 16)
	seriesList.Title = ""
	seriesList.SetFilteringEnabled(true)
	seriesList.SetShowStatusBar(false)
	seriesList.SetShowPagination(false)
	seriesList.SetShowHelp(false)
	seriesList.SetShowTitle(false)

	episodeList := list.New([]list.Item{}, episodeDelegate, 80, 16)
	episodeList.Title = ""
	episodeList.SetFilteringEnabled(true)
	episodeList.SetShowStatusBar(false)
	episodeList.SetShowPagination(false)
	episodeList.SetShowHelp(false)
	episodeList.SetShowTitle(false)

	// Chapters read like episodes (one selectable row per unit) but carry
	// manga numbering, so they get their own list rather than sharing the
	// episode list's items.
	chapterDelegate := list.NewDefaultDelegate()
	chapterDelegate.ShowDescription = false
	chapterDelegate.SetHeight(1)
	chapterDelegate.Styles.SelectedTitle = chapterDelegate.Styles.SelectedTitle.
		Foreground(colorPrimary).
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(colorPrimary).
		PaddingLeft(1)
	chapterDelegate.Styles.NormalTitle = chapterDelegate.Styles.NormalTitle.
		Foreground(colorText).
		BorderLeft(true).
		BorderStyle(lipgloss.HiddenBorder()).
		PaddingLeft(1)

	chapterList := list.New([]list.Item{}, chapterDelegate, 80, 16)
	chapterList.Title = ""
	chapterList.SetFilteringEnabled(true)
	chapterList.SetShowStatusBar(false)
	chapterList.SetShowPagination(false)
	chapterList.SetShowHelp(false)
	chapterList.SetShowTitle(false)

	historyDelegate := list.NewDefaultDelegate()
	historyDelegate.ShowDescription = true
	historyDelegate.Styles.SelectedTitle = historyDelegate.Styles.SelectedTitle.
		Foreground(colorPrimary).
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(colorPrimary).
		PaddingLeft(1)
	historyDelegate.Styles.NormalTitle = historyDelegate.Styles.NormalTitle.
		Foreground(colorText).
		BorderLeft(true).
		BorderStyle(lipgloss.HiddenBorder()).
		PaddingLeft(1)
	historyDelegate.Styles.SelectedDesc = historyDelegate.Styles.SelectedDesc.Foreground(colorMuted).PaddingLeft(1)
	historyDelegate.Styles.NormalDesc = historyDelegate.Styles.NormalDesc.Foreground(colorMuted).PaddingLeft(1)

	historyList := list.New([]list.Item{}, historyDelegate, 80, 16)
	historyList.Title = ""
	historyList.SetFilteringEnabled(true)
	historyList.SetShowStatusBar(false)
	historyList.SetShowPagination(false)
	historyList.SetShowHelp(false)
	historyList.SetShowTitle(false)

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	downloadBar := newDownloadBar()

	ai := textinput.New()
	ai.Placeholder = "Paste code here"
	ai.CharLimit = 4096

	hexInput := textinput.New()
	hexInput.Prompt = "#"
	hexInput.Placeholder = "be95ff"
	hexInput.CharLimit = 6
	hexInput.Width = 10

	modes := registry.AllModes()
	initialMode := provider.ContentType("")
	if len(modes) > 0 {
		initialMode = modes[0]
	}

	model := &modelImpl{
		mediaService:    mediaService,
		mangaService:    mangaService,
		mangaClient:     mangaClient,
		subtitleService: subtitleService,
		downloadService: downloadService,
		historyStore:    historyStore,
		traktClient:     traktClient,
		anilistClient:   anilistClient,
		appCtx:          ctx,
		appVersion:      appVersion,
		appCommit:       appCommit,
		activeView:      viewSearch,
		queryInput:      ti,
		authInput:       ai,
		hexInput:        hexInput,
		seriesList:      seriesList,
		episodeList:     episodeList,
		chapterList:     chapterList,
		historyList:     historyList,
		spinner:         sp,
		downloadBar:     downloadBar,
		readerRender:    make(map[int]string),

		keys:             defaultKeyMap(),
		searchQuery:      strings.TrimSpace(initialQuery),
		appMode:          initialMode,
		registry:         registry,
		modes:            modes,
		players:          players,
		availablePlayers: players.AvailablePlayers(),
		searchCache:      util.NewBoundedCache[searchCacheEntry](60),
		downloadChan:     make(chan tea.Msg, 10),
		resolveChan:      make(chan tea.Msg, 50),
		configuredModes:  (&settings.Data{}).NormalizedModes(),
		disabledModes:    make(map[string]bool),
		transitions:      true,
		baseBgColor:      HexToRGBA("#0B0D10"),
		qualityMode:      qualityAll,
		languageFilter:   make(map[string]bool),
		audioMode:        provider.AudioSub,
		subtitleLanguage: "en",
		skipProvider:     "hybrid",
		selectedEpisodes: make(map[int]struct{}),
		batchChan:        make(chan tea.Msg, 50),
		posterClient:     posterClient,
		imgProtocol:      termimg.Detect(),
		imagesEnabled:    true,
		// Rendered poster strings, not the images themselves — for the Kitty
		// protocol these are base64-encoded PNGs and can be well over 1MB
		// each, so this stays smaller than the image caches upstream in
		// internal/poster to keep a long browsing session's memory bounded.
		posterCache: util.NewBoundedCache[string](30),
	}
	model.selectedPlayer = model.defaultPlayerIndex()
	if downloadService != nil {
		job, err := downloadService.PendingJob()
		if err != nil {
			tuiLog.Warn("load pending download failed", "err", err)
		} else if job != nil {
			model.pendingDownload = job
			model.downloadPaused = true
			model.appMode = job.Mode
		}
	}
	if s := savedSettings; s != nil {
		if s.QualityMode >= qualityAll && s.QualityMode <= qualityLowest {
			model.qualityMode = s.QualityMode
		}
		if s.DownloadQuality >= downloadQualityAuto && s.DownloadQuality <= downloadQualitySD {
			model.downloadQuality = s.DownloadQuality
		}
		if len(s.LanguageFilter) > 0 {
			// Saved filters only ever record overrides (a language the user
			// explicitly disabled) — anything absent from the map is still
			// implicitly enabled, per languageEnabled. So checking the map's
			// values directly for a literal `true` rejects the common case
			// of a user disabling just one or two languages, since every
			// entry in that map is `false`. Apply it and ask
			// hasEnabledLanguage, which understands that "absent" means
			// "enabled", instead.
			prev := model.languageFilter
			model.languageFilter = s.LanguageFilter
			if !model.hasEnabledLanguage() {
				model.languageFilter = prev
			}
		}
		if code := lang.Normalize(s.SubtitleLanguage); code != "" {
			model.subtitleLanguage = code
		}
		if s.AccentColor != "" && !strings.EqualFold(s.AccentColor, "auto") {
			if normalized, ok := normalizeHexColor(s.AccentColor); ok {
				model.accentIndex = len(accentPresets) // default: custom slot
				for i, preset := range accentPresets {
					if strings.EqualFold(preset.hex, normalized) || strings.EqualFold(preset.name, s.AccentColor) {
						model.accentIndex = i
						normalized = ""
						break
					}
				}
				model.customAccentHex = normalized // "" when it matched a preset
			}
		} else {
			model.accentIndex = 0 // "Auto (per mode)"
		}
		if s.SkipProvider != "" {
			model.skipProvider = s.SkipProvider
		}
		model.autoSkipIntro = s.AutoSkipIntro
		model.autoSkipEnding = s.AutoSkipEnding
		model.skipRecap = s.SkipRecap
		model.skipPreview = s.SkipPreview
		if s.DefaultAnimeAudio != "" {
			model.audioMode = s.DefaultAnimeAudio
		}
		if s.PreferredPlayer != "" {
			for i, p := range model.availablePlayers {
				if strings.EqualFold(p, s.PreferredPlayer) {
					model.selectedPlayer = i
					break
				}
			}
		}
		model.autoplay = s.Autoplay
		model.imagesEnabled = !s.DisableImages
		model.disableAnimeSubtitles = s.DisableAnimeSubtitles
		if model.subtitleService != nil {
			model.subtitleService.SetDisableAnimeSubtitles(model.disableAnimeSubtitles)
		}
		model.startupSync = s.StartupSync
		model.defaultMode = s.DefaultMode
		model.configuredModes = s.NormalizedModes()
		model.disabledModes = s.DisabledModeSet()
		model.transitions = s.TransitionsEnabled()
		model.updateEffectiveModes()

		startupMode := strings.ToLower(strings.TrimSpace(s.DefaultMode))
		switch startupMode {
		case "", "last":
			if s.LastMode != "" && !model.disabledModes[s.LastMode] && model.isModeAvailable(s.LastMode) {
				model.appMode = provider.ContentType(s.LastMode)
			} else if len(model.modes) > 0 {
				model.appMode = model.modes[0]
			}
		case "first":
			if len(model.modes) > 0 {
				model.appMode = model.modes[0]
			}
		default:
			if !model.disabledModes[startupMode] && model.isModeAvailable(startupMode) {
				model.appMode = provider.ContentType(startupMode)
			} else if len(model.modes) > 0 {
				model.appMode = model.modes[0]
			}
		}
	}
	model.updateQueryPlaceholder()
	for i, code := range lang.SubtitleOptions {
		if code == model.subtitleLanguage {
			model.subtitleLanguageIndex = i
			break
		}
	}
	tuiLog.Info("image protocol detected", "protocol", model.imgProtocol.String())
	if historyLoadErr != nil {
		// historyStore is nil in this case, silently disabling watch
		// history/resume for the whole session — surface it instead of
		// leaving the user to wonder why "Continue Watching" is empty.
		model.setStatus(statusWarn, "Watch history unavailable: "+historyLoadErr.Error())
	}
	return model
}

func (m *modelImpl) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, m.spinner.Tick}
	if m.searchQuery != "" {
		m.loading = true
		m.loadingText = "Searching..."
		opID := m.newOpID()
		m.searchOpID = opID
		cmds = append(cmds, m.searchCmd(opID, m.searchQuery))
	}
	if m.statusText != "" {
		cmds = append(cmds, m.clearStatusAfter(statusClearDuration(m.statusType)))
	}
	cmds = append(cmds, m.checkForUpdateCmd())
	// Pull tracker state into local history on startup if enabled.
	if m.startupSync {
		if cmd := m.autoHistoryImportCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

type historyLoadedMsg struct{}

func (m *modelImpl) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	prevStatusID := m.statusID
	prevView := m.activeView
	prevMode := m.appMode

	mdl, cmd := m.updateMessage(msg)

	if updated, ok := mdl.(*modelImpl); ok {
		if updated.activeView != prevView || updated.appMode != prevMode {
			if updated.statusID == prevStatusID {
				updated.clearStatus()
			}
		}
		if updated.statusID != prevStatusID && updated.statusText != "" {
			cmd = tea.Batch(cmd, updated.clearStatusAfter(statusClearDuration(updated.statusType)))
		}
	}

	return mdl, cmd
}

func (m *modelImpl) updateMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	var spinnerCmd tea.Cmd
	// Keep the spinner animating while providers stream sources in
	// progressively: loading flips false on the first partial results, but
	// resolution isn't done until resolveOpID resets to zero.
	if m.loading || m.resolveOpID != 0 {
		m.spinner, spinnerCmd = m.spinner.Update(msg)
	}
	switch msg := msg.(type) {
	case themeCrossfadeTickMsg:
		if msg.opID == m.crossfadeOpID && m.crossfadeActive {
			m.crossfadeStep++
			if m.crossfadeStep >= 10 {
				m.crossfadeActive = false
				return m, nil
			}
			opID := m.crossfadeOpID
			step := m.crossfadeStep
			return m, tea.Tick(18*time.Millisecond, func(t time.Time) tea.Msg {
				return themeCrossfadeTickMsg{opID: opID, step: step + 1}
			})
		}
		return m, nil
	case updateCheckMsg:
		return m.onUpdateCheck(msg)
	case historyLoadedMsg:
		m.loading = false
		m.loadingText = ""
		m.pushView(viewHistory)
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeLists()
		// Page renders are dimension-specific; a resize invalidates them
		// and re-renders the current page at the new size.
		if m.activeView == viewReader {
			m.readerRender = make(map[int]string)
			m.readerCols, m.readerRows = 0, 0
			return m, tea.Batch(spinnerCmd, m.gotoReaderPage(m.readerPage))
		}
		return m, spinnerCmd

	case tea.KeyMsg:
		if cmd, handled := m.handleGlobalKeys(msg); handled {
			return m, tea.Batch(spinnerCmd, cmd)
		}

	case searchDoneMsg:
		mdl, cmd := m.onSearchDone(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case episodesDoneMsg:
		mdl, cmd := m.onEpisodesDone(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case episodeTitlesMsg:
		mdl, cmd := m.onEpisodeTitles(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case historyImportMsg:
		mdl, cmd := m.onHistoryImport(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case historyContinueEpisodesMsg:
		mdl, cmd := m.onHistoryContinueEpisodes(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case historyResolveSeriesMsg:
		mdl, cmd := m.onHistoryResolveSeries(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case resolveDoneMsg:
		mdl, cmd := m.onResolveDone(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case subtitleDoneMsg:
		mdl, cmd := m.onSubtitleDone(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case resolveProgressMsg:
		mdl, cmd := m.onResolveProgress(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case resolveWorkerDoneMsg:
		return m, spinnerCmd
	case playDoneMsg:
		mdl, cmd := m.onPlayDone(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case downloadDoneMsg:
		mdl, cmd := m.onDownloadDone(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case downloadProgressMsg:
		mdl, cmd := m.onDownloadProgress(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case downloadStartedMsg:
		m.cancelDownload = msg.cancel
		m.downloadOutputDir = msg.outputDir
		m.downloadTitle = msg.title
		m.drainDownloadChan()
		return m, tea.Batch(spinnerCmd, func() tea.Msg {
			return downloadProgressMsg{opID: msg.opID, progress: 0}
		})
	case batchProgressMsg:
		mdl, cmd := m.onBatchProgress(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case batchDoneMsg:
		mdl, cmd := m.onBatchDone(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case batchStartedMsg:
		m.batchCancel = msg.cancel
		m.batchCurrent = 0
		m.batchTotal = msg.total
		m.loadingText = fmt.Sprintf("Downloading 0/%d...", msg.total)
		return m, tea.Batch(spinnerCmd, m.batchSubscription())
	case playStartedMsg:
		if m.playOpID == msg.opID && m.loading {
			m.loading = false
			m.loadingText = ""
			m.setStatus(statusInfo, "Playing in progress...")
			m.statusExpiresAt = time.Time{}
		}
		return m, spinnerCmd
	case resetConfirmQuitMsg:
		m.confirmQuit = false
		m.clearStatus()
		return m, spinnerCmd
	case resetConfirmStopMsg:
		m.confirmStop = false
		m.clearStatus()
		return m, spinnerCmd
	case resetStatusMsg:
		if m.statusID == msg.id {
			m.clearStatus()
		}
		return m, spinnerCmd
	case posterLoadedMsg:
		switch msg.slot {
		case posterSlotSearch:
			if msg.opID == m.searchPosterOpID {
				m.searchPoster = msg.rendered
				m.searchPosterUnavailable = msg.err != nil
			}
		case posterSlotHistory:
			if msg.opID == m.historyPosterOpID {
				m.historyPoster = msg.rendered
				m.historyPosterUnavailable = msg.err != nil
			}
		case posterSlotPreview:
			if msg.opID == m.previewPosterOpID {
				if msg.err == nil && msg.rendered != "" {
					m.previewPoster = msg.rendered
					m.previewPosterUnavailable = false
				} else if m.previewPoster == "" {
					m.previewPosterUnavailable = msg.err != nil
				}
			}
		}
		return m, spinnerCmd
	case previewDetailsMsg:
		if msg.opID == m.previewPosterOpID && msg.err == nil {
			m.previewOverview = msg.overview
			m.previewGenres = msg.genres
			m.previewRating = msg.rating
		}
		return m, spinnerCmd
	case chaptersDoneMsg:
		mdl, cmd := m.onChaptersDone(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case pagesDoneMsg:
		mdl, cmd := m.onPagesDone(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case fallbackDoneMsg:
		mdl, cmd := m.onFallbackDone(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	case readerPageMsg:
		mdl, cmd := m.onReaderPage(msg)
		return mdl, tea.Batch(spinnerCmd, cmd)
	}

	mdl, cmd := m.updateActive(msg)
	return mdl, tea.Batch(spinnerCmd, cmd)
}

type themeCrossfadeTickMsg struct {
	opID int
	step int
}

func (m *modelImpl) startThemeCrossfade(fromKind, toKind model.Kind) tea.Cmd {
	override := ""
	if m.accentIndex > 0 && m.accentIndex < len(accentPresets) {
		override = accentPresets[m.accentIndex].name
	} else if m.customAccentHex != "" {
		override = m.customAccentHex
	}

	m.crossfadeActive = true
	m.crossfadeStep = 0
	m.crossfadeFrom = ThemeFor(fromKind, override)
	m.crossfadeTo = ThemeFor(toKind, override)
	m.crossfadeOpID++
	opID := m.crossfadeOpID
	return tea.Tick(18*time.Millisecond, func(t time.Time) tea.Msg {
		return themeCrossfadeTickMsg{opID: opID, step: 1}
	})
}

func (m *modelImpl) SetBaseBackgroundColor(c color.RGBA) {
	m.baseBgColor = c
}
