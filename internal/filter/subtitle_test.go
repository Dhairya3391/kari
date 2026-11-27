package filter

import (
	"testing"

	"kari/internal/provider"
)

func TestSubtitleScore(t *testing.T) {
	cases := []struct {
		name string
		src  provider.MediaSource
		pref SubtitlePreference
		mode provider.ContentType
		want int
	}{
		{
			name: "preferred language matches",
			src:  provider.MediaSource{Subtitles: []provider.SubtitleOption{{Language: "en"}}},
			pref: SubtitlePreference{Mode: SubtitlePreferred, Language: "en"},
			want: 2,
		},
		{
			name: "preferred language matches display label",
			src:  provider.MediaSource{Subtitles: []provider.SubtitleOption{{Language: "English (US)"}}},
			pref: SubtitlePreference{Mode: SubtitlePreferred, Language: "en"},
			want: 2,
		},
		{
			name: "fallback language matches",
			src:  provider.MediaSource{Subtitles: []provider.SubtitleOption{{Language: "en"}}},
			pref: SubtitlePreference{Mode: SubtitlePreferred, Language: "ja", Fallback: []string{"en"}},
			want: 1,
		},
		{
			name: "no track matches",
			src:  provider.MediaSource{Subtitles: []provider.SubtitleOption{{Language: "en"}}},
			pref: SubtitlePreference{Mode: SubtitlePreferred, Language: "ja"},
			want: 0,
		},
		{
			name: "off mode ignores tracks",
			src:  provider.MediaSource{Subtitles: []provider.SubtitleOption{{Language: "en"}}},
			pref: SubtitlePreference{Mode: SubtitleOff, Language: "en"},
			want: 0,
		},
		{
			name: "any mode rewards any track",
			src:  provider.MediaSource{Subtitles: []provider.SubtitleOption{{Language: "fr"}}},
			pref: SubtitlePreference{Mode: SubtitleAny},
			want: 1,
		},
		{
			name: "any mode without tracks scores zero",
			src:  provider.MediaSource{},
			pref: SubtitlePreference{Mode: SubtitleAny},
			want: 0,
		},
		{
			name: "anime hardsub bonus without tracks",
			src:  provider.MediaSource{SubType: provider.SubTypeHard},
			pref: SubtitlePreference{Mode: SubtitlePreferred, Language: "ja", PreferHard: true},
			mode: provider.ModeAnime,
			want: 1,
		},
		{
			name: "hardsub bonus suppressed when track exists",
			src:  provider.MediaSource{SubType: provider.SubTypeHard, Subtitles: []provider.SubtitleOption{{Language: "fr"}}},
			pref: SubtitlePreference{Mode: SubtitlePreferred, Language: "ja", PreferHard: true},
			mode: provider.ModeAnime,
			want: 0,
		},
		{
			name: "hardsub bonus is anime only",
			src:  provider.MediaSource{SubType: provider.SubTypeHard},
			pref: SubtitlePreference{Mode: SubtitlePreferred, Language: "ja", PreferHard: true},
			mode: provider.ModeMovies,
			want: 0,
		},
		{
			name: "hardsub bonus needs the preference",
			src:  provider.MediaSource{SubType: provider.SubTypeHard},
			pref: SubtitlePreference{Mode: SubtitlePreferred, Language: "ja"},
			mode: provider.ModeAnime,
			want: 0,
		},
		{
			name: "off mode suppresses hardsub bonus",
			src:  provider.MediaSource{SubType: provider.SubTypeHard},
			pref: SubtitlePreference{Mode: SubtitleOff, PreferHard: true},
			mode: provider.ModeAnime,
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SubtitleScore(tc.src, tc.pref, tc.mode); got != tc.want {
				t.Errorf("SubtitleScore = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestKeepMatchingSubtitlesUsesRankingParser(t *testing.T) {
	hard := RankedSource{Source: provider.MediaSource{URL: "hard", SubType: provider.SubTypeHard}}
	soft := RankedSource{Source: provider.MediaSource{
		URL:       "soft",
		Subtitles: []provider.SubtitleOption{{Language: "English SDH"}},
	}}
	ranked := []RankedSource{hard, soft}
	pref := SubtitlePreference{Mode: SubtitlePreferred, Language: "en"}

	if got := KeepMatchingSubtitles(ranked, pref, provider.ModeAnime); len(got) != 1 || got[0].Source.URL != "soft" {
		t.Errorf("matching sources = %+v, want only soft", got)
	}
	if got := KeepMatchingSubtitles(ranked, pref, provider.ModeMovies); len(got) != 2 {
		t.Errorf("non-anime sources = %+v, want both", got)
	}
	none := SubtitlePreference{Mode: SubtitlePreferred, Language: "ja"}
	if got := KeepMatchingSubtitles(ranked, none, provider.ModeAnime); len(got) != 2 {
		t.Errorf("no-match sources = %+v, want both", got)
	}
}
