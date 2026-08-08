// Package lang maps language tags to human-readable names and normalizes
// them to a canonical ISO 639-1-ish code. It has no dependencies of its own
// so it can be imported by internal/subtitles and internal/service without
// creating an import cycle between them.
//
// Real provider responses are messy: VidKing's own subtitle list for the
// same title has been observed mixing clean codes ("en", "fr") with full
// English names ("English", "French"), a nonstandard code ("in_id" for
// Indonesian), region variants ("zh-cn", "zh-tw"), and even a typo
// ("Protuguese (BR)") — all in one response. Normalize exists to fold all
// of that down to one canonical code so language-preference matching and
// display both work regardless of which form a given provider used.
package lang

import "strings"

var names = map[string]string{
	"en":  "English",
	"es":  "Spanish",
	"fr":  "French",
	"de":  "German",
	"it":  "Italian",
	"pt":  "Portuguese",
	"ru":  "Russian",
	"ja":  "Japanese",
	"ko":  "Korean",
	"zh":  "Chinese",
	"ar":  "Arabic",
	"hi":  "Hindi",
	"tr":  "Turkish",
	"nl":  "Dutch",
	"pl":  "Polish",
	"sv":  "Swedish",
	"th":  "Thai",
	"vi":  "Vietnamese",
	"id":  "Indonesian",
	"uk":  "Ukrainian",
	"el":  "Greek",
	"he":  "Hebrew",
	"fa":  "Persian",
	"ro":  "Romanian",
	"cs":  "Czech",
	"hu":  "Hungarian",
	"fi":  "Finnish",
	"da":  "Danish",
	"no":  "Norwegian",
	"bn":  "Bengali",
	"ta":  "Tamil",
	"te":  "Telugu",
	"ml":  "Malayalam",
	"kn":  "Kannada",
	"mr":  "Marathi",
	"gu":  "Gujarati",
	"ur":  "Urdu",
	"bs":  "Bosnian",
	"bg":  "Bulgarian",
	"hr":  "Croatian",
	"is":  "Icelandic",
	"kk":  "Kazakh",
	"mk":  "Macedonian",
	"ms":  "Malay",
	"sr":  "Serbian",
	"si":  "Sinhala",
	"sl":  "Slovenian",
	"fil": "Filipino",
	"pa":  "Punjabi",
	"sw":  "Swahili",
	"ha":  "Hausa",
}

// aliases maps other forms seen in the wild — full names, typos, regional
// variants, nonstandard codes — to one of the canonical codes in names.
var aliases = map[string]string{
	"english":         "en",
	"spanish":         "es",
	"french":          "fr",
	"german":          "de",
	"italian":         "it",
	"portuguese":      "pt",
	"portuguese (br)": "pt",
	"protuguese (br)": "pt", // yes, really — seen verbatim from VidKing
	"pt-br":           "pt",
	"brazilian":       "pt",
	"russian":         "ru",
	"japanese":        "ja",
	"korean":          "ko",
	"chinese":         "zh",
	"zh-cn":           "zh",
	"zh-tw":           "zh",
	"zh-hans":         "zh",
	"zh-hant":         "zh",
	"mandarin":        "zh",
	"arabic":          "ar",
	"hindi":           "hi",
	"turkish":         "tr",
	"dutch":           "nl",
	"polish":          "pl",
	"swedish":         "sv",
	"thai":            "th",
	"vietnamese":      "vi",
	"indonesian":      "id",
	"in_id":           "id", // VidKing's nonstandard Indonesian code
	"ukrainian":       "uk",
	"greek":           "el",
	"hebrew":          "he",
	"persian":         "fa",
	"farsi":           "fa",
	"romanian":        "ro",
	"czech":           "cs",
	"hungarian":       "hu",
	"finnish":         "fi",
	"danish":          "da",
	"norwegian":       "no",
	"bengali":         "bn",
	"tamil":           "ta",
	"telugu":          "te",
	"malayalam":       "ml",
	"kannada":         "kn",
	"marathi":         "mr",
	"gujarati":        "gu",
	"urdu":            "ur",
	"bosnian":         "bs",
	"bulgarian":       "bg",
	"croatian":        "hr",
	"icelandic":       "is",
	"kazakh":          "kk",
	"macedonian":      "mk",
	"malay":           "ms",
	"serbian":         "sr",
	"sinhala":         "si",
	"slovenian":       "sl",
	"filipino":        "fil",
	"punjabi":         "pa",
	"swahili":         "sw",
	"hausa":           "ha",
}

// SubtitleOptions is the curated, ordered list of languages selectable as
// the default subtitle language in settings.
var SubtitleOptions = []string{
	"en", "es", "fr", "de", "pt", "it", "ar", "hi", "ja", "ko", "zh", "ru", "tr", "id",
}

// Normalize folds a language tag from any provider into one of the
// canonical codes above, so tags representing the same language compare
// equal regardless of which form a provider used. Unrecognized tags are
// returned lowercased and trimmed, unchanged otherwise.
func Normalize(raw string) string {
	code := strings.ToLower(strings.TrimSpace(raw))
	if code == "" {
		return ""
	}
	if canon, ok := aliases[code]; ok {
		return canon
	}
	return code
}

// Name returns a human-readable name for a language tag in any form
// Normalize understands, falling back to the (normalized) tag itself,
// uppercased, if it's not in the curated list above.
func Name(raw string) string {
	code := Normalize(raw)
	if name, ok := names[code]; ok {
		return name
	}
	if code == "" {
		return "Unknown"
	}
	return strings.ToUpper(code)
}
