package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/lang"
	"kari/internal/player"
	"kari/internal/poster"
	"kari/internal/provider"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/settings"
	"kari/internal/termimg"
	"kari/internal/util"
)

func NewModel(ctx context.Context, initialQuery string, registry *provider.Registry, players *player.Registry, downloadDir string, mediaService *service.MediaService, downloadService *service.DownloadService, subtitleService *service.SubtitleService, historyStore *history.Store, historyLoadErr error, traktClient *scrobble.TraktClient, anilistClient *scrobble.AniListClient, posterClient *poster.Client) tea.Model {
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
	ti.Placeholder = "Search… (Esc for controls)"
	ti.Prompt = "search> "
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
		subtitleService: subtitleService,
		downloadService: downloadService,
		historyStore:    historyStore,
		traktClient:     traktClient,
		anilistClient:   anilistClient,
		appCtx:          ctx,
		activeView:      viewSearch,
		queryInput:      ti,
		authInput:       ai,
		hexInput:        hexInput,
		seriesList:      seriesList,
		episodeList:     episodeList,
		historyList:     historyList,
		spinner:         sp,
		downloadBar:     downloadBar,

		keys:             defaultKeyMap(),
		searchQuery:      strings.TrimSpace(initialQuery),
		appMode:          initialMode,
		registry:         registry,
		modes:            modes,
		players:          players,
		availablePlayers: players.AvailablePlayers(),
		searchCache:      util.NewBoundedCache[searchCacheEntry](60),
		downloadChan:     make(chan tea.Msg, 10),
		resolveChan:      make(chan tea.Msg, 10),
		audioMode:        "sub",
		qualityMode:      qualityAll,
		languageFilter:   make(map[string]bool),
		subtitleLanguage: "en",
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
	model.updateQueryPlaceholder()
	if s := savedSettings; s != nil {
		if s.QualityMode >= qualityAll && s.QualityMode <= qualityLowest {
			model.qualityMode = s.QualityMode
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
		model.imagesEnabled = !s.DisableImages
		if normalized, ok := normalizeHexColor(s.AccentColor); ok {
			model.accentIndex = len(accentPresets) // default: custom slot
			for i, preset := range accentPresets {
				if preset.hex == normalized {
					model.accentIndex = i
					normalized = ""
					break
				}
			}
			model.customAccentHex = normalized // "" when it matched a preset
		}
	}
	for i, code := range lang.SubtitleOptions {
		if code == model.subtitleLanguage {
			model.subtitleLanguageIndex = i
			break
		}
	}
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
	if m.statusType == statusWarn && m.statusText != "" {
		cmds = append(cmds, m.clearStatusAfter(statusClearDuration(statusWarn)))
	}
	return tea.Batch(cmds...)
}

type historyLoadedMsg struct{}

func (m *modelImpl) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var spinnerCmd tea.Cmd
	if m.loading {
		m.spinner, spinnerCmd = m.spinner.Update(msg)
	}

	switch msg := msg.(type) {
	case historyLoadedMsg:
		m.loading = false
		m.loadingText = ""
		m.pushView(viewHistory)
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeLists()
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
		m.downloadProvider = msg.provider
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
		}
		return m, spinnerCmd
	case resetConfirmQuitMsg:
		m.confirmQuit = false
		if m.cancelDownload != nil {
			m.loadingText = downloadLoadingText(m.downloadProgress, m.downloadTotalSize, m.downloadSpeed, m.downloadDownloaded, m.downloadETA)
			m.setStatus(statusInfo, "")
		} else if m.batchInProgress {
			m.loadingText = fmt.Sprintf("Downloading %d/%d...", m.batchCurrent, m.batchTotal)
			m.setStatus(statusInfo, "")
		} else {
			m.loadingText = ""
		}
		return m, spinnerCmd
	case resetConfirmStopMsg:
		m.confirmStop = false
		if m.cancelDownload != nil {
			m.loadingText = downloadLoadingText(m.downloadProgress, m.downloadTotalSize, m.downloadSpeed, m.downloadDownloaded, m.downloadETA)
			m.setStatus(statusInfo, "")
		} else if m.batchInProgress {
			m.loadingText = fmt.Sprintf("Downloading %d/%d...", m.batchCurrent, m.batchTotal)
			m.setStatus(statusInfo, "")
		} else {
			m.loadingText = ""
		}
		return m, spinnerCmd
	case resetStatusMsg:
		if m.statusID == msg.id {
			m.setStatus(statusInfo, "")
		}
		return m, spinnerCmd
	case posterLoadedMsg:
		switch msg.slot {
		case posterSlotSearch:
			if msg.opID == m.searchPosterOpID {
				m.searchPoster = msg.rendered
				m.searchPosterUnavailable = msg.err != nil
			}
		case posterSlotPreview:
			if msg.opID == m.previewPosterOpID {
				m.previewPoster = msg.rendered
				m.previewPosterUnavailable = msg.err != nil
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
	}

	mdl, cmd := m.updateActive(msg)
	return mdl, tea.Batch(spinnerCmd, cmd)
}
