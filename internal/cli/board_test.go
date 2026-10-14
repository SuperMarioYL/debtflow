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
	"github.com/SuperMarioYL/debtflow/internal/ledger"
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

	entries, err := ledger.Load(dir)
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ledger has %d entries, want 2", len(entries))
	}
	byID := map[string]ledger.Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}

	claude := byID[gitrepo.EntryID(claudeHash)]
	if claude.Status != ledger.StatusOpen {
		t.Errorf("status = %q, want open", claude.Status)
	}
	// Registration is the first event of the trail.
	if len(claude.History) == 0 || claude.History[0].Kind != ledger.EventImported {
		t.Errorf("claude history = %+v, want it to start with an %q event", claude.History, ledger.EventImported)
	}
	if claude.History[0].Interest.TouchedFiles != 2 {
		t.Errorf("imported event interest = %+v, want the 2 touched files of the registration", claude.History[0].Interest)
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
	// Init is an accrual read: the Codex commit stacked on the Claude entry
	// counts as dependent interest, and codex.go lands beyond its own files.
	if claude.Interest.TouchedFiles != 2 {
		t.Errorf("touched_files = %d, want 2", claude.Interest.TouchedFiles)
	}
	if claude.Interest.DependentCommits != 1 || claude.Interest.BlastRadius != 1 {
		t.Errorf("interest = %+v, want 1 dependent commit with blast radius 1", claude.Interest)
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
	if n := strings.Count(string(gitignore), ".debtflow/"); n != 1 {
		t.Errorf(".gitignore mentions .debtflow %d times, want 1", n)
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
	entries, err := ledger.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("ledger has %d entries after re-init, want 2", len(entries))
	}
	// Repeated init must not append another ignore block: ensureIgnored has
	// to recognize the `.debtflow/` line the first one wrote.
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(gitignore), ".debtflow"); n != 1 {
		t.Errorf(".gitignore mentions .debtflow %d times after two inits, want 1:\n%s", n, gitignore)
	}
}

