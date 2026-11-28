package filter

import (
	"slices"
	"testing"
	"time"

	"kari/internal/provider"
)

func TestPrepareRankingHierarchy(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "normal-hd", Resolver: "r", Height: 720},
		{URL: "normal-4k", Resolver: "r", Height: 2160},
		{URL: "fallback-fhd", Resolver: "r", Height: 1080, FallbackOnly: true},
		{URL: "normal-sd", Resolver: "r", Height: 480},
	}
	prefs := Preferences{Quality: QualityAll, Subtitles: SubtitlePreference{Mode: SubtitleOff}}
	got := Prepare(sources, prefs, provider.ModeMovies, nil, time.Time{})

	want := []string{"normal-4k", "normal-hd", "normal-sd", "fallback-fhd"}
	if URLs := rankedURLs(got); !slices.Equal(URLs, want) {
		t.Errorf("ranked order = %v, want %v", URLs, want)
	}
	if got[0].OriginalIndex != 1 || got[3].OriginalIndex != 2 {
		t.Errorf("original indices were not preserved: %+v", got)
	}
}

func TestPrepareFailedProviderWindow(t *testing.T) {
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	failed := map[string]time.Time{
		"recent": now.Add(-5 * time.Minute),
		"stale":  now.Add(-20 * time.Minute),
	}
	sources := []provider.MediaSource{
		{URL: "recent-4k", Resolver: "recent", Height: 2160},
		{URL: "healthy-sd", Resolver: "healthy", Height: 480},
		{URL: "stale-hd", Resolver: "stale", Height: 720},
	}
	prefs := Preferences{Quality: QualityAll, Subtitles: SubtitlePreference{Mode: SubtitleOff}}
	got := Prepare(sources, prefs, provider.ModeMovies, failed, now)

	want := []string{"stale-hd", "healthy-sd", "recent-4k"}
	if URLs := rankedURLs(got); !slices.Equal(URLs, want) {
		t.Errorf("ranked order = %v, want %v", URLs, want)
	}
}

func TestPrepareDualAudioMatchesBothTracks(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "sub", Resolver: "a", Height: 1080, Audio: provider.AudioInfo{Track: provider.TrackSub}},
		{URL: "dub", Resolver: "a", Height: 1080, Audio: provider.AudioInfo{Track: provider.TrackDub}},
		{URL: "dual", Resolver: "a", Height: 1080, Audio: provider.AudioInfo{Track: provider.TrackDual}},
	}
	cases := []struct {
		name    string
		track   provider.AudioTrack
		wantURL []string
	}{
		{"sub preference", provider.TrackSub, []string{"sub", "dual", "dub"}},
		{"dub preference", provider.TrackDub, []string{"dub", "dual", "sub"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefs := Preferences{Quality: QualityAll, Track: tc.track, Subtitles: SubtitlePreference{Mode: SubtitleOff}}
			got := rankedURLs(Prepare(sources, prefs, provider.ModeAnime, nil, time.Time{}))
			if !slices.Equal(got, tc.wantURL) {
				t.Errorf("ranked order = %v, want %v", got, tc.wantURL)
			}
		})
	}
}

func TestPrepareSubtitlePreferenceNeverHidesSources(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "no-subs", Height: 720},
		{URL: "with-subs", Height: 720, Subtitles: []provider.SubtitleOption{{Language: "en"}}},
	}
	for _, mode := range []SubtitleMode{SubtitleOff, SubtitlePreferred, SubtitleAny} {
		prefs := Preferences{Quality: QualityAll, Subtitles: SubtitlePreference{Mode: mode, Language: "en"}}
		got := Prepare(sources, prefs, provider.ModeMovies, nil, time.Time{})
		if len(got) != len(sources) {
			t.Errorf("subtitle mode %d kept %d sources, want all %d", mode, len(got), len(sources))
		}
	}
}

func TestPrepareLanguageHardFilter(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "en", Language: "en", Height: 1080},
		{URL: "hi", Language: "hi", Height: 1080},
	}
	prefs := Preferences{
		Quality:   QualityAll,
		Audio:     NewLanguagePreference(nil, []string{"English"}),
		Subtitles: SubtitlePreference{Mode: SubtitleOff},
	}
	got := rankedURLs(Prepare(sources, prefs, provider.ModeMovies, nil, time.Time{}))
	if !slices.Equal(got, []string{"hi"}) {
		t.Errorf("ranked order = %v, want only hi", got)
	}
}

// A guaranteed untiered row must sort last in Lowest mode. Ranking it at 0
// would put it ahead of every known tier (whose rank is negative), making an
// unknown-resolution source the default pick for a "lowest quality" user.
func TestPrepareLowestPutsUnknownLast(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "unknown", Resolver: "a", Quality: "Direct"},
		{URL: "known-sd", Resolver: "b", Quality: "480p"},
	}
	prefs := Preferences{Quality: QualityLowest, Subtitles: SubtitlePreference{Mode: SubtitleOff}}
	got := rankedURLs(Prepare(sources, prefs, provider.ModeMovies, nil, time.Time{}))
	if !slices.Equal(got, []string{"known-sd", "unknown"}) {
		t.Errorf("ranked order = %v, want known-sd before unknown", got)
	}
}

