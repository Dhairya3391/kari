package tui

// Pins for tracker → local history import from the settings screen:
// dispatch guards, stale-result safety, and user-facing outcome copy.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/history"
	"kari/internal/provider"
	"kari/internal/scrobble"
)

func importTestModel() *modelImpl {
	return &modelImpl{
		keys:              defaultKeyMap(),
		activeView:        viewSettings,
		settingsCategory:  CategoryAccounts,
		historyImportOpID: 3,
	}
}

// importConnectedModel wires a history store so dispatch reaches the
// connection guards.
func importConnectedModel(t *testing.T) *modelImpl {
	t.Helper()
	store, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(store.Close)
	m := importTestModel()
	m.historyStore = store
	return m
}

// Import without a connected tracker explains itself instead of failing.
func TestHistoryImportNeedsConnection(t *testing.T) {
	m := importConnectedModel(t)
	m.settingsIndex = 0
	mdl, _ := m.startHistoryImport("anilist")
	m = mdl.(*modelImpl)
	if m.statusType != statusWarn || !strings.Contains(m.statusText, "Connect AniList") {
		t.Errorf("status = %q/%q, want connect hint", m.statusType, m.statusText)
	}

	m.settingsIndex = 1
	mdl, _ = m.startHistoryImport("trakt")
	m = mdl.(*modelImpl)
	if m.statusType != statusWarn || !strings.Contains(m.statusText, "Connect Trakt") {
		t.Errorf("status = %q/%q, want connect hint", m.statusType, m.statusText)
	}
}

// Import without a history store explains itself instead of crashing.
func TestHistoryImportNeedsStore(t *testing.T) {
	m := importTestModel()
	mdl, _ := m.startHistoryImport("anilist")
	m = mdl.(*modelImpl)
	if m.statusType != statusError {
		t.Errorf("status = %q, want error without a store", m.statusType)
	}
}

// A successful import toasts counts; failures surface as errors;
// stale operations are ignored.
func TestOnHistoryImportReportsCounts(t *testing.T) {
	m := importTestModel()
	mdl, _ := m.onHistoryImport(historyImportMsg{
		source: "anilist", imported: 12, upgraded: 2, skipped: 5, opID: 3,
	})
	m = mdl.(*modelImpl)
	if m.loading {
		t.Error("loading must drop when the import finishes")
	}
	if m.activeToast == nil {
		t.Fatal("expected a result toast")
	}
	text := m.activeToast.Message
	for _, want := range []string{"anilist", "12 new", "2 completed", "5 already here"} {
		if !strings.Contains(text, want) {
			t.Errorf("toast %q missing %q", text, want)
		}
	}
}

func TestOnHistoryImportFailure(t *testing.T) {
	m := importTestModel()
	mdl, _ := m.onHistoryImport(historyImportMsg{
		source: "trakt", opID: 3, err: errors.New("boom"),
	})
	m = mdl.(*modelImpl)
	if m.statusType != statusError || !strings.Contains(m.statusText, "trakt") {
		t.Errorf("status = %q/%q, want trakt error", m.statusType, m.statusText)
	}
}

func TestOnHistoryImportIgnoresStaleOp(t *testing.T) {
	m := importTestModel()
	mdl, _ := m.onHistoryImport(historyImportMsg{
		source: "anilist", imported: 9, opID: 2,
	})
	m = mdl.(*modelImpl)
	if m.activeToast != nil {
		t.Error("stale import result must not toast")
	}
}

func TestTrackableEntrySkipsLive(t *testing.T) {
	live := []history.Entry{
		{Mode: "live", MediaType: "tv"},
		{Mode: "tv", MediaType: "live"},
		{Mode: "live", MediaType: "live"},
	}
	for _, e := range live {
		if trackableEntry(e) {
			t.Errorf("live entry %+v must stay out of tracker sync", e)
		}
	}
	trackable := []history.Entry{
		{Mode: "anime", MediaType: "anime"},
		{Mode: "tv", MediaType: "tv"},
		{Mode: "movies", MediaType: "movie"},
		{Mode: "manga", MediaType: "manga"},
	}
	for _, e := range trackable {
		if !trackableEntry(e) {
			t.Errorf("entry %+v must sync", e)
		}
	}
}

