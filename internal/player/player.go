package player

import (
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
