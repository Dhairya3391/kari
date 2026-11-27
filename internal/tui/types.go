package tui

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/calendar"
	"kari/internal/filter"
	"kari/internal/history"
	"kari/internal/manga"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/poster"
	"kari/internal/presence"
	"kari/internal/provider"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/termimg"
	"kari/internal/util"
)

// maxResolveAttempts bounds automatic retries of a fully-failed stream
// resolve (1 initial try + 2 retries).
const maxResolveAttempts = 3

type viewState string

// episodeListCache keeps the season/filter projection stable across key repeats.
type episodeListCache struct {
	source   []provider.Episode
	seasons  []int
	filtered []provider.Episode
	indices  []int
	season   int
	filter   string
	valid    bool
}

// episodeHistoryIndexCache avoids rebuilding the current series' index on
// every repaint.
type episodeHistoryIndexCache struct {
	title string
	index history.EpisodeIndex
	valid bool
}

// previewHeaderCache memoizes the plain-text preview metadata block. The
// overlay layout key needs that text on every message (spinner ticks, theme
// crossfades, keystrokes), but it only changes when the metadata or the
// available width does.
type previewHeaderCache struct {
	fingerprint uint64
	signature   string
	valid       bool
}

const (
	viewSearch    viewState = "search"
	viewEpisodes  viewState = "episodes"
	viewPreview   viewState = "preview"
	viewHistory   viewState = "history"
	viewSettings  viewState = "settings"
	viewDownloads viewState = "downloads"
	// viewChapters lists manga/comic chapters for the selected title —
	// the manga equivalent of the episodes screen and its preview page.
	viewChapters     viewState = "chapters"
	viewMangaSources viewState = "manga_sources"
	// viewReader is the fullscreen black-background page viewer.
	viewReader   viewState = "reader"
	viewCalendar viewState = "calendar"
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
	// qualityExplicitSD is the last persisted stream-quality tier. Picking an
	// explicit tier in settings stores 10-13 (mirroring filter.QualityUHD..SD),
	// and those values must survive a reload for "same as stream" downloads.
	qualityExplicitSD = 13
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
	// cachedAt lets a repeat search detect that the service collected slow
	// provider rows after this snapshot and refresh instead of reusing it.
	cachedAt time.Time
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
type calendarLoadedMsg struct {
	schedule calendar.WeeklySchedule
	opID     int
	err      error
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

// subtitleHotAddResultMsg reports the outcome of side-loading a subtitle into
// the running player. session binds the result to the playback that asked for
// it so a late answer from a replaced episode cannot mutate current state.
type subtitleHotAddResultMsg struct {
	session int
	path    string
	attempt int
	err     error
}

// retrySubtitleHotAddMsg re-attempts a failed side-load after a short delay.
type retrySubtitleHotAddMsg struct {
	session int
	path    string
	attempt int
}

type resolveProgressMsg struct {
	resolved model.ResolvedMedia
	opID     int
}

type previewSkipInfoMsg struct {
	opID int
	info player.SkipInfo
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

type addonTestDoneMsg struct {
	// opID identifies the check that produced this message. It is the only
	// reliable guard: a cancelled check can still emit a late message whose
	// manifestURL matches a newer check of the same URL.
	opID        uint64
	manifestURL string
	// replacing is non-empty when this check refreshes or rebinds an addon.
	replacing string
	addon     provider.Provider
	err       error
}

type startupAddonLoadedMsg struct {
	manifestURL string
	addon       provider.Provider
	err         error
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

type animeStatusLoadedMsg struct {
	opID          int
	entry         *scrobble.MediaListEntry
	totalEpisodes int
	err           error
}

type animeStatusUpdatedMsg struct {
	statusText string
	err        error
}

type historyExportMsg struct {
	synced  int
	skipped int
	err     error
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
type toastFadeTickMsg struct{ id int }
type posterSlot string

// History tabs into the Continue and Finished sections.
const (
	historyTabContinue = 0
	historyTabFinished = 1
)

const (
	posterSlotSearch   posterSlot = "search"
	posterSlotPreview  posterSlot = "preview"
	posterSlotHistory  posterSlot = "history"
	posterSlotCalendar posterSlot = "calendar"
	posterSlotLive     posterSlot = "live"
)

type posterLoadedMsg struct {
	slot     posterSlot
	opID     int
	rendered string
	err      error
}

type previewDetailsMsg struct {
	opID     int
	key      string
	overview string
	genres   []string
	rating   string
	err      error
}

// previewDetails is a cached plot synopsis, genre list, and score for one
// title. It survives navigation so returning to a screen shows them at once.
type previewDetails struct {
	overview string
	genres   []string
	rating   string
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

// historyProjection caches the sorted/filtered history rows between frames.
// Rebuilding them walks, sorts and flattens every group on every message.
type historyProjection struct {
	source        []history.Group
	finishedTab   bool
	modeFilter    provider.ContentType
	filter        string
	valid         bool
	activeTab     HistoryTab
	activeList    []history.Group
	modeFilters   []HistoryModeFilter
	continueCount int
	finishedCount int
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
	calendarClient  *calendar.Client
	registry        *provider.Registry
	players         *player.Registry
	// hotAddSubtitle is a test seam for side-loading into the running player;
	// nil means use the real IPC client.
	hotAddSubtitle func(context.Context, string) error
	// subtitleAddCtx is scoped to one playback session: cancelling it stops
	// in-flight side-loads so they cannot attach to a replacement player.
	subtitleAddCtx    context.Context
	subtitleAddCancel context.CancelFunc
	appCtx            context.Context
	appVersion        string
	appCommit         string
	width             int
	height            int

	activeView viewState

	queryInput  textinput.Model
	addonInput  textinput.Model
	seriesList  list.Model
	episodeList list.Model
	chapterList list.Model
	historyList list.Model
	// historyTab selects the Continue (0) / Finished (1) tab;
	// historyTabIndex remembers the cursor per tab across switches.
	historyTab                      int
	historyTabIndex                 [2]int
	historyModeFilter               provider.ContentType
	historyFilter                   string
	historyFiltering                bool
	historyTerminalInputPrefix      string
	historyDroppingTerminalResponse bool
	spinner                         spinner.Model
	downloadBar                     progress.Model
	calendarSchedule                calendar.WeeklySchedule
	calendarDayIndex                int
	calendarReleaseIndex            int
	calendarFilter                  calendar.FilterMode
	calendarQuery                   string
	calendarFiltering               bool
	calendarLoaded                  bool
	calendarOpID                    int

	keys keyMap

	allSeriesResults  []provider.SearchResult
	seriesResults     []provider.SearchResult
	seriesFilterCache struct {
		query   string
		source  []provider.SearchResult
		results []provider.SearchResult
		indices []int
		valid   bool
	}
	episodeResults   []provider.Episode
	episodeCache     episodeListCache
	episodeHistory   episodeHistoryIndexCache
	selectedSeries   *provider.SearchResult
	mangaSourceIndex int
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
	helpScroll             int
	scroll                 scrollGate
	previewSkipInfo        string
	previewSkipKey         string
	skipInfoOpID           int
	manualPlaybackSelected bool
	prevSourceResolver     string
	prevSourceLanguage     string
	prevSourceQuality      int
	availablePlayers       []string
	selectedPlayer         int
	autoPlayAfterResolve   bool
	autoplay               bool

	appMode             provider.ContentType
	defaultMode         string
	modes               []provider.ContentType
	configuredModes     []string
	disabledModes       map[string]bool
	disabledProviders   map[string]bool
	transitions         bool
	startupSearchActive bool
	autoUpdate          bool
	crossfadeActive     bool
	crossfadeStep       int
	crossfadeFrom       ModeTheme
	crossfadeTo         ModeTheme
	crossfadePalette    [17]ModeTheme
	crossfadeOpID       int
	nextOpID            int

	searchOpID           int
	searchQueryCancel    context.CancelFunc
	episodesOpID         int
	episodeTitlesOpID    int
	historyImportOpID    int
	historyContinueOpID  int
	pendingHistoryTarget *history.Entry
	resolveOpID          int
	resolveCancel        context.CancelFunc
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
	// subtitlePoolSize is len(rawSubtitles) at the last subtitle fetch:
	// a grown pool re-triggers the fetch when the earlier (partial)
	// pool produced nothing usable yet.
	subtitlePoolSize     int
	subtitlePoolComplete bool
	rawSubtitles         []model.SubtitleTrack
	// loadedSubtitlePaths holds every subtitle file confirmed loaded in the
	// active player. Resolve snapshots can re-fetch the same cached file, and
	// a source/language toggle can bring back an earlier one, so membership
	// (not just the last path) is needed to avoid duplicate external tracks.
	loadedSubtitlePaths map[string]struct{}
	// hotAddPendingPath is a side-load still in flight; a retry keeps it set
	// so a second fetch of the same path cannot start a competing add.
	hotAddPendingPath  string
	playOpID           int
	downloadOpID       int
	downloadProgress   float64
	downloadTotalSize  string
	downloadSpeed      string
	downloadDownloaded string
	downloadETA        string
	// completedDownloadsCache holds one directory scan for a short window so
	// the downloads screen can use it in both the body and the footer without
	// walking the download directory twice per frame.
	completedDownloadsCache []service.CompletedDownload
	completedDownloadsAt    time.Time
	historyCache            historyProjection
	previewHeader           previewHeaderCache
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
	disableHistory          bool
	disabledHistoryModes    map[string]bool
	discordPresence         bool
	// presenceSession tags the current now-playing card so a late background
	// artwork lookup can't revive a card that was already cleared or replaced.
	presenceSession       atomic.Int64
	searchCacheGeneration atomic.Uint64
	settingsIndex         int
	stremioAddonManifests []string
	addonInputOpen        bool
	addonManifestTester   func(context.Context, string) (provider.Provider, error)
	startupAddonLoader    func(context.Context, string) (provider.Provider, error)
	addonTestCancel       context.CancelFunc
	addonTesting          bool
	addonPendingURL       string
	// addonTestOpID tags the most recent check so a late result from a
	// cancelled check cannot be applied to a newer one.
	addonTestOpID uint64
	// addonRebindingURL is set while editing an existing addon's URL.
	addonRebindingURL string
	searchCache       *util.BoundedCache[searchCacheEntry]
	// filterScope is the single source of truth for per-mode ranking policy.
	// The legacy scalar fields below mirror it for downloads and older
	// settings files; only this scope feeds PlaybackOptions.Preferences.
	filterScope                 filter.Scope
	audioMode                   string
	qualityMode                 int
	downloadQuality             int
	preferredProviders          map[string]string
	providerPriorityOpen        bool
	providerPriorityIndex       int
	disableDownloadVerification bool
	languageFilter              map[string]bool
	subtitleLanguage            string
	disableAnimeSubtitles       bool
	accentIndex                 int
	customAccentHex             string
	editingAccentHex            bool
	hexInput                    textinput.Model
	skipProvider                string
	autoSkipIntro               bool
	autoSkipEnding              bool
	skipRecap                   bool
	skipPreview                 bool
	selectedEpisodes            map[int]struct{}
	batchInProgress             bool
	batchCurrent                int
	batchTotal                  int
	batchEpisodeProgress        float64
	batchCancel                 context.CancelFunc
	batchChan                   chan tea.Msg
	batchEpisodes               []provider.Episode
	batchSeries                 provider.SearchResult
	batchMode                   provider.ContentType
	// batchActiveDir/Title locate the episode being written right now so
	// x (cancel single) removes exactly its partial files.
	batchActiveDir   string
	batchActiveTitle string
	downloadPaused   bool
	singleResolved   *model.ResolvedMedia
	pendingDownload  *service.DownloadJob

	posterClient              *poster.Client
	presence                  *presence.Client
	imgProtocol               termimg.Protocol
	imagesEnabled             bool
	posterCache               *util.BoundedCache[string]
	previewDetailsCache       *util.BoundedCache[previewDetails]
	searchPoster              string
	searchPosterOpID          int
	searchPosterCancel        context.CancelFunc
	searchPosterUnavailable   bool
	livePoster                string
	livePosterOpID            int
	livePosterCancel          context.CancelFunc
	livePosterUnavailable     bool
	liveUpdatedAt             time.Time
	liveSources               int
	previewPoster             string
	previewPosterOpID         int
	previewPosterCancel       context.CancelFunc
	previewPosterUnavailable  bool
	previewOverview           string
	previewGenres             []string
	previewRating             string
	historyPoster             string
	historyPosterOpID         int
	historyPosterCancel       context.CancelFunc
	historyPosterUnavailable  bool
	historyPosterDrawn        bool
	calendarPoster            string
	calendarPosterOpID        int
	calendarPosterCancel      context.CancelFunc
	calendarPosterUnavailable bool
	// calendarPosterKey identifies the release the current artwork belongs to.
	// It lets Update reload the cover on selection changes without refetching
	// on every spinner tick or status message.
	calendarPosterKey string
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
	activeToast        *Toast
	toastID            int
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
	rankedSources        []filter.RankedSource
	previewSelectedIndex int
	stickyProviders      map[string]string
	loadingProviders     int
	totalProviders       int
	failedProviderName   string

	// Settings 5-category state
	settingsCategory    SettingsCategory
	audioPickerOpen     bool
	audioPickerIndex    int
	subtitlePickerOpen  bool
	subtitlePickerIndex int
	subtitlePickerMode  provider.ContentType

	// Anime Status picker modal
	animeStatusPickerOpen      bool
	animeStatusPickerIndex     int
	animeStatusTargetTitle     string
	animeStatusTargetAniListID int
	animeStatusTargetKey       history.GroupKey
	animeStatusLoading         bool
	animeStatusEntry           *scrobble.MediaListEntry
	animeStatusTotalEpisodes   int
	animeStatusOpID            int
	// Downloads Queue state
	activeDownloads        []ActiveDownload
	doneDownloads          []DoneDownload
	dismissedDoneDownloads map[string]bool
	downloadsIndex         int
}

// mangaResume is a series-level reading position: chapter catalog
// number plus 1-based page.
type mangaResume struct {
	number string
	page   int
}
