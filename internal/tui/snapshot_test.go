package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/ranking"
	"kari/internal/settings"
)

var snapshotSizes = []struct {
	cols int
	rows int
}{
	{cols: 80, rows: 24},
	{cols: 100, rows: 30},
	{cols: 140, rows: 40},
}

func verifyLinesWidth(t *testing.T, name string, rendered string, maxCols int) {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w > maxCols {
			t.Errorf("[%s] line %d width = %d, exceeds max %d:\n%q", name, i+1, w, maxCols, line)
		}
	}
}
func TestSnapshot_SearchHome(t *testing.T) {
	accent := ResolveAccent(model.KindAnime, "Auto")

	for _, sz := range snapshotSizes {
		dims := ComputeDims(sz.cols, sz.rows)
		data := SearchData{
			Modes:        []provider.ContentType{provider.ModeAnime, provider.ModeCartoon, provider.ModeJellyfin, provider.ModeManga, provider.ModeMovies, provider.ModeTV, provider.ModeLive},
			ActiveMode:   provider.ModeAnime,
			InputView:    "› search anime…",
			InputFocused: false,
			Width:        dims.ContentWidth,
			Accent:       accent,
		}

		body := RenderSearchScreen(data)
		header := RenderHeader([]string{"kari", "anime"}, accent, dims.ContentWidth)
		bindings := []KeyBinding{
			{Key: "space", Action: "search"},
			{Key: "tab", Action: "mode"},
			{Key: "h", Action: "history"},
			{Key: "d", Action: "downloads"},
			{Key: "s", Action: "settings"},
			{Key: "?", Action: "help"},
		}
		status := &IntegrationStatus{PlayerName: "mpv", TrackerName: "anilist", TrackerConnected: true}
		footer := RenderFooter(bindings, status, accent, dims.ContentWidth)

		frame := RenderFrame(dims, header, body, "", footer)
		verifyLinesWidth(t, "SearchHome", frame, sz.cols)
	}
}

func TestSnapshot_Results(t *testing.T) {
	modes := []struct {
		mode    provider.ContentType
		results []provider.SearchResult
	}{
		{
			mode: provider.ModeAnime,
			results: []provider.SearchResult{
				{Title: "Sousou no Frieren", Year: "2023", MediaType: provider.MediaTypeTV, TMDBID: 100},
				{Title: "Sousou no Frieren 2nd Season", Year: "2026", MediaType: provider.MediaTypeTV, TMDBID: 101},
				{Title: "Frieren Recap Movie", Year: "2024", MediaType: provider.MediaTypeMovie, TMDBID: 104},
			},
		},
		{
			mode: provider.ModeLive,
			results: []provider.SearchResult{
				{Title: "Premier League: Arsenal vs Chelsea", Year: "Live", MediaType: provider.MediaTypeLive, ID: "pp-live:1"},
				{Title: "Formula 1: Monza GP", Year: "Today 19:30", MediaType: provider.MediaTypeLive, ID: "pp-live:2"},
			},
		},
		{
			mode: provider.ModeManga,
			results: []provider.SearchResult{
				{Title: "Chainsaw Man", Year: "2018", MediaType: provider.MediaTypeManga, ID: "cm-1"},
				{Title: "Chainsaw Man (Color)", Year: "2020", MediaType: provider.MediaTypeManga, ID: "cm-2"},
			},
		},
		{
			mode: provider.ModeMovies,
			results: []provider.SearchResult{
				{Title: "How to Train Your Dragon", Year: "2025", MediaType: provider.MediaTypeMovie, TMDBID: 200},
				{Title: "Rockstar", Year: "2011", MediaType: provider.MediaTypeMovie, TMDBID: 201},
			},
		},
		{
			mode: provider.ModeTV,
			results: []provider.SearchResult{
				{Title: "The Boys", Year: "2019", MediaType: provider.MediaTypeTV, TMDBID: 300},
				{Title: "Supernatural", Year: "2005", MediaType: provider.MediaTypeTV, TMDBID: 301},
			},
		},
		{
			mode: provider.ModeCartoon,
			results: []provider.SearchResult{
				{Title: "Cars", Year: "2006", MediaType: provider.MediaTypeTV, TMDBID: 400},
				{Title: "Spider-Noir", Year: "2026", MediaType: provider.MediaTypeTV, TMDBID: 401},
			},
		},
	}

	for _, m := range modes {
		accent := ResolveAccent(model.FromKey(string(m.mode)), "Auto")
		for _, sz := range snapshotSizes {
			dims := ComputeDims(sz.cols, sz.rows)
			data := ResultsData{
				Query:         "sample",
				Mode:          m.mode,
				Results:       m.results,
				SelectedIndex: 0,
				Overview:      "Sample description for testing layout rendering across columns.",
				Genres:        []string{"Action", "Drama"},
				Rating:        "8.5",
				Width:         dims.ContentWidth,
				Height:        dims.BodyHeight,
				Accent:        accent,
			}

			body := RenderResultsScreen(data)
			header := RenderHeader([]string{"kari", string(m.mode), "search"}, accent, dims.ContentWidth)
			footer := RenderFooter([]KeyBinding{
				{Key: "enter", Action: "open"},
				{Key: "↑↓", Action: "move"},
				{Key: "tab", Action: "mode"},
				{Key: "/", Action: "new search"},
				{Key: "esc", Action: "back"},
				{Key: "?", Action: "help"},
			}, nil, accent, dims.ContentWidth)

			frame := RenderFrame(dims, header, body, "", footer)
			verifyLinesWidth(t, "Results_"+string(m.mode), frame, sz.cols)
		}
	}
}

