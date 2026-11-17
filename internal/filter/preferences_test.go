package filter

import (
	"testing"

	"kari/internal/provider"
)

func TestFromLegacyQuality(t *testing.T) {
	cases := []struct {
		name            string
		qualityMode     int
		downloadQuality int
		want            QualityMode
	}{
		{"playback all", 0, 0, QualityAll},
		{"playback highest", 1, 0, QualityHighest},
		{"playback data saver", 2, 0, QualityDataSaver},
		{"playback lowest", 3, 0, QualityLowest},
		{"download uhd overrides", 0, 10, QualityUHD},
		{"download fhd overrides", 1, 11, QualityFHD},
		{"download hd overrides", 2, 12, QualityHD},
		{"download sd overrides", 3, 13, QualitySD},
		{"non effective download ignored", 1, 2, QualityHighest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope := FromLegacy(tc.qualityMode, tc.downloadQuality, nil, "", "", false, "")
			if got := scope.For(provider.ModeMovies).Quality; got != tc.want {
				t.Errorf("quality = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFromLegacyLanguageFilter(t *testing.T) {
	scope := FromLegacy(0, 0, map[string]bool{
		"English":    false,
		"en":         false,
		"Hindi":      true,
		"fr":         true,
		"Portuguese": true,
	}, "", "", false, "")
	audio := scope.For(provider.ModeMovies).Audio

	if audio.Allows("en") || audio.Allows("English") {
		t.Error("English must be disabled by both the display and code keys")
	}
	if !audio.Allows("hi") || !audio.Allows("fr") {
		t.Error("enabled languages must remain allowed")
	}
	if !audio.Allows("es") {
		t.Error("unlisted languages must be allowed by default")
	}
	if got := audio.Priority("hi"); got != 0 {
		t.Errorf("hindi priority = %d, want 0", got)
	}
	if want := []string{"hi", "pt", "fr"}; !equalStrings(audio.Enabled, want) {
		t.Errorf("canonical enabled = %v, want %v", audio.Enabled, want)
	}
}

func TestFromLegacySubtitles(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantMode SubtitleMode
		wantLang string
	}{
		{"off", "off", SubtitleOff, ""},
		{"empty", "", SubtitleAny, ""},
		{"language", "ja", SubtitlePreferred, "ja"},
		{"language label", "English", SubtitlePreferred, "en"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefs := FromLegacy(0, 0, nil, tc.raw, "", false, "").For(provider.ModeMovies).Subtitles
			if prefs.Mode != tc.wantMode || prefs.Language != tc.wantLang {
				t.Errorf("subtitles = %+v, want mode %d language %q", prefs, tc.wantMode, tc.wantLang)
			}
		})
	}
}

func TestFromLegacyAnimeScope(t *testing.T) {
	scope := FromLegacy(0, 0, nil, "en", "Dub · English", true, "preferred")

	anime := scope.For(provider.ModeAnime)
	if anime.Track != provider.TrackDub {
		t.Errorf("anime track = %q, want dub", anime.Track)
	}
	if anime.Subtitles.PreferHard {
		t.Error("disableAnimeSubs must clear the hardsub preference")
	}
	if anime.Subtitles.Mode != SubtitleOff {
		t.Errorf("disableAnimeSubs must turn anime subtitles off: %+v", anime.Subtitles)
	}
	if anime.PreferredProvider != "preferred" {
		t.Errorf("anime lost the preferred provider: %q", anime.PreferredProvider)
	}

	movies := scope.For(provider.ModeMovies)
	if movies.Track != provider.TrackUnknown {
		t.Errorf("movie track = %q, want unknown", movies.Track)
	}
	if movies.Subtitles.PreferHard {
		t.Error("movie hardsub preference must stay off")
	}
}

func TestScopeForOverlay(t *testing.T) {
	defaults := Preferences{
		Quality:           QualityHighest,
		Audio:             NewLanguagePreference([]string{"en"}, nil),
		Subtitles:         SubtitlePreference{Mode: SubtitlePreferred, Language: "en"},
		PreferredProvider: "default",
	}
	animePrefs := defaults
	animePrefs.Quality = QualityLowest
	animePrefs.Track = provider.TrackDub
	animePrefs.Subtitles = SubtitlePreference{Mode: SubtitlePreferred, Language: "en", PreferHard: true}
	scope := Scope{
		Defaults: defaults,
		ByMode: map[provider.ContentType]Preferences{
			provider.ModeAnime: animePrefs,
		},
	}

	anime := scope.For(provider.ModeAnime)
	if anime.Quality != QualityLowest {
		t.Errorf("anime quality = %d, want lowest", anime.Quality)
	}
	if anime.Track != provider.TrackDub {
		t.Errorf("anime track = %q, want dub", anime.Track)
	}
	if !anime.Subtitles.PreferHard {
		t.Error("anime hardsub preference must be set")
	}
	if !equalStrings(anime.Audio.Enabled, []string{"en"}) || anime.PreferredProvider != "default" {
		t.Errorf("anime override lost inherited fields: %+v", anime)
	}

	movies := scope.For(provider.ModeMovies)
	if movies.Quality != QualityHighest || movies.Track != provider.TrackUnknown || movies.Subtitles.PreferHard {
		t.Errorf("movie preferences picked up anime overrides: %+v", movies)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
