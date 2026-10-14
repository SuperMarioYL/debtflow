package gitrepo

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitCmd runs git in dir and returns stdout, failing the test on error.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String()
}

// seedCommit writes files, commits them, and returns the new commit hash.
// An empty body omits the message body; an empty author keeps the repo's
// test identity.
func seedCommit(t *testing.T, dir, subject, body, author string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, dir, "add", "-A")
	args := []string{"commit", "-m", subject}
	if body != "" {
		args = append(args, "-m", body)
	}
	if author != "" {
		args = append(args, "--author", author)
	}
	gitCmd(t, dir, args...)
	return strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD"))
}

// seedRepo builds a throwaway repo: a human base commit, an agent commit
// carrying a Claude Co-Authored-By trailer, an agent commit authored by
// Codex, and a human commit with a non-agent trailer.
func seedRepo(t *testing.T) (dir string, claudeHash, codexHash string) {
	t.Helper()
	dir = t.TempDir()
	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.name", "Human Dev")
	gitCmd(t, dir, "config", "user.email", "human@example.com")
	gitCmd(t, dir, "config", "commit.gpgsign", "false")

	seedCommit(t, dir, "feat: human base", "", "", map[string]string{"base.go": "package demo\n"})

	claudeHash = seedCommit(t, dir, "feat: claude drafts a module",
		"Co-Authored-By: Claude <noreply@anthropic.com>", "",
		map[string]string{"claude.go": "package demo\n", "shared.go": "package demo\n"})

	codexHash = seedCommit(t, dir, "feat: codex writes a module", "",
		"Codex <codex@openai.com>",
		map[string]string{"codex.go": "package demo\n"})

	seedCommit(t, dir, "docs: human writes docs",
		"Co-Authored-By: Reviewer <reviewer@example.com>", "",
		map[string]string{"docs.md": "# demo\n"})
	return dir, claudeHash, codexHash
}

func TestAgentCommitsDetectsAgentAuthors(t *testing.T) {
	dir, claudeHash, codexHash := seedRepo(t)

	repo, err := Open(dir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	commits, err := repo.AgentCommits(DefaultAgentPatterns)
	if err != nil {
		t.Fatalf("agent commits: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d agent commits, want 2 (the human trailer and the base commit must not match)", len(commits))
	}

	// Newest first: the Codex-authored commit precedes the Claude trailer.
	if commits[0].Hash != codexHash {
		t.Errorf("newest agent commit = %s, want %s", commits[0].Hash, codexHash)
	}
	if commits[1].Hash != claudeHash {
		t.Errorf("older agent commit = %s, want %s", commits[1].Hash, claudeHash)
	}
	if got := strings.Join(commits[1].Files, ","); got != "claude.go,shared.go" {
		t.Errorf("claude commit files = %q, want claude.go,shared.go", got)
	}
	if got := strings.Join(commits[0].Files, ","); got != "codex.go" {
		t.Errorf("codex commit files = %q, want codex.go", got)
	}
	if commits[0].Subject != "feat: codex writes a module" {
		t.Errorf("subject = %q", commits[0].Subject)
	}
	if commits[0].Parent != claudeHash {
		t.Errorf("parent = %s, want %s", commits[0].Parent, claudeHash)
	}
	if commits[0].Time.IsZero() {
		t.Error("commit time not parsed")
	}
}

func TestAgentCommitsRespectsCustomPatterns(t *testing.T) {
	dir, _, _ := seedRepo(t)
	repo, err := Open(dir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	commits, err := repo.AgentCommits([]string{"cursor"})
	if err != nil {
		t.Fatalf("agent commits: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("got %d commits for pattern cursor, want 0", len(commits))
	}
}

func TestRecordSavepointPointsAtPreChangeTree(t *testing.T) {
	dir, claudeHash, codexHash := seedRepo(t)
	repo, err := Open(dir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}

	id := EntryID(codexHash)
	ref, err := repo.RecordSavepoint(id, claudeHash)
	if err != nil {
		t.Fatalf("record savepoint: %v", err)
	}
	if ref != SavepointRef(id) {
		t.Errorf("ref = %q, want %q", ref, SavepointRef(id))
	}
	if got := strings.TrimSpace(gitCmd(t, dir, "rev-parse", ref)); got != claudeHash {
		t.Errorf("savepoint %s points at %s, want the pre-change commit %s", ref, got, claudeHash)
	}
}

func TestOpenRejectsNonRepository(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Error("Open on a non-repository should fail")
	}
}
