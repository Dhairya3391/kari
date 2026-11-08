package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"

	"kari/internal/config"
	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/ranking"
	"kari/internal/service"
)

func unavailableTestModel() *modelImpl {
	m := rankingTestModel()
	m.appMode = provider.ModeAnime
	m.activeView = viewPreview
	m.backStack = []viewState{viewEpisodes}
	m.selectedSeries = &provider.SearchResult{Title: "ONE PIECE", ID: "21", Type: provider.ModeAnime}
	m.episodeResults = []provider.Episode{
		{Title: "Episode 1178", Episode: 1178, Season: 1, Audio: "dub", ID: "watch/x/21/dub/1178"},
		{Title: "Episode 1179", Episode: 1179, Season: 1, Audio: "dub", ID: "watch/x/21/dub/1179"},
	}
	m.selectedEpisode = &m.episodeResults[1]
	m.episodeIndex = 1
	m.selectedEpisodes = map[int]struct{}{1: {}}
	m.episodeList = list.New(nil, list.NewDefaultDelegate(), 80, 20)
	return m
}

func TestUnavailableAudioStaysOnPreview(t *testing.T) {
	m := unavailableTestModel()
	m.handleUnavailableAudio()

	if len(m.episodeResults) != 2 {
		t.Fatalf("episodes = %+v, want both rows retained", m.episodeResults)
	}
	if m.selectedEpisode == nil || m.selectedEpisode.Episode != 1179 {
		t.Fatalf("selectedEpisode = %+v, want episode 1179", m.selectedEpisode)
	}
	if m.activeView != viewPreview {
		t.Errorf("view = %v, want preview", m.activeView)
	}
	if !strings.Contains(m.statusText, "press a") {
		t.Errorf("status = %q, want a track-switch hint", m.statusText)
	}
}

// TestOnResolveDoneSkipsRetryOnAudioUnavailable proves a verified-missing
// track never triggers the pointless auto-retry loop.
func TestOnResolveDoneSkipsRetryOnAudioUnavailable(t *testing.T) {
	m := unavailableTestModel()
	m.resolveOpID = 7
	m.resolveAttempts = 0
	err := fmt.Errorf("all failed: %w", provider.ErrAudioUnavailable)

	_, cmd := m.onResolveDone(resolveDoneMsg{opID: 7, err: err})
	if cmd != nil {
		t.Error("audio-unavailable must not schedule a retry command")
	}
	if m.loading {
		t.Error("loading must be settled, not left spinning")
	}
	if len(m.episodeResults) != 2 {
		t.Errorf("episodes = %d, want both rows retained", len(m.episodeResults))
	}
	if m.activeView != viewPreview {
		t.Errorf("view = %v, want preview", m.activeView)
	}
	if !strings.Contains(m.statusText, "press a") {
		t.Errorf("status = %q, want a track-switch hint", m.statusText)
	}
}

func TestOnResolveDoneMaterializesProviderSubtitle(t *testing.T) {
	m := unavailableTestModel()
	m.appMode = provider.ModeMovies
	m.subtitleService = service.NewSubtitleService(&config.Config{})
	m.resolveOpID = 7
	m.loading = true
	m.loadingText = "Preparing playback..."
	m.selectedSeries = &provider.SearchResult{Title: "Avengers: Endgame", ID: "299534", MediaType: provider.MediaTypeMovie}
	m.selectedEpisode = &provider.Episode{Title: "Avengers: Endgame", ID: "299534"}

	mdl, _ := m.onResolveDone(resolveDoneMsg{opID: 7, resolved: model.ResolvedMedia{
		SeriesTitle: "Avengers: Endgame",
		MediaType:   provider.MediaTypeMovie,
		Playback: []provider.MediaSource{{
			URL:      "https://cdn.example.com/movie.m3u8",
			Resolver: "movysx",
			Referer:  "https://www.movy.sx/",
		}},
		Subtitles: []model.SubtitleTrack{{URL: "https://cdn.example.com/en.vtt", Language: "en", Resolver: "movysx"}},
	}})
	m = mdl.(*modelImpl)
	if m.subtitleOpID == 0 {
		t.Fatalf("provider subtitle state = opID %d", m.subtitleOpID)
	}
}

