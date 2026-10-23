package kari

import (
	_ "embed"
)

// ManPage is the raw groff/troff source of docs/kari.1.
//
//go:embed docs/kari.1
var ManPage []byte