func TestAutoImportSourcesEmptyWhenDisconnected(t *testing.T) {
	m := importTestModel()
	if got := m.autoImportSources(); len(got) != 0 {
		t.Errorf("sources = %v, want none without connected trackers", got)
	}
}

func TestAutoImportSourcesListsConnected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	cfgDir := filepath.Join(dir, ".config", "kari")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Long-lived tokens: AniList implicit-grant tokens carry no expiry,
	// Trakt tokens get a future one.
	if err := os.WriteFile(filepath.Join(cfgDir, "anilist_token.json"), []byte(`{"access_token":"x","expires_at":"0001-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatalf("write anilist token: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "trakt_token.json"), []byte(`{"access_token":"x","refresh_token":"","expires_at":"2999-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatalf("write trakt token: %v", err)
	}

	m := importTestModel()
	m.anilistClient = scrobble.NewAniListClient("id", "secret")
	m.traktClient = scrobble.NewTraktClient("id", "secret")
	got := m.autoImportSources()
	if len(got) != 2 || got[0] != "anilist" || got[1] != "trakt" {
		t.Errorf("sources = %v, want [anilist trakt]", got)
	}
}

func TestOnHistoryImportQuietStaysSilent(t *testing.T) {
	// Quiet success with nothing new: no toast, no status.
	m := importTestModel()
	mdl, _ := m.onHistoryImport(historyImportMsg{
		source: "sync", opID: 3, quiet: true,
	})
	m = mdl.(*modelImpl)
	if m.activeToast != nil {
		t.Error("quiet empty sync must not toast")
	}
	if m.statusType == statusError {
		t.Error("quiet sync must not set error status")
	}

	// Quiet success with arrivals: toast, so the second laptop's
	// screen confirms what came in.
	m = importTestModel()
	mdl, _ = m.onHistoryImport(historyImportMsg{
		source: "sync", imported: 4, upgraded: 1, opID: 3, quiet: true,
	})
	m = mdl.(*modelImpl)
	if m.activeToast == nil {
		t.Fatal("quiet sync with arrivals must toast")
	}

	// Quiet failure (offline start): fully silent.
	m = importTestModel()
	mdl, _ = m.onHistoryImport(historyImportMsg{
		source: "sync", opID: 3, quiet: true, err: errors.New("no route"),
	})
	m = mdl.(*modelImpl)
	if m.activeToast != nil || m.statusType == statusError {
		t.Error("quiet failure must stay silent")
	}
}

func TestAnilistIDFor(t *testing.T) {
	anime := &provider.SearchResult{ID: "154587"}
	if got := anilistIDFor(provider.ModeAnime, anime); got != 154587 {
		t.Errorf("anime numeric id = %d, want 154587", got)
	}
	for _, tc := range []struct {
		name   string
		mode   provider.ContentType
		series *provider.SearchResult
	}{
		{"tv mode never carries one", provider.ModeTV, &provider.SearchResult{ID: "123"}},
		{"movies mode never carries one", provider.ModeMovies, &provider.SearchResult{ID: "123"}},
		{"non-numeric anime id", provider.ModeAnime, &provider.SearchResult{ID: "watch/anikoto/1/sub/1"}},
		{"empty anime id", provider.ModeAnime, &provider.SearchResult{ID: ""}},
		{"nil series", provider.ModeAnime, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := anilistIDFor(tc.mode, tc.series); got != 0 {
				t.Errorf("anilistIDFor = %d, want 0", got)
			}
		})
	}
}

func TestStartupSyncOptionTogglesAndControlsAutoImport(t *testing.T) {
	m := importTestModel()
	m.activeView = viewSettings
	m.settingsCategory = CategoryAccounts
	m.settingsIndex = 2 // "Startup sync"

	// Initial startupSync is false
	if m.startupSync {
		t.Errorf("expected startupSync to be false initially")
	}

	// Toggle with Enter
	mdl, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("enter")})
	m = mdl.(*modelImpl)
	if !m.startupSync {
		t.Errorf("startupSync must be true after toggle")
	}

	// Toggle with Space
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = mdl.(*modelImpl)
	if m.startupSync {
		t.Errorf("startupSync must be false after second toggle")
	}
}
