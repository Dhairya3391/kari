package presence

import (
	"testing"
	"time"

	"kari/internal/config"
	"kari/internal/model"
	"kari/internal/provider"
)

func TestBuildActivityEpisode(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	np := NowPlaying{
		Media: model.ResolvedMedia{
			SeriesTitle:   "Attack on Titan",
			EpisodeTitle:  "Dawn of Humanity",
			MediaType:     provider.MediaTypeAnime,
			SeasonNumber:  4,
			EpisodeNumber: 28,
			TMDBID:        1429,
		},
		ImageURL:  "https://image.tmdb.org/t/p/w500/poster.jpg",
		AniListID: 16498,
	}
	act := buildActivity(np, Progress{PositionSecs: 60, DurationSecs: 1440}, now)

	if act.Details != "Attack on Titan" {
		t.Errorf("details = %q", act.Details)
	}
	if act.State != "S04E28 · Dawn of Humanity" {
		t.Errorf("state = %q", act.State)
	}
	if act.Timestamps == nil || act.Timestamps.Start != 999940 || act.Timestamps.End != 1001380 {
		t.Fatalf("timestamps = %+v", act.Timestamps)
	}
	if act.Assets.LargeImage != np.ImageURL {
		t.Errorf("large image = %q", act.Assets.LargeImage)
	}
	if len(act.Buttons) != 2 {
		t.Fatalf("buttons = %+v", act.Buttons)
	}
	if act.Buttons[0].URL != "https://www.themoviedb.org/tv/1429" {
		t.Errorf("tmdb button = %q", act.Buttons[0].URL)
	}
	if act.Buttons[1].URL != "https://anilist.co/anime/16498" {
		t.Errorf("anilist button = %q", act.Buttons[1].URL)
	}
}

func TestBuildActivityMovieFallbacksToLogo(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	np := NowPlaying{
		Media: model.ResolvedMedia{
			SeriesTitle: "Inception",
			MediaType:   provider.MediaTypeMovie,
			Year:        "2010",
			TMDBID:      27205,
		},
	}
	act := buildActivity(np, Progress{PositionSecs: 10}, now)

	if act.State != "2010" {
		t.Errorf("state = %q", act.State)
	}
	if act.Assets.LargeImage != config.DiscordAssetLogo {
		t.Errorf("large image = %q, want logo fallback", act.Assets.LargeImage)
	}
	if act.Buttons[0].URL != "https://www.themoviedb.org/movie/27205" {
		t.Errorf("tmdb button = %q", act.Buttons[0].URL)
	}
	if act.Timestamps.End != 0 {
		t.Errorf("end = %d, want 0 when duration unknown", act.Timestamps.End)
	}
}

func TestBuildActivityPausedFreezesTimer(t *testing.T) {
	now := time.Unix(3_000_000, 0)
	np := NowPlaying{Media: model.ResolvedMedia{SeriesTitle: "Show", MediaType: provider.MediaTypeTV}}
	act := buildActivity(np, Progress{PositionSecs: 100, DurationSecs: 600, Paused: true}, now)

	if act.Timestamps != nil {
		t.Errorf("timestamps = %+v, want nil while paused", act.Timestamps)
	}
	if act.Assets.SmallImage != config.DiscordAssetPaused {
		t.Errorf("small image = %q, want paused asset", act.Assets.SmallImage)
	}
}

func TestBuildActivityLive(t *testing.T) {
	now := time.Unix(4_000_000, 0)
	np := NowPlaying{Media: model.ResolvedMedia{SeriesTitle: "Lakers vs Celtics", MediaType: provider.MediaTypeLive, Live: true}}
	act := buildActivity(np, Progress{PositionSecs: 5}, now)

	if act.State != "Live" {
		t.Errorf("state = %q", act.State)
	}
	if act.Timestamps.End != 0 {
		t.Errorf("live stream should not carry an end timestamp")
	}
	if len(act.Buttons) != 0 {
		t.Errorf("live stream should carry no buttons, got %+v", act.Buttons)
	}
}

func TestBuildActivityManga(t *testing.T) {
	now := time.Unix(5_000_000, 0)
	np := NowPlaying{Media: model.ResolvedMedia{SeriesTitle: "One Piece", EpisodeTitle: "1191", MediaType: provider.MediaTypeManga}}
	act := buildActivity(np, Progress{}, now)

	if act.State != "Ch. 1191" {
		t.Errorf("state = %q", act.State)
	}
}

func TestClamp(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	got := clamp(long, 10)
	if len([]rune(got)) != 10 {
		t.Fatalf("clamp length = %d, want 10", len([]rune(got)))
	}
	if clamp("short", 10) != "short" {
		t.Errorf("clamp should not alter short strings")
	}
}

// mediaFixture is a minimal episodic media used by client tests.
func mediaFixture() model.ResolvedMedia {
	return model.ResolvedMedia{
		SeriesTitle:   "Kari Show",
		EpisodeTitle:  "Pilot",
		MediaType:     provider.MediaTypeTV,
		SeasonNumber:  1,
		EpisodeNumber: 2,
	}
}
