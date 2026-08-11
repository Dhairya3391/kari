package player

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"kari/internal/model"
)

// Player defines the interface implemented by playback backends.
type Player interface {
	Name() string
	Available() bool
	Play(sources []model.PlaybackSource, media model.ResolvedMedia) (PlaybackResult, error)
}

type PlaybackResult struct {
	FinalPositionSecs float64
	DurationSecs      float64
	Completed         bool // true if FinalPositionSecs/DurationSecs > 0.85
}

type NeedsCompletionConfirmError struct {
	Media model.ResolvedMedia
}

func (e *NeedsCompletionConfirmError) Error() string {
	return "needs completion confirmation"
}

// attemptSources tries each source in order, skipping blank URLs, and
// returns the first one play succeeds on. A *NeedsCompletionConfirmError
// from play counts as success too (registry.PlayWithSources unwraps it the
// same way) — that's how players with no way to observe real playback
// progress (VLC, Android intents) signal "launched fine, ask the user to
// confirm completion" instead of returning real stats. Every player's
// outer Play function otherwise repeated this same loop-and-join-errors
// shape, so it's centralized here.
func attemptSources(playerLabel string, sources []model.PlaybackSource, play func(model.PlaybackSource) (PlaybackResult, error)) (PlaybackResult, error) {
	if len(sources) == 0 {
		return PlaybackResult{}, fmt.Errorf("%s playback failed: no playback sources available", playerLabel)
	}

	errs := make([]string, 0, len(sources))
	for idx, source := range sources {
		if strings.TrimSpace(source.URL) == "" {
			continue
		}
		result, err := play(source)
		if err == nil {
			return result, nil
		}
		var needsConfirm *NeedsCompletionConfirmError
		if errors.As(err, &needsConfirm) {
			return result, err
		}
		label := strings.TrimSpace(source.Label)
		if label == "" {
			label = fmt.Sprintf("source %d", idx+1)
		}
		errs = append(errs, fmt.Sprintf("%s: %v", label, err))
	}

	if len(errs) == 0 {
		return PlaybackResult{}, fmt.Errorf("%s playback failed: no usable playback sources available", playerLabel)
	}
	return PlaybackResult{}, fmt.Errorf("%s playback failed: %s", playerLabel, strings.Join(errs, " | "))
}

func playerName(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "iina", "mpv", "mxplayer", "vlc":
		return v
	default:
		return ""
	}
}

func sanitizeMediaTitle(mediaTitle string) string {
	clean := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, mediaTitle)

	return strings.TrimSpace(strings.Join(strings.Fields(clean), " "))
}
