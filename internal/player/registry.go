package player

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"kari/internal/aniskip"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
)

// log scopes every line from this package/component.
var playerLog = logging.With("component", "player")

type cachedPlayer struct {
	Player
	once      sync.Once
	available bool
}

func (c *cachedPlayer) Available() bool {
	c.once.Do(func() { c.available = c.Player.Available() })
	return c.available
}

// Registry holds available players and picks between them by preference.
type Registry struct {
	players       []Player
	preferred     string
	aniskipClient *aniskip.Client
}

// Register adds a player implementation.
func (r *Registry) Register(p Player) {
	r.players = append(r.players, &cachedPlayer{Player: p})
}

// PlayWithSources tries sources in order against the preferred player,
// falling back to others when unavailable. A NeedsCompletionConfirmError
// from any source counts as launched-successfully.
func (r *Registry) PlayWithSources(sources []provider.MediaSource, media model.ResolvedMedia, preferred string) (PlaybackResult, error) {
	playerLog.Debug("playback starting", "media", media.DisplayTitle(), "preferredPlayer", preferred, "sources", len(sources))

	if preferred == "" {
		preferred = r.DefaultPlayer()
	}
	order := r.preferredPlayers(preferred)

	var lastErr error
	for _, p := range order {
		playerLog.Debug("trying player", "player", p.Name(), "available", p.Available())
		// If this is the explicitly preferred player, we try it even if it says it's unavailable,
		// to bypass broken Android package detection.
		if !p.Available() && p.Name() != playerName(preferred) {
			continue
		}
		result, err := p.Play(sources, media)
		if err != nil {
			var needsConfirm *NeedsCompletionConfirmError
			if errors.As(err, &needsConfirm) {
				playerLog.Info("playback launched", "player", p.Name())
				return result, nil
			}
			playerLog.Warn("player failed; falling back", "player", p.Name(), "err", err)
			lastErr = err
		} else {
			playerLog.Info("playback launched", "player", p.Name())
			return result, nil
		}
	}
	if lastErr != nil {
		return PlaybackResult{}, lastErr
	}
	return PlaybackResult{}, fmt.Errorf("no supported player found")
}

// AvailablePlayers lists names of registered players usable on this system.
func (r *Registry) AvailablePlayers() []string {
	out := make([]string, 0, len(r.players))
	for _, p := range r.players {
		if p.Available() {
			out = append(out, p.Name())
		}
	}
	return out
}

// DefaultPlayer returns the configured preference when installed, else the
// first available player (or empty string).
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

// NewRegistry constructs and populates the platform's player set with the
// user's preference applied.
func NewRegistry(preferred string, aniskipClient *aniskip.Client) *Registry {
	r := &Registry{
		preferred:     preferred,
		aniskipClient: aniskipClient,
	}
	registerPlayers(r)
	return r
}
