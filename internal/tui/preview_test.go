package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/ranking"
)

// A failed provider names capital-R retry: lowercase r restarts
// playback, so "r retry" once sent users into restart.
func TestPreviewFailedProviderRetryHint(t *testing.T) {
	data := PreviewData{
		SeriesTitle:    "Inception",
		FailedProvider: "Movy.sx",
		Width:          100,
		Height:         24,
		Accent:         ResolveAccent(model.KindMovie, "Auto"),
	}
	out := RenderPreviewScreen(data)
	if !strings.Contains(out, "R retry") {
		t.Errorf("retry hint must read R retry, got:\n%s", out)
	}
	if strings.Contains(out, "r retry") {
		t.Errorf("lowercase r retry collides with restart, got:\n%s", out)
	}
}

func TestRenderPreviewShowsSkipDataUnderAutoplay(t *testing.T) {
	data := PreviewData{
		SeriesTitle: "Fire Force",
		SkipInfo:    "AniSkip + SkipDB",
		Width:       100,
	}
	out := RenderPreviewScreen(data)
	autoAt := strings.Index(out, "autoplay")
	skipAt := strings.Index(out, "skip data")
	if autoAt < 0 || skipAt < 0 || skipAt < autoAt {
		t.Fatalf("preview should show skip data after autoplay:\n%s", out)
	}
	if !strings.Contains(out, data.SkipInfo) {
		t.Fatalf("preview missing selected skip provenance: %q", out)
	}
}

// An empty Preview table must distinguish "providers still resolving"
// from "nothing found", or users stare at a dead screen.

func TestPreviewEmptyTableLoadingState(t *testing.T) {
	base := PreviewData{
		SeriesTitle: "The Boys",
		EpisodeInfo: "S01 E01",
		Width:       100,
		Height:      24,
		Accent:      ResolveAccent(model.KindTV, "Auto"),
	}

	loading := base
	loading.Loading = true
	if out := RenderPreviewScreen(loading); !strings.Contains(out, "Getting sources from providers") {
		t.Errorf("resolving preview should show loading state, got:\n%s", out)
	}

	done := base
	if out := RenderPreviewScreen(done); !strings.Contains(out, "No playback sources found") {
		t.Errorf("settled empty preview should say none found, got:\n%s", out)
	}
}
func TestPreviewSubtitlesAccentColor(t *testing.T) {
	accent := ResolveAccent(model.KindTV, "Auto")
	st := NewStyles(accent)

	data := PreviewData{
		SeriesTitle:   "The Boys",
		SubtitlesText: "English ✓",
		Width:         100,
		Height:        24,
		Accent:        accent,
	}

	out := RenderPreviewScreen(data)
	expectedSub := st.Current.Render("English")
	if !strings.Contains(out, expectedSub) {
		t.Errorf("subtitles value must be rendered in accent color (st.Current), output:\n%s", out)
	}
}

func TestPreviewOmitsPlayingBanner(t *testing.T) {
	accent := ResolveAccent(model.KindMovie, "Auto")
	data := PreviewData{
		SeriesTitle: "The Truman Show",
		Width:       100,
		Height:      24,
		Accent:      accent,
	}

	out := RenderPreviewScreen(data)
	if strings.Contains(out, "Now Playing") || strings.Contains(out, "x stop") {
		t.Fatalf("preview must not render the playback banner:\n%s", out)
	}
}

// The sources table carries an audio column for non-anime modes — every
// row names its language (defaulting to English) so the column never
// shifts. Anime keeps the sub/dub header row instead.
func TestPreviewSourcesAudioColumn(t *testing.T) {
	ranked := func() []ranking.ScoredSource {
		return []ranking.ScoredSource{
			{Source: provider.MediaSource{URL: "https://cdn.example.com/a.m3u8", Quality: "1080p [MovieBox]", Resolver: "pengu"}},
			{Source: provider.MediaSource{URL: "https://cdn.example.com/b.m3u8", Quality: "1080p [MovieBox] (Hindi)", Resolver: "pengu", Language: "hi"}},
		}
	}
	t.Run("tv shows audio per row", func(t *testing.T) {
		data := PreviewData{
			SeriesTitle:   "The Boys",
			EpisodeInfo:   "S01 E01",
			RankedSources: ranked(),
			Mode:          provider.ModeTV,
			Width:         100,
			Accent:        ResolveAccent(model.KindTV, "Auto"),
		}
		out := RenderPreviewScreen(data)
		if !strings.Contains(out, "audio") {
			t.Errorf("audio column header missing, got:\n%s", out)
		}
		if !strings.Contains(out, "English") {
			t.Errorf("default English audio missing, got:\n%s", out)
		}
		if !strings.Contains(out, "Hindi") {
			t.Errorf("Hindi audio missing, got:\n%s", out)
		}
	})
	t.Run("anime hides audio column", func(t *testing.T) {
		data := PreviewData{
			SeriesTitle:   "Solo Leveling",
			EpisodeInfo:   "S01 E01",
			AudioText:     "sub · Japanese",
			RankedSources: ranked(),
			Mode:          provider.ModeAnime,
			Width:         100,
			Accent:        ResolveAccent(model.KindAnime, "Auto"),
		}
		out := RenderPreviewScreen(data)
		if strings.Contains(out, "English") || strings.Contains(out, "Hindi") {
			t.Errorf("anime table must not carry per-source audio, got:\n%s", out)
		}
	})
}

