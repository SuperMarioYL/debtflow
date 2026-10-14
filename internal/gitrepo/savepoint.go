// Package gitrepo shells out to the local git binary and owns the git-graph
// knowledge behind the ledger: agent-change detection and history walking
// live in agent_changes.go, and this file owns the repo handle plus the
// savepoint lifecycle — recording pre-change refs and restoring their trees.
//
// Git is ground truth; the ledger is derived state that can be rebuilt from
// history, so every fact stored in a DebtEntry originates here.
package gitrepo

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// savepointRefNamespace is where entry savepoints live. A savepoint ref points
// at the commit immediately BEFORE the agent change (the pre-entry tree), so
// restoring it later discards exactly the entry's change.
const savepointRefNamespace = "refs/debtflow/"

// Repo is a git working tree that debtflow operates on.
type Repo struct {
	dir string // repo root, as reported by git rev-parse --show-toplevel
}

// Open verifies that dir is inside a git work tree and returns a Repo rooted
// at the work tree's top level, so commands work from any subdirectory.
func Open(dir string) (*Repo, error) {
	r := &Repo{dir: dir}
	root, err := r.git("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not a git repository: %w", dir, err)
	}
	r.dir = strings.TrimSpace(root)
	return r, nil
}

// Root returns the repo root directory.
func (r *Repo) Root() string { return r.dir }

// SavepointRef names the ref that records the pre-entry tree for an entry id.
func SavepointRef(id string) string { return savepointRefNamespace + id }

// RecordSavepoint creates the savepoint ref for an entry, pointing at
// preCommit — the commit immediately before the agent change. Restoring that
// ref's tree later (debtflow default) discards exactly the entry's change.
func (r *Repo) RecordSavepoint(id, preCommit string) (string, error) {
	ref := SavepointRef(id)
	if _, err := r.git("update-ref", ref, preCommit); err != nil {
		return "", fmt.Errorf("record savepoint %s: %w", ref, err)
	}
	return ref, nil
}

// RestoreTree points the index and working tree at the tree of source — a
// commit-ish or tree-ish. An empty source means git's empty tree, the
// pre-change state of a root-commit entry. Files added on top of the source
// are removed, so the result is exactly the source tree.
func (r *Repo) RestoreTree(source string) error {
	if source == "" {
		var err error
		if source, err = r.emptyTree(); err != nil {
			return err
		}
	}
	if _, err := r.git("restore", "--source="+source, "--staged", "--worktree", ":/"); err != nil {
		return fmt.Errorf("restore tree %s: %w", source, err)
	}
	return nil
}

// emptyTree materializes git's empty tree object in the repo and returns its
// hash, so a root-commit entry (no savepoint) can restore to a pristine tree.
func (r *Repo) emptyTree() (string, error) {
	cmd := exec.Command("git", "mktree")
	cmd.Dir = r.dir
	cmd.Stdin = strings.NewReader("")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git mktree: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// HasUncommittedChanges reports whether tracked files differ between HEAD,
// the index or the working tree. Untracked files are ignored — they are the
// user's own and a tree restore leaves them alone. It guards the destructive
// verbs against wiping uncommitted work.
func (r *Repo) HasUncommittedChanges() (bool, error) {
	out, err := r.git("status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// CommitIfChanged commits the staged state when it differs from HEAD and
// reports whether a commit was made. debtflow's tree restores end here: a
// restore that matches HEAD needs no empty commit.
func (r *Repo) CommitIfChanged(message string) (bool, error) {
	dirty, err := r.HasUncommittedChanges()
	if err != nil {
		return false, err
	}
	if !dirty {
		return false, nil
	}
	if _, err := r.git("commit", "-m", message); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// git runs a git command inside the repo and returns its stdout.
func (r *Repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
