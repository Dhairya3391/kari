package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/poster"
	"kari/internal/provider"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/termimg"
	"kari/internal/util"
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

const (
	qualityAll       = 0
	qualityHighest   = 1
	qualityDataSaver = 2
	qualityLowest    = 3
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

// historyResolveSeriesMsg carries the result of re-searching currently
// registered providers for a history entry's title, so resume never depends
// on a provider name/URL saved from a possibly-removed provider.
type historyResolveSeriesMsg struct {
	entry  history.Entry
	group  *history.Group
	series model.SearchResult
	opID   int
	err    error
}

type resolveDoneMsg struct {
	resolved model.ResolvedMedia
	opID     int
	err      error
}

type resolveWorkerDoneMsg struct{}

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
	opID       int
	progress   float64
	totalSize  string
	speed      string
	downloaded string
	eta        string
}

type batchProgressMsg struct {
	opID            int
	current, total  int
	episodeTitle    string
	episodeProgress float64
	totalSize       string
	speed           string
	downloaded      string
	eta             string
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

type posterSlot string

const (
	posterSlotSearch  posterSlot = "search"
	posterSlotPreview posterSlot = "preview"
)

type posterLoadedMsg struct {
	slot     posterSlot
	opID     int
	rendered string
	err      error
}

type previewDetailsMsg struct {
	opID     int
	overview string
	genres   []string
	rating   string
	err      error
}

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
	appVersion      string

	width  int
	height int

	activeView viewState

	queryInput  textinput.Model
	seriesList  list.Model
	episodeList list.Model
	historyList list.Model
	spinner     spinner.Model
	downloadBar progress.Model

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

	loading              bool
	loadingText          string
	statusText           string
	statusType           statusLevel
	statusID             int
	showHelp             bool
	selectedPlayback     int
	prevSourceLanguage   string
	prevSourceQuality    int
	availablePlayers     []string
	selectedPlayer       int
	autoPlayAfterResolve bool
	pendingAutoPlay      bool
	pendingManualPlay    bool
	autoplay             bool

	appMode provider.ContentType
	modes   []provider.ContentType

	nextOpID int

	searchOpID            int
	episodesOpID          int
	historyContinueOpID   int
	pendingHistoryTarget  *history.Entry
	resolveOpID           int
	subtitleOpID          int
	subtitleResolverUsed  string
	subtitleLangUsed      string
	playOpID              int
	downloadOpID          int
	downloadProgress      float64
	downloadTotalSize     string
	downloadSpeed         string
	downloadDownloaded    string
	downloadETA           string
	downloadChan          chan tea.Msg
	resolveChan           chan tea.Msg
	cancelDownload        context.CancelFunc
	downloadTitle         string
	downloadProvider      string
	downloadOutputDir     string
	confirmQuit           bool
	confirmStop           bool
	confirmDelete         bool
	confirmClearHistory   bool
	confirmCompletion     bool
	traktAuthCode         string
	traktAuthURL          string
	traktAuthDeviceCode   string
	anilistAuthURL        string
	authInput             textinput.Model
	settingsIndex         int
	languageIndex         int
	searchCache           *util.BoundedCache[searchCacheEntry]
	audioMode             string
	qualityMode           int
	languageFilter        map[string]bool
	subtitleLanguage      string
	subtitleLanguageIndex int
	accentIndex           int
	customAccentHex       string
	editingAccentHex      bool
	hexInput              textinput.Model

	selectedEpisodes map[int]struct{}
	batchInProgress  bool
	batchCurrent     int
	batchTotal       int
	batchCancel      context.CancelFunc
	batchChan        chan tea.Msg

	posterClient             *poster.Client
	imgProtocol              termimg.Protocol
	imagesEnabled            bool
	posterCache              *util.BoundedCache[string]
	searchPoster             string
	searchPosterOpID         int
	searchPosterUnavailable  bool
	previewPoster            string
	previewPosterOpID        int
	previewPosterUnavailable bool
	previewOverview          string
	previewGenres            []string
	previewRating            string
}
