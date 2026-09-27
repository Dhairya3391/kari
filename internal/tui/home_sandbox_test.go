package tui

// Package-wide test guard: several update paths call saveSettings,
// which writes ~/.config/kari/settings.json. Point HOME at a throwaway
// directory for the whole test binary so no test (present or future)
// can clobber the developer's real settings file.

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kari-tui-test-home")
	if err != nil {
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	if err := os.Setenv("HOME", dir); err != nil {
		os.Exit(1)
	}
	os.Exit(m.Run())
}
