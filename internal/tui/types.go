package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/scrobble"
	"kari/internal/service"
)

type viewState string

const (
	viewSearch   viewState = "search"
	viewEpisodes viewState = "episodes"
	viewPreview  viewState = "preview"
	viewHistory  viewState = "history"
	viewSettings viewState = "settings"
)

type statusLevel string

const (
	statusInfo    statusLevel = "info"
	statusSuccess statusLevel = "success"
	statusWarn    statusLevel = "warn"
	statusError   statusLevel = "error"
)

type searchCacheEntry struct {
	results   []model.SearchResult
	usedQuery string
	warnings  []string
}

type searchDoneMsg struct {
	results   []model.SearchResult
	usedQuery string
	warnings  []string
	opID      int
	err       error
}

type episodesDoneMsg struct {
	results []model.EpisodeResult
	opID    int
	err     error
}

type historyContinueEpisodesMsg struct {
	group   history.Group
	results []model.EpisodeResult
	opID    int
	err     error
}

type resolveDoneMsg struct {
	resolved model.ResolvedMedia
	opID     int
	err      error
}

type subtitleDoneMsg struct {
	tracks []model.SubtitleTrack
	opID   int
	err    error
}

type resolveProgressMsg struct {
	resolved model.ResolvedMedia
	opID     int
}

type playDoneMsg struct {
	opID     int
	provider string
	result   player.PlaybackResult
	err      error
}

type playStartedMsg struct {
	opID int
}

type downloadDoneMsg struct {
	opID int
	err  error
}

type downloadProgressMsg struct {
	opID     int
	progress float64
}

type batchProgressMsg struct {
	opID            int
	current, total  int
	episodeTitle    string
	episodeProgress float64
}

type batchDoneMsg struct {
	opID      int
	completed int
	total     int
}

type downloadStartedMsg struct {
	opID      int
	cancel    context.CancelFunc
	outputDir string
	title     string
	provider  string
}

type batchStartedMsg struct {
	opID   int
	cancel context.CancelFunc
	total  int
}

type resetConfirmQuitMsg struct{}
type resetConfirmStopMsg struct{}
type resetStatusMsg struct{ id int }

type modelImpl struct {
	mediaService    *service.MediaService
	subtitleService *service.SubtitleService
	downloadService *service.DownloadService
	historyStore    *history.Store
	traktClient     *scrobble.TraktClient
	anilistClient   *scrobble.AniListClient
	registry        *provider.Registry
	players         *player.Registry
	appCtx          context.Context

	width  int
	height int

	activeView viewState

	queryInput  textinput.Model
	seriesList  list.Model
	episodeList list.Model
	historyList list.Model
	spinner     spinner.Model

	keys keyMap

	allSeriesResults []model.SearchResult
	seriesResults    []model.SearchResult
	episodeResults   []model.EpisodeResult
	selectedSeries   *model.SearchResult
	selectedEpisode  *model.EpisodeResult
	resolved         *model.ResolvedMedia

	searchQuery  string
	usedQuery    string
	backStack    []viewState
	searchIndex  int
	episodeIndex int

	loading          bool
	loadingText      string
	statusText       string
	statusType       statusLevel
	statusID         int
	showHelp         bool
	selectedPlayback int
	availablePlayers     []string
	selectedPlayer       int
	autoPlayAfterResolve bool
	pendingAutoPlay      bool
	pendingManualPlay    bool
	autoplay             bool

	appMode   provider.ContentType
	modes     []provider.ContentType

	nextOpID int

	searchOpID          int
	episodesOpID        int
	historyContinueOpID int
	resolveOpID         int
	subtitleOpID        int
	playOpID            int
	downloadOpID        int
	downloadProgress    float64
	downloadChan        chan tea.Msg
	resolveChan         chan tea.Msg
	cancelDownload      context.CancelFunc
	downloadTitle       string
	downloadProvider    string
	downloadOutputDir   string
	confirmQuit         bool
	confirmStop         bool
	confirmDelete       bool
	confirmClearHistory bool
	confirmCompletion   bool
	traktAuthCode       string
	traktAuthURL        string
	traktAuthDeviceCode string
	anilistAuthURL      string
	authInput           textinput.Model
	settingsIndex       int // 0 for Trakt, 1 for AniList
	searchCache         map[string]searchCacheEntry
	audioMode           string // "sub" or "dub"

	selectedEpisodes map[int]struct{}
	batchInProgress  bool
	batchCurrent     int
	batchTotal       int
	batchCancel      context.CancelFunc
	batchChan        chan tea.Msg
}