func TestStalePlayResultDoesNotMutateCurrentOperation(t *testing.T) {
	m := readySourceModel()
	m.playOpID = 9
	m.loading = true
	m.loadingText = "Opening player..."
	m.autoPlayAfterResolve = true

	mdl, _ := m.onPlayDone(playDoneMsg{opID: 8})
	m = mdl.(*modelImpl)
	if m.playOpID != 9 || !m.loading || m.loadingText != "Opening player..." || !m.autoPlayAfterResolve {
		t.Fatalf("stale result mutated active operation: %+v", m)
	}
}

// TestToggleAnimeAudioClearsResolved proves switching tracks on preview
// drops the old audio's rows instead of merging into them.
func TestToggleAnimeAudioClearsResolved(t *testing.T) {
	m := unavailableTestModel()
	m.audioMode = provider.AudioDub
	m.resolved = &model.ResolvedMedia{
		SeriesTitle: "ONE PIECE",
		Playback: []provider.MediaSource{
			{URL: "https://cdn.example.com/dub.m3u8", Quality: "1080p", Resolver: "x"},
		},
	}
	m.rankedSources = []ranking.ScoredSource{
		{Source: provider.MediaSource{URL: "https://cdn.example.com/dub.m3u8"}},
	}

	mdl, _ := m.toggleAnimeAudio()
	m2 := mdl.(*modelImpl)
	if m2.resolved != nil {
		t.Errorf("resolved = %+v, want cleared before re-resolve", m2.resolved.Playback)
	}
	if len(m2.rankedSources) != 0 {
		t.Errorf("ranked = %d, want cleared", len(m2.rankedSources))
	}
	if m2.audioMode != provider.AudioSub {
		t.Errorf("audioMode = %q, want sub", m2.audioMode)
	}
}

// TestToggleAnimeAudioDoesNotAppendOldSources proves that after toggling audio,
// arriving sources do not merge with the previous track's sources.
func TestToggleAnimeAudioDoesNotAppendOldSources(t *testing.T) {
	m := unavailableTestModel()
	m.audioMode = provider.AudioDub
	m.resolved = &model.ResolvedMedia{
		SeriesTitle: "ONE PIECE",
		Playback: []provider.MediaSource{
			{URL: "https://cdn.example.com/dub.m3u8", Quality: "1080p", Resolver: "x"},
		},
	}
	m.rankedSources = []ranking.ScoredSource{
		{Source: provider.MediaSource{URL: "https://cdn.example.com/dub.m3u8"}},
	}

	mdl, _ := m.toggleAnimeAudio()
	m2 := mdl.(*modelImpl)

	// New sources arrive for sub track
	subSource := provider.MediaSource{URL: "https://cdn.example.com/sub.m3u8", Quality: "1080p", Resolver: "y"}
	m2.mergeResolved(model.ResolvedMedia{
		SeriesTitle: "ONE PIECE",
		Playback:    []provider.MediaSource{subSource},
	})

	if len(m2.resolved.Playback) != 1 {
		t.Fatalf("resolved.Playback = %d, want 1; got %+v", len(m2.resolved.Playback), m2.resolved.Playback)
	}
	if m2.resolved.Playback[0].URL != subSource.URL {
		t.Errorf("expected only sub source, got %+v", m2.resolved.Playback[0])
	}
}

// audioProviderFake declares episode-level sub/dub selection for anime,
// mirroring the real providers that implement it.
type audioProviderFake struct {
	fakeProviderForSwitch
}

func (f *audioProviderFake) Features(mode provider.ContentType) provider.Features {
	if mode == provider.ModeAnime {
		return provider.Features{AudioSelection: true}
	}
	return provider.Features{}
}

