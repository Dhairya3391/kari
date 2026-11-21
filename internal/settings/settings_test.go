package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kari/internal/filter"
	"kari/internal/provider"
)

func TestNormalizedModesDefaults(t *testing.T) {
	var d *Data
	modes := d.NormalizedModes()
	if !reflect.DeepEqual(modes, DefaultModes) {
		t.Errorf("got %v, want defaults %v", modes, DefaultModes)
	}

	empty := &Data{}
	modesEmpty := empty.NormalizedModes()
	if !reflect.DeepEqual(modesEmpty, DefaultModes) {
		t.Errorf("got %v, want defaults %v", modesEmpty, DefaultModes)
	}
}

func TestNormalizedModesCustomOrderAndMissingAppends(t *testing.T) {
	d := &Data{
		Modes: []string{"tv", "movies", "unknown_mode", "anime"},
	}
	modes := d.NormalizedModes()
	want := []string{"tv", "movies", "anime", "cartoon", "stremio", "live", "manga", "jellyfin"}
	if !reflect.DeepEqual(modes, want) {
		t.Errorf("got %v, want %v", modes, want)
	}
}

func TestDisabledModeSet(t *testing.T) {
	d := &Data{
		ModesDisabled: []string{"manga", "live"},
	}
	set := d.DisabledModeSet()
	if !set["manga"] || !set["live"] || set["anime"] {
		t.Errorf("unexpected disabled set: %+v", set)
	}
}

func TestDisabledHistoryModeSet(t *testing.T) {
	d := &Data{HistoryModesDisabled: []string{" TV ", "MANGA", ""}}
	set := d.DisabledHistoryModeSet()
	if !set["tv"] || !set["manga"] || set[""] || set["anime"] {
		t.Fatalf("unexpected disabled history set: %+v", set)
	}
}

func TestDisabledProviderSet(t *testing.T) {
	var nilData *Data
	if set := nilData.DisabledProviderSet(); len(set) != 0 {
		t.Errorf("expected empty set for nil Data, got %+v", set)
	}

	d := &Data{
		DisabledProviders: []string{"  FAKEAGG  ", "AniVault", "", "   ", "anime:anikoto"},
	}
	set := d.DisabledProviderSet()
	if !set["fakeagg"] || !set["anivault"] || !set["anime:anikoto"] || set[""] {
		t.Errorf("unexpected disabled provider set: %+v", set)
	}
}

func TestSettingsJsonRoundtripBackwardCompatible(t *testing.T) {
	rawJson := `{"quality_mode": 1, "accent_color": "#BB9AF7"}`
	var d Data
	if err := json.Unmarshal([]byte(rawJson), &d); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if d.QualityMode != 1 || d.AccentColor != "#BB9AF7" {
		t.Errorf("unexpected fields: %+v", d)
	}
	if !d.TransitionsEnabled() {
		t.Error("transitions should default to true")
	}
	if !d.StartupSearchActiveEnabled() {
		t.Error("startup_search_active should default to true")
	}
	if !reflect.DeepEqual(d.NormalizedModes(), DefaultModes) {
		t.Errorf("modes = %v, want defaults", d.NormalizedModes())
	}
}

func TestDefaultModeAndCustomOrderRoundtrip(t *testing.T) {
	rawJson := `{"default_mode": "anime", "modes": ["movies", "tv", "anime"]}`
	var d Data
	if err := json.Unmarshal([]byte(rawJson), &d); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if d.DefaultMode != "anime" {
		t.Errorf("DefaultMode = %q, want 'anime'", d.DefaultMode)
	}
	want := []string{"movies", "tv", "anime", "cartoon", "stremio", "live", "manga", "jellyfin"}
	if !reflect.DeepEqual(d.NormalizedModes(), want) {
		t.Errorf("NormalizedModes = %v, want %v", d.NormalizedModes(), want)
	}
}

