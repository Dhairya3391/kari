package tui

// Pins for load idempotency (one pipeline at a time) and preview play
// readiness (first source + first sub).

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/ranking"
	"kari/internal/service"
)

func selectTestSubtitle(m *modelImpl, path, language string) {
	track := model.SubtitleTrack{Path: path, Language: language}
	m.resolved.SelectedSubtitle = &track
}

// repairFake is a scripted provider double for the preferred-repair path.
type repairFake struct {
	name    string
	mode    provider.ContentType
	sources []provider.MediaSource
	err     error
}

func (f *repairFake) Name() string { return f.name }
func (f *repairFake) Modes() []provider.Mode {
	return []provider.Mode{{Name: f.mode, Priority: 1}}
}
func (f *repairFake) Search(ctx context.Context, q string, m provider.ContentType) ([]provider.SearchResult, error) {
	return nil, provider.ErrNoResults
}
func (f *repairFake) FetchEpisodes(ctx context.Context, s provider.SearchResult) ([]provider.Episode, error) {
	return nil, provider.ErrNoEpisodes
}
func (f *repairFake) ResolveSource(ctx context.Context, mediaID string, e provider.Episode) ([]provider.MediaSource, error) {
	return f.sources, f.err
}

// A partial resolve with the preferred provider down triggers one quiet
// background repair — no manual R needed for movy-first.
func TestPreferredRepairTriggersOnce(t *testing.T) {
	reg := &provider.Registry{}
	reg.Register(&repairFake{name: "movysx", mode: provider.ModeMovies, err: provider.ErrTimeout})
	reg.Register(&repairFake{name: "pengu", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/b.m3u8", Quality: "1080p"}}})

	m := rankingTestModel()
	m.appMode = provider.ModeMovies
	m.activeView = viewPreview
	m.mediaService = service.NewMediaService(reg)
	series := provider.SearchResult{Title: "T", ID: "1", Provider: "movysx", TMDBID: 42, MediaType: provider.MediaTypeMovie}
	episode := provider.Episode{TMDBID: 42}
	m.selectedSeries = &series
	m.selectedEpisode = &episode

	// Drive a real resolve so LastFailures names movysx.
	resolved, err := m.mediaService.Resolve(context.Background(), provider.ModeMovies, series, episode, nil, service.ResolveOptions{})
	if err != nil {
		t.Fatalf("seed resolve: %v", err)
	}
	if len(resolved.Playback) != 1 {
		t.Fatalf("seed playback = %+v", resolved.Playback)
	}

	m.resolveOpID = 9
	mdl, cmd := m.onResolveDone(resolveDoneMsg{opID: 9, resolved: resolved})
	m = mdl.(*modelImpl)
	if cmd == nil || m.resolveOpID == 0 {
		t.Fatal("preferred failure with partial sources must trigger a background repair")
	}
	if m.preferredRepairAttempts != 1 {
		t.Errorf("repair attempts = %d, want 1", m.preferredRepairAttempts)
	}

	// The repair itself must not chain another: one attempt per pick.
	m.resolveOpID = m.resolveOpID + 1
	mdl, _ = m.onResolveDone(resolveDoneMsg{opID: m.resolveOpID, resolved: resolved})
	m = mdl.(*modelImpl)
	if m.preferredRepairAttempts != 1 {
		t.Errorf("repair must not chain: attempts = %d", m.preferredRepairAttempts)
	}
}

// retryExclude skips delivered providers that did not fail, always
// re-queries failed ones, and refreshes fully when nothing failed.
func TestRetryExcludeSkipsWinnersOnly(t *testing.T) {
	m := rankingTestModel()
	reg := &provider.Registry{}
	reg.Register(&repairFake{name: "movysx", mode: provider.ModeMovies, err: provider.ErrTimeout})
	reg.Register(&repairFake{name: "pengu", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/a.m3u8", Quality: "1080p"}}})
	m.mediaService = service.NewMediaService(reg)
	m.appMode = provider.ModeMovies
	m.resolved = &model.ResolvedMedia{SeriesTitle: "T", Playback: []provider.MediaSource{
		{URL: "https://cdn.example.com/a.m3u8", Resolver: "pengu"},
	}}

	// Nothing failed: full refresh, no exclusion.
	if got := m.retryExclude(); len(got) != 0 {
		t.Errorf("no failures must mean full refresh, got %v", got)
	}

	// movysx failed (never delivered): skip pengu, re-ask movysx.
	series := provider.SearchResult{Title: "T", ID: "1", Provider: "pengu", TMDBID: 42, MediaType: provider.MediaTypeMovie}
	if _, err := m.mediaService.Resolve(context.Background(), provider.ModeMovies, series, provider.Episode{TMDBID: 42}, nil, service.ResolveOptions{}); err != nil {
		t.Fatalf("seed resolve: %v", err)
	}
	got := m.retryExclude()
	if len(got) != 1 || got[0] != "pengu" {
		t.Errorf("retryExclude = %v, want [pengu]", got)
	}
}

// pruneResolvedToResolvers keeps excluded winners' rows, drops rows for
// providers about to be re-queried, and nils the resolve on full refresh.
func TestPruneResolvedToResolvers(t *testing.T) {
	mk := func() *modelImpl {
		m := rankingTestModel()
		m.resolved = &model.ResolvedMedia{SeriesTitle: "T", Playback: []provider.MediaSource{
			{URL: "https://cdn.example.com/a.m3u8", Resolver: "anikoto"},
			{URL: "https://cdn.example.com/b.m3u8", Resolver: "miruro"},
		}, Subtitles: []model.SubtitleTrack{
			{URL: "https://cdn.example.com/a.vtt", Resolver: "anikoto"},
			{URL: "https://cdn.example.com/b.vtt", Resolver: "miruro"},
		}}
		return m
	}

	m := mk()
	m.pruneResolvedToResolvers([]string{"anikoto"})
	if m.resolved == nil || len(m.resolved.Playback) != 1 || m.resolved.Playback[0].Resolver != "anikoto" {
		t.Fatalf("winners must be kept: %+v", m.resolved)
	}
	if len(m.resolved.Subtitles) != 1 || m.resolved.Subtitles[0].Resolver != "anikoto" {
		t.Fatalf("winner subtitles must be kept: %+v", m.resolved.Subtitles)
	}

	m = mk()
	m.pruneResolvedToResolvers(nil)
	if m.resolved != nil {
		t.Fatalf("full refresh must nil the resolve: %+v", m.resolved)
	}

	m = mk()
	m.resolved = nil
	m.pruneResolvedToResolvers([]string{"anikoto"})
	if m.resolved != nil {
		t.Error("nil resolve must stay nil")
	}
}

// The footer offers R retry exactly when a provider failed.
func TestPreviewFooterOffersRetryWhenFailed(t *testing.T) {
	m := rankingTestModel()
	m.appMode = provider.ModeMovies
	m.activeView = viewPreview
	found := false
	for _, b := range m.activeKeyBindings() {
		if b.Key == "R" && b.Action == "retry" {
			found = true
		}
	}
	if found {
		t.Error("no failure: footer must not offer retry")
	}
	m.failedProviderName = "Movy.sx"
	for _, b := range m.activeKeyBindings() {
		if b.Key == "R" && b.Action == "retry" {
			found = true
		}
	}
	if !found {
		t.Error("failed provider: footer must offer R retry")
	}
}

// newSearchInputForTest builds a production-shaped search input (the
// switch-model harness leaves it zero-valued, whose nil cursor panics
// on Focus like production never does).
func newSearchInputForTest() textinput.Model {
	ti := textinput.New()
	ti.CharLimit = 150
	ti.Width = 70
	ti.Placeholder = "search…"
	ti.Prompt = "› "
	ti.Focus()
	return ti
}

func guardTestModel() *modelImpl {
	m := rankingTestModel()
	m.appMode = provider.ModeMovies
	m.subtitleLanguage = "en"
	return m
}

// While episodes load, picking another series waits instead of
// abandoning the in-flight operation.
func TestSelectSeriesBlockedWhileLoading(t *testing.T) {
	m := guardTestModel()
	m.seriesResults = []provider.SearchResult{
		{Title: "A", ID: "1", MediaType: provider.MediaTypeMovie},
		{Title: "B", ID: "2", MediaType: provider.MediaTypeMovie},
	}
	m.loading = true
	m.loadingText = "Loading episodes..."

	mdl, cmd := m.selectSeries(1)
	m = mdl.(*modelImpl)
	if cmd != nil {
		t.Error("selectSeries while loading must not start a command")
	}
	if m.selectedSeries != nil {
		t.Error("selectSeries while loading must not change selection")
	}
	if m.statusType != statusInfo {
		t.Errorf("status = %q, want info with the running load named", m.statusType)
	}
}

// While a resolve is still streaming (opID active, spinner already
// down after the first snapshot), starting another episode waits.
func TestEpisodeResolutionBlockedWhileResolveActive(t *testing.T) {
	m := guardTestModel()
	m.episodeResults = []provider.Episode{{Title: "E1", ID: "e1"}}
	m.resolveOpID = 7

	mdl, cmd := m.startEpisodeResolution(0, false)
	m = mdl.(*modelImpl)
	if cmd != nil {
		t.Error("second resolve while one streams must not start")
	}
	if m.selectedEpisode != nil {
		t.Error("selection must not change while a resolve streams")
	}
}

// Search while anything loads waits with feedback.
func TestSearchBlockedWhileLoading(t *testing.T) {
	m := guardTestModel()
	m.queryInput.SetValue("inception")
	m.playOpID = 3

	mdl, _ := m.startSearchFromInput()
	m = mdl.(*modelImpl)
	if m.searchOpID != 0 {
		t.Error("search while player launches must not start")
	}
}

// Settled state lets loads through.
func TestGuardPassesWhenSettled(t *testing.T) {
	m := guardTestModel()
	m.episodeResults = []provider.Episode{{Title: "E1", ID: "e1"}}
	if !m.guardLoad() {
		t.Error("settled model must pass the guard")
	}
}

// Playback needs the first source: none yet means no start.
func TestPreviewEnterWithoutSourcesDoesNotStart(t *testing.T) {
	m := guardTestModel()
	m.resolved = &model.ResolvedMedia{SeriesTitle: "T"}
	m.activeView = viewPreview

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if m.playOpID != 0 {
		t.Error("enter without sources must not start playback")
	}
}

// Enter with sources starts playback immediately — subtitles never gate.
func TestPreviewEnterStartsWithoutSubtitles(t *testing.T) {
	m := readySourceModel()
	m.activeView = viewPreview

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if m.playOpID == 0 {
		t.Fatal("enter with sources must start playback without waiting for subtitles")
	}
}

// A background subtitle fetch does not delay the launch.
func TestPreviewEnterStartsWhileSubtitleFetchInFlight(t *testing.T) {
	m := readySourceModel()
	m.activeView = viewPreview
	m.subtitleOpID = 7

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if m.playOpID == 0 {
		t.Fatal("enter must start playback while a subtitle fetch runs in the background")
	}
}

// readySourceModel carries one playable source and no subtitles.
func readySourceModel() *modelImpl {
	m := guardTestModel()
	m.resolved = &model.ResolvedMedia{SeriesTitle: "T", Playback: []provider.MediaSource{
		{URL: "https://cdn.example.com/x.m3u8", Quality: "1080p", Resolver: "movysx"},
	}}
	m.rankedSources = ranking.RankSources(m.resolved.Playback, ranking.Criteria{Mode: provider.ModeMovies})
	return m
}

// Enter on preview with sources but no subs starts playback right away;
// the subtitle fetch continues in the background.
func TestPreviewEnterStartsWithoutSubs(t *testing.T) {
	m := readySourceModel()
	m.activeView = viewPreview

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if m.playOpID == 0 {
		t.Fatal("enter must start playback without waiting for subtitles")
	}
}

func TestProviderWaitCountdown(t *testing.T) {
	m := guardTestModel()
	m.registry.Register(&fakeProviderForSwitch{name: "movysx", modes: []provider.Mode{{Name: provider.ModeMovies}}})
	m.registry.Register(&fakeProviderForSwitch{name: "pengu", modes: []provider.Mode{{Name: provider.ModeMovies}}})
	m.beginProviderWait(provider.ModeMovies)
	if m.totalProviders == 0 || m.loadingProviders != m.totalProviders {
		t.Fatalf("wait not opened: total=%d loading=%d", m.totalProviders, m.loadingProviders)
	}
	m.resolved = &model.ResolvedMedia{SeriesTitle: "T", Playback: []provider.MediaSource{
		{URL: "https://cdn.example.com/a.m3u8", Resolver: "movysx"},
	}}
	m.refreshProviderWait()
	if m.loadingProviders != m.totalProviders-1 {
		t.Errorf("one provider answered, loading=%d want %d", m.loadingProviders, m.totalProviders-1)
	}
}

// Space from the results list refocuses search AND restarts the cursor
// blink — without the blink cmd a refocused input looks dead.
func TestSpaceRefocusRestartsBlink(t *testing.T) {
	m := newTestSwitchModel()
	m.appMode = provider.ModeMovies
	m.activeView = viewSearch
	m.seriesResults = []provider.SearchResult{{Title: "T", ID: "1"}}
	m.queryInput = newSearchInputForTest()
	m.queryInput.Blur()

	m.queryInput.SetValue("stale query")
	mdl, cmd := m.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = mdl.(*modelImpl)
	if !m.queryInput.Focused() {
		t.Fatal("space must refocus the search input")
	}
	if cmd == nil {
		t.Error("refocus must restart the blink loop (non-nil cmd)")
	}
	if m.queryInput.Value() != "" {
		t.Errorf("space must open a fresh empty box, got %q", m.queryInput.Value())
	}
}

// langProviderFake declares audio languages for picker tests.
type langProviderFake struct {
	fakeProviderForSwitch
	langs []provider.AudioLanguage
}

func (f *langProviderFake) AudioLanguages() []provider.AudioLanguage { return f.langs }

// Spacebar (which reports " ", not "space") toggles the focused
// language in the audio picker.
func TestAudioPickerSpaceToggles(t *testing.T) {
	// The toggle path calls saveSettings: keep test writes off the
	// real ~/.config/kari/settings.json.
	t.Setenv("HOME", t.TempDir())
	m := newTestSwitchModel()
	m.registry.Register(&langProviderFake{
		fakeProviderForSwitch: fakeProviderForSwitch{name: "p", modes: []provider.Mode{{Name: provider.ModeMovies}}},
		langs:                 []provider.AudioLanguage{{Code: "en", Display: "English"}, {Code: "hi", Display: "Hindi"}},
	})
	m.audioPickerOpen = true
	m.audioPickerIndex = 1
	m.languageFilter = map[string]bool{}

	mdl, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = mdl.(*modelImpl)
	if m.languageFilter["hi"] != false {
		t.Errorf("space must disable Hindi, filter = %v", m.languageFilter)
	}
	if m.languageEnabled("hi") {
		t.Error("disabled Hindi must read disabled")
	}

	// Committed-code scheme stores an explicit true on re-enable (not
	// a delete), so the choice survives a save/load round trip.
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = mdl.(*modelImpl)
	if m.languageFilter["hi"] != true {
		t.Errorf("second space must explicitly re-enable Hindi, filter = %v", m.languageFilter)
	}
	if !m.languageEnabled("hi") {
		t.Error("re-enabled Hindi must read enabled")
	}
}

// A saved all-but-one filter reloads intact: absent stays enabled and
// the choice persists across the settings round trip.
func TestLanguageFilterPersistsAcrossReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestSwitchModel()
	m.registry.Register(&langProviderFake{
		fakeProviderForSwitch: fakeProviderForSwitch{name: "p", modes: []provider.Mode{{Name: provider.ModeMovies}}},
		langs:                 []provider.AudioLanguage{{Code: "en", Display: "English"}, {Code: "hi", Display: "Hindi"}},
	})
	m.languageFilter = map[string]bool{"hi": false, "Hindi": false}
	if !m.hasEnabledLanguage() {
		t.Fatal("one disabled language must not void the filter")
	}
	// Simulate the model.go load guard with an all-disabled map: it
	// must reject, keeping the previous selection.
	m.languageFilter = map[string]bool{"en": false, "English": false, "hi": false, "Hindi": false}
	if m.hasEnabledLanguage() {
		t.Error("all-disabled filter must be rejected at load")
	}
}

// Playback stays in-place on preview for both Live and VOD playback.
func TestLivePlayStaysOnPreview(t *testing.T) {
	m := readySourceModel()
	m.appMode = provider.ModeLive
	m.activeView = viewPreview
	selectTestSubtitle(m, "/tmp/en.srt", "en")

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if m.playOpID == 0 {
		t.Fatal("enter must start playback")
	}
	if m.activeView != viewPreview {
		t.Errorf("live playback must stay on preview, got %q", m.activeView)
	}
}

func TestVodPlayStaysOnPreview(t *testing.T) {
	m := readySourceModel()
	m.appMode = provider.ModeMovies
	m.activeView = viewPreview
	selectTestSubtitle(m, "/tmp/en.srt", "en")

	mdl, _ := m.updatePreview(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if m.playOpID == 0 {
		t.Fatal("enter must start playback")
	}
	if m.activeView != viewPreview {
		t.Errorf("VOD playback must stay on preview, got %q", m.activeView)
	}
}

// Backend display names come from the quality tag, not the aggregator.
func TestSourceBackendName(t *testing.T) {
	display := func(s string) string { return "Pengu-" + s }
	cases := []struct {
		quality string
		want    string
	}{
		{"4K [4KHDHub] (Hindi)", "4KHDHub"},
		{"1080p (Vidstream-2)", "Vidstream-2"},
		{"1080p (Hindi)", "Pengu-pengu"},
		{"Auto", "Pengu-pengu"},
	}
	for _, tc := range cases {
		src := provider.MediaSource{Quality: tc.quality, Resolver: "pengu"}
		if got := sourceBackendName(src, display); got != tc.want {
			t.Errorf("backend(%q) = %q, want %q", tc.quality, got, tc.want)
		}
	}
	if got := sourceBackendName(provider.MediaSource{}, nil); got != "—" {
		t.Errorf("empty source backend = %q, want —", got)
	}
}

// TestPlayingStatusStaysWhilePlaying pins that "Playing in progress..."
// never expires on its own: it lasts until playDoneMsg replaces it with
// the finished/failed status (which keeps the normal threshold).
func TestPlayingStatusStaysWhilePlaying(t *testing.T) {
	m := readySourceModel()
	m.width = 100
	m.height = 24
	m.activeView = viewPreview
	m.playOpID = 10
	m.loading = true

	mdl, _ := m.Update(playStartedMsg{opID: 10})
	m2 := mdl.(*modelImpl)

	if m2.statusText != "Playing in progress..." {
		t.Fatalf("statusText = %q, want playing status", m2.statusText)
	}
	if !m2.statusExpiresAt.IsZero() {
		t.Fatal("playing status must not expire while the player runs")
	}

	mdl2, _ := m2.Update(playDoneMsg{opID: 10, result: player.PlaybackResult{Completed: true}})
	m3 := mdl2.(*modelImpl)
	if m3.statusText != "Playback finished" {
		t.Fatalf("statusText = %q, want finished status", m3.statusText)
	}
	if m3.statusExpiresAt.IsZero() {
		t.Fatal("finished status must keep the normal expiry")
	}
}
