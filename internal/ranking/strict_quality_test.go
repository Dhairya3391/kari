package ranking

import (
	"testing"

	"kari/internal/provider"
)

func TestStrictQualityTierInvariant(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "https://example.com/4k.m3u8", Quality: "4K (2160p)", Resolver: "A"},
		{URL: "https://example.com/fhd.m3u8", Quality: "1080p FHD", Resolver: "A"},
		{URL: "https://example.com/hd.m3u8", Quality: "720p HD", Resolver: "A"},
		{URL: "https://example.com/sd.m3u8", Quality: "480p SD", Resolver: "A"},
		{URL: "https://example.com/low.m3u8", Quality: "360p", Resolver: "A"},
	}

	// 1. QualityHighest must place 4K first, then 1080p, 720p, 480p, 360p
	critHighest := Criteria{Mode: provider.ModeMovies, QualityMode: QualityHighest}
	rankedHighest := RankSources(sources, critHighest)
	if rankedHighest[0].Source.Quality != "4K (2160p)" {
		t.Errorf("QualityHighest: expected 4K first, got %s", rankedHighest[0].Source.Quality)
	}
	if rankedHighest[len(rankedHighest)-1].Source.Quality != "360p" {
		t.Errorf("QualityHighest: expected 360p last, got %s", rankedHighest[len(rankedHighest)-1].Source.Quality)
	}

	// 2. QualityDataSaver must place 1080p first, then 720p, 480p, 360p, and 4K last
	critDataSaver := Criteria{Mode: provider.ModeMovies, QualityMode: QualityDataSaver}
	rankedDataSaver := RankSources(sources, critDataSaver)
	if rankedDataSaver[0].Source.Quality != "1080p FHD" {
		t.Errorf("QualityDataSaver: expected 1080p first, got %s", rankedDataSaver[0].Source.Quality)
	}
	if rankedDataSaver[len(rankedDataSaver)-1].Source.Quality != "4K (2160p)" {
		t.Errorf("QualityDataSaver: expected 4K demoted, got %s", rankedDataSaver[len(rankedDataSaver)-1].Source.Quality)
	}

	// 3. QualityLowest must place 360p first, then 480p, 720p, 1080p, 4K
	critLowest := Criteria{Mode: provider.ModeMovies, QualityMode: QualityLowest}
	rankedLowest := RankSources(sources, critLowest)
	if rankedLowest[0].Source.Quality != "360p" {
		t.Errorf("QualityLowest: expected 360p first, got %s", rankedLowest[0].Source.Quality)
	}
	if rankedLowest[len(rankedLowest)-1].Source.Quality != "4K (2160p)" {
		t.Errorf("QualityLowest: expected 4K last, got %s", rankedLowest[len(rankedLowest)-1].Source.Quality)
	}
}
