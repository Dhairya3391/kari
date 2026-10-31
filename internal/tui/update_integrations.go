package tui

import (
	"context"
	"errors"
	"fmt"
	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/util"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type authDoneMsg struct {
	service string
	err     error
}

// historyImportMsg carries the result of a one-way tracker import
// (AniList/Trakt watched lists into the local history store). Quiet
// results come from the automatic startup sync: they toast only when
// something actually arrived and never surface errors.
type historyImportMsg struct {
	source             string
	imported, upgraded int
	skipped            int
	opID               int
	quiet              bool
	err                error
}
type traktCodeMsg struct {
	userCode, verificationURL, deviceCode string
	interval, expiresIn                   int
}

func (m *modelImpl) startTraktAuth() (tea.Model, tea.Cmd) {
	if m.traktClient == nil {
		return m, m.setStatusTimed(statusError, "Trakt integration not configured")
	}
	m.loading = true
	m.loadingText = "Requesting code..."
	return m, func() tea.Msg {
		userCode, verificationURL, deviceCode, interval, expiresIn, err := m.traktClient.StartDeviceAuth(m.appCtx)
		if err != nil {
			return authDoneMsg{service: "Trakt", err: err}
		}
		return traktCodeMsg{
			userCode:        userCode,
			verificationURL: verificationURL,
			deviceCode:      deviceCode,
			interval:        interval,
			expiresIn:       expiresIn,
		}
	}
}

func (m *modelImpl) onTraktCode(msg traktCodeMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingText = ""
	m.traktAuthActive = true
	m.traktUserCode = msg.userCode
	m.traktVerifyURL = msg.verificationURL
	m.traktDeviceCode = msg.deviceCode
	_ = util.OpenBrowser(msg.verificationURL)
	statusCmd := m.setStatusTimed(statusInfo, fmt.Sprintf("Enter code %s at %s", msg.userCode, msg.verificationURL))
	parentCtx := m.appCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(parentCtx)
	m.traktCancel = cancel
	pollCmd := m.pollTraktAuthCmd(ctx, msg.deviceCode, msg.interval, msg.expiresIn)
	return m, tea.Batch(statusCmd, pollCmd)
}

func (m *modelImpl) pollTraktAuthCmd(ctx context.Context, deviceCode string, interval, expiresIn int) tea.Cmd {
	return func() tea.Msg {
		err := m.traktClient.PollDeviceAuth(ctx, deviceCode, interval, expiresIn)
		return authDoneMsg{service: "Trakt", err: err}
	}
}

func (m *modelImpl) onAuthDone(msg authDoneMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingText = ""
	switch msg.service {
	case "Trakt":
		m.traktAuthActive = false
		m.traktUserCode = ""
		m.traktVerifyURL = ""
		m.traktDeviceCode = ""
		if m.traktCancel != nil {
			m.traktCancel()
			m.traktCancel = nil
		}
	case "AniList":
		m.anilistAuthURL = ""
		m.authInput.Blur()
	}
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		return m, m.setStatusTimed(statusError, fmt.Sprintf("%s auth failed: %s", msg.service, cleanErrorForUI(msg.err)))
	}
	m.saveSettings()
	return m, m.setStatusTimed(statusSuccess, fmt.Sprintf("%s connected successfully", msg.service))
}

// startHistoryImport fetches the focused tracker's watched list and
// merges it into the local history store: one-way, additive, and async
// so the UI never blocks on the network.
func (m *modelImpl) startHistoryImport(source string) (tea.Model, tea.Cmd) {
	if m.historyStore == nil {
		return m, m.setStatusTimed(statusError, "Watch history unavailable")
	}
	switch source {
	case "anilist":
		if m.anilistClient == nil || !m.anilistClient.IsAuthenticated() {
			return m, m.setStatusTimed(statusWarn, "Connect AniList first (c)")
		}
	case "trakt":
		if m.traktClient == nil || !m.traktClient.IsAuthenticated() {
			return m, m.setStatusTimed(statusWarn, "Connect Trakt first (c)")
		}
	default:
		return m, nil
	}
	m.loading = true
	m.loadingText = fmt.Sprintf("Importing watched history from %s...", source)
	opID := m.newOpID()
	m.historyImportOpID = opID
	return m, tea.Batch(m.spinner.Tick, m.historyImportCmd(opID, source))
}

// historyImportCmd runs the fetch+merge off the UI thread: tracker calls
// take their own timeouts, and the merge is plain store writes.
func (m *modelImpl) historyImportCmd(opID int, source string) tea.Cmd {
	return func() tea.Msg {
		parentCtx := m.appCtx
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(parentCtx, 90*time.Second)
		defer cancel()

		items, err := m.fetchSourceItems(ctx, source)
		if err != nil {
			return historyImportMsg{source: source, opID: opID, err: err}
		}
		imported, upgraded, skipped := service.ImportWatched(m.historyStore, items)
		return historyImportMsg{source: source, opID: opID, imported: imported, upgraded: upgraded, skipped: skipped}
	}
}

// fetchSourceItems pulls every watch list one tracker owns: anime and
// manga from AniList, episodes and movies from Trakt.
func (m *modelImpl) fetchSourceItems(ctx context.Context, source string) ([]scrobble.WatchedItem, error) {
	switch source {
	case "anilist":
		anime, err := m.anilistClient.FetchWatchedList(ctx)
		if err != nil {
			return nil, err
		}
		manga, err := m.anilistClient.FetchMangaList(ctx)
		if err != nil {
			return nil, err
		}
		return append(anime, manga...), nil
	case "trakt":
		return m.traktClient.FetchWatchedHistory(ctx)
	default:
		return nil, fmt.Errorf("unknown import source %q", source)
	}
}

