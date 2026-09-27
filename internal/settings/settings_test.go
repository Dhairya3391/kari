package settings

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
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
	want := []string{"tv", "movies", "anime", "cartoon", "live", "manga", "jellyfin"}
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
	want := []string{"movies", "tv", "anime", "cartoon", "live", "manga", "jellyfin"}
	if !reflect.DeepEqual(d.NormalizedModes(), want) {
		t.Errorf("NormalizedModes = %v, want %v", d.NormalizedModes(), want)
	}
}

func TestAllSettingsFieldsPresentInSerializedJSON(t *testing.T) {
	transitions := true
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
	}

	for _, key := range expectedKeys {
		if !strings.Contains(jsonStr, `"`+key+`":`) {
			t.Errorf("expected serialized JSON to contain key %q, got:\n%s", key, jsonStr)
		}
	}
}
