package model

import (
	"testing"

	"kari/internal/provider"
)

func TestIsEpisodeBased(t *testing.T) {
	tests := []struct {
		mediaType string
		want      bool
	}{
		{"anime", true},
		{"tv", true},
		{"cartoon", true},
		{"TV", true}, // case-insensitive
		{" anime ", true},
		{"movie", false},
		{"film", false},
		{"live", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsEpisodeBased(tt.mediaType); got != tt.want {
			t.Errorf("IsEpisodeBased(%q)=%v want %v", tt.mediaType, got, tt.want)
		}
	}
}

func TestDisplayTitle(t *testing.T) {
	tests := []struct {
		name string
		r    ResolvedMedia
		want string
	}{
		{
			name: "episode with distinct titles",
			r:    ResolvedMedia{SeriesTitle: "Show", EpisodeTitle: "Pilot", SeasonNumber: 1, EpisodeNumber: 2, MediaType: provider.MediaTypeTV},
			want: "Show - S01E02 - Pilot",
		},
		{
			name: "episode sharing series title drops the duplicate",
			r:    ResolvedMedia{SeriesTitle: "Show", EpisodeTitle: "show", EpisodeNumber: 3, MediaType: provider.MediaTypeTV},
			want: "Show - E03",
		},
		{
			name: "episode sharing series title without season/episode number returns prefix cleanly",
			r:    ResolvedMedia{SeriesTitle: "Show", EpisodeTitle: "show", SeasonNumber: 0, EpisodeNumber: 0, MediaType: provider.MediaTypeTV},
			want: "Show",
		},
		{
			name: "episode with empty episode title without season/episode number returns prefix cleanly",
			r:    ResolvedMedia{SeriesTitle: "Special Series", SeasonNumber: 0, EpisodeNumber: 0, MediaType: provider.MediaTypeTV},
			want: "Special Series",
		},
		{
			name: "movie appends year",
			r:    ResolvedMedia{SeriesTitle: "Film", Year: "2019", MediaType: provider.MediaTypeMovie},
			want: "Film (2019)",
		},
		{
			name: "movie year already in title stays untouched",
			r:    ResolvedMedia{SeriesTitle: "Film (2019)", Year: "2019", MediaType: provider.MediaTypeMovie},
			want: "Film (2019)",
		},
		{
			name: "live event renders single title",
			r:    ResolvedMedia{SeriesTitle: "🔴 LIVE: Match 1", MediaType: provider.MediaTypeLive},
			want: "🔴 LIVE: Match 1",
		},
		{
			name: "empty everything renders empty",
			r:    ResolvedMedia{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.DisplayTitle(); got != tt.want {
				t.Fatalf("DisplayTitle()=%q want %q", got, tt.want)
			}
		})
	}
}

func TestSubtitlePathUsesOnlySelectedLocalTrack(t *testing.T) {
	selected := SubtitleTrack{Path: "/tmp/selected.srt"}
	r := ResolvedMedia{
		Subtitles: []SubtitleTrack{
			{Path: "/tmp/candidate.srt"},
			{URL: "https://cdn.example.com/en.vtt"},
		},
		SelectedSubtitle: &selected,
	}
	if got := r.SubtitlePath(); got != "/tmp/selected.srt" {
		t.Fatalf("SubtitlePath()=%q want %q", got, "/tmp/selected.srt")
	}

	r.SelectedSubtitle = nil
	if got := r.SubtitlePath(); got != "" {
		t.Fatalf("unselected candidates must not reach playback, got %q", got)
	}
}

func TestSubtitlePathRequiresMaterializedTrack(t *testing.T) {
	selected := SubtitleTrack{URL: "https://cdn.example.com/en.vtt"}
	r := ResolvedMedia{SelectedSubtitle: &selected}
	if got := r.SubtitlePath(); got != "" {
		t.Fatalf("remote subtitle must be materialized before playback, got %q", got)
	}
}
