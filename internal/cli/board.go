// Package cli implements the debtflow commands. m1 owns registering debt:
// init imports unreviewed agent commits as ledger entries backed by
// savepoint refs, and board prints the ledger table. The settle verbs
// (paydown, refinance, default) land in a later stage.
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SuperMarioYL/debtflow/internal/gitrepo"
	"github.com/SuperMarioYL/debtflow/internal/render"
	"github.com/spf13/cobra"
)

// The ledger is an append-only JSONL file at .debtflow/ledger.jsonl inside
// the tracked repo — one Entry per line, derived state over git history.
// m1 keeps the entry model and store here; later stages lift accrual and the
// settle verbs onto the same schema.
const (
	ledgerDir  = ".debtflow"
	ledgerFile = "ledger.jsonl"
)

// Entry statuses. m1 only ever writes StatusOpen; the settle verbs own the
// rest.
const (
	StatusOpen       = "open"
	StatusPaid       = "paid"
	StatusRefinanced = "refinanced"
	StatusDefaulted  = "defaulted"
)

// Interest is the accrual snapshot stored with an entry. Registration starts
// the clock: touched files come from the entry's own diff, while dependent
// commits and blast radius stay at zero until the m2 accrual pass recounts
// them from the git graph on every read.
type Interest struct {
	DependentCommits int `json:"dependent_commits"`
	TouchedFiles     int `json:"touched_files"`
	BlastRadius      int `json:"blast_radius"`
}

// Entry is one line of the ledger: an unreviewed agent change recorded as
// debt.
type Entry struct {
	ID           string   `json:"id"`            // derived from the agent commit (gitrepo.EntryID)
	Change       string   `json:"change"`        // agent-authored commit range, "<parent>..<hash>"
	SavepointRef string   `json:"savepoint_ref"` // refs/debtflow/<id>, the pre-change tree; empty for a root commit
	Status       string   `json:"status"`        // open | paid | refinanced | defaulted
	CommitTime   string   `json:"commit_time"`   // when the change landed (RFC3339); the age column reads this
	Files        []string `json:"files"`         // files in the entry's own diff
	Interest     Interest `json:"interest"`
}

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
// first. m1 keeps the board read-only — auto-importing commits that landed
// after the last init arrives with the m2 accrual pass.
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

	entries, err := loadLedger(repo.Root())
	if err != nil {
		return err
	}
	tracked := make(map[string]bool, len(entries))
	for _, e := range entries {
		tracked[e.ID] = true
	}

	commits, err := repo.AgentCommits(patterns)
	if err != nil {
		return err
	}

	imported, already := 0, 0
	for _, commit := range commits {
		id := gitrepo.EntryID(commit.Hash)
		if tracked[id] {
			already++
			continue
		}

		ref := ""
		if commit.Parent != "" {
			if ref, err = repo.RecordSavepoint(id, commit.Parent); err != nil {
				return err
			}
		}
		change := commit.Hash
		if commit.Parent != "" {
			change = commit.Parent + ".." + commit.Hash
		}
		entry := Entry{
			ID:           id,
			Change:       change,
			SavepointRef: ref,
			Status:       StatusOpen,
			CommitTime:   commit.Time.Format(time.RFC3339),
			Files:        commit.Files,
			Interest:     Interest{TouchedFiles: len(commit.Files)},
		}
		if err := appendLedger(repo.Root(), entry); err != nil {
			return err
		}
		imported++
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
	entries, err := loadLedger(repo.Root())
	if err != nil {
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

// loadLedger reads the ledger file. A missing file is an empty ledger, not an
// error — debtflow init creates it.
func loadLedger(root string) ([]Entry, error) {
	f, err := os.Open(filepath.Join(root, ledgerDir, ledgerFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("corrupt ledger line: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, sc.Err()
}

// appendLedger appends one entry as a single JSON line.
func appendLedger(root string, e Entry) error {
	f, err := os.OpenFile(filepath.Join(root, ledgerDir, ledgerFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}
