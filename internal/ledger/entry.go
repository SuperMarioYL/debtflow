// Package ledger owns the review-debt ledger: the DebtEntry schema with its
// appended history events, the append-only JSONL store at
// .debtflow/ledger.jsonl, and the deterministic accrual pass that recomputes
// an open entry's interest from the git graph. Git is ground truth; the
// ledger is derived state that can be rebuilt from history.
package ledger

import (
	"strings"
	"time"
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

// ChangeTip returns the commit at the head of an entry's change range: the
// hash after "..", or the whole value for a root-commit entry.
func ChangeTip(change string) string {
	if _, tip, ok := strings.Cut(change, ".."); ok {
		return tip
	}
	return change
}
