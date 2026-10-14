// Package cli implements the debtflow commands: root.go declares the command
// surface and Execute, init.go and board.go register and display debt (the
// board auto-imports new agent commits and accrues interest on every read),
// and settle.go owns the three settle verbs.
package cli

import (
	"github.com/spf13/cobra"
)

// version is the debtflow build version. It defaults to the repo VERSION file
// value; the release workflow injects the tagged version via -ldflags
// -X github.com/SuperMarioYL/debtflow/internal/cli.version. Keep the default
// in sync with the VERSION file — internal/cli/version_test.go enforces it.
var version = "0.1.0"

// Execute builds the debtflow command surface and runs it; the returned
// error is for main to exit on.
func Execute() error {
	return newRootCmd().Execute()
}

// newRootCmd declares the debtflow command surface. Registering debt (init)
// and reading the board live in internal/cli/init.go and board.go, the settle
// verbs in internal/cli/settle.go; the ledger and its accrual pass live in
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
		NewInitCmd(),
		NewBoardCmd(),
		NewPaydownCmd(),
		NewRefinanceCmd(),
		NewDefaultCmd(),
	)
	return root
}
