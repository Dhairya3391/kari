package tui

import (
	"context"
	"image/color"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/manga"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/poster"
	"kari/internal/provider"
	"kari/internal/ranking"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/termimg"
	"kari/internal/util"
)

// maxResolveAttempts bounds automatic retries of a fully-failed stream
// resolve (1 initial try + 2 retries).
const maxResolveAttempts = 3

type viewState string

const (
	viewSearch    viewState = "search"
	viewEpisodes  viewState = "episodes"
	viewPreview   viewState = "preview"
	viewHistory   viewState = "history"
	viewSettings  viewState = "settings"
	viewDownloads viewState = "downloads"
	// viewChapters lists manga/comic chapters for the selected title —
	// the manga equivalent of the episodes screen and its preview page.
	viewChapters viewState = "chapters"
	// viewReader is the fullscreen black-background page viewer.
	viewReader viewState = "reader"
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

const (
	downloadQualityAuto = 0 // "Auto (same as stream)"
	downloadQuality4K   = 1 // "4K (2160p)"
	downloadQualityFHD  = 2 // "FHD (1080p)"
	downloadQualityHD   = 3 // "HD (720p)"
	downloadQualitySD   = 4 // "SD (480p)"
)

type searchCacheEntry struct {
	results   []provider.SearchResult
	usedQuery string
	warnings  []string
}

type searchDoneMsg struct {
	results   []provider.SearchResult
	usedQuery string
	warnings  []string
	opID      int
	err       error
}

type episodesDoneMsg struct {
	results []provider.Episode
	opID    int
	err     error
}

// episodeTitlesMsg carries real episode titles looked up on AniList for
// an anime series whose provider only sent placeholders ("Episode 1").
type episodeTitlesMsg struct {
	titles map[int]string
	opID   int
}

type historyContinueEpisodesMsg struct {
	group   history.Group
	results []provider.Episode
	opID    int
	err     error
}

// historyResolveSeriesMsg carries the result of re-searching currently
// registered providers for a history entry's title, so resume never depends
// on a provider name/URL saved from a possibly-removed provider.
type historyResolveSeriesMsg struct {
	entry  history.Entry
	group  *history.Group
	series provider.SearchResult
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
	track model.SubtitleTrack
	opID  int
	err   error
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
	// Output location of the episode being written right now; the cancel
	// and skip actions clean up exactly these files.
	outputDir string
	fileTitle string
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

// History tabs into the Continue and Finished sections.
const (
	historyTabContinue = 0
	historyTabFinished = 1
)

const (
	posterSlotSearch  posterSlot = "search"
	posterSlotPreview posterSlot = "preview"
	posterSlotHistory posterSlot = "history"
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

// chaptersDoneMsg carries a fetched chapter listing for the manga mode.
type chaptersDoneMsg struct {
	results []provider.MangaChapter
	opID    int
	err     error
}

// pagesDoneMsg carries a chapter's page-image URLs.
type pagesDoneMsg struct {
	chapter provider.MangaChapter
	pages   []provider.MangaPage
	opID    int
	err     error
}

// fallbackDoneMsg carries a chapter resolved readable on any source,
// possibly a different provider than the one selected.
type fallbackDoneMsg struct {
	readable service.ReadableChapter
	opID     int
	err      error
}

// readerPageMsg carries one rendered reader page: rendered is the
// terminal string for dims (cols x rows), so stale renders (after a
// resize) are discarded by dimension check instead of shown.
type readerPageMsg struct {
	chapter int
	index   int
	cols    int
	rows    int
	render  string
	opID    int
	err     error
}

type modelImpl struct {
	mediaService    *service.MediaService
	mangaService    *service.MangaService
	mangaClient     *manga.Client
	subtitleService *service.SubtitleService
	downloadService *service.DownloadService
	historyStore    *history.Store
	traktClient     *scrobble.TraktClient
	anilistClient   *scrobble.AniListClient
	registry        *provider.Registry
	players         *player.Registry
	appCtx          context.Context
	appVersion      string
	appCommit       string
	width           int
	height          int

	activeView viewState

	queryInput  textinput.Model
	seriesList  list.Model
	episodeList list.Model
	chapterList list.Model
	historyList list.Model
	// historyTab selects the Continue (0) / Finished (1) tab;
	// historyTabIndex remembers the cursor per tab across switches.
	historyTab      int
	historyTabIndex [2]int
	spinner         spinner.Model
	downloadBar     progress.Model

	keys keyMap

	allSeriesResults []provider.SearchResult
	seriesResults    []provider.SearchResult
	episodeResults   []provider.Episode
	selectedSeries   *provider.SearchResult
	selectedEpisode  *provider.Episode
	resolved         *model.ResolvedMedia

	searchQuery  string
	usedQuery    string
	backStack    []viewState
	searchIndex  int
	episodeIndex int

	loading                bool
	loadingText            string
	statusText             string
	statusType             statusLevel
	statusID               int
	statusExpiresAt        time.Time
	showHelp               bool
	bodyScroll             int
	helpScroll             int
	selectedPlayback       int
	manualPlaybackSelected bool
	prevSourceLanguage     string
	prevSourceQuality      int
	availablePlayers       []string
	selectedPlayer         int
	autoPlayAfterResolve   bool
	autoplay               bool

	appMode         provider.ContentType
	defaultMode     string
	modes           []provider.ContentType
	configuredModes []string
	disabledModes   map[string]bool
	transitions     bool
	baseBgColor     color.RGBA
	crossfadeActive bool
	crossfadeStep   int
	crossfadeFrom   ModeTheme
	crossfadeTo     ModeTheme
	crossfadeOpID   int
	nextOpID        int

	searchOpID           int
	episodesOpID         int
	episodeTitlesOpID    int
	historyImportOpID    int
	historyContinueOpID  int
	pendingHistoryTarget *history.Entry
	resolveOpID          int
	// resolveAttempts counts automatic retries of a fully-failed resolve;
	// reset on success and on a new episode pick.
	resolveAttempts int
	// preferredRepairAttempts counts background repairs of a partially
	// failed resolve whose failed provider is routes-table preferred
	// (movy-first). Bounded to one per episode pick so a dead favorite
	// never loops; reset alongside resolveAttempts.
	preferredRepairAttempts int
	subtitleOpID            int
	subtitleResolverUsed    string
	subtitleLangUsed        string
	subtitleSourceUsed      string
	rawSubtitles            []model.SubtitleTrack
	playOpID                int
	downloadOpID            int
	downloadProgress        float64
	downloadTotalSize       string
	downloadSpeed           string
	downloadDownloaded      string
	downloadETA             string
	downloadChan            chan tea.Msg
	resolveChan             chan tea.Msg
	cancelDownload          context.CancelFunc
	downloadTitle           string
	downloadOutputDir       string
	confirmQuit             bool
	confirmStop             bool
	confirmDelete           bool
	confirmClearHistory     bool
	anilistAuthURL          string
	authInput               textinput.Model
	traktAuthActive         bool
	traktUserCode           string
	traktVerifyURL          string
	traktDeviceCode         string
	traktCancel             context.CancelFunc
	startupSync             bool
	settingsIndex           int
	searchCache             *util.BoundedCache[searchCacheEntry]
	audioMode               string
	qualityMode             int
	downloadQuality         int
	languageFilter          map[string]bool
	subtitleLanguage        string
	subtitleLanguageIndex   int
	disableAnimeSubtitles   bool
	accentIndex             int
	customAccentHex         string
	editingAccentHex        bool
	hexInput                textinput.Model
	skipProvider            string
	autoSkipIntro           bool
	autoSkipEnding          bool
	skipRecap               bool
	skipPreview             bool
	selectedEpisodes        map[int]struct{}
	batchInProgress         bool
	batchCurrent            int
	batchTotal              int
	batchEpisodeProgress    float64
	batchCancel             context.CancelFunc
	batchChan               chan tea.Msg
	batchEpisodes           []provider.Episode
	batchSeries             provider.SearchResult
	batchMode               provider.ContentType
	// batchActiveDir/Title locate the episode being written right now so
	// x (cancel single) removes exactly its partial files.
	batchActiveDir   string
	batchActiveTitle string
	downloadPaused   bool
	singleResolved   *model.ResolvedMedia
	pendingDownload  *service.DownloadJob

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
	historyPoster            string
	historyPosterOpID        int
	historyPosterUnavailable bool
	// historyGroups mirrors the history list rows so selection resolves
	// back to a group without re-aggregating the store.
	historyGroups []history.Group

	// Manga/comic reading state. Chapters belong to the selected series;
	// pages belong to the selected chapter; the reader renders pages
	// on demand with neighbors prefetched.
	chapters          []provider.MangaChapter
	selectedChapter   *provider.MangaChapter
	chapterIndex      int
	chaptersOpID      int
	pages             []provider.MangaPage
	pagesOpID         int
	readerPage        int
	readerOpID        int
	readerRender      map[int]string
	readerCols        int
	readerRows        int
	readerUnavailable bool
	// autoAdvance counts consecutive automatic skips over chapters whose
	// uploads were removed, bounded by maxAutoAdvance. Reset on every
	// manual chapter pick so only dead runs chain.
	autoAdvance int
	// fallbackTried marks that other sources were already attempted for
	// the current chapter pick, so the cross-source search runs once.
	fallbackTried bool
	// readerRelisted marks that the current chapter's pages were already
	// re-resolved once mid-read, so a repeatedly dying upload falls
	// through to the chapter-level recovery instead of looping.
	readerRelisted bool
	// readerResumePage preserves the reader position across a mid-read
	// page re-resolution; -1 means start at the first page.
	readerResumePage int
	// pendingMangaResume carries a history-resume target across the
	// async chapter listing: consumed by onChaptersDone, cleared on
	// new picks.
	pendingMangaResume *mangaResume

	// Redesign Navigation & Router
	activeToast *Toast
	// Season grouping on Episodes screen
	activeSeason       int
	seasonEpisodeIndex int
	selectMode         bool
	// Text filter on Episodes screen (/ to start, esc clears)
	episodeFilter    string
	episodeFiltering bool
	// Text filter on Results screen (/ to start, esc clears)
	resultsFilter    string
	resultsFiltering bool
	// Source Ranking & Progressive loading on Preview
	rankedSources        []ranking.ScoredSource
	previewSelectedIndex int
	stickyProviders      map[string]string
	failedProviders      map[string]time.Time
	loadingProviders     int
	totalProviders       int
	failedProviderName   string

	// Settings 5-category state
	settingsCategory SettingsCategory
	audioPickerOpen  bool
	audioPickerIndex int

	// Downloads Queue state
	activeDownloads []ActiveDownload
	doneDownloads   []DoneDownload
	downloadsIndex  int
}

// mangaResume is a series-level reading position: chapter catalog
// number plus 1-based page.
type mangaResume struct {
	number string
	page   int
}
