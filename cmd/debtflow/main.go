// Command debtflow turns unreviewed coding-agent commits into
// interest-bearing review debt; the command surface lives in internal/cli.
package main

import (
	"os"

	"github.com/SuperMarioYL/debtflow/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
