package kit

import (
	"strings"
	"testing"
)

func TestDecryptMegaplayEnc(t *testing.T) {
	encStr := "wdeBruh3qqn_i5wUNnyaPcXqidp1UWP84FfPHzGyKXDMoY_RzXqbC0h49XmoI7d0vYZArA5rcKY-FQxnEk8NNEWezec9dd2jwxp1UbLN43p_7CMxXPDF5BUX86bUm0_Uuw0-dlv_yT9MKsnmlOgDFm3ReCPZPJloNlqBF7aftTk"
	got, err := DecryptMegaplayEnc(encStr)
	if err != nil {
		t.Fatalf("decryptMegaplayEnc failed: %v", err)
	}
	want := "https://fetch.nexabloom.top/anime/8d9a0adb7c204239c9635426f35c9522/25e29cc9773a7a51e35941a5f67ef1f9/master.m3u8"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSignCDNURL(t *testing.T) {
	raw := "https://fetch.nexabloom.top/anime/8d9a0adb7c204239c9635426f35c9522/25e29cc9773a7a51e35941a5f67ef1f9/master.m3u8"
	signed := SignCDNURL(raw, 86400)
	if !strings.HasPrefix(signed, raw+"?token=") {
		t.Errorf("expected signed url prefix, got %q", signed)
	}
}
