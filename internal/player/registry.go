package player

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/util"
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
	players          []Player
	preferred        string
	skipClients      SkipClients
	skipSettings     SkipSettings
	skipCache        *util.BoundedCache[combinedSkipTimes]
	progressObserver ProgressObserver
}

// Register adds a player implementation.
func (r *Registry) Register(p Player) {
	r.players = append(r.players, &cachedPlayer{Player: p})
	if r.progressObserver != nil {
		if po, ok := p.(progressObservable); ok {
			po.setProgressObserver(r.progressObserver)
		}
	}
}

// PlayWithSources tries sources in order against the selected player only.
// It does not fall back to other installed players if the selected player fails.
// Players that cannot observe completion return NeedsCompletionConfirmError after a successful launch.
func (r *Registry) PlayWithSources(sources []provider.MediaSource, media model.ResolvedMedia, preferred string) (PlaybackResult, error) {
	playerLog.Debug("playback starting", "media", media.DisplayTitle(), "preferredPlayer", preferred, "sources", len(sources))

	if preferred == "" {
		preferred = r.DefaultPlayer()
	}
	p := r.findPlayer(preferred)
	if p == nil {
		return PlaybackResult{}, fmt.Errorf("player %q not found", preferred)
	}

	playerLog.Debug("trying player", "player", p.Name(), "available", p.Available())
	// If this is the explicitly preferred player, we try it even if it says it's unavailable,
	// to bypass broken Android package detection.
	if !p.Available() && p.Name() != playerName(preferred) {
		return PlaybackResult{}, fmt.Errorf("player %s is not available", p.Name())
	}

	result, err := p.Play(sources, media)
	if err != nil {
		var needsConfirm *NeedsCompletionConfirmError
		if errors.As(err, &needsConfirm) {
			playerLog.Info("playback launched", "player", p.Name())
			return result, err
		}
		playerLog.Warn("player failed", "player", p.Name(), "err", err)
		return PlaybackResult{}, err
	}

	playerLog.Info("playback launched", "player", p.Name())
	return result, nil
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
		if p.Name() == envPlayer && p.Available() {
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

func (r *Registry) findPlayer(preferred string) Player {
	if strings.TrimSpace(preferred) == "" {
		preferred = r.preferred
	}
	prefTrimmed := strings.TrimSpace(preferred)
	prefNorm := playerName(preferred)
	for _, p := range r.players {
		if strings.EqualFold(p.Name(), prefTrimmed) || (prefNorm != "" && strings.EqualFold(p.Name(), prefNorm)) {
			return p
		}
	}
	return nil
}

type skipConfigurable interface {
	setSkipSettings(s SkipSettings)
}

// progressObservable is implemented by players that can report live playback
// telemetry (mpv/IINA via IPC). The registry forwards the observer to them.
type progressObservable interface {
	setProgressObserver(obs ProgressObserver)
}

// SetProgressObserver installs a callback that receives live playback samples
// from players that can observe them. Passing nil clears it.
func (r *Registry) SetProgressObserver(obs ProgressObserver) {
	r.progressObserver = obs
	for _, p := range r.players {
		target := p
		if cp, ok := p.(*cachedPlayer); ok {
			target = cp.Player
		}
		if po, ok := target.(progressObservable); ok {
			po.setProgressObserver(obs)
		}
	}
}

// SetSkipSettings updates skip options for all player instances.
func (r *Registry) SetSkipSettings(s SkipSettings) {
	r.skipSettings = s
	for _, p := range r.players {
		target := p
		if cp, ok := p.(*cachedPlayer); ok {
			target = cp.Player
		}
		if sc, ok := target.(skipConfigurable); ok {
			sc.setSkipSettings(s)
		}
	}
}

// ResolveSkipInfo loads the selected skip timings and returns their provenance.
// The boolean is false when the lookup was incomplete (a provider errored or
// the deadline expired), so the caller may retry; a confirmed absence of skip
// data is cached and reported as true.
func (r *Registry) ResolveSkipInfo(media model.ResolvedMedia) (SkipInfo, bool) {
	if r == nil || r.skipCache == nil {
		return SkipInfo{}, true
	}
	mode := strings.ToLower(strings.TrimSpace(r.skipSettings.Provider))
	if mode == "" {
		mode = "hybrid"
	}
	if mode == "off" || mode == "none" {
		return SkipInfo{}, true
	}
	_, script := getSkipArgs(r.skipClients, r.skipSettings, media, r.skipCache)
	cleanupSkipScript(script)
	cached, ok := r.skipCache.Get(skipCacheKey(media, mode))
	if !ok {
		return SkipInfo{}, false
	}
	return SkipInfo{
		Opening: infoText(cached.OpSource),
		Ending:  infoText(cached.EdSource),
		Recap:   infoText(cached.RecapSource),
		Preview: infoText(cached.PrevSource),
	}, true
}

// NewRegistry constructs and populates the platform's player set with the
// user's preference applied.
func NewRegistry(preferred string, clients SkipClients, skipSettings SkipSettings) *Registry {
	r := &Registry{
		preferred:    preferred,
		skipClients:  clients,
		skipSettings: skipSettings,
		skipCache:    util.NewBoundedCache[combinedSkipTimes](100),
	}
	registerPlayers(r)
	return r
}
