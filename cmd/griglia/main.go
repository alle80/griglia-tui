package main

import (
	"os"

	"github.com/alle80/griglia-tui/internal/cli"
)

// Release builds inject these with:
//
//	go build -ldflags "-X main.version=... -X main.commit=... -X main.date=..."
//
// Local development builds keep the defaults; Git is never required at runtime.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, cli.Options{Version: version, Commit: commit, BuildDate: date}))
}
