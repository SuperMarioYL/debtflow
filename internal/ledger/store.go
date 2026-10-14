// Package ledger owns the review-debt ledger: the DebtEntry schema, the
// append-only JSONL store at .debtflow/ledger.jsonl, and the deterministic
// accrual pass that recomputes an open entry's interest from the git graph.
// Git is ground truth; the ledger is derived state that can be rebuilt from
// history.
package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SuperMarioYL/debtflow/internal/gitrepo"
)

// The ledger lives at .debtflow/ledger.jsonl inside the tracked repo — one
// Entry per line, derived state over git history. It is local state, never
// committed.
const (
	ledgerDir  = ".debtflow"
	ledgerFile = "ledger.jsonl"
)

// Entry statuses. Registration writes StatusOpen; the settle verbs own the
// rest. A settled entry's interest freezes at its last accrual.
const (
	StatusOpen       = "open"
	StatusPaid       = "paid"
	StatusRefinanced = "refinanced"
	StatusDefaulted  = "defaulted"
)

// Interest is the accrual snapshot stored with an entry. Registration starts
// the clock: touched files come from the entry's own diff, while dependent
// commits and blast radius are recounted from the git graph on every read.
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

// Load reads the ledger file. A missing file is an empty ledger, not an
// error — debtflow init creates it.
func Load(root string) ([]Entry, error) {
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

// Append appends one entry as a single JSON line.
func Append(root string, e Entry) error {
	if err := os.MkdirAll(filepath.Join(root, ledgerDir), 0o755); err != nil {
		return err
	}
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

// Save rewrites the ledger file with the given entries. New entries always
// enter the ledger through Append; Save is how recomputed interest and verb
// status changes are persisted onto the lines already there. The write goes
// through a temp file and a rename so a crash cannot tear a line in half.
func Save(root string, entries []Entry) error {
	var buf bytes.Buffer
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	path := filepath.Join(root, ledgerDir, ledgerFile)
	if err := os.MkdirAll(filepath.Join(root, ledgerDir), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Exists reports whether the repo already has a ledger file — debtflow init
// has run. The board only auto-imports on top of an existing ledger; on a
// repo that never opted in it stays a pure read.
func Exists(root string) bool {
	_, err := os.Stat(filepath.Join(root, ledgerDir, ledgerFile))
	return err == nil
}

// ChangeTip returns the commit at the head of an entry's change range: the
// hash after "..", or the whole value for a root-commit entry.
func ChangeTip(change string) string {
	if _, tip, ok := strings.Cut(change, ".."); ok {
		return tip
	}
	return change
}

// Accrue recomputes the interest of every open entry from the git graph:
// dependent commits are the commits stacked on the entry's change along the
// first-parent line up to HEAD, anchored at the merge base so commits folded
// in by side-branch merges do not count; the blast radius is the distinct
// files those commits touch beyond the entry's own files. Settled entries
// are skipped — their interest stays frozen at the last accrual. Entries are
// updated in place; persist them with Save.
func Accrue(repo *gitrepo.Repo, entries []Entry) error {
	for i := range entries {
		e := &entries[i]
		if e.Status != StatusOpen {
			continue
		}
		stacked, err := repo.StackedCommits(ChangeTip(e.Change))
		if err != nil {
			return fmt.Errorf("entry %s: %w", e.ID, err)
		}
		own := make(map[string]bool, len(e.Files))
		for _, f := range e.Files {
			own[f] = true
		}
		beyond := make(map[string]bool)
		for _, c := range stacked {
			for _, f := range c.Files {
				if !own[f] {
					beyond[f] = true
				}
			}
		}
		e.Interest = Interest{
			DependentCommits: len(stacked),
			TouchedFiles:     len(e.Files),
			BlastRadius:      len(beyond),
		}
	}
	return nil
}
