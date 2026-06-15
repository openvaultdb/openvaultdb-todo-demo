// Command todo-backend is the OpenVaultDB to-do demo backend.
//
// It is a thin entry point: it forwards os.Args to internal/cli.Run and exits
// non-zero on error. All command wiring lives in internal/cli.
package main

import (
	"os"

	"github.com/openvaultdb/openvaultdb-todo-demo/internal/cli"
)

func main() {
	if err := cli.Run(os.Args); err != nil {
		os.Exit(1)
	}
}
