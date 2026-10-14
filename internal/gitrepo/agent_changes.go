// Agent-change detection and history walking: which commits on the
// first-parent line are agent-authored, what is stacked on a ledger entry,
// and the merge-base anchoring that keeps side-branch noise out of the
// interest numbers.

package gitrepo

import (
	"fmt"
	"strings"
	"time"
)

// DefaultAgentPatterns are the author / Co-Authored-By substrings that mark a
// commit as agent-authored. Matching is case-insensitive.
var DefaultAgentPatterns = []string{"claude", "codex", "cursor"}

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

// MergeBase returns the best common ancestor of two commits.
func (r *Repo) MergeBase(a, b string) (string, error) {
	out, err := r.git("merge-base", a, b)
	if err != nil {
		return "", fmt.Errorf("no common ancestor for %s and %s: %w", a, b, err)
	}
	return strings.TrimSpace(out), nil
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
