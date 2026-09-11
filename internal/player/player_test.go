//go:build !android

package player

import (
	"strings"
	"testing"

	"kari/internal/model"
	"kari/internal/provider"
)

func TestAppendAudioLangArgs(t *testing.T) {
	tests := []struct {
		lang string
		want string
	}{
		{"hi", "--alang=hi,hin,hindi,en,eng"},
		{"hindi", "--alang=hi,hin,hindi,en,eng"},
		{"ja", "--alang=ja,jpn,japanese,en,eng"},
		{"es", "--alang=es,spa,spanish,esla,es-la,en,eng"},
		{"", ""},
	}

	for _, tt := range tests {
		got := appendAudioLangArgs(nil, tt.lang)
		if tt.want == "" {
			if len(got) != 0 {
				t.Errorf("appendAudioLangArgs(nil, %q) = %v, want empty", tt.lang, got)
			}
			continue
		}
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("appendAudioLangArgs(nil, %q) = %v, want %q", tt.lang, got, tt.want)
		}
	}
}

func TestBuildMPVArgsIncludesAlang(t *testing.T) {
	source := provider.MediaSource{
		URL:      "https://example.com/stream.m3u8",
		Language: "hi",
	}
	media := model.ResolvedMedia{
		SeriesTitle: "The Boys",
	}

	args := buildMPVArgs(source, media, "/tmp/sock", nil)

	foundAlang := false
	for _, a := range args {
		if strings.HasPrefix(a, "--alang=") {
			foundAlang = true
			if !strings.Contains(a, "hi") {
				t.Errorf("expected --alang to contain 'hi', got %q", a)
			}
		}
	}
	if !foundAlang {
		t.Errorf("buildMPVArgs missing --alang option: %v", args)
	}
}

func TestDesktopPlayersUseTheirNativeAudioLanguageOptions(t *testing.T) {
	source := provider.MediaSource{URL: "https://example.com/stream.m3u8", Language: "hi"}
	media := model.ResolvedMedia{}

	vlcArgs := buildVLCArgs(source, media)
	if !containsArg(vlcArgs, "--audio-language=hi") {
		t.Errorf("VLC args missing --audio-language: %v", vlcArgs)
	}
	if containsArg(vlcArgs, "--alang=hi") {
		t.Errorf("VLC args contain unsupported --alang: %v", vlcArgs)
	}

	iinaArgs := buildIINAArgs(source, media, "/tmp/iina.sock")
	if !containsArg(iinaArgs, "--alang=hi,hin,hindi,en,eng") {
		t.Errorf("IINA args missing mpv --alang: %v", iinaArgs)
	}
}

func TestBuildIINAArgsUsesSubFilesOption(t *testing.T) {
	source := provider.MediaSource{URL: "https://example.com/stream.m3u8"}
	media := model.ResolvedMedia{
		SeriesTitle: "Sintel",
		Subtitles: []model.SubtitleTrack{
			{Path: "/Users/test/.config/kari/subs/test.srt", Language: "en"},
		},
	}

	args := buildIINAArgs(source, media, "/tmp/iina.sock")

	if !containsArg(args, "--sub-files=/Users/test/.config/kari/subs/test.srt") {
		t.Errorf("IINA args missing --sub-files option: %v", args)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--sub-file=") || strings.HasPrefix(a, "--sub-files-append=") {
			t.Errorf("IINA args contain option IINA ignores via libmpv: %q (full args: %v)", a, args)
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestBuildMPVArgsIncludesIPCServer(t *testing.T) {
	source := provider.MediaSource{
		URL: "https://example.com/stream.m3u8",
	}
	media := model.ResolvedMedia{
		SeriesTitle: "Inception",
	}

	socketPath := "/tmp/test-mpv.sock"
	args := buildMPVArgs(source, media, socketPath, nil)

	foundIPC := false
	for _, a := range args {
		if a == "--input-ipc-server="+socketPath {
			foundIPC = true
			break
		}
	}
	if !foundIPC {
		t.Errorf("buildMPVArgs missing --input-ipc-server option: %v", args)
	}
}

func TestBuildMPVArgsIncludesExtraArgs(t *testing.T) {
	source := provider.MediaSource{
		URL:       "https://example.com/stream.m3u8",
		ExtraArgs: []string{"--demuxer-lavf-o=timeout=5000000"},
	}
	media := model.ResolvedMedia{
		SeriesTitle: "Inception",
	}

	args := buildMPVArgs(source, media, "/tmp/sock", nil)

	foundExtra := false
	for _, a := range args {
		if a == "--demuxer-lavf-o=timeout=5000000" {
			foundExtra = true
			break
		}
	}
	if !foundExtra {
		t.Errorf("buildMPVArgs missing ExtraArgs: %v", args)
	}
}
