package main

import (
	"os"

	"github.com/alle80/griglia-tui/internal/buildinfo"
	"github.com/alle80/griglia-tui/internal/cli"
)

// Release builds inject these with:
//
//	go build -ldflags "-X main.version=... -X main.commit=... -X main.date=..."
//
// Binaries built without ldflags (go install module@version, plain go build)
// fall back to the metadata the Go toolchain embeds in the binary; Git is
// never required at runtime.
var (
	version = buildinfo.DefaultVersion
	commit  = buildinfo.DefaultCommit
	date    = buildinfo.DefaultDate
)

func main() {
	v, c, d := buildinfo.Resolve(version, commit, date)
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, cli.Options{Version: v, Commit: c, BuildDate: d}))
}