// TestEnsureIgnoredRecognizesBothSpellings pins the dedup against every
// spelling of the ignore line — a pre-existing one must satisfy ensureIgnored
// just like the `.debtflow/` line it writes itself.
func TestEnsureIgnoredRecognizesBothSpellings(t *testing.T) {
	for _, line := range []string{".debtflow", ".debtflow/", "/.debtflow", "/.debtflow/"} {
		path := filepath.Join(t.TempDir(), ".gitignore")
		if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ensureIgnored(path); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "# debtflow") {
			t.Errorf("existing line %q: ensureIgnored appended a duplicate block:\n%s", line, data)
		}
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

func TestBoardAccruesInterestAndAutoImports(t *testing.T) {
	dir, _, claudeHash, _ := seedRepo(t)
	t.Chdir(dir)
	claudeID := gitrepo.EntryID(claudeHash)

	runCmd(t, NewInitCmd())
	runCmd(t, NewBoardCmd())
	entries, err := ledger.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var claude ledger.Entry
	for _, e := range entries {
		if e.ID == claudeID {
			claude = e
		}
	}
	if claude.Interest.DependentCommits != 1 || claude.Interest.BlastRadius != 1 {
		t.Fatalf("interest after init = %+v, want 1 dependent commit, blast radius 1", claude.Interest)
	}

	trailBefore := len(claude.History)

	// An unchanged board read must not grow the history — the accrual pass
	// only appends when a snapshot actually moves.
	runCmd(t, NewBoardCmd())
	entries, err = ledger.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.ID == claudeID {
			claude = e
		}
	}
	if got := len(claude.History); got != trailBefore {
		t.Errorf("unchanged board read grew the history from %d to %d events", trailBefore, got)
	}

	// A human commit and a Cursor agent commit stack on the debt; the board
	// read must raise the entry's interest and import the agent commit.
	seedCommit(t, dir, "docs: human writes docs", "", "", "2026-10-04T09:00:00+08:00",
		map[string]string{"human.go": "package demo\n"})
	cursorHash := seedCommit(t, dir, "feat: cursor tweaks the module",
		"Co-Authored-By: Cursor <agent@cursor.sh>", "", "2026-10-05T09:00:00+08:00",
		map[string]string{"cursor.go": "package demo\n"})

	out := runCmd(t, NewBoardCmd())
	if !strings.Contains(out, "Auto-imported 1 new agent commit(s)") {
		t.Fatalf("board output = %q, want the auto-import notice", out)
	}

	entries, err = ledger.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ledger.Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	if len(entries) != 3 {
		t.Fatalf("ledger has %d entries, want 3 with the auto-imported Cursor commit", len(entries))
	}
	claude = byID[claudeID]
	// Blast radius counts the .gitignore that init wrote — the human commit
	// records it alongside human.go — so 4 files beyond the entry's own two.
	if claude.Interest.DependentCommits != 3 || claude.Interest.BlastRadius != 4 {
		t.Errorf("interest after stacking = %+v, want 3 dependent commits, blast radius 4", claude.Interest)
	}
	// The moved snapshot earned an accrued event; the auto-imported entry's
	// trail starts at its registration.
	if got := len(claude.History); got != trailBefore+1 || claude.History[got-1].Kind != ledger.EventAccrued {
		t.Errorf("claude history = %+v, want one %q event appended by the rise", claude.History, ledger.EventAccrued)
	}
	cursor := byID[gitrepo.EntryID(cursorHash)]
	if cursor.Status != ledger.StatusOpen {
		t.Errorf("auto-imported entry status = %q, want open", cursor.Status)
	}
	if len(cursor.History) == 0 || cursor.History[0].Kind != ledger.EventImported {
		t.Errorf("cursor history = %+v, want it to start with an %q event", cursor.History, ledger.EventImported)
	}
}

// TestAccrualIgnoresSideBranch is the schema-smoke check: commits folded in
// by a side-branch merge must not inflate interest, and a plain rev-list
// range over anchor..HEAD would count them.
func TestAccrualIgnoresSideBranch(t *testing.T) {
	dir, _, claudeHash, codexHash := seedRepo(t)
	t.Chdir(dir)
	runCmd(t, NewInitCmd())

	// An agent commit on a side branch, merged into main with --no-ff after
	// another agent commit lands on main.
	gitCmd(t, dir, "checkout", "-q", "-b", "side", codexHash)
	seedCommit(t, dir, "feat: claude works on a branch",
		"Co-Authored-By: Claude <noreply@anthropic.com>", "", "2026-10-04T09:00:00+08:00",
		map[string]string{"side.go": "package demo\n"})
	gitCmd(t, dir, "checkout", "-q", "main")
	seedCommit(t, dir, "feat: cursor tweaks the module",
		"Co-Authored-By: Cursor <agent@cursor.sh>", "", "2026-10-05T09:00:00+08:00",
		map[string]string{"cursor.go": "package demo\n"})
	gitCmd(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")

	runCmd(t, NewBoardCmd())
	entries, err := ledger.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("ledger has %d entries, want 3 — the side-branch commit must not be imported", len(entries))
	}
	byID := map[string]ledger.Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	claude := byID[gitrepo.EntryID(claudeHash)]
	// Stacked on the Claude entry: the Codex commit, the Cursor commit and
	// the merge commit — the side commit stays off the first-parent line.
	if claude.Interest.DependentCommits != 3 {
		t.Errorf("dependent_commits = %d, want 3 (side-branch commit must not count)", claude.Interest.DependentCommits)
	}
	codex := byID[gitrepo.EntryID(codexHash)]
	if codex.Interest.DependentCommits != 2 {
		t.Errorf("codex dependent_commits = %d, want 2", codex.Interest.DependentCommits)
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