// After 'a' switches tracks on Preview, 'n' must resolve the next episode
// on the picked track instead of the lane the listing was fetched with.
func TestNextEpisodeKeepsToggledAudioTrack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := unavailableTestModel()
	m.registry.Register(&audioProviderFake{
		fakeProviderForSwitch: fakeProviderForSwitch{name: "p", modes: []provider.Mode{{Name: provider.ModeAnime}}},
	})
	m.audioMode = provider.AudioSub
	// The listing was fetched in sub, so every handle carries the sub lane.
	for i := range m.episodeResults {
		m.episodeResults[i].Audio = provider.AudioSub
	}
	m.episodeIndex = 0
	m.selectedEpisode = &m.episodeResults[0]

	mdl, _ := m.toggleAnimeAudio()
	m = mdl.(*modelImpl)
	if !stringsEqualFold(m.audioMode, provider.AudioDub) {
		t.Fatalf("audioMode = %q, want dub after toggle", m.audioMode)
	}

	// The in-place track switch settles before the user moves on.
	m.resolveOpID = 0
	m.loading = false

	mdl, _ = m.playNextEpisode()
	m = mdl.(*modelImpl)
	if m.selectedEpisode == nil || m.selectedEpisode.Episode != 1179 {
		t.Fatalf("selectedEpisode = %+v, want episode 1179", m.selectedEpisode)
	}
	if !stringsEqualFold(m.selectedEpisode.Audio, provider.AudioDub) {
		t.Errorf("next episode audio = %q, want dub", m.selectedEpisode.Audio)
	}
}

// When the toggled track is genuinely missing for a later episode, the app
// reports a clear miss instead of silently playing the other lane.
func TestNextEpisodeAudioUnavailableShowsHint(t *testing.T) {
	m := unavailableTestModel()
	m.registry.Register(&audioProviderFake{
		fakeProviderForSwitch: fakeProviderForSwitch{name: "p", modes: []provider.Mode{{Name: provider.ModeAnime}}},
	})
	m.audioMode = provider.AudioDub
	for i := range m.episodeResults {
		m.episodeResults[i].Audio = provider.AudioDub
	}
	m.episodeIndex = 0
	m.selectedEpisode = &m.episodeResults[0]

	mdl, _ := m.startEpisodeResolution(1, false)
	m = mdl.(*modelImpl)
	mdl, _ = m.onResolveDone(resolveDoneMsg{
		opID: m.resolveOpID,
		err:  fmt.Errorf("all providers failed: %w", provider.ErrAudioUnavailable),
	})
	m = mdl.(*modelImpl)

	if m.loading || m.loadingText != "" {
		t.Errorf("loading = %v %q, want settled", m.loading, m.loadingText)
	}
	if len(m.episodeResults) != 2 {
		t.Errorf("episodes = %d, want both rows retained", len(m.episodeResults))
	}
	if m.selectedEpisode == nil || m.selectedEpisode.Episode != 1179 {
		t.Fatalf("selectedEpisode = %+v, want episode 1179", m.selectedEpisode)
	}
	if m.statusType != statusWarn || !strings.Contains(m.statusText, "press a") {
		t.Errorf("status = %q/%q, want a warn with the track-switch hint", m.statusType, m.statusText)
	}
}

// Direct-resolve (movies that skip the episode list) stamps the current
// sub/dub track only when the mode's providers actually support audio
// selection, never as a hardcoded anime check.
func TestSelectSeriesStampsAudioOnlyWithAudioSelection(t *testing.T) {
	newModel := func(p provider.Provider) *modelImpl {
		m := rankingTestModel()
		m.appMode = provider.ModeAnime
		m.audioMode = provider.AudioDub
		m.registry.Register(p)
		m.seriesResults = []provider.SearchResult{{
			Title:     "A Silent Voice",
			ID:        "28851",
			MediaType: provider.MediaTypeMovie,
			Type:      provider.ModeAnime,
		}}
		m.seriesList = list.New(nil, list.NewDefaultDelegate(), 80, 20)
		return m
	}

	supported := newModel(&audioProviderFake{
		fakeProviderForSwitch: fakeProviderForSwitch{name: "p", modes: []provider.Mode{{Name: provider.ModeAnime}}},
	})
	mdl, _ := supported.selectSeries(0)
	supported = mdl.(*modelImpl)
	if supported.selectedEpisode == nil {
		t.Fatal("direct resolve must select an episode handle")
	}
	if !stringsEqualFold(supported.selectedEpisode.Audio, provider.AudioDub) {
		t.Errorf("episode audio = %q, want dub", supported.selectedEpisode.Audio)
	}

	unsupported := newModel(&fakeProviderForSwitch{name: "plain", modes: []provider.Mode{{Name: provider.ModeAnime}}})
	mdl, _ = unsupported.selectSeries(0)
	unsupported = mdl.(*modelImpl)
	if unsupported.selectedEpisode == nil {
		t.Fatal("direct resolve must select an episode handle")
	}
	if unsupported.selectedEpisode.Audio != "" {
		t.Errorf("episode audio = %q, want empty without audio selection", unsupported.selectedEpisode.Audio)
	}
}