// A partial provider failure does not interrupt a usable source list; the
// footer keeps the retry binding available.
func TestPreviewFailureLineHiddenForPartialResults(t *testing.T) {
	data := PreviewData{
		SeriesTitle:    "ONE PIECE",
		Mode:           provider.ModeAnime,
		AudioText:      "sub · Japanese",
		FailedProvider: "anicine",
		RankedSources: []ranking.ScoredSource{
			{Source: provider.MediaSource{URL: "https://cdn.example.com/a.m3u8", Quality: "1080p", Resolver: "x"}},
		},
		Width:  100,
		Height: 30,
		Accent: ResolveAccent(model.KindAnime, "Auto"),
	}
	out := RenderPreviewScreen(data)
	if strings.Contains(out, "✗ anicine failed") {
		t.Fatalf("partial failure line should be hidden, got:\n%s", out)
	}
}

func TestPreviewFailureLineShownWithoutSources(t *testing.T) {
	data := PreviewData{
		SeriesTitle:    "ONE PIECE",
		FailedProvider: "anicine",
		Width:          100,
		Height:         30,
		Accent:         ResolveAccent(model.KindAnime, "Auto"),
	}
	out := RenderPreviewScreen(data)
	if !strings.Contains(out, "✗ anicine failed") || !strings.Contains(out, "R retry") {
		t.Fatalf("no-source failure line missing, got:\n%s", out)
	}
}

func TestPreviewSubtitlesWithSubtitleType(t *testing.T) {
	accent := ResolveAccent(model.KindAnime, "Auto")
	st := NewStyles(accent)

	t.Run("explicit soft subs", func(t *testing.T) {
		data := PreviewData{
			SeriesTitle:   "Frieren",
			AudioText:     "sub · Japanese",
			SubtitlesText: "en ✓",
			SubtitleType:  "soft subs",
			Width:         100,
			Height:        24,
			Accent:        accent,
		}
		out := RenderPreviewScreen(data)
		expected := st.Dim.Render("subtitles  ") + st.Current.Render("en") + " " + st.Ok.Render("✓") + st.Dim.Render(" · soft subs")
		if !strings.Contains(out, expected) {
			t.Errorf("want %q in output, got:\n%s", expected, out)
		}
	})

	t.Run("explicit hard subs", func(t *testing.T) {
		data := PreviewData{
			SeriesTitle:   "Frieren",
			AudioText:     "sub · Japanese",
			SubtitlesText: "en ✓",
			SubtitleType:  "hard subs",
			Width:         100,
			Height:        24,
			Accent:        accent,
		}
		out := RenderPreviewScreen(data)
		expected := st.Dim.Render("subtitles  ") + st.Current.Render("en") + " " + st.Ok.Render("✓") + st.Dim.Render(" · hard subs")
		if !strings.Contains(out, expected) {
			t.Errorf("want %q in output, got:\n%s", expected, out)
		}
	})

	t.Run("embedded in subtitles text string", func(t *testing.T) {
		data := PreviewData{
			SeriesTitle:   "Frieren",
			AudioText:     "sub · Japanese",
			SubtitlesText: "en ✓ · soft subs",
			Width:         100,
			Height:        24,
			Accent:        accent,
		}
		out := RenderPreviewScreen(data)
		expected := st.Dim.Render("subtitles  ") + st.Current.Render("en") + " " + st.Ok.Render("✓") + st.Dim.Render(" · soft subs")
		if !strings.Contains(out, expected) {
			t.Errorf("want %q in output, got:\n%s", expected, out)
		}
	})

	t.Run("with subtitle provider", func(t *testing.T) {
		data := PreviewData{
			SeriesTitle:      "Supernatural",
			SubtitlesText:    "en ✓",
			SubtitleProvider: "Cinecat",
			Width:            100,
			Height:           24,
			Accent:           accent,
		}
		out := RenderPreviewScreen(data)
		expected := st.Dim.Render("subtitles  ") + st.Current.Render("en") + " " + st.Ok.Render("✓") + st.Dim.Render(" · Cinecat")
		if !strings.Contains(out, expected) {
			t.Errorf("want %q in output, got:\n%s", expected, out)
		}
	})

	t.Run("with subtitle provider and subtitle type", func(t *testing.T) {
		data := PreviewData{
			SeriesTitle:      "Frieren",
			SubtitlesText:    "en ✓",
			SubtitleProvider: "Cinecat",
			SubtitleType:     "soft subs",
			Width:            100,
			Height:           24,
			Accent:           accent,
		}
		out := RenderPreviewScreen(data)
		expected := st.Dim.Render("subtitles  ") + st.Current.Render("en") + " " + st.Ok.Render("✓") + st.Dim.Render(" · Cinecat") + st.Dim.Render(" · soft subs")
		if !strings.Contains(out, expected) {
			t.Errorf("want %q in output, got:\n%s", expected, out)
		}
	})
}

