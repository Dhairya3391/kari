package player

import (
	"strings"
	"testing"

	"kari/internal/model"
	"kari/internal/provider"
)

func TestExtraHeaderFields(t *testing.T) {
	source := provider.MediaSource{ExtraHeaders: map[string]string{
		"Referer":    "https://skipped.example/",
		"User-Agent": "Mozilla/5.0",
		"Cookie":     "a=b",
		"Origin":     "https://origin.example",
		"X-Cnc":      "1",
	}}
	got := extraHeaderFields(source)
	want := []string{"Origin: https://origin.example", "X-Cnc: 1"}
	if len(got) != len(want) {
		t.Fatalf("extraHeaderFields() = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extraHeaderFields()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtraHeaderCommaEncoding(t *testing.T) {
	source := provider.MediaSource{ExtraHeaders: map[string]string{"X-Forwarded": "1.2.3.4, 5.6.7.8"}}
	if got := extraHeaderFields(source); len(got) != 1 || got[0] != "X-Forwarded: 1.2.3.4, 5.6.7.8" {
		t.Fatalf("curl form = %q, want unescaped commas", got)
	}
	if got := mpvExtraHeaderFields(source); len(got) != 1 || got[0] != `X-Forwarded: 1.2.3.4\, 5.6.7.8` {
		t.Fatalf("mpv form = %q, want escaped commas", got)
	}
}

func TestExplicitOrigin(t *testing.T) {
	source := provider.MediaSource{ExtraHeaders: map[string]string{"origin": "https://explicit.example"}}
	if got := explicitOrigin(source); got != "https://explicit.example" {
		t.Fatalf("explicitOrigin() = %q", got)
	}
	if got := explicitOrigin(provider.MediaSource{ExtraHeaders: map[string]string{"X-Cnc": "1"}}); got != "" {
		t.Fatalf("explicitOrigin() = %q, want empty", got)
	}
}

func TestBuildMPVArgsExplicitOriginWins(t *testing.T) {
	source := provider.MediaSource{
		URL:          "https://cdn.example/stream.m3u8",
		Referer:      "https://derived.example/",
		ExtraHeaders: map[string]string{"Origin": "https://explicit.example"},
	}
	args := buildMPVArgs(source, model.ResolvedMedia{}, "/tmp/mpv.sock", nil)
	joined := strings.Join(args, "\x00")
	if strings.Contains(joined, "Origin: https://derived.example") {
		t.Fatal("derived Origin was emitted despite an explicit Origin")
	}
	if n := strings.Count(joined, "Origin: https://explicit.example"); n != 1 {
		t.Fatalf("explicit Origin emitted %d times, want 1", n)
	}
}
