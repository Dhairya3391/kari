package app

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want cliArgs
	}{
		{name: "empty", argv: nil, want: cliArgs{}},
		{name: "single query", argv: []string{"naruto"}, want: cliArgs{query: "naruto"}},
		{name: "multi word query", argv: []string{"one piece", "episode", "5"}, want: cliArgs{query: "one piece episode 5"}},
		{name: "trims query", argv: []string{"  naruto  "}, want: cliArgs{query: "naruto"}},
		{name: "flags removed mid query", argv: []string{"a", "-v", "b", "--update", "-h"}, want: cliArgs{query: "a b", version: true, update: true, help: true}},
		{name: "update aliases", argv: []string{"-u", "-U"}, want: cliArgs{update: true}},
		{name: "version alias", argv: []string{"--version"}, want: cliArgs{version: true}},
		{name: "help aliases", argv: []string{"-h", "--help"}, want: cliArgs{help: true}},
		{name: "unknown flag stays in query", argv: []string{"-x"}, want: cliArgs{query: "-x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseArgs(tt.argv); got != tt.want {
				t.Errorf("parseArgs(%q) = %+v, want %+v", tt.argv, got, tt.want)
			}
		})
	}
}

func TestHandleCLI(t *testing.T) {
	updateErr := errors.New("update failed")
	versionOut := fmt.Sprintf("Kari version %s (%s)\n", Version, Commit)
	tests := []struct {
		name        string
		args        cliArgs
		wantHandled bool
		wantOut     string
		wantUpdate  bool
		updateErr   error
		wantErr     error
	}{
		{name: "ordinary query", args: cliArgs{query: "naruto"}},
		{name: "version", args: cliArgs{version: true, query: "naruto"}, wantHandled: true, wantOut: versionOut},
		{name: "help", args: cliArgs{help: true}, wantHandled: true, wantOut: helpText},
		{name: "help wins over version and update", args: cliArgs{help: true, version: true, update: true}, wantHandled: true, wantOut: helpText},
		{name: "version wins over update", args: cliArgs{version: true, update: true}, wantHandled: true, wantOut: versionOut},
		{name: "update dispatched", args: cliArgs{update: true, query: "naruto"}, wantHandled: true, wantUpdate: true},
		{name: "update error surfaced", args: cliArgs{update: true}, wantHandled: true, wantUpdate: true, updateErr: updateErr, wantErr: updateErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			called := false
			handled, err := handleCLI(tt.args, &out, func() error {
				called = true
				return tt.updateErr
			})
			if handled != tt.wantHandled {
				t.Errorf("handled = %v, want %v", handled, tt.wantHandled)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if out.String() != tt.wantOut {
				t.Errorf("output = %q, want %q", out.String(), tt.wantOut)
			}
			if called != tt.wantUpdate {
				t.Errorf("update called = %v, want %v", called, tt.wantUpdate)
			}
		})
	}
}