// autoImportSources lists the trackers able to feed the startup sync:
// connected clients only, so the sync never prompts or errors.
func (m *modelImpl) autoImportSources() []string {
	var out []string
	if m.anilistClient != nil && m.anilistClient.IsAuthenticated() {
		out = append(out, "anilist")
	}
	if m.traktClient != nil && m.traktClient.IsAuthenticated() {
		out = append(out, "trakt")
	}
	return out
}

// autoHistoryImportCmd syncs every connected tracker into the local
// store at startup, quietly: one background command, one result. It
// returns nil when there is nothing to sync from.
func (m *modelImpl) autoHistoryImportCmd() tea.Cmd {
	if m.historyStore == nil {
		return nil
	}
	sources := m.autoImportSources()
	if len(sources) == 0 {
		return nil
	}
	opID := m.newOpID()
	m.historyImportOpID = opID
	return func() tea.Msg {
		parentCtx := m.appCtx
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(parentCtx, 180*time.Second)
		defer cancel()

		var (
			imported, upgraded int
			skipped            int
		)
		for _, source := range sources {
			items, err := m.fetchSourceItems(ctx, source)
			if err != nil {
				// A source that errored simply contributes nothing;
				// the next startup (or a manual import) retries it.
				tuiLog.Debug("startup sync fetch failed", "source", source, "err", err)
				continue
			}
			i, u, s := service.ImportWatched(m.historyStore, items)
			imported, upgraded, skipped = imported+i, upgraded+u, skipped+s
		}
		return historyImportMsg{source: "sync", opID: opID, quiet: true, imported: imported, upgraded: upgraded, skipped: skipped}
	}
}

// onHistoryImport reports the import outcome: a toast with counts on
// success, a status line on failure. Stale results are ignored. Quiet
// (startup) results stay silent unless something arrived, and never
// surface errors — an offline start must not nag.
func (m *modelImpl) onHistoryImport(msg historyImportMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.historyImportOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		if msg.quiet {
			tuiLog.Debug("startup sync failed", "source", msg.source, "err", msg.err)
			return m, nil
		}
		logging.Error("history import failed", "source", msg.source, "err", msg.err)
		return m, m.setStatusTimed(statusError, fmt.Sprintf("%s import failed: %s", msg.source, cleanErrorForUI(msg.err)))
	}
	tuiLog.Info("history import done", "source", msg.source, "imported", msg.imported, "upgraded", msg.upgraded, "skipped", msg.skipped)
	if msg.quiet && msg.imported+msg.upgraded == 0 {
		return m, nil
	}
	parts := []string{fmt.Sprintf("%d new", msg.imported)}
	if msg.upgraded > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", msg.upgraded))
	}
	if msg.skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d already here", msg.skipped))
	}
	return m, m.setToast(fmt.Sprintf("Imported from %s: %s", msg.source, strings.Join(parts, ", ")), ToastSuccess)
}

func (m *modelImpl) startAniListAuth() (tea.Model, tea.Cmd) {
	if m.anilistClient == nil {
		return m, m.setStatusTimed(statusError, "AniList integration not configured")
	}
	url := m.anilistClient.AuthURL()
	m.anilistAuthURL = url
	m.authInput.Focus()
	m.authInput.SetValue("")
	_ = util.OpenBrowser(url)
	return m, tea.Batch(m.setStatusTimed(statusInfo, "Browser opened — authorize Kari and paste code"), textinput.Blink)
}

func (m *modelImpl) triggerScrobble(entry history.Entry, media *model.ResolvedMedia) {
	if !trackableEntry(entry) || media == nil {
		return
	}
	if entry.Complete {
		if m.anilistClient != nil && entry.Mode == string(provider.ModeAnime) {
			go func() {
				if err := m.anilistClient.UpdateProgress(context.Background(), *media); err != nil {
					tuiLog.Error("anilist episode progress sync failed", "title", media.SeriesTitle, "episode", media.EpisodeNumber, "err", err)
					return
				}
				tuiLog.Info("anilist episode progress synced", "title", media.SeriesTitle, "episode", media.EpisodeNumber)
			}()
		}
		if m.traktClient != nil {
			go func() {
				var err error
				if entry.MediaType == provider.MediaTypeMovie {
					err = m.traktClient.ScrobbleMovie(context.Background(), *media, 100)
				} else {
					err = m.traktClient.ScrobbleEpisode(context.Background(), *media, 100)
				}
				if err != nil {
					tuiLog.Error("trakt watch sync failed", "title", media.SeriesTitle, "episode", media.EpisodeNumber, "err", err)
					return
				}
				tuiLog.Info("trakt watch synced", "title", media.SeriesTitle, "episode", media.EpisodeNumber)
			}()
		}
	}
}

var skipProviderOptions = []string{"hybrid", "skipdb", "introdb", "anime-skip", "aniskip", "off"}

// trackableEntry reports whether a history entry belongs in tracker
// sync at all. Live has no tracker equivalent (ephemeral schedules, no
// progress model), so it stays out of both directions: never scrobbled
// out, never imported in (the merge layer skips it too).
func trackableEntry(entry history.Entry) bool {
	return entry.Mode != string(provider.ModeLive) && entry.MediaType != provider.MediaTypeLive
}

func cycleSkipProvider(current string, reverse bool) string {
	idx := 0
	for i, opt := range skipProviderOptions {
		if strings.EqualFold(opt, current) {
			idx = i
			break
		}
	}
	step := 1
	if reverse {
		step = -1
	}
	next := (idx + step + len(skipProviderOptions)) % len(skipProviderOptions)
	return skipProviderOptions[next]
}
