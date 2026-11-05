package calendar

import (
	"time"

	"kari/internal/provider"
)

// Release represents a scheduled episode broadcast for an anime or TV series.
type Release struct {
	ID            string               `json:"id"`
	Title         string               `json:"title"`
	RomajiTitle   string               `json:"romaji_title,omitempty"`
	Mode          provider.ContentType `json:"mode"`
	Season        int                  `json:"season"`
	Episode       int                  `json:"episode"`
	EpisodeTitle  string               `json:"episode_title,omitempty"`
	AiringAt      time.Time            `json:"airing_at"`
	AniListID     int                  `json:"anilist_id,omitempty"`
	TMDBID        int                  `json:"tmdb_id,omitempty"`
	CoverURL      string               `json:"cover_url,omitempty"`
	Country       string               `json:"country,omitempty"`
	Status        string               `json:"status,omitempty"`
	TotalEpisodes int                  `json:"total_episodes,omitempty"`
	Description   string               `json:"description,omitempty"`
	Rating        float64              `json:"rating,omitempty"`
}

// WatchStatus classifies the viewer's progress relative to a scheduled release.
type WatchStatus string

const (
	// StatusNotStarted indicates the show is not in the viewer's watch history.
	StatusNotStarted WatchStatus = "not_started"
	// StatusBehind indicates the viewer has watched previous episodes and has unviewed releases left.
	StatusBehind WatchStatus = "behind"
	// StatusNewToday indicates the viewer was caught up, and a new episode airs today.
	StatusNewToday WatchStatus = "new_today"
	// StatusUpToDate indicates the viewer has already completed this episode.
	StatusUpToDate WatchStatus = "up_to_date"
)

// ReleaseItem combines a scheduled release with the user's watch history.
type ReleaseItem struct {
	Release            Release
	InHistory          bool
	LastWatchedSeason  int
	LastWatchedEpisode int
	NextEpisode        int
	UnwatchedCount     int
	Status             WatchStatus
	Badge              string
}

// FilterMode specifies which releases are displayed on the calendar screen.
type FilterMode string

const (
	// FilterAll displays all scheduled releases regardless of media type.
	FilterAll FilterMode = "all"
	// FilterAnime displays only anime releases.
	FilterAnime FilterMode = "anime"
	// FilterSeries displays only TV series releases.
	FilterSeries FilterMode = "series"
	// FilterWatching displays only shows present in the user's watch history.
	FilterWatching FilterMode = "watching"
)

// DaySchedule contains all releases for a single calendar day.
type DaySchedule struct {
	Date         time.Time
	DayName      string // Full name, e.g. "Wednesday"
	ShortDayName string // Abbreviated, e.g. "Wed"
	IsToday      bool
	Releases     []ReleaseItem
}

// WeeklySchedule contains 7 days of scheduled broadcasts.
type WeeklySchedule struct {
	Days      []DaySchedule
	UpdatedAt time.Time
}