func TestPreviewEpisodeIndexAndCount(t *testing.T) {
	episodes := make([]provider.Episode, 327)
	for i := range episodes {
		season := (i / 22) + 1
		epNum := (i % 22) + 1
		title := fmt.Sprintf("Episode %d", epNum)
		if i == 0 {
			title = "Pilot"
		} else if i == 1 {
			title = "Wendigo"
		}
		episodes[i] = provider.Episode{
			Season:  season,
			Episode: epNum,
			Title:   title,
			ID:      fmt.Sprintf("ep-%d", i),
		}
	}

	m := &modelImpl{
		keys:           defaultKeyMap(),
		activeView:     viewPreview,
		episodeResults: episodes,
		selectedSeries: &provider.SearchResult{Title: "Supernatural", Type: provider.ModeTV},
	}

	// Select S01 E02 (Wendigo) at index 1
	m.selectedEpisode = &episodes[1]
	m.episodeIndex = 1

	if idx := m.selectedEpisodeIndex(); idx != 1 {
		t.Fatalf("selectedEpisodeIndex() = %d, want 1", idx)
	}

	data := m.collectPreviewData(100, 30, lipgloss.AdaptiveColor{})
	wantInfo := "S01 E02 · Wendigo  (2 of 327)"
	if data.EpisodeInfo != wantInfo {
		t.Errorf("EpisodeInfo = %q, want %q", data.EpisodeInfo, wantInfo)
	}

	// Select S01 E01 (Pilot) at index 0
	m.selectedEpisode = &episodes[0]
	m.episodeIndex = 0
	if idx := m.selectedEpisodeIndex(); idx != 0 {
		t.Fatalf("selectedEpisodeIndex() = %d, want 0", idx)
	}
	data = m.collectPreviewData(100, 30, lipgloss.AdaptiveColor{})
	wantInfo = "S01 E01 · Pilot  (1 of 327)"
	if data.EpisodeInfo != wantInfo {
		t.Errorf("EpisodeInfo = %q, want %q", data.EpisodeInfo, wantInfo)
	}

	// Out-of-sync episodeIndex repaired by selectedEpisode matching
	m.selectedEpisode = &provider.Episode{Season: 1, Episode: 2, Title: "Wendigo", ID: "ep-1"}
	m.episodeIndex = 0
	if idx := m.selectedEpisodeIndex(); idx != 1 {
		t.Errorf("selectedEpisodeIndex() with matching ID = %d, want 1", idx)
	}
}

func TestStartEpisodeResolutionSyncsState(t *testing.T) {
	episodes := make([]provider.Episode, 50)
	for i := range episodes {
		season := (i / 22) + 1
		epNum := (i % 22) + 1
		episodes[i] = provider.Episode{
			Season:  season,
			Episode: epNum,
			Title:   fmt.Sprintf("S%02dE%02d", season, epNum),
			ID:      fmt.Sprintf("ep-%d", i),
		}
	}

	m := &modelImpl{
		keys:           defaultKeyMap(),
		appMode:        provider.ModeTV,
		activeView:     viewEpisodes,
		episodeResults: episodes,
		selectedSeries: &provider.SearchResult{Title: "Supernatural", Type: provider.ModeTV},
		registry:       &provider.Registry{},
	}

	// Resolving index 23 (Season 2 Episode 2)
	m.startEpisodeResolution(23, false)
	if m.episodeIndex != 23 {
		t.Errorf("episodeIndex = %d, want 23", m.episodeIndex)
	}
	if m.selectedEpisode == nil || m.selectedEpisode.Episode != 2 || m.selectedEpisode.Season != 2 {
		t.Errorf("selectedEpisode = %+v, want S02E02", m.selectedEpisode)
	}
	if m.activeSeason != 1 {
		t.Errorf("activeSeason = %d, want 1 (Season 2)", m.activeSeason)
	}
	if m.seasonEpisodeIndex != 1 {
		t.Errorf("seasonEpisodeIndex = %d, want 1 (second episode of Season 2)", m.seasonEpisodeIndex)
	}
	if idx := m.selectedEpisodeIndex(); idx != 23 {
		t.Errorf("selectedEpisodeIndex() = %d, want 23", idx)
	}

	data := m.collectPreviewData(100, 30, lipgloss.AdaptiveColor{})
	wantInfo := "S02 E02 · S02E02  (24 of 50)"
	if data.EpisodeInfo != wantInfo {
		t.Errorf("EpisodeInfo = %q, want %q", data.EpisodeInfo, wantInfo)
	}
}
