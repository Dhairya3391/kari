package model

import "kari/internal/provider"

// SubtitleTrack is one subtitle offering attached to resolved media. Tracks
// arrive from providers or subtitle services as URL references; Path is
// filled in once a track has been downloaded to disk.
type SubtitleTrack struct {
	Label    string
	Language string
	Path     string
	URL      string
	Referer  string
	Default  bool
	Resolver string
}

// ResolvedMedia aggregates every playback source and subtitle track
// resolved for one selection, plus the metadata services need to scrobble,
// organize downloads, and display it.
type ResolvedMedia struct {
	SeriesTitle   string
	SeriesURL     string
	EpisodeTitle  string
	EpisodeURL    string
	MediaURL      string
	MediaType     string
	Year          string
	TMDBID        int
	SeasonNumber  int
	EpisodeNumber int
	Resolver      string
	// Playback holds the merged, quality-sorted sources across providers.
	// Entries are provider.MediaSource values with Resolver stamped by the
	// MediaService aggregation.
	Playback  []provider.MediaSource
	Subtitles []SubtitleTrack
	StartTime float64
}