func TestSnapshot_Episodes(t *testing.T) {
	accent := ResolveAccent(model.KindTV, "Auto")
	episodes := []provider.Episode{
		{Season: 1, Episode: 1, Title: "The Name of the Game"},
		{Season: 1, Episode: 2, Title: "Cherry"},
		{Season: 1, Episode: 3, Title: "Get Some"},
		{Season: 1, Episode: 4, Title: "The Female of the Species"},
		{Season: 1, Episode: 5, Title: "Good for the Soul"},
	}

	for _, sz := range snapshotSizes {
		dims := ComputeDims(sz.cols, sz.rows)

		// Normal mode
		dataNormal := EpisodesData{
			SeriesTitle:   "The Boys",
			Episodes:      episodes,
			SelectedIndex: 2,
			SeasonCount:   5,
			ActiveSeason:  0,
			SelectMode:    false,
			Width:         dims.ContentWidth,
			Height:        dims.BodyHeight,
			Accent:        accent,
			TermWidth:     sz.cols,
		}
		bodyNormal := RenderEpisodesScreen(dataNormal)
		header := RenderHeader([]string{"kari", "tv", "the boys"}, accent, dims.ContentWidth)
		footerNormal := RenderFooter([]KeyBinding{
			{Key: "enter", Action: "open"},
			{Key: "space", Action: "select"},
			{Key: "[ ]", Action: "season"},
			{Key: "/", Action: "filter"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}, nil, accent, dims.ContentWidth)
		frameNormal := RenderFrame(dims, header, bodyNormal, "", footerNormal)
		verifyLinesWidth(t, "EpisodesNormal", frameNormal, sz.cols)

		// Select mode
		dataSelect := dataNormal
		dataSelect.SelectMode = true
		dataSelect.SelectedIDs = map[int]struct{}{2: {}, 3: {}, 4: {}}
		bodySelect := RenderEpisodesScreen(dataSelect)
		footerSelect := RenderFooter([]KeyBinding{
			{Key: "space", Action: "toggle"},
			{Key: "ctrl+a", Action: "all"},
			{Key: "ctrl+d", Action: "none"},
			{Key: "D", Action: "download"},
			{Key: "esc", Action: "cancel"},
		}, nil, accent, dims.ContentWidth)
		frameSelect := RenderFrame(dims, header, bodySelect, "", footerSelect)
		verifyLinesWidth(t, "EpisodesSelect", frameSelect, sz.cols)
	}
}

func TestSnapshot_Preview(t *testing.T) {
	accent := ResolveAccent(model.KindTV, "Auto")
	sources := []provider.MediaSource{
		{Quality: "4K 9.80 GB", Language: "English · Hindi", Resolver: "4KHDHub"},
		{Quality: "4K 7.41 GB", Language: "English", Resolver: "4KHDHub"},
		{Quality: "4K 8.12 GB", Language: "Hindi", Resolver: "VidFast"},
		{Quality: "FHD 3.05 GB", Language: "English", Resolver: "4KHDHub"},
		{Quality: "FHD 6.17 GB", Language: "Hindi", Resolver: "4KHDHub"},
	}
	ranked := ranking.RankSources(sources, ranking.Criteria{Mode: provider.ModeTV})

	for _, sz := range snapshotSizes {
		dims := ComputeDims(sz.cols, sz.rows)
		data := PreviewData{
			SeriesTitle:    "The Boys",
			EpisodeInfo:    "S01 E01 · The Name of the Game  (1 of 40)",
			Genres:         []string{"Sci-Fi & Fantasy", "Action & Adventure"},
			Rating:         "8.4",
			Overview:       "Vigilantes with no powers set out to expose and take down corrupt superheroes.",
			SubtitlesText:  "English ✓",
			ResumePosition: "13:08",
			Sources:        sources,
			RankedSources:  ranked,
			SelectedIndex:  0,
			PlayerName:     "mpv",
			Autoplay:       false,
			Width:          dims.ContentWidth,
			Height:         dims.BodyHeight,
			Accent:         accent,
		}

		body := RenderPreviewScreen(data)
		header := RenderHeader([]string{"kari", "tv", "the boys", "s01e01"}, accent, dims.ContentWidth)
		footer := RenderFooter([]KeyBinding{
			{Key: "enter", Action: "play"},
			{Key: "↑↓", Action: "source"},
			{Key: "n/p", Action: "episode"},
			{Key: "D", Action: "download"},
			{Key: "a", Action: "autoplay"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}, nil, accent, dims.ContentWidth)

		frame := RenderFrame(dims, header, body, "", footer)
		verifyLinesWidth(t, "Preview", frame, sz.cols)
	}
}

func TestSnapshot_Chapters(t *testing.T) {
	accent := ResolveAccent(model.KindManga, "Auto")
	for _, sz := range snapshotSizes {
		dims := ComputeDims(sz.cols, sz.rows)
		data := ChaptersData{
			SeriesTitle:   "One Piece",
			Genres:        []string{"Action", "Adventure", "Fantasy"},
			ProviderName:  "WeebCentral",
			ChaptersCount: 1100,
			Overview:      "Gol D. Roger was known as the 'Pirate King', the strongest and most infamous being to have sailed the Grand Line.",
			ListView:      "  Ch 1100  ·  Powers On Another Level\n▌ Ch 1099  ·  Pacifist\n  Ch 1098  ·  The Birth of Bonney",
			Width:         dims.ContentWidth,
			Height:        dims.BodyHeight,
			Accent:        accent,
		}

		body := RenderChaptersScreen(data)
		header := RenderHeader([]string{"kari", "manga", "one piece"}, accent, dims.ContentWidth)
		footer := RenderFooter([]KeyBinding{
			{Key: "enter", Action: "read"},
			{Key: "↑↓", Action: "move"},
			{Key: "/", Action: "filter"},
			{Key: "g / G", Action: "first / last"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}, nil, accent, dims.ContentWidth)

		frame := RenderFrame(dims, header, body, "", footer)
		verifyLinesWidth(t, "Chapters", frame, sz.cols)
	}
}

func TestSnapshot_Preview_Playing(t *testing.T) {
	accent := ResolveAccent(model.KindTV, "Auto")
	sources := []provider.MediaSource{
		{Quality: "4K 9.80 GB", Language: "English · Hindi", Resolver: "4KHDHub"},
		{Quality: "4K 7.41 GB", Language: "English", Resolver: "4KHDHub"},
		{Quality: "FHD 3.05 GB", Language: "English", Resolver: "4KHDHub"},
	}
	ranked := ranking.RankSources(sources, ranking.Criteria{Mode: provider.ModeTV})

	for _, sz := range snapshotSizes {
		dims := ComputeDims(sz.cols, sz.rows)
		data := PreviewData{
			SeriesTitle:    "The Boys",
			EpisodeInfo:    "S01 E01 · The Name of the Game  (1 of 40)",
			Genres:         []string{"Sci-Fi & Fantasy", "Action & Adventure"},
			Rating:         "8.4",
			Overview:       "Vigilantes with no powers set out to expose and take down corrupt superheroes.",
			SubtitlesText:  "English ✓",
			ResumePosition: "13:08",
			Sources:        sources,
			RankedSources:  ranked,
			SelectedIndex:  0,
			PlayerName:     "mpv",
			Autoplay:       false,
			Width:          dims.ContentWidth,
			Height:         dims.BodyHeight,
			Accent:         accent,
		}

		body := RenderPreviewScreen(data)
		header := RenderHeader([]string{"kari", "tv", "the boys", "s01e01"}, accent, dims.ContentWidth)
		status := &IntegrationStatus{PlayerName: "mpv", TrackerName: "playing", TrackerConnected: true}
		footer := RenderFooter([]KeyBinding{
			{Key: "enter", Action: "play"},
			{Key: "n", Action: "next"},
			{Key: "A", Action: "autoplay"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}, status, accent, dims.ContentWidth)

		frame := RenderFrame(dims, header, body, "", footer)
		verifyLinesWidth(t, "PreviewPlaying", frame, sz.cols)
	}
}

func TestSnapshot_History(t *testing.T) {
	accent := ResolveAccent(model.KindAnime, "Auto")
	groups := []history.Group{
		{
			Title: "Jujutsu Kaisen",
			Mode:  "manga",
			ContinueEntry: history.Entry{
				Title:        "Jujutsu Kaisen",
				Mode:         "manga",
				EpisodeTitle: "1",
				PositionSecs: 1,
				DurationSecs: 58,
				WatchedAt:    time.Now().Add(-9 * time.Minute),
			},
		},
		{
			Title: "Rockstar",
			Mode:  "movies",
			ContinueEntry: history.Entry{
				Title:        "Rockstar",
				Mode:         "movies",
				MediaType:    "movie",
				PositionSecs: 46 * 60,
				DurationSecs: 159 * 60,
				WatchedAt:    time.Now().Add(-14 * 24 * time.Hour),
			},
		},
	}

	for _, sz := range snapshotSizes {
		dims := ComputeDims(sz.cols, sz.rows)
		data := HistoryData{
			ActiveTab:     HistoryTabContinue,
			ContinueCount: 15,
			FinishedCount: 6,
			Groups:        groups,
			SelectedIndex: 0,
			Width:         dims.ContentWidth,
			Height:        dims.BodyHeight,
			Accent:        accent,
		}

		body := RenderHistoryScreen(data)
		header := RenderHeader([]string{"kari", "history"}, accent, dims.ContentWidth)
		footer := RenderFooter([]KeyBinding{
			{Key: "enter", Action: "open"},
			{Key: "tab", Action: "section"},
			{Key: "/", Action: "filter"},
			{Key: "d", Action: "delete"},
			{Key: "D", Action: "clear all"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}, nil, accent, dims.ContentWidth)

		frame := RenderFrame(dims, header, body, "", footer)
		verifyLinesWidth(t, "History", frame, sz.cols)
	}
}

func TestSnapshot_Settings(t *testing.T) {
	accent := ResolveAccent(model.KindAnime, "Auto")

	for _, sz := range snapshotSizes {
		dims := ComputeDims(sz.cols, sz.rows)

		for catIdx := range SettingsCategoryNames {
			var modeRows []ModeRow
			for _, m := range settings.DefaultModes {
				kind := model.FromKey(m)
				modeRows = append(modeRows, ModeRow{
					Key:       m,
					Label:     kind.Spec().Label,
					Enabled:   true,
					Available: true,
					Accent:    ThemeFor(kind, "Auto").Accent,
				})
			}
			data := SettingsData{
				ActiveCategory:      SettingsCategory(catIdx),
				FocusedRowIndex:     0,
				Width:               dims.ContentWidth,
				Height:              dims.BodyHeight,
				Accent:              accent,
				PlayerName:          "mpv",
				QualityName:         "Highest",
				DownloadQualityName: "Auto (same as stream)",
				Autoplay:            false,
				SubtitleLanguage:    "English",
				AnimeAudioTrack:     "Sub · Japanese",
				AnimeSubtitles:      true,
				SkipSource:          "Hybrid",
				DefaultModeName:     "Last active",
				ModesRows:           modeRows,
				StartupSync:         false,
				PosterArtwork:       true,
				AccentName:          "Auto (per mode)",
				Transitions:         true,
			}

			body := RenderSettingsScreen(data)
			header := RenderHeader([]string{"kari", "settings"}, accent, dims.ContentWidth)
			footer := RenderFooter([]KeyBinding{
				{Key: "↑↓", Action: "move"},
				{Key: "←→", Action: "change"},
				{Key: "enter", Action: "edit"},
				{Key: "tab", Action: "category"},
				{Key: "?", Action: "help"},
				{Key: "esc", Action: "back"},
			}, nil, accent, dims.ContentWidth)

			frame := RenderFrame(dims, header, body, "", footer)
			verifyLinesWidth(t, "Settings", frame, sz.cols)
		}
	}
}

func TestSnapshot_Downloads(t *testing.T) {
	accent := ResolveAccent(model.KindTV, "Auto")

	for _, sz := range snapshotSizes {
		dims := ComputeDims(sz.cols, sz.rows)
		data := DownloadsData{
			Active: []ActiveDownload{
				{Title: "The Boys s01e01", Quality: "4K", Progress: 0.48, Speed: "3.2 MB/s", ETA: "4m left"},
				{Title: "The Boys s01e02", Quality: "4K", Queued: true},
			},
			Done: []DoneDownload{
				{Title: "The Boys s01e03", Quality: "FHD", Size: "3.05 GB"},
			},
			SelectedIndex: 0,
			Width:         dims.ContentWidth,
			Height:        dims.BodyHeight,
			Accent:        accent,
		}

		body := RenderDownloadsScreen(data)
		header := RenderHeader([]string{"kari", "downloads"}, accent, dims.ContentWidth)
		footer := RenderFooter([]KeyBinding{
			{Key: "p", Action: "pause"},
			{Key: "x", Action: "cancel item"},
			{Key: "X", Action: "cancel all"},
			{Key: "enter", Action: "open folder"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}, nil, accent, dims.ContentWidth)

		frame := RenderFrame(dims, header, body, "", footer)
		verifyLinesWidth(t, "Downloads", frame, sz.cols)
	}
}

func TestSnapshot_HelpOverlay(t *testing.T) {
	accent := ResolveAccent(model.KindTV, "Auto")

	for _, sz := range snapshotSizes {
		dims := ComputeDims(sz.cols, sz.rows)
		helpData := HelpData{
			ScreenName: "Preview",
			ContextKeys: []KeyBinding{
				{Key: "enter", Action: "play selected source"},
				{Key: "↑↓ j k", Action: "choose source"},
				{Key: "n / p", Action: "next / previous episode"},
				{Key: "D", Action: "download selected source"},
				{Key: "A", Action: "toggle autoplay"},
				{Key: "ctrl+p", Action: "switch player"},
			},
			GlobalKeys: []KeyBinding{
				{Key: "esc", Action: "back one level"},
				{Key: "?", Action: "this help"},
				{Key: "ctrl+c", Action: "quit"},
				{Key: "q", Action: "quit (from Search)"},
			},
			Width:  dims.ContentWidth,
			Accent: accent,
		}

		body := RenderHelpOverlay(helpData)
		header := RenderHeader([]string{"kari", "tv", "the boys", "s01e01", "help"}, accent, dims.ContentWidth)
		footer := RenderFooter([]KeyBinding{
			{Key: "esc", Action: "close help"},
		}, nil, accent, dims.ContentWidth)

		frame := RenderFrame(dims, header, body, "", footer)
		verifyLinesWidth(t, "HelpOverlay", frame, sz.cols)
	}
}

func TestSnapshot_Live(t *testing.T) {
	accent := ResolveAccent(model.KindLive, "Auto")
	now := time.Now()
	results := []provider.SearchResult{
		{Title: "Premier League · Arsenal v Chelsea", Group: "Football", Live: true, MediaType: provider.MediaTypeLive, ID: "pp-live:1"},
		{Title: "Formula 1 · Practice 2", Group: "Motorsport", Live: true, MediaType: provider.MediaTypeLive, ID: "pp-live:2"},
		{Title: "Sky Sports Main Event", Group: "Channel", MediaType: provider.MediaTypeLive, ID: "pp-live:3"},
		{Title: "La Liga · Real Madrid v Getafe", Group: "Football", StartsAt: time.Date(now.Year(), now.Month(), now.Day(), 19, 30, 0, 0, now.Location()), MediaType: provider.MediaTypeLive, ID: "pp-live:4"},
		{Title: "NBA · Lakers v Celtics", Group: "Basketball", StartsAt: time.Date(now.Year(), now.Month(), now.Day(), 23, 0, 0, 0, now.Location()), MediaType: provider.MediaTypeLive, ID: "pp-live:5"},
		{Title: "Serie A · Inter v Roma", Group: "Football", StartsAt: now.AddDate(0, 0, 1), MediaType: provider.MediaTypeLive, ID: "pp-live:6"},
		{Title: "World Cup Qualifier", Group: "Football", StartsAt: now.AddDate(0, 0, 5), MediaType: provider.MediaTypeLive, ID: "pp-live:7"},
	}

	for _, sz := range snapshotSizes {
		dims := ComputeDims(sz.cols, sz.rows)
		data := ResultsData{
			Query:         "search live…",
			Mode:          provider.ModeLive,
			Results:       results,
			SelectedIndex: 0,
			Width:         dims.ContentWidth,
			Height:        dims.BodyHeight,
			Accent:        accent,
		}

		body := RenderLiveResultsScreen(data)
		header := RenderHeader([]string{"kari", "live"}, accent, dims.ContentWidth)
		footer := RenderFooter([]KeyBinding{
			{Key: "enter", Action: "open"},
			{Key: "↑↓", Action: "move"},
			{Key: "tab", Action: "mode"},
			{Key: "r", Action: "refresh"},
			{Key: "/", Action: "filter"},
			{Key: "?", Action: "help"},
			{Key: "esc", Action: "back"},
		}, nil, accent, dims.ContentWidth)

		frame := RenderFrame(dims, header, body, "", footer)
		verifyLinesWidth(t, "Live", frame, sz.cols)
	}
}
