package lang

import "strings"

// AudioLangs maps a user language preference to the ordered --alang track
// list passed to mpv. Variants fold to the same list (ISO codes plus common
// English names); English is always the fallback tail so playback never
// ends up with no audio track. Empty input yields nil.
func AudioLangs(language string) []string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "":
		return nil
	case "hi", "hindi":
		return []string{"hi", "hin", "hindi", "en", "eng"}
	case "ja", "japanese":
		return []string{"ja", "jpn", "japanese", "en", "eng"}
	case "es", "spanish":
		return []string{"es", "spa", "spanish", "esla", "es-la", "en", "eng"}
	case "fr", "french":
		return []string{"fr", "fra", "fre", "french", "en", "eng"}
	case "de", "german":
		return []string{"de", "deu", "ger", "german", "en", "eng"}
	case "it", "italian":
		return []string{"it", "ita", "italian", "en", "eng"}
	case "pt", "portuguese":
		return []string{"pt", "por", "portuguese", "ptbr", "pt-br", "en", "eng"}
	case "ru", "russian":
		return []string{"ru", "rus", "russian", "en", "eng"}
	case "ar", "arabic":
		return []string{"ar", "ara", "arabic", "en", "eng"}
	case "ko", "korean":
		return []string{"ko", "kor", "korean", "en", "eng"}
	case "zh", "chinese":
		return []string{"zh", "chi", "zho", "chinese", "en", "eng"}
	case "ta", "tamil":
		return []string{"ta", "tam", "tamil", "en", "eng"}
	case "te", "telugu":
		return []string{"te", "tel", "telugu", "en", "eng"}
	default:
		return []string{strings.ToLower(strings.TrimSpace(language)), "en", "eng"}
	}
}
