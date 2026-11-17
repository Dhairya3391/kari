package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"kari/internal/filter"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/settings"
)

// Ranking reads the typed scope, so editing the active mode's quality must
// change the rows the Preview shows even though Preview has no quality keys.
func TestTypedQualityChangeReranksPreviewSources(t *testing.T) {
	m := rankingTestModel()
	m.appMode = provider.ModeMovies
	m.setQuality(provider.ModeMovies, filter.QualityAll)
	m.mergeResolved(model.ResolvedMedia{Playback: []provider.MediaSource{
		{URL: "sd", Quality: "480p", Resolver: "A"},
		{URL: "fhd", Quality: "1080p", Resolver: "A"},
		{URL: "4k", Quality: "2160p", Resolver: "B"},
	}})
	if len(m.rankedSources) != 3 {
		t.Fatalf("all-quality rows = %d, want 3", len(m.rankedSources))
	}

	m.setQuality(provider.ModeMovies, filter.QualityFHD)
	m.refreshRanking()
	if len(m.rankedSources) != 2 {
		t.Fatalf("1080p rows = %d, want 2 (one per resolver): %v", len(m.rankedSources), urls(m.rankedSources))
	}
	if got := m.filterScope.For(provider.ModeMovies).Quality; got != filter.QualityFHD {
		t.Errorf("typed quality = %v, want FHD", got)
	}
}

// Anime's track is an override: changing it must not touch the movies policy
// even though both modes share the same default policy object.
func TestAnimeTrackPreferenceIsPerMode(t *testing.T) {
	m := rankingTestModel()
	movieBefore := m.filterScope.For(provider.ModeMovies)

	m.setAudioTrack(provider.TrackDub)
	if got := m.filterScope.For(provider.ModeAnime).Track; got != provider.TrackDub {
		t.Errorf("anime track = %v, want dub", got)
	}
	if got := m.filterScope.For(provider.ModeMovies).Track; got != provider.TrackUnknown {
		t.Errorf("movies track = %v, want unchanged", got)
	}
	if after := m.filterScope.For(provider.ModeMovies); !reflect.DeepEqual(after, movieBefore) {
		t.Errorf("movies policy changed: before=%+v after=%+v", movieBefore, after)
	}

	m.setQuality(provider.ModeMovies, filter.QualityFHD)
	if got := m.filterScope.For(provider.ModeAnime).Track; got != provider.TrackDub {
		t.Errorf("anime track drifted after movies edit: %v", got)
	}
}

// The typed scope is persisted verbatim and restored on the next launch.
func TestTypedScopeSurvivesSaveReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestSwitchModel()
	m.appMode = provider.ModeMovies
	m.filterScope = filter.FromLegacy(qualityAll, 0, nil, "", "", false, "")
	m.setQuality(provider.ModeMovies, filter.QualityUHD)
	m.setAudioLanguages(filter.NewLanguagePreference([]string{"ja"}, []string{"hi"}))
	m.setSubtitleLanguage(provider.ModeMovies, "ja")
	m.setAudioTrack(provider.TrackDual)
	m.setPreferredProvider(provider.ModeMovies, "movie_p")
	m.saveSettings()

	want := m.filterScope
	saved := settings.Load()
	if saved == nil || !saved.FiltersMigrated {
		t.Fatal("typed scope was not persisted")
	}
	if !reflect.DeepEqual(saved.Filters, want) {
		t.Errorf("saved scope = %+v, want %+v", saved.Filters, want)
	}

	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	reloaded := NewModel(context.Background(), "", &provider.Registry{}, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	rm := reloaded.(*modelImpl)
	if !reflect.DeepEqual(rm.filterScope, want) {
		t.Errorf("reloaded scope = %+v, want %+v", rm.filterScope, want)
	}
}

// An explicit stream-quality tier (10-13) must survive a reload in the legacy
// field that "same as stream" downloads still consult.
func TestExplicitQualityTierSurvivesReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestSwitchModel()
	m.appMode = provider.ModeMovies
	m.filterScope = filter.FromLegacy(qualityAll, 0, nil, "", "", false, "")
	m.setQuality(provider.ModeMovies, filter.QualityUHD)
	m.saveSettings()

	if saved := settings.Load(); saved == nil || saved.QualityMode != 10 {
		t.Fatalf("saved QualityMode = %+v, want 10", saved)
	}
	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	reloaded := NewModel(context.Background(), "", &provider.Registry{}, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "").(*modelImpl)
	if reloaded.qualityMode != 10 {
		t.Fatalf("reloaded qualityMode = %d, want 10", reloaded.qualityMode)
	}
	if got := reloaded.effectiveDownloadQuality(); got != 10 {
		t.Fatalf("effectiveDownloadQuality = %d, want 10", got)
	}
}

// Each filter row has one explicit owner: no two rows share a label or write
// the same top-level preference field.
func TestSettingsFilterRowsHaveDistinctOwners(t *testing.T) {
	m := newTestSwitchModel()
	m.appMode = provider.ModeMovies
	data := m.collectSettingsData(120, 40)

	rendered := func(cat SettingsCategory) string {
		data.ActiveCategory = cat
		return RenderSettingsScreen(data)
	}

	languages := rendered(CategoryLanguages)
	if strings.Count(languages, "Subtitle language") != 1 {
		t.Errorf("Languages must have exactly one Subtitle language row:\n%s", languages)
	}
	if strings.Contains(languages, "Subtitles") {
		t.Errorf("Languages must not keep the ambiguous Subtitles label:\n%s", languages)
	}

	anime := rendered(CategoryAnime)
	if strings.Count(anime, "Anime subtitles") != 1 {
		t.Errorf("Anime must have exactly one Anime subtitles row:\n%s", anime)
	}

	before := m.filterScope.For(provider.ModeMovies)
	m.setSubtitleLanguage(provider.ModeMovies, "ja")
	afterSubs := m.filterScope.For(provider.ModeMovies)
	if reflect.DeepEqual(before, afterSubs) {
		t.Fatal("subtitle language row did not change the movies policy")
	}
	animeBefore := m.filterScope.For(provider.ModeAnime)
	m.toggleAnimeSubtitles()
	animeAfter := m.filterScope.For(provider.ModeAnime)
	if animeAfter.Track != animeBefore.Track || !reflect.DeepEqual(animeAfter.Audio, animeBefore.Audio) {
		t.Error("anime subtitles row must not change the audio track or languages")
	}
	if animeAfter.Subtitles.Mode == animeBefore.Subtitles.Mode {
		t.Error("anime subtitles row must change the anime subtitle mode")
	}
}
