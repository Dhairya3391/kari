//go:build android

package player

func registerPlayers(r *Registry) {
	launcher := newAndroidLauncher()
	r.Register(&MPVPlayer{launcher: launcher})
	r.Register(&MXPlayer{launcher: launcher})
}
