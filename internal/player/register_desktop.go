//go:build !android

package player

import "runtime"

// registerDesktopPlayers registers the players available on desktop
// platforms. mpv is universal; IINA joins on macOS where VLC is uncommon.
func registerPlayers(r *Registry) {
	r.Register(&MPVPlayer{aniskip: r.aniskipClient})
	if runtime.GOOS == "darwin" {
		r.Register(&IINAPlayer{})
		return
	}
	r.Register(&VLCPlayer{})
}
