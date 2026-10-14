// Package ledger owns the review-debt ledger: the DebtEntry schema with its
// appended history events, the append-only JSONL store at
// .debtflow/ledger.jsonl, and the deterministic accrual pass that recomputes
// an open entry's interest from the git graph. Git is ground truth; the
// ledger is derived state that can be rebuilt from history.
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
	"time"

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

// Event kinds appended to an entry's history. Registration records imported,
// the accrual pass records accrued, and the settle verbs record the rest.
const (
	EventImported   = "imported"
	EventAccrued    = "accrued"
	EventPaid       = "paid"
	EventRefinanced = "refinanced"
	EventDefaulted  = "defaulted"
)

// Event is one record of an entry's history: an accrual snapshot or a verb
// operation. Every event freezes the entry's interest as of the moment it was
// recorded, so the history doubles as an interest audit trail.
type Event struct {
	Kind     string    `json:"kind"`             // imported | accrued | paid | refinanced | defaulted
	At       time.Time `json:"at"`               // when the event was recorded
	Interest Interest  `json:"interest"`         // interest snapshot at event time
	Detail   string    `json:"detail,omitempty"` // verb detail, e.g. the bundle a refinanced entry joined
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
	History      []Event  `json:"history,omitempty"` // accrual snapshots + verb operations, in recorded order
}

// RecordEvent appends an event to the entry's history: the kind, the interest
// snapshot as of now, and an optional verb detail.
func (e *Entry) RecordEvent(kind, detail string) {
	e.History = append(e.History, Event{
		Kind:     kind,
		At:       time.Now().UTC(),
		Interest: e.Interest,
		Detail:   detail,
	})
}

// MarkPaid marks a reviewed entry paid and appends the paid event — a status
// change never happens without its history event.
func (e *Entry) MarkPaid() {
	e.Status = StatusPaid
	e.RecordEvent(EventPaid, "")
}

// MarkRefinanced marks an entry refinanced into the bundle with the given id
// and appends the refinanced event naming it.
func (e *Entry) MarkRefinanced(bundleID string) {
	e.Status = StatusRefinanced
	e.RecordEvent(EventRefinanced, "bundled into "+bundleID)
}

// MarkDefaulted marks an entry defaulted after its savepoint tree was
// restored, and appends the defaulted event.
func (e *Entry) MarkDefaulted() {
	e.Status = StatusDefaulted
	e.RecordEvent(EventDefaulted, "")
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
// enter the ledger through Append; Save is how recomputed interest, appended
// history events, and verb status changes are persisted onto the lines
// already there. The ledger stays append-only in spirit: entries are never
// deleted or reordered, and every state change is recorded inside the entry's
// history as an appended event — the interest and status fields only carry
// the latest state those events describe. A write goes through a temp file
// and a rename so a crash cannot tear a line in half, and a pass that changed
// nothing leaves the file untouched: reading the board does not rewrite the
// ledger.
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
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, buf.Bytes()) {
		return nil
	}
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
// are skipped — their interest stays frozen at the last accrual. A snapshot
// that moves an entry's interest is appended to its history as an accrued
// event, so the trail records when interest moved and what it moved to;
// unchanged reads append nothing. Entries are updated in place; persist them
// with Save.
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
		e.accrueTo(Interest{
			DependentCommits: len(stacked),
			TouchedFiles:     len(e.Files),
			BlastRadius:      len(beyond),
		})
	}
	return nil
}

// accrueTo moves the entry's interest to a freshly computed snapshot,
// appending an accrued event only when the snapshot actually moved — a read
// that recomputed the same numbers must not grow the history.
func (e *Entry) accrueTo(next Interest) {
	if next == e.Interest {
		return
	}
	e.Interest = next
	e.RecordEvent(EventAccrued, "")
}
