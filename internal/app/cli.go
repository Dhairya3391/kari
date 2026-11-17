package app

import (
	"fmt"
	"io"
	"strings"
)

// helpText is printed by -h/--help. It intentionally stays brief; the full
// reference is in the man page (man kari).
const helpText = `Usage: kari [OPTIONS] [QUERY]

Stream anime, movies, and TV shows from your terminal.

OPTIONS
  -v, --version   Print version and exit.
  -u, --update    Self-update to the latest release and exit.
  -h, --help      Show this message and exit.

QUERY
  Optional search term. When provided, kari pre-fills the search
  input and executes the search immediately on launch.

KEYS (inside TUI)
  Space           Search        Tab/Shift+Tab   Switch category
  Enter           Select/Play   Esc             Go back
  j/k or ↑/↓      Navigate      h/H             History
  n               Next episode  A               Toggle auto-play
  a               Sub/Dub       s/S             Settings
  d               Download      r               Restart episode
  Ctrl+P          Switch player q/Ctrl+C        Quit

For the full reference including all environment variables and
configuration options, run:

  man kari

Source and issue tracker: https://github.com/Dhairya3391/kari
`

// cliArgs is the parsed command line: the search query plus the flags that
// make the application exit before it starts wiring anything.
type cliArgs struct {
	query   string
	version bool
	update  bool
	help    bool
}

// parseArgs separates early-exit flags from positional query words. Unknown
// flags are kept as query text rather than rejected, so a search term that
// begins with a dash still works.
func parseArgs(argv []string) cliArgs {
	var words []string
	var a cliArgs
	for _, arg := range argv {
		switch arg {
		case "-v", "--version":
			a.version = true
		case "-u", "-U", "--update":
			a.update = true
		case "-h", "--help":
			a.help = true
		default:
			words = append(words, arg)
		}
	}
	a.query = strings.TrimSpace(strings.Join(words, " "))
	return a
}

// handleCLI serves the early-exit flags and reports whether Run should stop.
// The writer and update callback are injected so flag precedence is testable
// without printing to stdout or reaching the network.
func handleCLI(a cliArgs, out io.Writer, update func() error) (bool, error) {
	switch {
	case a.help:
		fmt.Fprint(out, helpText)
		return true, nil
	case a.version:
		fmt.Fprintf(out, "Kari version %s (%s)\n", Version, Commit)
		return true, nil
	case a.update:
		return true, update()
	default:
		return false, nil
	}
}
