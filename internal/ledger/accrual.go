// Accrual: interest is recomputed from the git graph on every read, so the
// board and the settle verbs always settle against current numbers.

package ledger

import (
	"fmt"

	"github.com/SuperMarioYL/debtflow/internal/gitrepo"
)

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
