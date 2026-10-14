package cli

// The settle verbs operate the ledger: paydown clears a reviewed entry,
// refinance bundles two entries into one review, and default restores the
// entry's savepoint tree, discarding the change.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/SuperMarioYL/debtflow/internal/gitrepo"
	"github.com/SuperMarioYL/debtflow/internal/ledger"
	"github.com/spf13/cobra"
)

// NewPaydownCmd builds `debtflow paydown <id>`: mark a reviewed entry as
// paid. Its interest freezes at the current accrual.
func NewPaydownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "paydown <id>",
		Short: "Mark a reviewed entry as paid",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPaydown(cmd.OutOrStdout(), args[0])
		},
	}
}

// NewRefinanceCmd builds `debtflow refinance <id1> <id2>`: bundle two open
// entries into one review. Both are marked refinanced and a fresh open entry
// covers their combined change, anchored at the older entry's savepoint.
func NewRefinanceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refinance <id1> <id2>",
		Short: "Bundle entries into a single refinanced review",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRefinance(cmd.OutOrStdout(), args[0], args[1])
		},
	}
}

// NewDefaultCmd builds `debtflow default <id>`: restore the entry's recorded
// pre-change tree and commit it. This is a savepoint-tree restore, not a
// range revert — the tree is reset wholesale, so anything stacked on the
// entry is discarded as well.
func NewDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default <id>",
		Short: "Restore the entry's savepoint tree and mark it defaulted",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDefault(cmd.OutOrStdout(), args[0])
		},
	}
}

// readAccruedLedger loads the ledger with freshly accrued interest — every
// read of the ledger is an accrual pass, so the verbs settle against current
// numbers.
func readAccruedLedger() (*gitrepo.Repo, []ledger.Entry, error) {
	repo, err := gitrepo.Open(".")
	if err != nil {
		return nil, nil, err
	}
	entries, err := ledger.Load(repo.Root())
	if err != nil {
		return nil, nil, err
	}
	if err := ledger.Accrue(repo, entries); err != nil {
		return nil, nil, err
	}
	return repo, entries, nil
}

// findEntry locates an entry by its ledger id.
func findEntry(entries []ledger.Entry, id string) (int, error) {
	for i := range entries {
		if entries[i].ID == id {
			return i, nil
		}
	}
	return 0, fmt.Errorf("entry %s not found in the ledger — run `debtflow init` to import agent commits", id)
}

func runPaydown(out io.Writer, id string) error {
	repo, entries, err := readAccruedLedger()
	if err != nil {
		return err
	}
	idx, err := findEntry(entries, id)
	if err != nil {
		return err
	}
	e := &entries[idx]
	if e.Status != ledger.StatusOpen {
		return fmt.Errorf("entry %s is %s — only open entries can be paid down", id, e.Status)
	}
	e.Status = ledger.StatusPaid
	if err := ledger.Save(repo.Root(), entries); err != nil {
		return err
	}
	fmt.Fprintf(out, "Entry %s marked paid — review debt cleared.\n", id)
	return nil
}

func runRefinance(out io.Writer, id1, id2 string) error {
	if id1 == id2 {
		return fmt.Errorf("refinance needs two different entries, got %s twice", id1)
	}
	repo, entries, err := readAccruedLedger()
	if err != nil {
		return err
	}
	i, err := findEntry(entries, id1)
	if err != nil {
		return err
	}
	j, err := findEntry(entries, id2)
	if err != nil {
		return err
	}
	first, second := &entries[i], &entries[j]
	for _, e := range []*ledger.Entry{first, second} {
		if e.Status != ledger.StatusOpen {
			return fmt.Errorf("entry %s is %s — only open entries can be refinanced", e.ID, e.Status)
		}
	}

	// Bundle along the first-parent line: the older entry anchors the range
	// and lends its savepoint and age, the newer one sets the tip the
	// bundle's interest accrues from.
	older, newer, err := orderAlongLine(repo, first, second)
	if err != nil {
		return err
	}
	bundle := ledger.Entry{
		ID:           bundleID(older.ID, newer.ID),
		Change:       bundleChange(older, newer),
		SavepointRef: older.SavepointRef,
		Status:       ledger.StatusOpen,
		CommitTime:   older.CommitTime,
		Files:        unionFiles(older.Files, newer.Files),
	}
	// Accrue works on a copy inside the slice; read the interest back.
	accrueBundle := []ledger.Entry{bundle}
	if err := ledger.Accrue(repo, accrueBundle); err != nil {
		return err
	}
	bundle = accrueBundle[0]

	older.Status = ledger.StatusRefinanced
	newer.Status = ledger.StatusRefinanced
	// The bundle enters the append-only log through Append and joins the
	// in-memory entries so the Save below persists it alongside the
	// refinanced sources.
	entries = append(entries, bundle)
	if err := ledger.Append(repo.Root(), bundle); err != nil {
		return err
	}
	if err := ledger.Save(repo.Root(), entries); err != nil {
		return err
	}
	fmt.Fprintf(out, "Bundled %s and %s into %s — one review now covers both changes.\n", older.ID, newer.ID, bundle.ID)
	return nil
}

