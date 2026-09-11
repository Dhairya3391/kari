//go:build !android

package player

import "runtime"

// registerDesktopPlayers registers the players available on desktop
// platforms. mpv is universal; IINA joins on macOS where it is common, and
// VLC stays available everywhere its binary can be found.
func registerPlayers(r *Registry) {
	r.Register(&MPVPlayer{
		aniskip:      r.aniskipClient,
		animeskip:    r.animeskipClient,
		skipSettings: r.skipSettings,
	})
	if runtime.GOOS == "darwin" {
		r.Register(&IINAPlayer{})
	}
	r.Register(&VLCPlayer{})
}
