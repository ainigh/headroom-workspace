// Command sysq is the terminal surface of the read-only query engine:
// describe, run, explain, fixtures, audit, and one generated subcommand per
// recipe (sysq disk hogs --root ~/Library).
package main

import (
	"fmt"
	"os"
)

// version is set at build time (-ldflags "-X main.version=..."), as for sysqd.
var version = "dev"

func main() {
	root := newRoot()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "sysq:", err)
		os.Exit(1)
	}
}
