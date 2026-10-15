// Package presence publishes Kari playback as Discord Rich Presence over the
// local Discord IPC socket. It is an outbound integration in the same spirit
// as internal/scrobble: it reflects what the user is watching, never blocks
// the UI, and silently no-ops when Discord is not running.
package presence

import (
	"fmt"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/model"
	"kari/internal/provider"
)

// ActivityTypeWatching renders the presence as "Watching <application name>".
const ActivityTypeWatching = 3

// Discord caps several activity strings; values are clamped to these so a
// long episode title never causes the whole update to be rejected.
const (
	maxDetailsLen = 128
	maxStateLen   = 128
	maxImageLen   = 256
	maxButtonText = 32
)

// Activity is one Discord Rich Presence activity payload. Optional fields are
// omitted so an update only carries what it means to change.
type Activity struct {
	Type       int         `json:"type,omitempty"`
	Details    string      `json:"details,omitempty"`
	State      string      `json:"state,omitempty"`
	Timestamps *Timestamps `json:"timestamps,omitempty"`
	Assets     *Assets     `json:"assets,omitempty"`
	Buttons    []Button    `json:"buttons,omitempty"`
}

// Timestamps drive Discord's live elapsed/remaining counter. Start is the
// unix time the media "began", so Discord animates the timer client-side
// without further updates. End is set only when a duration is known.
type Timestamps struct {
	Start int64 `json:"start,omitempty"`
	End   int64 `json:"end,omitempty"`
}

// Assets carries the artwork shown on the card. LargeImage is a per-title
// poster URL (external URLs are supported) or a bundled asset key.
type Assets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
	SmallImage string `json:"small_image,omitempty"`
	SmallText  string `json:"small_text,omitempty"`
}

// Button is one clickable link under the activity; Discord allows two.
type Button struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// NowPlaying is the media context an activity is built from. It is stored on
// the client so live telemetry can refresh the card without the caller
// resending the media.
type NowPlaying struct {
	Media     model.ResolvedMedia
	ImageURL  string
	AniListID int
}

// Progress is one live playback sample applied to the current card.
type Progress struct {
	PositionSecs float64
	DurationSecs float64
	Paused       bool
}

// buildActivity renders the card for a now-playing context and a progress
// sample. It is pure so it is table-testable.
func buildActivity(np NowPlaying, p Progress, now time.Time) *Activity {
	media := np.Media
	isLive := media.IsLive()

	details := firstNonEmpty(media.SeriesTitle, media.EpisodeTitle)
	act := &Activity{
		Type:    ActivityTypeWatching,
		Details: clamp(details, maxDetailsLen),
		State:   clamp(describeState(media), maxStateLen),
	}

	image := strings.TrimSpace(np.ImageURL)
	if image == "" {
		image = config.DiscordAssetLogo
	}
	act.Assets = &Assets{
		LargeImage: clamp(image, maxImageLen),
		LargeText:  act.Details,
		SmallImage: config.DiscordAssetLogo,
		SmallText:  config.AppName,
	}
	if p.Paused {
		// Discord has no native pause; freezing the timer (by omitting
		// timestamps below) plus a paused badge is the conventional signal.
		act.Assets.SmallImage = config.DiscordAssetPaused
		act.Assets.SmallText = "Paused"
	}

	if !p.Paused {
		start := now.Add(-secondsToDuration(p.PositionSecs))
		ts := &Timestamps{Start: start.Unix()}
		if !isLive && p.DurationSecs > 0 {
			ts.End = start.Add(secondsToDuration(p.DurationSecs)).Unix()
		}
		act.Timestamps = ts
	}

	act.Buttons = buttonsFor(media, np.AniListID, isLive)
	return act
}

// describeState renders the second line of the card: the episode tag and
// title for episodic media, the year for movies, or a reading marker for
// manga. It falls back to a generic verb when there is nothing specific.
func describeState(media model.ResolvedMedia) string {
	if media.IsLive() {
		return "Live"
	}
	if media.MediaType == provider.MediaTypeManga {
		if chapter := strings.TrimSpace(media.EpisodeTitle); chapter != "" {
			return "Ch. " + chapter
		}
		return "Reading"
	}
	if model.IsEpisodeBased(media.MediaType) {
		tag := episodeTag(media.SeasonNumber, media.EpisodeNumber)
		title := strings.TrimSpace(media.EpisodeTitle)
		namedEpisode := title != "" && !strings.EqualFold(title, strings.TrimSpace(media.SeriesTitle))
		switch {
		case tag != "" && namedEpisode:
			return tag + " · " + title
		case tag != "":
			return tag
		case namedEpisode:
			return title
		default:
			return "Watching"
		}
	}
	if year := strings.TrimSpace(media.Year); year != "" {
		return year
	}
	return "Movie"
}

// buttonsFor builds up to two catalog links for the card: the canonical TMDB
// page and, when known, the AniList page. Live content has no stable page.
func buttonsFor(media model.ResolvedMedia, anilistID int, isLive bool) []Button {
	if isLive {
		return nil
	}
	var buttons []Button
	if media.TMDBID > 0 {
		kind := "movie"
		if model.IsEpisodeBased(media.MediaType) {
			kind = "tv"
		}
		buttons = append(buttons, Button{
			Label: clamp("View on TMDB", maxButtonText),
			URL:   fmt.Sprintf("%s/%s/%d", config.TMDBBaseURL, kind, media.TMDBID),
		})
	}
	if anilistID > 0 {
		buttons = append(buttons, Button{
			Label: clamp("View on AniList", maxButtonText),
			URL:   fmt.Sprintf("%s/anime/%d", config.AniListAuthBase, anilistID),
		})
	}
	if len(buttons) > 2 {
		buttons = buttons[:2]
	}
	return buttons
}

// episodeTag renders "S01E02" (or "E02" when the season is unknown), reusing
// the shared SxxExx convention.
func episodeTag(season, episode int) string {
	switch {
	case season > 0 && episode > 0:
		return fmt.Sprintf("S%02dE%02d", season, episode)
	case episode > 0:
		return fmt.Sprintf("E%02d", episode)
	default:
		return ""
	}
}

func secondsToDuration(secs float64) time.Duration {
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// clamp truncates s to max runes, appending an ellipsis when cut.
func clamp(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
