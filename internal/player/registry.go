package player

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"kari/internal/aniskip"
	"kari/internal/logging"
	"kari/internal/model"
)

type cachedPlayer struct {
	Player
	once      sync.Once
	available bool
}

func (c *cachedPlayer) Available() bool {
	c.once.Do(func() { c.available = c.Player.Available() })
	return c.available
}

// Registry stores players and handles selection/dispatch.
type Registry struct {
	players       []Player
	preferred     string
	aniskipClient *aniskip.Client
}

// Register adds a player to the registry.
func (r *Registry) Register(p Player) {
	r.players = append(r.players, &cachedPlayer{Player: p})
}

// PlayWithSources plays media using the preferred player when possible.
func (r *Registry) PlayWithSources(sources []model.PlaybackSource, media model.ResolvedMedia, preferred string) (PlaybackResult, error) {
	logging.Debugf("PlayWithSources: media=%q preferred_player=%q sources_count=%d", media.DisplayTitle(), preferred, len(sources))

	if preferred == "" {
		preferred = r.DefaultPlayer()
	}
	order := r.preferredPlayers(preferred)

	var lastErr error
	for _, p := range order {
		logging.Debugf("PlayWithSources: trying player=%s available=%t", p.Name(), p.Available())
		// If this is the explicitly preferred player, we try it even if it says it's unavailable,
		// to bypass broken Android package detection.
		if !p.Available() && p.Name() != playerName(preferred) {
			continue
		}
		result, err := p.Play(sources, media)
		if err != nil {
			var needsConfirm *NeedsCompletionConfirmError
			if errors.As(err, &needsConfirm) {
				logging.Infof("PlayWithSources: player %s succeeded", p.Name())
				return result, nil
			}
			logging.Warnf("PlayWithSources: player %s failed: %v", p.Name(), err)
			lastErr = err
		} else {
			logging.Infof("PlayWithSources: player %s succeeded", p.Name())
			return result, nil
		}
	}
	if lastErr != nil {
		return PlaybackResult{}, lastErr
	}
	return PlaybackResult{}, fmt.Errorf("no supported player found")
}

// AvailablePlayers returns the names of available players.
func (r *Registry) AvailablePlayers() []string {
	out := make([]string, 0, len(r.players))
	for _, p := range r.players {
		if p.Available() {
			out = append(out, p.Name())
		}
	}
	return out
}

// DefaultPlayer returns the selected default player name.
func (r *Registry) DefaultPlayer() string {
	envPlayer := playerName(r.preferred)
	for _, p := range r.players {
		if p.Name() == envPlayer {
			return p.Name()
		}
	}

	for _, p := range r.players {
		if p.Name() == "mpv" && p.Available() {
			return "mpv"
		}
	}

	for _, p := range r.players {
		if p.Available() {
			return p.Name()
		}
	}

	// Fallback to "mpv" if detection fails completely (especially for Android package visibility restrictions)
	return "mpv"
}

func (r *Registry) preferredPlayers(preferred string) []Player {
	if strings.TrimSpace(preferred) == "" {
		preferred = r.preferred
	}
	prefName := playerName(preferred)

	// Try to find the preferred player
	for _, p := range r.players {
		if p.Name() == prefName {
			// Return a list with preferred first, then others
			ordered := []Player{p}
			for _, other := range r.players {
				if other.Name() != prefName {
					ordered = append(ordered, other)
				}
			}
			return ordered
		}
	}

	return r.players
}

// NewRegistry constructs a registry with platform-supported players.
func NewRegistry(preferred string, aniskipClient *aniskip.Client) *Registry {
	r := &Registry{
		preferred:     preferred,
		aniskipClient: aniskipClient,
	}
	registerPlayers(r)
	return r
}
