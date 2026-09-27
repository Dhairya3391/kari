//go:build !android

package player

import (
	"runtime"

	"kari/internal/util"
)

// registerDesktopPlayers registers the players available on desktop
// platforms. mpv is universal; IINA joins on macOS where it is common, and
// VLC stays available everywhere its binary can be found.
func registerPlayers(r *Registry) {
	r.Register(&MPVPlayer{
		skipClients:  r.skipClients,
		skipSettings: r.skipSettings,
		skipCache:    util.NewBoundedCache[combinedSkipTimes](100),
	})
	if runtime.GOOS == "darwin" {
		r.Register(&IINAPlayer{})
	}
	r.Register(&VLCPlayer{})
}
