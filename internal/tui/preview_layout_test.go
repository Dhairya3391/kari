package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/filter"
	"kari/internal/provider"
	"kari/internal/util"
)

func lineCount(s string) int { return strings.Count(s, "\n") + 1 }

func rankedSources(n int) []filter.RankedSource {
	out := make([]filter.RankedSource, n)
	for i := range out {
		out[i] = filter.RankedSource{
			Source: provider.MediaSource{
				URL:      fmt.Sprintf("https://cdn.example/%d.m3u8", i),
				Quality:  "1080p",
				Resolver: "cinejoy",
				Backend:  "Light",
				Type:     provider.SourceTypeHLS,
			},
			OriginalIndex: i,
		}
	}
	return out
}

func assertStableGeometry(t *testing.T, name string, renders []string) {
	t.Helper()
	want := lineCount(renders[0])
	for i, got := range renders[1:] {
		if lineCount(got) != want {
			t.Errorf("%s state %d has %d lines, want %d", name, i+1, lineCount(got), want)
		}
	}
}

func TestFixedBlockReservesExactGeometry(t *testing.T) {
	block := fixedBlock("POSTER_ART", 36, 12)
	lines := strings.Split(block, "\n")
	if len(lines) != 12 {
		t.Fatalf("fixedBlock lines = %d, want 12", len(lines))
	}
	for i, line := range lines {
		if lipgloss.Width(line) != 36 {
			t.Errorf("fixedBlock line %d width = %d, want 36", i, lipgloss.Width(line))
		}
	}
	if !strings.Contains(block, "POSTER_ART") {
		t.Fatal("fixedBlock dropped the artwork text")
	}
	if fixedBlock("POSTER_ART", 36, 12) != block {
		t.Fatal("fixedBlock is not deterministic")
	}
}

func TestPreviewGeometryStableAcrossDataArrival(t *testing.T) {
	base := PreviewData{
		SeriesTitle: "A Very Long Series Title That Will Be Truncated",
		EpisodeInfo: "S01 E01 · First  (1 of 24)",
		TermWidth:   120,
		Width:       112,
		Height:      40,
		Mode:        provider.ModeMovies,
		Accent:      lipgloss.AdaptiveColor{Light: "#000000", Dark: "#ffffff"},
	}

	empty := RenderPreviewScreen(base)

	withDetails := base
	withDetails.Overview = "A long synopsis that will eventually arrive from the metadata service and must not move the source table beneath it."
	withDetails.Genres = []string{"Drama", "Mystery"}
	withDetails.Rating = "8.4"
	details := RenderPreviewScreen(withDetails)

	withMetadata := withDetails
	withMetadata.AudioText = "English"
	withMetadata.SubtitlesText = "en ✓"
	withMetadata.SkipInfo = "AniSkip"
	withMetadata.ResumePosition = "13:08"
	metadata := RenderPreviewScreen(withMetadata)

	withPoster := withMetadata
	withPoster.PosterBlock = fixedBlock("POSTER_ART", previewPosterMaxCols, 12)
	poster := RenderPreviewScreen(withPoster)

	withSources := withPoster
	withSources.RankedSources = rankedSources(8)
	sources := RenderPreviewScreen(withSources)

	withFailure := withSources
	withFailure.FailedProvider = "example"
	withFailure.Loading = false
	failure := RenderPreviewScreen(withFailure)

	assertStableGeometry(t, "preview", []string{empty, details, metadata, poster, sources, failure})
	if previewPosterWidth(base) != previewPosterMaxCols+posterGutter {
		t.Fatal("poster column is not reserved at its fixed width")
	}
}

func TestPreviewSourceRowsIgnoreStatusRow(t *testing.T) {
	base := PreviewData{
		SeriesTitle:   "Title",
		TermWidth:     120,
		Width:         112,
		Height:        40,
		windowHeight:  40,
		Accent:        lipgloss.AdaptiveColor{},
		RankedSources: rankedSources(20),
	}

	countRows := func(out string) int {
		n := 0
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "cinejoy") {
				n++
			}
		}
		return n
	}

	idle := RenderPreviewScreen(base)
	playing := base
	playing.Height--
	playing.statusRows = 1
	active := RenderPreviewScreen(playing)
	twoStatus := base
	twoStatus.Height -= 2
	twoStatus.statusRows = 2
	twoActive := RenderPreviewScreen(twoStatus)

	want := countRows(idle)
	if want == 0 {
		t.Fatal("idle preview rendered no source rows")
	}
	if got := countRows(active); got != want {
		t.Errorf("source rows with status = %d, want %d (idle)", got, want)
	}
	if got := lineCount(active); got > playing.Height {
		t.Errorf("playing preview has %d lines, budget %d", got, playing.Height)
	}
	if got := countRows(twoActive); got != want {
		t.Errorf("source rows with two status rows = %d, want %d (idle)", got, want)
	}
	if got := lineCount(twoActive); got > twoStatus.Height {
		t.Errorf("preview with two status rows has %d lines, budget %d", got, twoStatus.Height)
	}
}

func TestPreviewSourceRowsStableAcrossPlaybackStatus(t *testing.T) {
	m := readySourceModel()
	m.width, m.height = 120, 40
	m.activeView = viewPreview
	m.rankedSources = rankedSources(20)

	countRows := func(out string) int {
		n := 0
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "cinejoy") {
				n++
			}
		}
		return n
	}

	idle := countRows(m.renderMainView())
	if idle == 0 {
		t.Fatal("idle preview rendered no source rows")
	}
	m.loading = true
	m.loadingText = "Opening player..."
	m.playOpID = 9
	active := countRows(m.renderMainView())
	if active != idle {
		t.Errorf("source rows while playing = %d, want %d (idle)", active, idle)
	}
	m.statusText = "downloaded 12/12 episodes"
	if m.statusRowCount() != 2 {
		t.Fatalf("status rows = %d, want 2", m.statusRowCount())
	}
	twoActive := countRows(m.renderMainView())
	if twoActive != idle {
		t.Errorf("source rows with two status rows = %d, want %d (idle)", twoActive, idle)
	}
}

