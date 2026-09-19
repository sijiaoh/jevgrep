// Command jevgrep greps lines by meaning.
package main

import (
	"os"

	"github.com/sijiaoh/jevgrep/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
