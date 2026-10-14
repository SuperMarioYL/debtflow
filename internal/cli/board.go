// Reading the board: `debtflow board` prints the ledger table, oldest debt
// first. Reading the board is the accrual pass — new agent commits are
// auto-imported through the init.go import pass and every open entry's
// interest is recomputed from the git graph, so interest rises between runs
// without a separate command.

package cli

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/SuperMarioYL/debtflow/internal/gitrepo"
	"github.com/SuperMarioYL/debtflow/internal/ledger"
	"github.com/SuperMarioYL/debtflow/internal/render"
	"github.com/spf13/cobra"
)

// NewBoardCmd builds `debtflow board`: print the ledger table, oldest debt
// first. Reading the board is the accrual pass — new agent commits are
// auto-imported and every open entry's interest is recomputed from the git
// graph, so interest rises between runs without a separate command.
func NewBoardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "board",
		Short: "Print the ledger table with each entry's accrued interest",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBoard(cmd.OutOrStdout())
		},
	}
}

func runBoard(out io.Writer) error {
	repo, err := gitrepo.Open(".")
	if err != nil {
		return err
	}
	if !ledger.Exists(repo.Root()) {
		fmt.Fprintln(out, "Ledger is clean — no debt entries. Run `debtflow init` to import agent commits.")
		return nil
	}
	entries, err := ledger.Load(repo.Root())
	if err != nil {
		return err
	}
	entries, imported, _, err := importAgentCommits(repo, entries, gitrepo.DefaultAgentPatterns)
	if err != nil {
		return err
	}
	if imported > 0 {
		fmt.Fprintf(out, "Auto-imported %d new agent commit(s) as debt entries.\n", imported)
	}
	if err := ledger.Accrue(repo, entries); err != nil {
		return err
	}
	if err := ledger.Save(repo.Root(), entries); err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "Ledger is clean — no debt entries. Run `debtflow init` to import agent commits.")
		return nil
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].CommitTime < entries[j].CommitTime // oldest debt first
	})

	now := time.Now()
	rows := make([]render.BoardRow, 0, len(entries))
	for _, e := range entries {
		landed, err := time.Parse(time.RFC3339, e.CommitTime)
		if err != nil {
			return fmt.Errorf("entry %s: bad commit_time %q: %w", e.ID, e.CommitTime, err)
		}
		rows = append(rows, render.BoardRow{
			ID:               e.ID,
			Age:              formatAge(now.Sub(landed)),
			DependentCommits: e.Interest.DependentCommits,
			TouchedFiles:     e.Interest.TouchedFiles,
			BlastRadius:      e.Interest.BlastRadius,
			Status:           e.Status,
		})
	}
	fmt.Fprintln(out, render.Board(rows))
	return nil
}

// formatAge renders a debt's age compactly: hours under a day, days beyond.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return "<1h"
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
