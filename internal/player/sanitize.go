package player

import (
	"strings"
	"unicode"
)

func sanitizeMediaTitle(mediaTitle string) string {
	clean := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, mediaTitle)

	return strings.TrimSpace(strings.Join(strings.Fields(clean), " "))
}