func TestAllSettingsFieldsPresentInSerializedJSON(t *testing.T) {
	transitions := true
	startupSearchActive := true
	data := &Data{
		QualityMode:           0,
		DownloadQuality:       0,
		LanguageFilter:        map[string]bool{"hi": true},
		SubtitleLanguage:      "en",
		DisableAnimeSubtitles: false,
		DefaultAnimeAudio:     "sub",
		PreferredPlayer:       "mpv",
		Autoplay:              false,
		DisableImages:         false,
		AccentColor:           "auto",
		SkipProvider:          "hybrid",
		AutoSkipIntro:         false,
		AutoSkipEnding:        false,
		SkipRecap:             false,
		SkipPreview:           false,
		StartupSync:           false,
		DefaultMode:           "last",
		Modes:                 DefaultModes,
		ModesDisabled:         []string{},
		LastMode:              "anime",
		Transitions:           &transitions,
		StartupSearchActive:   &startupSearchActive,
		AutoUpdate:            true,
	}

	bytes, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	jsonStr := string(bytes)

	expectedKeys := []string{
		"quality_mode",
		"download_quality",
		"language_filter",
		"disable_anime_subtitles",
		"default_anime_audio",
		"preferred_player",
		"autoplay",
		"disable_images",
		"accent_color",
		"skip_provider",
		"auto_skip_intro",
		"auto_skip_ending",
		"skip_recap",
		"skip_preview",
		"startup_sync",
		"default_mode",
		"modes",
		"modes_disabled",
		"last_mode",
		"transitions",
		"startup_search_active",
		"auto_update",
	}

	for _, key := range expectedKeys {
		if !strings.Contains(jsonStr, `"`+key+`":`) {
			t.Errorf("expected serialized JSON to contain key %q, got:\n%s", key, jsonStr)
		}
	}
}

