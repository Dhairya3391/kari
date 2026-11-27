// Package lang maps language tags to human-readable names and normalizes
// them to a canonical ISO 639-1-ish code. It has no dependencies of its own
// so it can be imported by internal/subtitles and internal/service without
// creating an import cycle between them.
//
// Real provider responses are messy: upstream subtitle lists for the
// same title have been observed mixing clean codes ("en", "fr") with full
// English names ("English", "French"), nonstandard codes ("in_id" for
// Indonesian), region variants ("zh-cn", "zh-tw"), and typos
// ("Portuguese (BR)") — all in one response. Normalize exists to fold all
// of that down to one canonical code so language-preference matching and
// display both work regardless of which form a given provider used.
package lang

import "strings"

var names = map[string]string{
	"off": "Off",
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
	"et":  "Estonian",
}

// aliases maps other forms seen in the wild — full names, typos, regional
// variants, nonstandard codes — to one of the canonical codes in names.
var aliases = map[string]string{
	"english":         "en",
	"eng":             "en",
	"spanish":         "es",
	"spa":             "es",
	"french":          "fr",
	"fra":             "fr",
	"fre":             "fr",
	"german":          "de",
	"deu":             "de",
	"ger":             "de",
	"italian":         "it",
	"ita":             "it",
	"portuguese":      "pt",
	"por":             "pt",
	"portuguese (br)": "pt",
	"protuguese (br)": "pt", //nolint:misspell // upstream subtitle typo
	"protuguese":      "pt", //nolint:misspell // same typo without the region tag
	"pt-br":           "pt",
	"brazilian":       "pt",
	"russian":         "ru",
	"rus":             "ru",
	"japanese":        "ja",
	"jpn":             "ja",
	"korean":          "ko",
	"kor":             "ko",
	"chinese":         "zh",
	"zho":             "zh",
	"chi":             "zh",
	"zh-cn":           "zh",
	"zh-tw":           "zh",
	"zh-hans":         "zh",
	"zh-hant":         "zh",
	"mandarin":        "zh",
	"arabic":          "ar",
	"ara":             "ar",
	"hindi":           "hi",
	"hin":             "hi",
	"turkish":         "tr",
	"tur":             "tr",
	"dutch":           "nl",
	"nld":             "nl",
	"dut":             "nl",
	"polish":          "pl",
	"pol":             "pl",
	"swedish":         "sv",
	"swe":             "sv",
	"thai":            "th",
	"tha":             "th",
	"vietnamese":      "vi",
	"vie":             "vi",
	"indonesian":      "id",
	"ind":             "id",
	"in_id":           "id", // nonstandard Indonesian code variant
	"ukrainian":       "uk",
	"ukr":             "uk",
	"greek":           "el",
	"ell":             "el",
	"gre":             "el",
	"hebrew":          "he",
	"heb":             "he",
	"persian":         "fa",
	"fas":             "fa",
	"per":             "fa",
	"farsi":           "fa",
	"romanian":        "ro",
	"ron":             "ro",
	"rum":             "ro",
	"czech":           "cs",
	"ces":             "cs",
	"cze":             "cs",
	"hungarian":       "hu",
	"hun":             "hu",
	"finnish":         "fi",
	"fin":             "fi",
	"estonian":        "et",
	"est":             "et",
	"danish":          "da",
	"dan":             "da",
	"norwegian":       "no",
	"nor":             "no",
	"bengali":         "bn",
	"ben":             "bn",
	"tamil":           "ta",
	"tam":             "ta",
	"telugu":          "te",
	"tel":             "te",
	"malayalam":       "ml",
	"mal":             "ml",
	"kannada":         "kn",
	"kan":             "kn",
	"marathi":         "mr",
	"mar":             "mr",
	"gujarati":        "gu",
	"guj":             "gu",
	"urdu":            "ur",
	"urd":             "ur",
	"bosnian":         "bs",
	"bos":             "bs",
	"bulgarian":       "bg",
	"bul":             "bg",
	"croatian":        "hr",
	"hrv":             "hr",
	"icelandic":       "is",
	"isl":             "is",
	"kazakh":          "kk",
	"kaz":             "kk",
	"macedonian":      "mk",
	"mkd":             "mk",
	"malay":           "ms",
	"may":             "ms",
	"off":             "off",
	"disabled":        "off",
	"none":            "off",
	"disable":         "off",
	"msa":             "ms",
	"serbian":         "sr",
	"srp":             "sr",
	"sinhala":         "si",
	"sin":             "si",
	"slovenian":       "sl",
	"slv":             "sl",
	"filipino":        "fil",
	"tgl":             "fil",
	"punjabi":         "pa",
	"pan":             "pa",
	"swahili":         "sw",
	"swa":             "sw",
	"hausa":           "ha",
	"hau":             "ha",
}

