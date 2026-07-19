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
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/settings"
)

func NewModel(ctx context.Context, initialQuery string, registry *provider.Registry, players *player.Registry, downloadDir string, mediaService *service.MediaService, downloadService *service.DownloadService, subtitleService *service.SubtitleService, historyStore *history.Store, traktClient *scrobble.TraktClient, anilistClient *scrobble.AniListClient) tea.Model {
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

	ai := textinput.New()
	ai.Placeholder = "Paste code here"
	ai.CharLimit = 4096

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
		seriesList:      seriesList,
		episodeList:     episodeList,
		historyList:     historyList,
		spinner:         sp,

		keys:             defaultKeyMap(),
		searchQuery:      strings.TrimSpace(initialQuery),
		appMode:          initialMode,
		registry:         registry,
		modes:            modes,
		players:          players,
		availablePlayers: players.AvailablePlayers(),
		searchCache:      make(map[string]searchCacheEntry),
		downloadChan:     make(chan tea.Msg, 10),
		resolveChan:      make(chan tea.Msg, 10),
		audioMode:        "sub",
		qualityMode:      qualityAll,
		languageFilter:   make(map[string]bool),
		selectedEpisodes: make(map[int]struct{}),
		batchChan:        make(chan tea.Msg, 50),
	}
	model.selectedPlayer = model.defaultPlayerIndex()
	model.updateQueryPlaceholder()
	if s := settings.Load(); s != nil {
		if s.QualityMode >= qualityAll && s.QualityMode <= qualityLowest {
			model.qualityMode = s.QualityMode
		}
		if len(s.LanguageFilter) > 0 {
			hasEnabled := false
			for _, enabled := range s.LanguageFilter {
				if enabled {
					hasEnabled = true
					break
				}
			}
			if hasEnabled {
				model.languageFilter = s.LanguageFilter
			}
		}
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
			m.setStatus(statusInfo, "Playback in progress...")
		}
		return m, spinnerCmd
	case resetConfirmQuitMsg:
		m.confirmQuit = false
		if m.cancelDownload != nil {
			m.loadingText = fmt.Sprintf("Downloading... %.1f%%", m.downloadProgress)
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
			m.loadingText = fmt.Sprintf("Downloading... %.1f%%", m.downloadProgress)
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
	}

	mdl, cmd := m.updateActive(msg)
	return mdl, tea.Batch(spinnerCmd, cmd)
}
