package lang

// SubtitleLangs maps a user subtitle preference to the ordered --slang list
// passed to mpv for embedded track selection. "off" and empty yield nil so
// the player leaves track selection to the user's own mpv configuration.
// The list falls back to English for the same reason AudioLangs does: a
// preferred language that is absent still gets the user a readable track.
func SubtitleLangs(language string) []string {
	switch Normalize(language) {
	case "", "off":
		return nil
	case "en":
		return []string{"en", "eng", "enUS", "en-US"}
	default:
		return AudioLangs(language)
	}
}