// SubtitleOptions is the ordered list of languages selectable as the default
// subtitle language in settings. It covers every code Normalize can produce a
// display name for, so a track tagged in any provider's language list stays
// pickable; keep it in sync with names (TestSubtitleOptionsCoverNames).
var SubtitleOptions = []string{
	"off", "en", "es", "fr", "de", "pt", "it", "ar", "hi", "ja", "ko", "zh", "ru", "tr", "id", "th", "vi", "ms", "fil", "pl", "nl",
	"sv", "uk", "el", "he", "fa", "ro", "cs", "hu", "fi", "da", "no", "bn", "ta", "te", "ml", "kn", "mr", "gu", "ur", "bs", "bg",
	"hr", "is", "kk", "mk", "sr", "si", "sl", "pa", "sw", "ha", "et",
}

// nativeAliases maps the native-script spellings of our curated languages
// as they appear in upstream data (HLS LANGUAGE attributes, provider APIs)
// to canonical codes, so a track tagged हिन्दी or தமிழ் still displays as
// "Hindi"/"Tamil" instead of raw native script leaking into the UI.
var nativeAliases = map[string]string{
	"हिन्दी":           "hi",
	"हिंदी":            "hi",
	"বাংলা":            "bn",
	"தமிழ்":            "ta",
	"తెలుగు":           "te",
	"മലയാളം":           "ml",
	"ಕನ್ನಡ":            "kn",
	"मराठी":            "mr",
	"ગુજરાતી":          "gu",
	"ਪੰਜਾਬੀ":           "pa",
	"پنجابی":           "pa",
	"اردو":             "ur",
	"العربية":          "ar",
	"عربي":             "ar",
	"فارسی":            "fa",
	"עברית":            "he",
	"ελληνικά":         "el",
	"русский":          "ru",
	"українська":       "uk",
	"日本語":              "ja",
	"한국어":              "ko",
	"中文":               "zh",
	"ไทย":              "th",
	"tiếng việt":       "vi",
	"bahasa indonesia": "id",
}

// Normalize folds a language tag from any provider into one of the
// canonical codes above, so tags representing the same language compare
// equal regardless of which form a given provider used — English names,
// 2/3-letter codes, typos, regional variants, and native-script spellings.
// Trailing parenthetical annotations name the track source, not the
// language ("english (cr english)", "English (US)"), so they are dropped
// before matching; without this such tracks compare equal to nothing.
// Unrecognized tags are returned lowercased and trimmed, unchanged otherwise.
func Normalize(raw string) string {
	code := strings.ToLower(strings.TrimSpace(raw))
	if code == "" {
		return ""
	}
	if i := strings.Index(code, "("); i >= 0 {
		code = strings.TrimSpace(code[:i])
	}
	if code == "" {
		return ""
	}
	if canon, ok := aliases[code]; ok {
		return canon
	}
	if canon, ok := nativeAliases[code]; ok {
		return canon
	}
	return code
}

// FromLabel resolves a provider-supplied language label to a canonical code.
// Upstream labels carry trailing noise — source annotations
// ("english (cr english)"), bracketed tags ("[SoM]"), hearing-impaired
// markers ("Hi5", "SDH"), and ordinal suffixes ("Brazilian10") — that would
// otherwise make identical languages compare unequal. The cleaned, lowercased
// label is fed through Normalize; unknown names fall back to the cleaned text
// itself so nothing is silently dropped.
func FromLabel(raw string) string {
	cleaned := scrubLabel(raw)
	if normalized := Normalize(cleaned); normalized != cleaned {
		return normalized
	}
	return cleaned
}

