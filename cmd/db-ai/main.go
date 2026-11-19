// Command db-ai exposes existing SQLite databases as a virtual filesystem.
//
// This is the wiring layer: the CLI contract, dispatch and exit codes live in
// internal/cli.
package main

import (
	"os"

	"dbvfs/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
