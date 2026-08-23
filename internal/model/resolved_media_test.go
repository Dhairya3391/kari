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

func TestSubtitlePathsSkipsUndownloaded(t *testing.T) {
	r := ResolvedMedia{Subtitles: []SubtitleTrack{
		{Label: "downloaded", Path: "/tmp/a.srt"},
		{Label: "remote-only"},
		{Label: "also-downloaded", Path: "/tmp/b.ass"},
	}}
	got := r.SubtitlePaths()
	if len(got) != 2 || got[0] != "/tmp/a.srt" || got[1] != "/tmp/b.ass" {
		t.Fatalf("SubtitlePaths()=%v", got)
	}
}
