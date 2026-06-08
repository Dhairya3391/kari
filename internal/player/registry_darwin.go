//go:build darwin && !android

package player

func registerPlayers(r *Registry) {
	r.Register(&MPVPlayer{aniskip: r.aniskipClient})
	r.Register(&IINAPlayer{})
}