func TestCollectEpisodesDataUsesSearchGenres(t *testing.T) {
	m := &modelImpl{
		selectedSeries: &provider.SearchResult{
			Title: "One Piece", MediaType: provider.MediaTypeAnime,
			Genres: []string{"Action", "Adventure"},
		},
		episodeResults: []provider.Episode{{Title: "Episode 1", Episode: 1}},
	}
	data := m.collectEpisodesData(112, 40, lipgloss.AdaptiveColor{})
	if len(data.SeriesGenres) != 2 || data.SeriesGenres[0] != "Action" {
		t.Fatalf("SeriesGenres = %v, want the search result's genres", data.SeriesGenres)
	}
}

func TestEpisodesDataRestoresCachedDetails(t *testing.T) {
	m := &modelImpl{
		selectedSeries: &provider.SearchResult{
			Title: "One Piece", MediaType: provider.MediaTypeAnime, TMDBID: 21,
		},
		episodeResults:      []provider.Episode{{Title: "Episode 1", Episode: 1}},
		previewDetailsCache: util.NewBoundedCache[previewDetails](10),
	}
	m.previewDetailsCache.Set(previewDetailsKey(21, provider.MediaTypeAnime, "One Piece"), previewDetails{
		genres: []string{"Action"}, rating: "8.8/10",
	})
	data := m.collectEpisodesData(112, 40, lipgloss.AdaptiveColor{})
	if data.SeriesRating != "8.8/10" || len(data.SeriesGenres) != 1 {
		t.Fatalf("cached details not restored: %q / %v", data.SeriesRating, data.SeriesGenres)
	}
}

func TestScreenBodiesFitBodyHeight(t *testing.T) {
	for _, height := range []int{24, 30, 40} {
		preview := PreviewData{
			SeriesTitle: "Title", EpisodeInfo: "S01 E01 · Name",
			Overview:  "Synopsis text that should be clipped to the reserved slots.",
			TermWidth: 120, Width: 112, Height: height,
			Accent:        lipgloss.AdaptiveColor{},
			RankedSources: rankedSources(20),
		}
		if got := lineCount(RenderPreviewScreen(preview)); got > height {
			t.Errorf("preview height %d: body has %d lines, exceeds budget", height, got)
		}

		results := ResultsData{
			Results:   make([]provider.SearchResult, 30),
			TermWidth: 120, Width: 112, Height: height,
			Mode: provider.ModeMovies, Accent: lipgloss.AdaptiveColor{},
		}
		for i := range results.Results {
			results.Results[i] = provider.SearchResult{Title: fmt.Sprintf("Result %d", i)}
		}
		if got := lineCount(RenderResultsScreen(results)); got > height {
			t.Errorf("results height %d: body has %d lines, exceeds budget", height, got)
		}

		episodes := EpisodesData{
			SeriesTitle: "Series",
			Episodes:    make([]provider.Episode, 30),
			TermWidth:   120, Width: 112, Height: height,
			Accent: lipgloss.AdaptiveColor{},
		}
		for i := range episodes.Episodes {
			episodes.Episodes[i] = provider.Episode{Title: fmt.Sprintf("Episode %d", i)}
		}
		if got := lineCount(RenderEpisodesScreen(episodes)); got > height {
			t.Errorf("episodes height %d: body has %d lines, exceeds budget", height, got)
		}
	}
}

func TestResultsAndEpisodesGeometryStableAcrossDataArrival(t *testing.T) {
	resultBase := ResultsData{
		Query:     "frieren",
		Results:   []provider.SearchResult{{Title: "Frieren", Year: "2023"}, {Title: "Second"}},
		TermWidth: 120,
		Width:     112,
		Height:    40,
		Mode:      provider.ModeMovies,
		Accent:    lipgloss.AdaptiveColor{Light: "#000000", Dark: "#ffffff"},
	}
	resultsEmpty := RenderResultsScreen(resultBase)
	resultFilled := resultBase
	resultFilled.PosterBlock = fixedBlock("POSTER_ART", searchPosterMaxCols, 12)
	resultFilled.Genres = []string{"Anime"}
	resultFilled.Rating = "9.1"
	assertStableGeometry(t, "results", []string{resultsEmpty, RenderResultsScreen(resultFilled)})

	episodeBase := EpisodesData{
		SeriesTitle: "Frieren",
		Episodes:    []provider.Episode{{Title: "Episode 1"}, {Title: "Episode 2"}},
		TermWidth:   120,
		Width:       112,
		Height:      40,
		Accent:      lipgloss.AdaptiveColor{Light: "#000000", Dark: "#ffffff"},
	}
	episodesEmpty := RenderEpisodesScreen(episodeBase)
	episodesFilled := episodeBase
	episodesFilled.PosterBlock = fixedBlock("POSTER_ART", searchPosterMaxCols, 12)
	episodesFilled.SeriesGenres = []string{"Adventure"}
	episodesFilled.SeriesRating = "9.0"
	episodesFilled.SelectedEpisodeTitle = "Episode 2"
	assertStableGeometry(t, "episodes", []string{episodesEmpty, RenderEpisodesScreen(episodesFilled)})
}
