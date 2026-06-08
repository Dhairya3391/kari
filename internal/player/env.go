package player

import "strings"

func playerName(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "iina", "mpv", "mxplayer", "vlc":
		return v
	default:
		return ""
	}
}