func TestStartupSearchActiveEnabled(t *testing.T) {
	var nilData *Data
	if !nilData.StartupSearchActiveEnabled() {
		t.Error("nil Data should default to true")
	}

	emptyData := &Data{}
	if !emptyData.StartupSearchActiveEnabled() {
		t.Error("empty Data should default to true")
	}

	f := false
	disabledData := &Data{StartupSearchActive: &f}
	if disabledData.StartupSearchActiveEnabled() {
		t.Error("explicit false should return false")
	}

	tr := true
	enabledData := &Data{StartupSearchActive: &tr}
	if !enabledData.StartupSearchActiveEnabled() {
		t.Error("explicit true should return true")
	}

	var parsed Data
	if err := json.Unmarshal([]byte(`{"startup_search_active": false}`), &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed.StartupSearchActiveEnabled() {
		t.Error("JSON with startup_search_active: false should parse to false")
	}
}

func TestEffectiveFiltersMigratesLegacyFieldsOnce(t *testing.T) {
	d := &Data{
		QualityMode:           1,
		DownloadQuality:       11,
		LanguageFilter:        map[string]bool{"hi": true, "English": false},
		SubtitleLanguage:      "ja",
		DefaultAnimeAudio:     "dub",
		DisableAnimeSubtitles: true,
		PreferredProviders:    map[string]string{"anime": "anikoto"},
	}

	scope := d.EffectiveFilters()
	if !d.FiltersMigrated {
		t.Fatal("first EffectiveFilters call must mark the document migrated")
	}
	if got := scope.For(provider.ModeMovies).Quality; got != filter.QualityFHD {
		t.Errorf("movies quality = %v, want FHD (legacy download tier 11)", got)
	}
	if got := scope.For(provider.ModeAnime).Track; got != provider.TrackDub {
		t.Errorf("anime track = %v, want dub", got)
	}
	if got := scope.For(provider.ModeAnime).Subtitles.Mode; got != filter.SubtitleOff {
		t.Errorf("anime subtitles = %v, want off", got)
	}
	if got := scope.For(provider.ModeAnime).PreferredProvider; got != "anikoto" {
		t.Errorf("anime provider = %q, want anikoto", got)
	}
	if scope.For(provider.ModeAnime).Audio.Allows("en") {
		t.Error("English must be disabled by the legacy filter")
	}

	// A later call must not re-seed from legacy fields.
	d.QualityMode = 3
	if got := d.EffectiveFilters().For(provider.ModeMovies).Quality; got != filter.QualityFHD {
		t.Errorf("migrated scope was re-seeded from legacy fields: %v", got)
	}
}

func TestFiltersScopeJSONRoundTrip(t *testing.T) {
	prefs := filter.Preferences{
		Quality:           filter.QualityDataSaver,
		Audio:             filter.NewLanguagePreference([]string{"Hindi", "ja"}, []string{"English"}),
		Track:             provider.TrackDual,
		Subtitles:         filter.SubtitlePreference{Mode: filter.SubtitlePreferred, Language: "hi"},
		PreferredProvider: "fakeagg",
	}
	in := &Data{FiltersMigrated: true, Filters: filter.Scope{
		Defaults: prefs,
		ByMode:   map[provider.ContentType]filter.Preferences{provider.ModeAnime: prefs},
	}}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Data
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	got := out.EffectiveFilters().For(provider.ModeAnime)
	if got.Quality != prefs.Quality || got.Track != prefs.Track || got.PreferredProvider != prefs.PreferredProvider {
		t.Errorf("round-trip lost preferences: %+v", got)
	}
	if !reflect.DeepEqual(got.Audio.Enabled, []string{"hi", "ja"}) {
		t.Errorf("audio enabled = %v, want canonical [hi ja]", got.Audio.Enabled)
	}
	if !got.Audio.Allows("hi") || got.Audio.Allows("en") {
		t.Errorf("audio disable set not preserved: %+v", got.Audio)
	}
}

func TestLoadMigratesRenamedProviderIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".config", "kari")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := &Data{
		DisabledProviders:  []string{"sweetfox", "anime: SweetFox ", "anivault"},
		PreferredProviders: map[string]string{"movies": "SweetFox", "anime": "anikoto"},
		FiltersMigrated:    true,
		Filters: filter.Scope{
			Defaults: filter.Preferences{PreferredProvider: "sweetfox"},
			ByMode: map[provider.ContentType]filter.Preferences{
				provider.ModeMovies: {PreferredProvider: "SweetFox"},
				provider.ModeTV:     {PreferredProvider: "solis"},
			},
		},
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), body, 0600); err != nil {
		t.Fatal(err)
	}

	got := Load()
	if got == nil {
		t.Fatal("Load() returned nil")
	}
	if want := []string{"vidgold", "anime:vidgold", "anivault"}; !reflect.DeepEqual(got.DisabledProviders, want) {
		t.Errorf("DisabledProviders = %v, want %v", got.DisabledProviders, want)
	}
	if want := map[string]string{"movies": "vidgold", "anime": "anikoto"}; !reflect.DeepEqual(got.PreferredProviders, want) {
		t.Errorf("PreferredProviders = %v, want %v", got.PreferredProviders, want)
	}
	if v := got.Filters.Defaults.PreferredProvider; v != "vidgold" {
		t.Errorf("Filters.Defaults.PreferredProvider = %q, want vidgold", v)
	}
	if v := got.Filters.ByMode[provider.ModeMovies].PreferredProvider; v != "vidgold" {
		t.Errorf("Filters.ByMode[movies] = %q, want vidgold", v)
	}
	if v := got.Filters.ByMode[provider.ModeTV].PreferredProvider; v != "solis" {
		t.Errorf("Filters.ByMode[tv] = %q, want solis (unrelated value must not change)", v)
	}

	// The migration is idempotent: a second load must not change the values.
	if again := Load(); !reflect.DeepEqual(again.DisabledProviders, got.DisabledProviders) {
		t.Errorf("second load changed DisabledProviders: %v", again.DisabledProviders)
	}
}
