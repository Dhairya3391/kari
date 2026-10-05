package ranking

import (
	"testing"
	"time"

	"kari/internal/provider"
)

func TestParseResolution(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"4K [4KHDHub]", 2160},
		{"UHD 2160p", 2160},
		{"QHD 1440p", 1440},
		{"FHD [VegaMovies]", 1080},
		{"1080p WebRip", 1080},
		{"720p HD", 720},
		{"576p PAL", 576},
		{"480p SD", 480},
		{"360p", 360},
		{"Unknown", 0},
		{"", 0},
	}

	for _, tt := range tests {
		got := ParseResolution(tt.input)
		if got != tt.want {
			t.Errorf("ParseResolution(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseFileSizeMB(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"9.80 GB", 9.80 * 1024},
		{"3.05 GB", 3.05 * 1024},
		{"500 MB", 500},
		{"1024 KB", 1},
		{"no size", 0},
	}

	for _, tt := range tests {
		got := ParseFileSizeMB(tt.input)
		if got != tt.want {
			t.Errorf("ParseFileSizeMB(%q) = %f, want %f", tt.input, got, tt.want)
		}
	}
}

func TestRankSources_AudioMatch(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "s1", Resolver: "P1", Quality: "1080p", Language: "Japanese"},
		{URL: "s2", Resolver: "P2", Quality: "1080p", Language: "English"},
		{URL: "s3", Resolver: "P3", Quality: "1080p", Language: "Hindi"},
	}

	crit := Criteria{
		Mode:                  provider.ModeMovies,
		QualityMode:           QualityHighest,
		EnabledAudioLanguages: []string{"Hindi", "English"},
	}

	ranked := RankSources(sources, crit)
	if len(ranked) != 3 {
		t.Fatalf("expected 3 ranked sources, got %d", len(ranked))
	}

	// Hindi is priority 1, English priority 2, Japanese not in list
	if ranked[0].Source.URL != "s3" {
		t.Errorf("expected first source s3 (Hindi), got %s", ranked[0].Source.URL)
	}
	if ranked[1].Source.URL != "s2" {
		t.Errorf("expected second source s2 (English), got %s", ranked[1].Source.URL)
	}
	if ranked[2].Source.URL != "s1" {
		t.Errorf("expected third source s1 (Japanese), got %s", ranked[2].Source.URL)
	}
}

func TestRankSources_AnimeAudio(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "s1", Resolver: "P1", Quality: "1080p", Language: "sub"},
		{URL: "s2", Resolver: "P2", Quality: "1080p", Language: "dub"},
	}

	crit := Criteria{
		Mode:        provider.ModeAnime,
		AnimeAudio:  "dub",
		QualityMode: QualityHighest,
	}

	ranked := RankSources(sources, crit)
	if ranked[0].Source.URL != "s2" {
		t.Errorf("expected dub source s2 to win, got %s", ranked[0].Source.URL)
	}
}

func TestRankSources_Quality(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "s1", Resolver: "P1", Quality: "720p 1.40 GB"},
		{URL: "s2", Resolver: "P2", Quality: "4K 9.80 GB"},
		{URL: "s3", Resolver: "P3", Quality: "1080p 3.05 GB"},
	}

	// Highest / All quality mode
	critHighest := Criteria{
		Mode:        provider.ModeTV,
		QualityMode: QualityHighest,
	}
	rankedHighest := RankSources(sources, critHighest)
	if rankedHighest[0].Source.URL != "s2" { // 4K wins
		t.Errorf("QualityHighest: expected 4K source s2, got %s", rankedHighest[0].Source.URL)
	}

	// Data Saver quality mode: 1080p or below preferred over 4K
	critDataSaver := Criteria{
		Mode:        provider.ModeTV,
		QualityMode: QualityDataSaver,
	}
	rankedDataSaver := RankSources(sources, critDataSaver)
	if rankedDataSaver[0].Source.URL != "s3" { // 1080p wins over 4K and 720p
		t.Errorf("QualityDataSaver: expected 1080p source s3, got %s", rankedDataSaver[0].Source.URL)
	}

	// Lowest quality mode
	critLowest := Criteria{
		Mode:        provider.ModeTV,
		QualityMode: QualityLowest,
	}
	rankedLowest := RankSources(sources, critLowest)
	if rankedLowest[0].Source.URL != "s1" { // 720p wins
		t.Errorf("QualityLowest: expected 720p source s1, got %s", rankedLowest[0].Source.URL)
	}
}

func TestRankSources_StickyProvider(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "s1", Resolver: "ProviderA", Quality: "1080p"},
		{URL: "s2", Resolver: "ProviderB", Quality: "1080p"},
	}

	crit := Criteria{
		Mode:           provider.ModeTV,
		QualityMode:    QualityHighest,
		StickyProvider: "ProviderB",
	}

	ranked := RankSources(sources, crit)
	if ranked[0].Source.URL != "s2" {
		t.Errorf("expected sticky provider source s2, got %s", ranked[0].Source.URL)
	}
}

