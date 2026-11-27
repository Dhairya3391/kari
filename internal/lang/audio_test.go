package lang

import (
	"strings"
	"testing"
)

func TestAudioLangs(t *testing.T) {
	joined := func(code string) string { return strings.Join(AudioLangs(code), ",") }
	if got := joined("hindi"); got != "hi,hin,hindi,en,eng,english" {
		t.Errorf("AudioLangs(hindi) = %q", got)
	}
	if got := joined("ja"); got != "ja,jpn,japanese,en,eng,english" {
		t.Errorf("AudioLangs(ja) = %q", got)
	}
	if got := joined("klingon"); got != "klingon,en,eng,english" {
		t.Errorf("AudioLangs(klingon) = %q", got)
	}
	if AudioLangs("") != nil {
		t.Errorf("AudioLangs(\"\") must be nil")
	}
	if got := joined("  SPANISH "); got != "es,spa,esla,es-la,spanish,en,eng,english" {
		t.Errorf("AudioLangs must trim and fold case, got %q", got)
	}
	// Every curated subtitle language must map to its ISO 639-2 tag and
	// English name so embedded tracks tagged either way still match.
	if got := joined("bn"); got != "bn,ben,bengali,en,eng,english" {
		t.Errorf("AudioLangs(bn) = %q", got)
	}
	if got := joined("tamil"); got != "ta,tam,tamil,en,eng,english" {
		t.Errorf("AudioLangs(tamil) = %q", got)
	}
}

func TestAudioLangsFor(t *testing.T) {
	got := strings.Join(AudioLangsFor([]string{"es", "de"}), ",")
	want := "es,spa,esla,es-la,spanish,de,deu,ger,german,en,eng,english"
	if got != want {
		t.Errorf("AudioLangsFor(es, de) = %q, want %q", got, want)
	}
	if AudioLangsFor(nil) != nil || AudioLangsFor([]string{""}) != nil {
		t.Errorf("AudioLangsFor of empty preferences must be nil")
	}
}

func TestAudioLangsForExceptDropsDisabledLanguages(t *testing.T) {
	// English stays the automatic fallback when the user did not turn it off.
	if got := strings.Join(AudioLangsForExcept([]string{"ja"}, nil), ","); got != "ja,jpn,japanese,en,eng,english" {
		t.Errorf("default fallback = %q", got)
	}
	// An explicit "English off" removes the tail as well as the code itself.
	if got := strings.Join(AudioLangsForExcept([]string{"ja"}, []string{"en"}), ","); got != "ja,jpn,japanese" {
		t.Errorf("disabled English = %q", got)
	}
	if got := strings.Join(AudioLangsForExcept([]string{"es", "de"}, []string{"de"}), ","); got != "es,spa,esla,es-la,spanish,en,eng,english" {
		t.Errorf("disabled preference = %q", got)
	}
	if AudioLangsForExcept(nil, []string{"en"}) != nil {
		t.Errorf("no preferences must yield no --alang tail")
	}
}
