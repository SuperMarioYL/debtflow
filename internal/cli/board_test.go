package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuperMarioYL/debtflow/internal/gitrepo"
	"github.com/spf13/cobra"
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

// seedCommit writes files and commits them; an empty body omits the message
// body, an empty author keeps the repo's test identity, an empty date keeps
// the current time.
func seedCommit(t *testing.T, dir, subject, body, author, date string, files map[string]string) string {
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
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if date != "" {
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD"))
}

// seedRepo builds a repo with one human commit and two agent commits (a
// Claude Co-Authored-By trailer, then a Codex author) on distinct dates,
// oldest first.
func seedRepo(t *testing.T) (dir, baseHash, claudeHash, codexHash string) {
	t.Helper()
	dir = t.TempDir()
	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.name", "Human Dev")
	gitCmd(t, dir, "config", "user.email", "human@example.com")
	gitCmd(t, dir, "config", "commit.gpgsign", "false")

	baseHash = seedCommit(t, dir, "feat: human base", "", "", "2026-10-01T09:00:00+08:00", map[string]string{"base.go": "package demo\n"})
	claudeHash = seedCommit(t, dir, "feat: claude drafts a module",
		"Co-Authored-By: Claude <noreply@anthropic.com>", "", "2026-10-02T09:00:00+08:00",
		map[string]string{"claude.go": "package demo\n", "shared.go": "package demo\n"})
	codexHash = seedCommit(t, dir, "feat: codex writes a module", "",
		"Codex <codex@openai.com>", "2026-10-03T09:00:00+08:00",
		map[string]string{"codex.go": "package demo\n"})
	return dir, baseHash, claudeHash, codexHash
}

// runCmd executes a command with the given args and returns its output.
func runCmd(t *testing.T, cmd *cobra.Command, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%s: %v", cmd.Name(), err)
	}
	return buf.String()
}

func TestInitImportsAgentCommits(t *testing.T) {
	dir, baseHash, claudeHash, codexHash := seedRepo(t)
	t.Chdir(dir)

	out := runCmd(t, NewInitCmd())
	if !strings.Contains(out, "Imported 2 agent commit(s)") {
		t.Fatalf("init output = %q, want 2 imports", out)
	}

	entries, err := loadLedger(dir)
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ledger has %d entries, want 2", len(entries))
	}
	byID := map[string]Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}

	claude := byID[gitrepo.EntryID(claudeHash)]
	if claude.Status != StatusOpen {
		t.Errorf("status = %q, want open", claude.Status)
	}
	if claude.Change != baseHash+".."+claudeHash {
		t.Errorf("change = %q, want %s..%s", claude.Change, baseHash, claudeHash)
	}
	if claude.SavepointRef != gitrepo.SavepointRef(claude.ID) {
		t.Errorf("savepoint ref = %q", claude.SavepointRef)
	}
	if strings.Join(claude.Files, ",") != "claude.go,shared.go" {
		t.Errorf("files = %q", claude.Files)
	}
	if claude.Interest.TouchedFiles != 2 || claude.Interest.DependentCommits != 0 {
		t.Errorf("interest = %+v, want touched_files 2 at registration", claude.Interest)
	}

	// The savepoint ref must point at the pre-change commit.
	for _, e := range entries {
		want := baseHash
		if e.ID == gitrepo.EntryID(codexHash) {
			want = claudeHash
		}
		if got := strings.TrimSpace(gitCmd(t, dir, "rev-parse", e.SavepointRef)); got != want {
			t.Errorf("savepoint %s points at %s, want %s", e.SavepointRef, got, want)
		}
	}

	// The ledger must never end up committed.
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(gitignore), ledgerDir+"/"); n != 1 {
		t.Errorf(".gitignore mentions %s %d times, want 1", ledgerDir, n)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	dir, _, _, _ := seedRepo(t)
	t.Chdir(dir)

	runCmd(t, NewInitCmd())
	out := runCmd(t, NewInitCmd())
	if !strings.Contains(out, "No new agent commits — 2 already tracked") {
		t.Fatalf("second init output = %q", out)
	}
	entries, err := loadLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("ledger has %d entries after re-init, want 2", len(entries))
	}
}

func TestInitWithoutAgentCommits(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.name", "Human Dev")
	gitCmd(t, dir, "config", "user.email", "human@example.com")
	gitCmd(t, dir, "config", "commit.gpgsign", "false")
	seedCommit(t, dir, "feat: human only", "", "", "", map[string]string{"base.go": "package demo\n"})
	t.Chdir(dir)

	out := runCmd(t, NewInitCmd())
	if !strings.Contains(out, "No agent-authored commits found") {
		t.Fatalf("init output = %q", out)
	}
}

func TestBoardListsLedger(t *testing.T) {
	dir, _, claudeHash, codexHash := seedRepo(t)
	t.Chdir(dir)

	runCmd(t, NewInitCmd())
	out := runCmd(t, NewBoardCmd())
	for _, want := range []string{"ENTRY", "AGE", "BLAST RADIUS", "open"} {
		if !strings.Contains(out, want) {
			t.Errorf("board output missing %q:\n%s", want, out)
		}
	}
	// Oldest debt first: the Claude commit precedes the Codex commit.
	claudeID, codexID := gitrepo.EntryID(claudeHash), gitrepo.EntryID(codexHash)
	if strings.Index(out, claudeID) > strings.Index(out, codexID) {
		t.Errorf("board rows out of order:\n%s", out)
	}
}

func TestBoardWithoutLedger(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.name", "Human Dev")
	gitCmd(t, dir, "config", "user.email", "human@example.com")
	t.Chdir(dir)

	out := runCmd(t, NewBoardCmd())
	if !strings.Contains(out, "Ledger is clean") {
		t.Fatalf("board output = %q", out)
	}
}

func TestFormatAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Minute, "<1h"},
		{5 * time.Hour, "5h"},
		{36 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
	}
	for _, c := range cases {
		if got := formatAge(c.d); got != c.want {
			t.Errorf("formatAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