// Language filtering must run before tier selection. Otherwise a resolver's
// only tier-matching row can be language-disabled and removed, leaving no
// source even though another allowed tier from that resolver exists.
func TestPrepareLanguageBeforeTierSelection(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "en-1080", Resolver: "a", Language: "en", Height: 1080},
		{URL: "hi-720", Resolver: "a", Language: "hi", Height: 720},
	}
	prefs := Preferences{
		Audio:     NewLanguagePreference(nil, []string{"English"}),
		Subtitles: SubtitlePreference{Mode: SubtitleOff},
	}
	for _, tc := range []struct {
		name string
		mode QualityMode
	}{
		{"highest", QualityHighest},
		{"full HD", QualityFHD},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefs.Quality = tc.mode
			got := rankedURLs(Prepare(sources, prefs, provider.ModeMovies, nil, time.Time{}))
			if !slices.Equal(got, []string{"hi-720"}) {
				t.Errorf("ranked order = %v, want the allowed Hindi source", got)
			}
		})
	}
}

func TestPrepareAdaptiveVisibleInAllModes(t *testing.T) {
	for _, mode := range []QualityMode{
		QualityAll, QualityHighest, QualityDataSaver, QualityLowest,
		QualityUHD, QualityFHD, QualityHD, QualitySD,
	} {
		sources := []provider.MediaSource{
			{URL: "adaptive", Resolver: "a", Quality: "Auto"},
			{URL: "known", Resolver: "b", Quality: "720p"},
		}
		prefs := Preferences{Quality: mode, Subtitles: SubtitlePreference{Mode: SubtitleOff}}
		got := rankedURLs(Prepare(sources, prefs, provider.ModeMovies, nil, time.Time{}))
		if len(got) != 2 {
			t.Errorf("mode %d kept %v, want both adaptive and known rows", mode, got)
		}
	}
}

func TestPreparePerResolverGuarantee(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "a-high", Resolver: "a", Height: 1080},
		{URL: "b-low", Resolver: "b", Height: 480},
	}
	prefs := Preferences{Quality: QualityUHD, Subtitles: SubtitlePreference{Mode: SubtitleOff}}
	got := rankedURLs(Prepare(sources, prefs, provider.ModeMovies, nil, time.Time{}))
	if len(got) != 2 {
		t.Fatalf("kept %v, want one row per resolver", got)
	}
}

// TestGroupByProviderSkipsGuaranteedOnlyProvider keeps a resolver that only
// survived via the per-resolver quality guarantee from being promoted ahead of
// a row that actually matches the selected tier. Without this, an explicit
// download tier could be replaced by the preferred provider's lower-quality
// source.
func TestGroupByProviderSkipsGuaranteedOnlyProvider(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "preferred-720", Resolver: "preferred", Height: 720},
		{URL: "other-1080", Resolver: "other", Height: 1080},
	}
	prefs := Preferences{Quality: QualityFHD, Subtitles: SubtitlePreference{Mode: SubtitleOff}}
	ranked := Prepare(sources, prefs, provider.ModeMovies, nil, time.Time{})
	grouped := GroupByProvider(ranked, "preferred")
	if len(grouped) != 2 {
		t.Fatalf("kept %d rows, want both (guaranteed row stays visible)", len(grouped))
	}
	if got := grouped[0].Source.URL; got != "other-1080" {
		t.Fatalf("first source = %q, want other-1080", got)
	}
	if !grouped[1].Guaranteed {
		t.Fatal("expected the guaranteed row at the back to be marked as such")
	}
}

func TestGroupByProviderStability(t *testing.T) {
	rows := []RankedSource{
		{Source: provider.MediaSource{Resolver: "b"}, OriginalIndex: 0},
		{Source: provider.MediaSource{Resolver: "a"}, OriginalIndex: 1},
		{Source: provider.MediaSource{Resolver: "b"}, OriginalIndex: 2},
		{Source: provider.MediaSource{Resolver: "c"}, OriginalIndex: 3},
		{Source: provider.MediaSource{Resolver: "a"}, OriginalIndex: 4},
	}
	cases := []struct {
		name      string
		preferred string
		want      []int
	}{
		{"first seen order", "", []int{0, 2, 1, 4, 3}},
		{"preferred group first", "a", []int{1, 4, 0, 2, 3}},
		{"unknown preferred is ignored", "z", []int{0, 2, 1, 4, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GroupByProvider(rows, tc.preferred)
			indices := make([]int, 0, len(got))
			for _, row := range got {
				indices = append(indices, row.OriginalIndex)
			}
			if !slices.Equal(indices, tc.want) {
				t.Errorf("grouped indices = %v, want %v", indices, tc.want)
			}
		})
	}
}

func rankedURLs(rows []RankedSource) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Source.URL)
	}
	return out
}
