package lang

import (
	"strings"
	"testing"
)

func TestAudioLangs(t *testing.T) {
	joined := func(code string) string { return strings.Join(AudioLangs(code), ",") }
	if got := joined("hindi"); got != "hi,hin,hindi,en,eng" {
		t.Errorf("AudioLangs(hindi) = %q", got)
	}
	if got := joined("ja"); got != "ja,jpn,japanese,en,eng" {
		t.Errorf("AudioLangs(ja) = %q", got)
	}
	if got := joined("klingon"); got != "klingon,en,eng" {
		t.Errorf("AudioLangs(klingon) = %q", got)
	}
	if AudioLangs("") != nil {
		t.Errorf("AudioLangs(\"\") must be nil")
	}
	if got := joined("  SPANISH "); got != "es,spa,spanish,esla,es-la,en,eng" {
		t.Errorf("AudioLangs must trim and fold case, got %q", got)
	}
}
