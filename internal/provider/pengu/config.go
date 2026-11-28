package pengu

import (
	"encoding/json"
	"net/url"
)

// The PenguPlay share link carries its configuration as a JSON object in the
// URL path. That object accepts every toggle the site's configurator exposes,
// so Kari can enable live TV and pin the filter set it wants instead of
// inheriting whatever the bundled token last saved. Values are the strings the
// addon's form submits ("checked"/"unchecked").
const (
	optionChecked   = "checked"
	optionUnchecked = "unchecked"
)

// buildAPIConfig returns the configuration object Kari sends upstream for one
// token. json.Marshal sorts map keys, so the encoded path is stable.
func buildAPIConfig(token string) map[string]string {
	cfg := map[string]string{
		// Live TV is off by default upstream; enabling it is what makes the
		// pp-live-* catalogs appear in the manifest and return rows.
		"auth_token":         token,
		"source_live-sports": optionChecked,
		// Subtitles and direct streams are wanted. The site already defaults
		// this way; the explicit values guard against a changed default.
		"subtitles_disabled": optionUnchecked,
		"emulate_vpn":        optionUnchecked,
		"disable_direct":     optionUnchecked,
	}
	// Sources stay explicit so a change in Kari's source list is a deliberate
	// edit here rather than an inherited upstream default.
	for _, source := range []string{
		"4khdhub", "moviebox", "vegamovies", "moviesdrives", "vaplayer",
		"miruro", "vidlink", "vidfast", "2peckle", "cinejoy", "kisskh",
		"hdhub4u", "anikoto", "vidy", "obejrzyj", "arctic", "toonworld4all",
		"anipm", "reanime", "lamovie", "sololatino", "moflix", "vidly", "nexa",
		"vidrift",
	} {
		cfg["source_"+source] = optionChecked
	}
	// Kari ranks and filters lanes itself, so every bucket and audio language
	// is kept for its own matching logic.
	for _, resolution := range []string{"2160", "1080", "720", "480", "360"} {
		cfg["res_"+resolution] = optionChecked
	}
	for _, language := range []string{
		"english", "spanish", "hindi", "tamil", "telugu", "french", "german",
		"italian", "portuguese", "japanese", "korean", "russian", "chinese",
		"arabic", "polish", "thai", "vietnamese", "malay", "bengali",
		"indonesian", "filipino", "kannada",
	} {
		cfg["audio_"+language] = optionChecked
	}
	return cfg
}

// encodedAPIConfig percent-encodes the configuration object as the path
// segment share links use.
func encodedAPIConfig(token string) string {
	raw, _ := json.Marshal(buildAPIConfig(token))
	return url.PathEscape(string(raw))
}
