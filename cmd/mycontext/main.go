// Command mycontext is the deterministic local core of the personal operations
// context system. Every invocation is short-lived: it opens only what it
// needs, performs one operation and exits.
package main

import (
	"os"

	"github.com/scottzx/mycontext/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
