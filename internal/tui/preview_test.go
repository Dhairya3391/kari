package tui

import (
	"strings"
	"testing"

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
}
