package app

// Version is set at build time via -ldflags.
// Scheme:
//   Tag v1.0.0       → "1.0.0"        (exact release)
//   Commits after tag → "1.0.3"        (patch auto-increments)
//   No tags           → "1.0.42"       (commit count as patch)
//   Dirty tree        → "1.0.0-dirty"  (uncommitted changes)
//   go run/build      → "0.0.0-dev"    (no ldflags injected)
var (
	Version = "0.0.0-dev"
	Commit  = ""
	Date    = ""
)
