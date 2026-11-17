package app

import (
	"context"
	"fmt"
	"os"

	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/settings"
	"kari/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

// Version and Commit are set at build time via -ldflags (see build.sh),
// which derives Version from git tags/commit count — there's nothing to
// keep in sync here manually. The defaults below are only what a plain
// `go run`/`go build` without those ldflags will show.
var (
	Version = "0.0.0-dev"
	Commit  = "dev"
)

// Run boots the application: it serves the CLI early exits, then loads
// configuration, wires the component graph, and hands control to bubbletea.
func Run() error {
	a := parseArgs(os.Args[1:])
	if handled, err := handleCLI(a, os.Stdout, Update); handled {
		return err
	}
	return run(a.query)
}

// run starts a normal interactive session with an optional pre-filled query.
func run(query string) error {
	logging.Info("starting app", "query", query)
	if p := logging.Path(); p != "" {
		logging.Info("log file ready", "path", p)
	}
	startHousekeeping()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	saved := settings.Load()

	configDir := resolveConfigDir()
	migrateConfigDir(configDir)

	deps, err := newDependencies(cfg, saved, configDir)
	if err != nil {
		return err
	}
	defer deps.close()

	m := newTUIModel(context.Background(), deps, query, saved)
	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithFilter(tui.FilterScrollInput),
	)
	_, err = p.Run()
	if err != nil {
		logging.Error("program exited with error", "err", err)
	}
	return err
}
