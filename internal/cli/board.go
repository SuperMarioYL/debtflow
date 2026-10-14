// Package cli implements the debtflow commands: init and board register and
// display debt (board auto-imports new agent commits and accrues interest on
// every read), and settle.go owns the three settle verbs.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SuperMarioYL/debtflow/internal/gitrepo"
	"github.com/SuperMarioYL/debtflow/internal/ledger"
	"github.com/SuperMarioYL/debtflow/internal/render"
	"github.com/spf13/cobra"
)

// The ledger directory inside the tracked repo. The entry model and the JSONL
// store itself live in internal/ledger.
const ledgerDir = ".debtflow"

// NewInitCmd builds `debtflow init`: scan the repo for agent commits that are
// not in the ledger yet, create an entry plus savepoint ref for each, and
// print the count.
func NewInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Import unreviewed agent commits as ledger entries with savepoint refs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			patterns, _ := cmd.Flags().GetStringSlice("agents")
			return runInit(cmd.OutOrStdout(), patterns)
		},
	}
	cmd.Flags().StringSlice("agents", gitrepo.DefaultAgentPatterns,
		"comma-separated author / Co-Authored-By patterns marking agent commits")
	return cmd
}

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

func runInit(out io.Writer, patterns []string) error {
	repo, err := gitrepo.Open(".")
	if err != nil {
		return err
	}
	if err := ensureLedger(repo.Root()); err != nil {
		return err
	}

	entries, err := ledger.Load(repo.Root())
	if err != nil {
		return err
	}
	entries, imported, already, err := importAgentCommits(repo, entries, patterns)
	if err != nil {
		return err
	}
	if err := ledger.Accrue(repo, entries); err != nil {
		return err
	}
	if err := ledger.Save(repo.Root(), entries); err != nil {
		return err
	}

	switch {
	case imported > 0:
		fmt.Fprintf(out, "Imported %d agent commit(s) as debt entries (%d already tracked).\n", imported, already)
	case already > 0:
		fmt.Fprintf(out, "No new agent commits — %d already tracked in the ledger.\n", already)
	default:
		fmt.Fprintln(out, "No agent-authored commits found on the first-parent line — nothing to record.")
	}
	return nil
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

// importAgentCommits appends a ledger entry — with a savepoint ref pointing
// at the pre-change commit — for every agent commit not tracked yet, and
// reports the counts. The returned slice carries the new entries so the
// caller's accrual pass covers them; the file gets them through Append and
// the caller's Save.
func importAgentCommits(repo *gitrepo.Repo, entries []ledger.Entry, patterns []string) (_ []ledger.Entry, imported, already int, err error) {
	tracked := make(map[string]bool, len(entries))
	for _, e := range entries {
		tracked[e.ID] = true
	}

	commits, err := repo.AgentCommits(patterns)
	if err != nil {
		return entries, 0, 0, err
	}

	for _, commit := range commits {
		id := gitrepo.EntryID(commit.Hash)
		if tracked[id] {
			already++
			continue
		}

		ref := ""
		if commit.Parent != "" {
			if ref, err = repo.RecordSavepoint(id, commit.Parent); err != nil {
				return entries, imported, already, err
			}
		}
		change := commit.Hash
		if commit.Parent != "" {
			change = commit.Parent + ".." + commit.Hash
		}
		entry := ledger.Entry{
			ID:           id,
			Change:       change,
			SavepointRef: ref,
			Status:       ledger.StatusOpen,
			CommitTime:   commit.Time.Format(time.RFC3339),
			Files:        commit.Files,
			Interest:     ledger.Interest{TouchedFiles: len(commit.Files)},
		}
		// Registration is the first event of the entry's history.
		entry.RecordEvent(ledger.EventImported, "")
		if err := ledger.Append(repo.Root(), entry); err != nil {
			return entries, imported, already, err
		}
		entries = append(entries, entry)
		imported++
	}
	return entries, imported, already, nil
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

// ensureLedger creates the .debtflow directory and makes sure the repo's
// .gitignore ignores it — the ledger is local derived state, never committed.
func ensureLedger(root string) error {
	if err := os.MkdirAll(filepath.Join(root, ledgerDir), 0o755); err != nil {
		return err
	}
	return ensureIgnored(filepath.Join(root, ".gitignore"))
}

func ensureIgnored(gitignore string) error {
	data, err := os.ReadFile(gitignore)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed == ledgerDir || trimmed == "/"+ledgerDir {
			return nil
		}
	}

	f, err := os.OpenFile(gitignore, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		if _, err := f.Write([]byte("\n")); err != nil {
			return err
		}
	}
	_, err = f.Write([]byte("# debtflow\n" + ledgerDir + "/\n"))
	return err
}