// scrubLabel strips non-language decoration from a raw upstream label and
// returns the lowercased remainder. It is deliberately conservative with
// digits: a trailing number is only removed when it attaches directly to a
// name, so regional codes such as "es-419" keep their meaningful suffix.
func scrubLabel(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))

	// Bracketed tags and text after a track separator describe the source,
	// not the language; the language name itself comes first.
	s = stripBracketed(s)
	if i := strings.IndexAny(s, "·•|"); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = s[:i]
	}

	fields := strings.Fields(s)
	if len(fields) > 1 && isHIMarker(fields[len(fields)-1]) {
		last := fields[len(fields)-1]
		s = strings.TrimSpace(strings.TrimSuffix(s, last))
	}
	return stripTrailingDigits(strings.TrimSpace(s))
}

// stripBracketed removes every [...]-delimited annotation group from s.
func stripBracketed(s string) string {
	for {
		start := strings.IndexByte(s, '[')
		if start < 0 {
			return s
		}
		end := strings.IndexByte(s, ']')
		if end < start {
			return s
		}
		s = s[:start] + s[end+1:]
	}
}

// isHIMarker reports whether token is a hearing-impaired marker such as
// "hi", "hi5", or "sdh". Tokens are lowercased by the caller before matching.
func isHIMarker(token string) bool {
	switch token {
	case "hi", "sdh":
		return true
	}
	return strings.HasPrefix(token, "hi") && allDigits(token[2:])
}

// stripTrailingDigits removes a digit run that directly follows a name
// letter. A hyphen before the digits marks a regional code ("es-419"), which
// must survive intact for alias lookup.
func stripTrailingDigits(s string) string {
	end := len(s)
	start := end
	for start > 0 && s[start-1] >= '0' && s[start-1] <= '9' {
		start--
	}
	if start == end || start == 0 || !isASCIILetter(s[start-1]) {
		return s
	}
	return s[:start]
}

func isASCIILetter(b byte) bool {
	return b >= 'a' && b <= 'z'
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Name returns the English display name for a language tag in any form
// Normalize understands, falling back to the (normalized) tag itself,
// uppercased, if it's not in the curated lists. All user-facing rendering
// goes through here so raw provider tags or native scripts never leak into
// the UI.
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

// Names returns the display names of the given language tags, de-duplicated
// and joined for a single UI label. Unknown tags render as their uppercased
// code so a multi-language channel never hides languages the catalog lists.
func Names(raw []string) string {
	seen := make(map[string]struct{}, len(raw))
	display := make([]string, 0, len(raw))
	for _, tag := range raw {
		code := Normalize(tag)
		if code == "" {
			continue
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		display = append(display, Name(code))
	}
	return strings.Join(display, " · ")
}

// Canonicalize maps each tag through Normalize, drops empty results, and
// returns the surviving codes in first-seen order without duplicates.
func Canonicalize(list []string) []string {
	seen := make(map[string]struct{}, len(list))
	var out []string
	for _, tag := range list {
		code := Normalize(tag)
		if code == "" {
			continue
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out
}

// FromCodeOrLabel resolves an audio language from a provider code and its
// human-readable label. Some upstreams (notably bingr) use "fi" for Filipino
// even though "fi" is the valid ISO 639-1 code for Finnish, so aliasing "fi"
// globally would break genuine Finnish tracks. When the label resolves to a
// curated language it is trusted over the code — "fi" plus "Filipino" yields
// "fil" while "fi" plus "Finnish" stays "fi". Otherwise the code is normalized.
func FromCodeOrLabel(code, label string) string {
	if label != "" {
		if candidate := FromLabel(label); candidate != "" {
			if _, curated := names[candidate]; curated {
				return candidate
			}
		}
	}
	return Normalize(code)
}