func TestRankSources_HealthDemotion(t *testing.T) {
	now := time.Now()
	sources := []provider.MediaSource{
		{URL: "s1", Resolver: "SickProvider", Quality: "1080p"},
		{URL: "s2", Resolver: "HealthyProvider", Quality: "1080p"},
	}

	crit := Criteria{
		Mode:        provider.ModeTV,
		QualityMode: QualityHighest,
		Now:         now,
		FailedProviders: map[string]time.Time{
			"sickprovider": now.Add(-2 * time.Minute), // failed 2 mins ago
		},
	}

	ranked := RankSources(sources, crit)
	if ranked[0].Source.URL != "s2" {
		t.Errorf("expected healthy provider source s2 to win, got %s", ranked[0].Source.URL)
	}
}

// Challenged-host rows sort below every playable-now source, even at a
// higher labeled quality: playback must never wait out their bot
// challenges first.
func TestRankSources_ChallengedHostAlwaysLast(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "https://vault-01.uwucdn.top/stream/01/uwu.m3u8", Resolver: "miruro", Quality: "1080p (animepahe)"},
		{URL: "https://cdn.example.com/low.m3u8", Resolver: "miruro", Quality: "360p"},
		{URL: "https://fetch.nexabloom.top/a/b/master.m3u8", Resolver: "anikoto", Quality: "Auto (Vidstream-2)"},
	}

	crit := Criteria{
		Mode:        provider.ModeAnime,
		QualityMode: QualityHighest,
	}

	ranked := RankSources(sources, crit)
	if len(ranked) != 3 {
		t.Fatalf("ranked = %d, want 3", len(ranked))
	}
	last := ranked[2].Source.URL
	if last != "https://vault-01.uwucdn.top/stream/01/uwu.m3u8" {
		t.Errorf("challenged host must rank last, got order %q, %q, %q",
			ranked[0].Source.URL, ranked[1].Source.URL, ranked[2].Source.URL)
	}
}

func TestRankSources_StableTiebreak(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "s1", Resolver: "P1", Quality: "1080p"},
		{URL: "s2", Resolver: "P2", Quality: "1080p"},
		{URL: "s3", Resolver: "P3", Quality: "1080p"},
	}

	crit := Criteria{
		Mode:        provider.ModeTV,
		QualityMode: QualityHighest,
	}

	ranked := RankSources(sources, crit)
	if ranked[0].Source.URL != "s1" || ranked[1].Source.URL != "s2" || ranked[2].Source.URL != "s3" {
		t.Errorf("expected exact order s1, s2, s3, got %s, %s, %s", ranked[0].Source.URL, ranked[1].Source.URL, ranked[2].Source.URL)
	}
}

// Hardsubs sort ahead of softsubs at equal quality; unknown stays between.
func TestSubTypeScoreOrdersHardFirst(t *testing.T) {
	mk := func(subType, quality string) provider.MediaSource {
		return provider.MediaSource{URL: "https://cdn.example.com/" + subType + quality + ".m3u8", Quality: quality, SubType: subType}
	}
	sources := []provider.MediaSource{
		mk(provider.SubTypeSoft, "1080p"),
		mk("", "1080p"),
		mk(provider.SubTypeHard, "1080p"),
		mk(provider.SubTypeHard, "720p"),
	}
	ranked := RankSources(sources, Criteria{Mode: provider.ModeAnime, QualityMode: QualityHighest})
	if len(ranked) != 4 {
		t.Fatalf("ranked = %d", len(ranked))
	}
	want := []string{provider.SubTypeHard, "", provider.SubTypeSoft, provider.SubTypeHard}
	for i, w := range want {
		if ranked[i].Source.SubType != w {
			t.Errorf("ranked[%d].SubType = %q, want %q", i, ranked[i].Source.SubType, w)
		}
	}
}

func TestSubTypeIsIgnoredOutsideAnime(t *testing.T) {
	hard := provider.MediaSource{URL: "https://cdn.example.com/hard.m3u8", Quality: "1080p", SubType: provider.SubTypeHard}
	soft := provider.MediaSource{URL: "https://cdn.example.com/soft.m3u8", Quality: "1080p", SubType: provider.SubTypeSoft}
	if scoreSubType(hard, provider.ModeMovies) != scoreSubType(soft, provider.ModeMovies) {
		t.Fatal("subtitle kind changed non-anime ranking")
	}
}

func TestSubTypeScoreDoesNotTrustHardWithTracks(t *testing.T) {
	hardWithTracks := provider.MediaSource{
		URL:       "https://cdn.example.com/hard.m3u8",
		Quality:   "1080p",
		SubType:   provider.SubTypeHard,
		Subtitles: []provider.SubtitleOption{{URL: "https://cdn.example.com/en.vtt", Language: "en"}},
	}
	soft := provider.MediaSource{URL: "https://cdn.example.com/soft.m3u8", Quality: "1080p", SubType: provider.SubTypeSoft}
	if got := scoreSubType(hardWithTracks, provider.ModeAnime); got != scoreSubType(soft, provider.ModeAnime) {
		t.Fatalf("hard source with subtitle tracks scored %d, soft scored %d", got, scoreSubType(soft, provider.ModeAnime))
	}
}
