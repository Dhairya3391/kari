//go:build !darwin && !android && !windows

package player

func registerPlayers(r *Registry) {
	r.Register(&MPVPlayer{aniskip: r.aniskipClient})
	r.Register(&VLCPlayer{})
}
