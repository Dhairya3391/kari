package lang

import "strings"

// AudioLangs maps a user language preference to the ordered --alang track
// list passed to mpv. Variants fold to the same list (ISO codes plus common
// English names); English is the fallback tail so playback never ends up with
// no audio track. Empty input yields nil.
func AudioLangs(language string) []string {
	return AudioLangsForExcept([]string{language}, nil)
}

// AudioLangsFor maps an ordered set of language preferences to one --alang
// list. Earlier languages win track selection; one English tail is appended
// after every requested alias so an absent preference still gets audio.
func AudioLangsFor(languages []string) []string {
	return AudioLangsForExcept(languages, nil)
}

// AudioLangsForExcept is AudioLangsFor with an explicit off-list. English is
// still the automatic last resort, but a language the user switched off is
// dropped from the whole list — including the fallback tail — so "English
// off" cannot make the player prefer an English track when a preference is
// absent from the stream.
func AudioLangsForExcept(languages, disabled []string) []string {
	off := make(map[string]struct{}, len(disabled))
	for _, raw := range disabled {
		if code := Normalize(raw); code != "" {
			off[code] = struct{}{}
		}
	}
	var out []string
	seen := make(map[string]struct{})
	add := func(alias string) {
		if _, blocked := off[Normalize(alias)]; blocked {
			return
		}
		if _, ok := seen[alias]; ok {
			return
		}
		seen[alias] = struct{}{}
		out = append(out, alias)
	}
	for _, language := range languages {
		if _, blocked := off[Normalize(language)]; blocked {
			continue
		}
		for _, alias := range audioAliases(language) {
			add(alias)
		}
	}
	if len(out) == 0 {
		return nil
	}
	// Use the same alias set as an explicit English preference so tracks
	// tagged with the full name are reachable as the final fallback too.
	for _, fallback := range audioAliases("en") {
		add(fallback)
	}
	return out
}

// audioAliasTable maps a canonical language code to the extra track tags mpv
// may see: ISO 639-2 forms (both bibliographic and terminology where they
// differ), common regional variants, and provider quirks. The canonical code
// and the English name are added by audioAliases itself, so providers that tag
// a track "tam" or "Tamil" still match a "ta" preference.
var audioAliasTable = map[string][]string{
	"en":  {"eng"},
	"es":  {"spa", "esla", "es-la"},
	"fr":  {"fra", "fre"},
	"de":  {"deu", "ger"},
	"it":  {"ita"},
	"pt":  {"por", "ptbr", "pt-br"},
	"ru":  {"rus"},
	"ja":  {"jpn"},
	"ko":  {"kor"},
	"zh":  {"chi", "zho"},
	"ar":  {"ara"},
	"hi":  {"hin"},
	"tr":  {"tur"},
	"nl":  {"nld", "dut"},
	"pl":  {"pol"},
	"sv":  {"swe"},
	"th":  {"tha"},
	"vi":  {"vie"},
	"id":  {"ind", "in_id"},
	"uk":  {"ukr"},
	"el":  {"ell", "gre"},
	"he":  {"heb"},
	"fa":  {"fas", "per"},
	"ro":  {"ron", "rum"},
	"cs":  {"ces", "cze"},
	"hu":  {"hun"},
	"fi":  {"fin"},
	"da":  {"dan"},
	"no":  {"nor"},
	"bn":  {"ben"},
	"ta":  {"tam"},
	"te":  {"tel"},
	"ml":  {"mal"},
	"kn":  {"kan"},
	"mr":  {"mar"},
	"gu":  {"guj"},
	"ur":  {"urd"},
	"bs":  {"bos"},
	"bg":  {"bul"},
	"hr":  {"hrv"},
	"is":  {"isl", "ice"},
	"kk":  {"kaz"},
	"mk":  {"mkd", "mac"},
	"ms":  {"msa", "may"},
	"sr":  {"srp"},
	"si":  {"sin"},
	"sl":  {"slv"},
	"fil": {"tgl"},
	"pa":  {"pan"},
	"sw":  {"swa"},
	"ha":  {"hau"},
	"et":  {"est"},
}

// audioAliases returns the aliases of one preference, without the shared
// English fallback that AudioLangsFor appends once for the whole list.
func audioAliases(language string) []string {
	code := Normalize(language)
	if code == "" {
		return nil
	}
	aliases := make([]string, 0, 2+len(audioAliasTable[code]))
	aliases = append(aliases, code)
	aliases = append(aliases, audioAliasTable[code]...)
	if name, ok := names[code]; ok && !strings.EqualFold(name, code) {
		aliases = append(aliases, strings.ToLower(name))
	}
	return aliases
}
