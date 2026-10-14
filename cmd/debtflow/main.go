// Command debtflow turns unreviewed coding-agent commits into
// interest-bearing review debt. Every agent-authored change in a git repo
// becomes a ledger entry backed by a savepoint ref; as other commits stack on
// an unreviewed entry it accrues measurable interest — dependent commits,
// touched files, blast radius — and the debt is settled with three verbs:
// paydown (review it now), refinance (bundle entries into one review) and
// default (restore the savepoint tree, discarding the change).
//
// Interest is recomputed from git history on every read: deterministic
// git-graph math, no model in the loop.
//
// Usage:
//
//	debtflow init                  import unreviewed agent commits as entries
//	debtflow board                 print the ledger with accrued interest
//	debtflow paydown <id>          mark a reviewed entry as paid
//	debtflow refinance <id1> <id2> bundle entries into a single review
//	debtflow default <id>          restore the savepoint tree, discard the change
package main

import (
	"os"

	"github.com/SuperMarioYL/debtflow/internal/cli"
	"github.com/spf13/cobra"
)

// version is the debtflow build version. It defaults to the repo VERSION file
// value; the release workflow injects the tagged version via -ldflags
// -X main.version. Keep the default in sync with the VERSION file.
var version = "0.1.0"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// newRootCmd declares the debtflow command surface. Registering debt (init)
// and reading the board live in internal/cli/board.go, the settle verbs in
// internal/cli/settle.go; the ledger and its accrual pass live in
// internal/ledger.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "debtflow",
		Short: "Track unreviewed coding-agent changes as interest-bearing review debt",
		Long: `debtflow records every unreviewed coding-agent commit in a git repo as a
ledger entry backed by a savepoint ref, accrues measurable interest as other
commits stack on it — dependent commits, touched files, blast radius — and
settles the debt with three verbs: paydown, refinance, default.

Interest is recomputed from git history on every read: deterministic
git-graph math, no model in the loop.`,
		Version:      version,
		SilenceUsage: true,
	}
	root.SetVersionTemplate("debtflow {{.Version}}\n")
	root.AddCommand(
		cli.NewInitCmd(),
		cli.NewBoardCmd(),
		cli.NewPaydownCmd(),
		cli.NewRefinanceCmd(),
		cli.NewDefaultCmd(),
	)
	return root
}
