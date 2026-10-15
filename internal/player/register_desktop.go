//go:build !android

package player

import (
	"runtime"
)

// registerDesktopPlayers registers the players available on desktop
// platforms. mpv is universal; IINA joins on macOS where it is common, and
// VLC stays available everywhere its binary can be found.
func registerPlayers(r *Registry) {
	r.Register(&MPVPlayer{
		skipClients:  r.skipClients,
		skipSettings: r.skipSettings,
		skipCache:    r.skipCache,
	})
	if runtime.GOOS == "darwin" {
		r.Register(&IINAPlayer{
			skipClients:  r.skipClients,
			skipSettings: r.skipSettings,
			skipCache:    r.skipCache,
		})
	}
	r.Register(&VLCPlayer{})
}