// normalizeAudioMode keeps saved and historical values on the provider
// vocabulary; anything but an explicit dub resolves as sub.
func TestNormalizeAudioMode(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"dub", provider.AudioDub},
		{"DUB", provider.AudioDub},
		{" dub ", provider.AudioDub},
		{"sub", provider.AudioSub},
		{"SUB", provider.AudioSub},
		{"", provider.AudioSub},
		{"dual", provider.AudioSub},
	}
	for _, tt := range tests {
		if got := normalizeAudioMode(tt.in); got != tt.want {
			t.Errorf("normalizeAudioMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Resuming from history canonicalizes the saved audio mode before it
// becomes the active preference.
func TestResumeHistoryNormalizesAudioMode(t *testing.T) {
	store, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(store.Close)

	entry := history.Entry{
		Key: history.EntryKey{
			Title:     "Frieren",
			Mode:      string(provider.ModeAnime),
			MediaType: provider.MediaTypeAnime,
			Season:    1,
			Episode:   3,
		},
		Title:     "Frieren",
		Mode:      string(provider.ModeAnime),
		MediaType: provider.MediaTypeAnime,
		AudioMode: "DUB",
	}
	if err := store.Upsert(entry); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	m := rankingTestModel()
	m.historyStore = store
	mdl, _ := m.resumeHistoryEntry(entry)
	m = mdl.(*modelImpl)
	if m.audioMode != provider.AudioDub {
		t.Errorf("audioMode = %q, want %q", m.audioMode, provider.AudioDub)
	}
}

// A finished episode records the track that actually played, and autoplay
// carries that same track into the next episode.
func TestAutoplayAfterAudioToggleKeepsTrack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(store.Close)

	m := unavailableTestModel()
	m.registry.Register(&audioProviderFake{
		fakeProviderForSwitch: fakeProviderForSwitch{name: "p", modes: []provider.Mode{{Name: provider.ModeAnime}}},
	})
	m.historyStore = store
	m.autoplay = true
	m.audioMode = provider.AudioSub
	for i := range m.episodeResults {
		m.episodeResults[i].Audio = provider.AudioSub
	}
	m.episodeIndex = 0
	m.selectedEpisode = &m.episodeResults[0]

	mdl, _ := m.toggleAnimeAudio()
	m = mdl.(*modelImpl)
	if !stringsEqualFold(m.audioMode, provider.AudioDub) {
		t.Fatalf("audioMode = %q, want dub after toggle", m.audioMode)
	}

	// The in-place track switch settles, then the episode finishes.
	m.resolveOpID = 0
	m.loading = false
	m.resolved = &model.ResolvedMedia{
		SeriesTitle:   "ONE PIECE",
		MediaType:     provider.MediaTypeAnime,
		SeasonNumber:  1,
		EpisodeNumber: 1178,
	}
	m.playOpID = 5

	mdl, cmd := m.onPlayDone(playDoneMsg{
		opID: 5,
		result: player.PlaybackResult{
			FinalPositionSecs: 100,
			DurationSecs:      100,
			Completed:         true,
		},
	})
	m = mdl.(*modelImpl)
	if cmd == nil {
		t.Fatal("expected autoplay to start the next episode")
	}

	entry, ok := store.Get(history.EntryKey{
		Title:     "ONE PIECE",
		Mode:      string(provider.ModeAnime),
		MediaType: provider.MediaTypeAnime,
		Season:    1,
		Episode:   1178,
	})
	if !ok {
		t.Fatal("history entry for the finished episode missing")
	}
	if !stringsEqualFold(entry.AudioMode, provider.AudioDub) {
		t.Errorf("history audioMode = %q, want dub", entry.AudioMode)
	}

	if m.selectedEpisode == nil || m.selectedEpisode.Episode != 1179 {
		t.Fatalf("selectedEpisode = %+v, want episode 1179", m.selectedEpisode)
	}
	if !stringsEqualFold(m.selectedEpisode.Audio, provider.AudioDub) {
		t.Errorf("next episode audio = %q, want dub", m.selectedEpisode.Audio)
	}
}

func TestAutoplayOnlyAdvancesWhenEpisodeFinished(t *testing.T) {
	m := readySourceModel()
	m.autoplay = true
	m.selectedSeries = &provider.SearchResult{Title: "Fire Force"}
	m.episodeResults = []provider.Episode{
		{Title: "Episode 1", Episode: 1},
		{Title: "Episode 2", Episode: 2},
	}
	m.episodeIndex = 0
	m.resolved = &model.ResolvedMedia{
		SeriesTitle: "Fire Force",
		MediaType:   provider.MediaTypeAnime,
	}
	m.playOpID = 10

	// Case 1: Video stopped at 20% (unfinished) -> Autoplay must not start next episode
	mdl1, cmd1 := m.onPlayDone(playDoneMsg{
		opID: 10,
		result: player.PlaybackResult{
			FinalPositionSecs: 200,
			DurationSecs:      1000,
			Completed:         false,
		},
	})
	m1 := mdl1.(*modelImpl)
	if cmd1 != nil {
		t.Fatal("expected no command when episode is not finished, but got one")
	}
	if m1.episodeIndex != 0 {
		t.Fatalf("episodeIndex changed to %d on unfinished playback", m1.episodeIndex)
	}

	// Case 2: Video stopped at 82% (>= 80%) -> Autoplay starts next episode
	m.playOpID = 11
	mdl2, cmd2 := m.onPlayDone(playDoneMsg{
		opID: 11,
		result: player.PlaybackResult{
			FinalPositionSecs: 820,
			DurationSecs:      1000,
			Completed:         false,
		},
	})
	m2 := mdl2.(*modelImpl)
	if cmd2 == nil {
		t.Fatal("expected autoplay command when episode is >= 80% finished")
	}
	if m2.episodeIndex != 1 {
		t.Fatalf("episodeIndex = %d, want 1", m2.episodeIndex)
	}
}

func TestAutoplayPrefersSameProviderSource(t *testing.T) {
	m := rankingTestModel()
	m.autoPlayAfterResolve = true
	m.prevSourceResolver = "anikoto"

	miruroFHD := provider.MediaSource{URL: "https://cdn.example.com/miruro.m3u8", Quality: "1080p", Resolver: "miruro"}
	anikotoFHD := provider.MediaSource{URL: "https://cdn.example.com/anikoto.m3u8", Quality: "1080p", Resolver: "anikoto"}

	// Miruro arrives first
	m.mergeResolved(model.ResolvedMedia{
		SeriesTitle: "Fire Force",
		Playback:    []provider.MediaSource{miruroFHD},
	})
	if m.previewSelectedIndex != 0 {
		t.Fatalf("only miruro available, index = %d", m.previewSelectedIndex)
	}

	// Later, Anikoto arrives: because prevSourceResolver is anikoto, it should switch to anikoto
	m.mergeResolved(model.ResolvedMedia{
		SeriesTitle: "Fire Force",
		Playback:    []provider.MediaSource{miruroFHD, anikotoFHD},
	})
	if got := m.rankedSources[m.previewSelectedIndex].Source.Resolver; got != "anikoto" {
		t.Fatalf("selected resolver = %q, want 'anikoto' (same as previous episode)", got)
	}
}
