//go:build android

package player

func registerPlayers(r *Registry) {
	r.Register(&MPVPlayer{})
	r.Register(&MXPlayer{})
}
