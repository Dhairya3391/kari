package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/config"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/ranking"
	"kari/internal/service"
)

func TestDetectSubtitleType(t *testing.T) {
	cases := []struct {
		name         string
		src          provider.MediaSource
		resolved     *model.ResolvedMedia
		rawSubtitles []model.SubtitleTrack
		mode         provider.ContentType
		audioMode    string
		want         string
	}{
		{
			name: "explicit hard subtype",
			src:  provider.MediaSource{SubType: provider.SubTypeHard},
			mode: provider.ModeAnime,
			want: "hard subs",
		},
		{
			name: "verified hard subtype survives global subtitle tracks",
			src:  provider.MediaSource{SubType: provider.SubTypeHard},
			mode: provider.ModeAnime,
			resolved: &model.ResolvedMedia{
				Subtitles: []model.SubtitleTrack{{Path: "/tmp/sub.srt", Language: "en"}},
			},
			want: "hard subs",
		},
		{
			name: "verified hard subtype survives attached sidecar tracks",
			src: provider.MediaSource{
				SubType:   provider.SubTypeHard,
				Subtitles: []provider.SubtitleOption{{URL: "https://cdn.example.com/sub.vtt", Language: "en"}},
			},
			mode: provider.ModeAnime,
			want: "hard subs",
		},
		{
			name: "explicit soft subtype",
			src:  provider.MediaSource{SubType: provider.SubTypeSoft},
			mode: provider.ModeAnime,
			want: "soft subs",
		},
		{
			name: "hard sub in quality tag is not trusted",
			src:  provider.MediaSource{Quality: "1080p [Hard sub]"},
			mode: provider.ModeAnime,
			want: "",
		},
		{
			name: "soft sub in quality tag",
			src:  provider.MediaSource{Quality: "1080p [Soft sub]"},
			mode: provider.ModeAnime,
			want: "soft subs",
		},
		{
			name: "hardsub in URL is not trusted",
			src:  provider.MediaSource{URL: "https://cdn.example.com/ep1-hardsub.m3u8"},
			mode: provider.ModeAnime,
			want: "",
		},
		{
			name: "source subtitles non-empty",
			src: provider.MediaSource{
				Subtitles: []provider.SubtitleOption{{URL: "https://cdn.example.com/sub.vtt", Language: "en"}},
			},
			mode: provider.ModeAnime,
			want: "soft subs",
		},
		{
			name: "resolved subtitles non-empty",
			src:  provider.MediaSource{},
			mode: provider.ModeAnime,
			resolved: &model.ResolvedMedia{
				Subtitles: []model.SubtitleTrack{{Path: "/tmp/sub.srt", Language: "en"}},
			},
			want: "soft subs",
		},
		{
			name:         "raw subtitles non-empty",
			src:          provider.MediaSource{},
			rawSubtitles: []model.SubtitleTrack{{URL: "https://cdn.example.com/sub.vtt"}},
			mode:         provider.ModeAnime,
			want:         "soft subs",
		},
		{
			name:      "anime sub without separate tracks is unknown",
			src:       provider.MediaSource{Quality: "1080p"},
			mode:      provider.ModeAnime,
			audioMode: "sub",
			want:      "",
		},
		{
			name:      "anime dub without separate tracks has no subs",
			src:       provider.MediaSource{Quality: "1080p"},
			mode:      provider.ModeAnime,
			audioMode: "dub",
			want:      "",
		},
		{
			name: "non-anime subtype is hidden",
			src:  provider.MediaSource{SubType: provider.SubTypeHard},
			mode: provider.ModeMovies,
			want: "",
		},
		{
			name:      "movies without separate tracks has no subs",
			src:       provider.MediaSource{Quality: "1080p"},
			mode:      provider.ModeMovies,
			audioMode: "",
			want:      "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectSubtitleType(tc.src, tc.resolved, tc.rawSubtitles, tc.mode, tc.audioMode)
			if got != tc.want {
				t.Errorf("detectSubtitleType() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCollectPreviewDataSubtitleType(t *testing.T) {
	m := unavailableTestModel()
	m.appMode = provider.ModeAnime
	m.audioMode = "sub"
	m.subtitleLanguage = "en"
	m.subtitleLangUsed = "en"

	hardSrc := provider.MediaSource{URL: "https://cdn.example.com/hard.m3u8", Quality: "1080p", SubType: provider.SubTypeHard}
	softSrc := provider.MediaSource{URL: "https://cdn.example.com/soft.m3u8", Quality: "1080p", SubType: provider.SubTypeSoft}

	m.resolved = &model.ResolvedMedia{
		SeriesTitle: "Frieren",
		Playback:    []provider.MediaSource{hardSrc, softSrc},
		Subtitles:   []model.SubtitleTrack{{URL: "https://cdn.example.com/en.vtt", Language: "en"}},
	}
	selectTestSubtitle(m, "/tmp/en.srt", "en")
	m.refreshRanking()

	// Initially cursor is on hardSrc (hard subs sort first)
	data := m.collectPreviewData(100, 30, lipgloss.AdaptiveColor{})
	if data.SubtitlesText != "en ✓" {
		t.Errorf("SubtitlesText = %q, want 'en ✓'", data.SubtitlesText)
	}
	if data.SubtitleType != "hard subs" {
		t.Errorf("SubtitleType = %q, want 'hard subs'", data.SubtitleType)
	}

	// Move cursor to softSrc
	m.previewSelectedIndex = 1
	data2 := m.collectPreviewData(100, 30, lipgloss.AdaptiveColor{})
	if data2.SubtitleType != "soft subs" {
		t.Errorf("SubtitleType = %q, want 'soft subs'", data2.SubtitleType)
	}
}

func TestPreviewSubtitleCheckmarkWaitsForDownloadedPath(t *testing.T) {
	m := unavailableTestModel()
	m.appMode = provider.ModeMovies
	m.subtitleLanguage = "en"
	m.subtitleLangUsed = "en"
	m.resolved = &model.ResolvedMedia{
		Playback:  []provider.MediaSource{{URL: "https://cdn.example.com/movie.m3u8", Quality: "1080p", Resolver: "movysx"}},
		Subtitles: []model.SubtitleTrack{{URL: "https://cdn.example.com/en.vtt", Language: "en"}},
	}
	m.rankedSources = []ranking.ScoredSource{{Source: m.resolved.Playback[0]}}
	data := m.collectPreviewData(100, 24, lipgloss.AdaptiveColor{})
	if data.SubtitlesText != "en" {
		t.Fatalf("subtitle text = %q, want no checkmark before download", data.SubtitlesText)
	}
}

func TestTriggerSubtitleSyncMaterializesProviderURL(t *testing.T) {
	m := readySourceModel()
	m.subtitleService = service.NewSubtitleService(&config.Config{})
	m.rawSubtitles = []model.SubtitleTrack{{
		URL:      "https://cdn.example.com/en.vtt",
		Language: "en",
		Resolver: "movysx",
	}}
	m.resolved.Subtitles = append([]model.SubtitleTrack{}, m.rawSubtitles...)
	m.subtitleLanguage = "en"
	if cmd := m.triggerSubtitleSync(); cmd == nil {
		t.Fatal("provider subtitle URLs must start a materialization command")
	}
	if m.subtitleOpID == 0 {
		t.Fatalf("subtitle state = opID %d", m.subtitleOpID)
	}
}

func TestSubtitlesOffClearsSelectedTrack(t *testing.T) {
	m := readySourceModel()
	m.subtitleLanguage = "off"
	selectTestSubtitle(m, "/tmp/en.srt", "en")

	if cmd := m.triggerSubtitleSync(); cmd != nil {
		t.Fatal("disabled subtitles must not start a fetch")
	}
	if m.resolved.SelectedSubtitle != nil {
		t.Fatal("disabled subtitles must clear the selected track")
	}
}

func TestHardSubAnimeDoesNotWaitForExternalSubtitle(t *testing.T) {
	m := readySourceModel()
	m.appMode = provider.ModeAnime
	m.resolved.MediaType = provider.MediaTypeAnime
	m.resolved.Playback[0].SubType = provider.SubTypeHard
	m.refreshRanking()

	if m.subtitlesWanted() {
		t.Fatal("hard-sub anime must not fetch an external subtitle")
	}
}

func TestOnSubtitleDoneSelectsOneTrackWithoutMutatingCandidates(t *testing.T) {
	m := readySourceModel()
	m.rawSubtitles = []model.SubtitleTrack{{URL: "https://cdn.example.com/en.vtt", Language: "en"}}
	m.resolved.Subtitles = append([]model.SubtitleTrack{}, m.rawSubtitles...)
	m.subtitleOpID = 4
	track := model.SubtitleTrack{Path: "/tmp/en.srt", Language: "en", Resolver: "movysx"}

	mdl, _ := m.onSubtitleDone(subtitleDoneMsg{opID: 4, track: track})
	m = mdl.(*modelImpl)
	if m.resolved.SelectedSubtitle == nil || m.resolved.SelectedSubtitle.Path != track.Path {
		t.Fatalf("selected subtitle = %+v", m.resolved.SelectedSubtitle)
	}
	if len(m.resolved.Subtitles) != 1 || m.resolved.Subtitles[0].URL == "" {
		t.Fatalf("provider candidates were mutated: %+v", m.resolved.Subtitles)
	}
}

func TestPlaybackStatusRendersAboveFooter(t *testing.T) {
	m := unavailableTestModel()
	m.width = 100
	m.height = 24
	m.setStatus(statusSuccess, "Playback finished")
	out := m.renderMainView()
	if !strings.Contains(out, "Playback finished") {
		t.Fatalf("playback status missing from main view:\n%s", out)
	}
}

func TestAutoPlayAfterResolveWaitsForSubtitles(t *testing.T) {
	m := readySourceModel()
	m.subtitleService = service.NewSubtitleService(&config.Config{})
	m.rawSubtitles = []model.SubtitleTrack{{
		URL:      "https://cdn.example.com/en.vtt",
		Language: "en",
		Resolver: "movysx",
	}}
	m.subtitleLanguage = "en"
	m.autoPlayAfterResolve = true
	m.resolveOpID = 5

	resolved := model.ResolvedMedia{
		SeriesTitle: "The Boys",
		Playback:    m.resolved.Playback,
		Subtitles:   m.rawSubtitles,
	}

	mdl, cmd := m.onResolveDone(resolveDoneMsg{opID: 5, resolved: resolved})
	m = mdl.(*modelImpl)
	if cmd == nil {
		t.Fatal("expected subtitle fetch command")
	}
	if m.playOpID != 0 {
		t.Fatalf("playback must wait for subtitle fetch, got playOpID = %d", m.playOpID)
	}
	if m.subtitleOpID == 0 {
		t.Fatal("expected subtitleOpID to be set for in-flight fetch")
	}
	if !m.loading || m.loadingText != "Loading subtitles..." {
		t.Fatalf("loading = %v, loadingText = %q; want loading subtitles", m.loading, m.loadingText)
	}

	track := model.SubtitleTrack{Path: "/tmp/subs/en.vtt", Language: "en", Resolver: "movysx"}
	mdl, playCmd := m.onSubtitleDone(subtitleDoneMsg{opID: m.subtitleOpID, track: track})
	m = mdl.(*modelImpl)
	if playCmd == nil {
		t.Fatal("expected play command after subtitle arrives")
	}
	if m.playOpID == 0 {
		t.Fatal("playback must start once subtitle arrives")
	}
	if m.resolved.SelectedSubtitle == nil || m.resolved.SelectedSubtitle.Path != "/tmp/subs/en.vtt" {
		t.Fatalf("selected subtitle = %+v, want /tmp/subs/en.vtt", m.resolved.SelectedSubtitle)
	}
}

func TestAutoPlayAfterResolveLaunchesImmediatelyWhenNoSubtitles(t *testing.T) {
	m := readySourceModel()
	m.subtitleLanguage = "off"
	m.autoPlayAfterResolve = true
	m.resolveOpID = 5

	resolved := model.ResolvedMedia{
		SeriesTitle: "The Boys",
		Playback:    m.resolved.Playback,
	}

	mdl, cmd := m.onResolveDone(resolveDoneMsg{opID: 5, resolved: resolved})
	m = mdl.(*modelImpl)
	if cmd == nil {
		t.Fatal("expected play command")
	}
	if m.playOpID == 0 {
		t.Fatal("playback should start immediately when subtitles are off")
	}
}

func TestAutoPlayAfterResolveLaunchesOnSubtitleError(t *testing.T) {
	m := readySourceModel()
	m.autoPlayAfterResolve = true
	m.subtitleOpID = 7

	mdl, cmd := m.onSubtitleDone(subtitleDoneMsg{opID: 7, err: errors.New("network error")})
	m = mdl.(*modelImpl)
	if cmd == nil {
		t.Fatal("expected play command after subtitle error")
	}
	if m.playOpID == 0 {
		t.Fatal("playback must start even if subtitle fetch fails")
	}
	if m.resolved.SelectedSubtitle != nil {
		t.Fatalf("expected nil SelectedSubtitle on error, got %+v", m.resolved.SelectedSubtitle)
	}
}
