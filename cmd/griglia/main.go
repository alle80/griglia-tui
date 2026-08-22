package main

import (
	"os"

	"github.com/alle80/griglia-tui/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, cli.Options{Version: version}))
}
