// Package gitrepo shells out to the local git binary and owns the git-graph
// knowledge behind the ledger: walking a repo's first-parent history,
// detecting agent-authored commits, recording savepoint refs, and restoring
// savepoint trees.
//
// Git is ground truth; the ledger is derived state that can be rebuilt from
// history, so every fact stored in a DebtEntry originates here.
package gitrepo

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultAgentPatterns are the author / Co-Authored-By substrings that mark a
// commit as agent-authored. Matching is case-insensitive.
var DefaultAgentPatterns = []string{"claude", "codex", "cursor"}

// savepointRefNamespace is where entry savepoints live. A savepoint ref points
// at the commit immediately BEFORE the agent change (the pre-entry tree), so
// restoring it later discards exactly the entry's change.
const savepointRefNamespace = "refs/debtflow/"

// idLen is the number of commit-hash hex characters used as an entry id. It
// keeps ref names short while making collisions practically impossible.
const idLen = 12

// AgentCommit is one agent-authored commit on the repo's first-parent line.
type AgentCommit struct {
	Hash    string    // full commit hash
	Parent  string    // first-parent hash, empty for a root commit
	Subject string    // commit subject line
	Time    time.Time // committer date, the moment the change landed
	Files   []string  // files changed relative to the first parent
}

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

// AgentCommits walks the first-parent line from HEAD and returns the
// agent-authored commits, newest first. A commit counts as agent-authored
// when its author name/email or any Co-Authored-By trailer matches one of the
// patterns (case-insensitive substrings). Stacking and merge-base anchoring
// are first-parent mechanics, so side-branch commits are intentionally
// invisible here.
func (r *Repo) AgentCommits(patterns []string) ([]AgentCommit, error) {
	if _, err := r.git("rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return nil, nil // repo without any commit yet: nothing to walk
	}
	out, err := r.git("log", "--first-parent",
		"--format=%H%x1f%P%x1f%cI%x1f%an%x1f%ae%x1f%s%x1f%(trailers:only,unfold)%x1e")
	if err != nil {
		return nil, err
	}

	var commits []AgentCommit
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		f := strings.Split(record, "\x1f")
		if len(f) != 7 {
			return nil, fmt.Errorf("unexpected git log record: %q", record)
		}
		if !isAgentCommit(f[3], f[4], f[6], patterns) {
			continue
		}
		landed, err := time.Parse(time.RFC3339, f[2])
		if err != nil {
			return nil, fmt.Errorf("commit %s: bad committer date %q: %w", f[0], f[2], err)
		}
		parent := firstParent(f[1])
		files, err := r.changedFiles(f[0], parent)
		if err != nil {
			return nil, err
		}
		commits = append(commits, AgentCommit{
			Hash:    f[0],
			Parent:  parent,
			Subject: f[5],
			Time:    landed,
			Files:   files,
		})
	}
	return commits, nil
}

// EntryID derives the ledger entry id for a commit: the leading hex characters
// of its hash. The id is stable across runs, which makes ledger imports
// idempotent.
func EntryID(commitHash string) string {
	if len(commitHash) <= idLen {
		return commitHash
	}
	return commitHash[:idLen]
}

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

// MergeBase returns the best common ancestor of two commits.
func (r *Repo) MergeBase(a, b string) (string, error) {
	out, err := r.git("merge-base", a, b)
	if err != nil {
		return "", fmt.Errorf("no common ancestor for %s and %s: %w", a, b, err)
	}
	return strings.TrimSpace(out), nil
}

// StackedCommit is one commit stacked above a ledger entry's change.
type StackedCommit struct {
	Hash  string
	Files []string // files changed relative to the commit's first parent
}

// StackedCommits returns the commits stacked on commit up to HEAD along the
// first-parent line, anchored at the merge base of commit and HEAD. Commits
// folded in by side-branch merges stay off the first-parent chain and do not
// count; a plain rev-list range would count them. Newest first. For a merge
// commit on the line, the changed files are its first-parent diff — the tree
// the merge brought onto the line, side-branch content included.
func (r *Repo) StackedCommits(commit string) ([]StackedCommit, error) {
	anchor, err := r.MergeBase(commit, "HEAD")
	if err != nil {
		return nil, err
	}
	out, err := r.git("log", "--first-parent", "--format=%H%x1f%P%x1e", anchor+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("walk the first-parent line above %s: %w", commit, err)
	}
	var stacked []StackedCommit
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		f := strings.Split(record, "\x1f")
		if len(f) != 2 {
			return nil, fmt.Errorf("unexpected git log record: %q", record)
		}
		files, err := r.changedFiles(f[0], firstParent(f[1]))
		if err != nil {
			return nil, err
		}
		stacked = append(stacked, StackedCommit{Hash: f[0], Files: files})
	}
	return stacked, nil
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

// changedFiles lists the files a commit changes, relative to its first
// parent. A root commit diffs against the empty tree.
func (r *Repo) changedFiles(hash, parent string) ([]string, error) {
	var (
		out string
		err error
	)
	if parent == "" {
		out, err = r.git("diff-tree", "--root", "--no-commit-id", "--name-status", "-r", hash)
	} else {
		out, err = r.git("diff", "--name-status", "-r", parent, hash)
	}
	if err != nil {
		return nil, fmt.Errorf("diff %s: %w", hash, err)
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// "M\tpath" — the status letter precedes a tab; keep the path only.
		if _, path, ok := strings.Cut(line, "\t"); ok {
			files = append(files, path)
		}
	}
	return files, nil
}

// isAgentCommit reports whether an author or a Co-Authored-By trailer matches
// any pattern. Patterns are lowercase substrings; "claude" alone would also
// match a human named Claudine, so teams can narrow the list via
// debtflow init --agents.
func isAgentCommit(authorName, authorEmail, trailers string, patterns []string) bool {
	haystacks := []string{strings.ToLower(authorName + " " + authorEmail)}
	for _, line := range strings.Split(trailers, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "co-authored-by:") {
			haystacks = append(haystacks, strings.ToLower(line))
		}
	}
	for _, pattern := range patterns {
		for _, hay := range haystacks {
			if strings.Contains(hay, strings.ToLower(pattern)) {
				return true
			}
		}
	}
	return false
}

// firstParent returns the first hash of a git %P parent list, empty for a
// root commit.
func firstParent(parents string) string {
	if parents == "" {
		return ""
	}
	if p, _, ok := strings.Cut(parents, " "); ok {
		return p
	}
	return parents
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
