package app

// Version and Commit are set at build time via -ldflags (see build.sh),
// which derives Version from git tags/commit count — there's nothing to
// keep in sync here manually. The defaults below are only what a plain
// `go run`/`go build` without those ldflags will show.
var (
	Version = "0.0.0-dev"
	Commit  = "dev"
)