func runDefault(out io.Writer, id string) error {
	repo, entries, err := readAccruedLedger()
	if err != nil {
		return err
	}
	idx, err := findEntry(entries, id)
	if err != nil {
		return err
	}
	e := &entries[idx]
	if e.Status != ledger.StatusOpen {
		return fmt.Errorf("entry %s is %s — only open entries can be defaulted", id, e.Status)
	}
	dirty, err := repo.HasUncommittedChanges()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("working tree has uncommitted changes — commit or stash them before defaulting %s", id)
	}

	source := e.SavepointRef
	sourceLabel := source
	if source == "" {
		sourceLabel = "the empty tree" // root-commit entry: the pre-change state is pristine
	}
	if err := repo.RestoreTree(source); err != nil {
		return err
	}
	message := fmt.Sprintf("debtflow: default entry %s\n\nRestore the working tree to %s, the entry's recorded pre-change savepoint.", id, sourceLabel)
	committed, err := repo.CommitIfChanged(message)
	if err != nil {
		return err
	}

	e.Status = ledger.StatusDefaulted
	if err := ledger.Save(repo.Root(), entries); err != nil {
		return err
	}
	if committed {
		fmt.Fprintf(out, "Defaulted %s — working tree restored to %s and committed.\n", id, sourceLabel)
	} else {
		fmt.Fprintf(out, "Defaulted %s — working tree already matched %s, nothing to commit.\n", id, sourceLabel)
	}
	return nil
}

// orderAlongLine reports which of two entries comes first on the first-parent
// line: the bundle is anchored at the older entry and accrues from the newer.
func orderAlongLine(repo *gitrepo.Repo, a, b *ledger.Entry) (older, newer *ledger.Entry, err error) {
	tipA, tipB := ledger.ChangeTip(a.Change), ledger.ChangeTip(b.Change)
	base, err := repo.MergeBase(tipA, tipB)
	if err != nil {
		return nil, nil, fmt.Errorf("entries %s and %s are not on a common first-parent line", a.ID, b.ID)
	}
	switch base {
	case tipA:
		return a, b, nil // b descends from a
	case tipB:
		return b, a, nil // a descends from b
	default:
		return nil, nil, fmt.Errorf("entries %s and %s diverged at %s — refinance bundles entries along one first-parent line", a.ID, b.ID, base)
	}
}

// bundleChange spans the bundled review: from the older entry's recorded
// range start to the newer entry's commit. For a root-commit entry there is
// no range start, so the bundle's range is just the newer tip.
func bundleChange(older, newer *ledger.Entry) string {
	if parent, _, ok := strings.Cut(older.Change, ".."); ok {
		return parent + ".." + ledger.ChangeTip(newer.Change)
	}
	return ledger.ChangeTip(newer.Change)
}

// bundleID derives a stable id for a refinanced bundle from the two source
// entry ids, hashed to the same 12-hex width as the commit-derived ids.
func bundleID(a, b string) string {
	sum := sha256.Sum256([]byte("refinance:" + a + ":" + b))
	return hex.EncodeToString(sum[:])[:12]
}

// unionFiles merges two entries' file lists, keeping first-seen order.
func unionFiles(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var union []string
	for _, list := range [][]string{a, b} {
		for _, f := range list {
			if !seen[f] {
				seen[f] = true
				union = append(union, f)
			}
		}
	}
	return union
}
