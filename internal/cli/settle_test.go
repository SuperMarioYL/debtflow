package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/SuperMarioYL/debtflow/internal/gitrepo"
	"github.com/SuperMarioYL/debtflow/internal/ledger"
	"github.com/spf13/cobra"
)

// errRun executes a command expected to fail and returns its output and error.
func errRun(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// entryByID indexes a loaded ledger by entry id.
func entryByID(t *testing.T, dir string) map[string]ledger.Entry {
	t.Helper()
	entries, err := ledger.Load(dir)
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	byID := map[string]ledger.Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	return byID
}

func TestPaydownMarksEntryPaid(t *testing.T) {
	dir, _, claudeHash, codexHash := seedRepo(t)
	t.Chdir(dir)
	claudeID, codexID := gitrepo.EntryID(claudeHash), gitrepo.EntryID(codexHash)

	runCmd(t, NewInitCmd())
	before := entryByID(t, dir)
	out := runCmd(t, NewPaydownCmd(), claudeID)
	if !strings.Contains(out, "marked paid") {
		t.Fatalf("paydown output = %q", out)
	}

	byID := entryByID(t, dir)
	if byID[claudeID].Status != ledger.StatusPaid {
		t.Errorf("claude status = %q, want paid", byID[claudeID].Status)
	}
	if byID[codexID].Status != ledger.StatusOpen {
		t.Errorf("codex status = %q, want open", byID[codexID].Status)
	}
	// The paydown appended a paid event to the settled entry only.
	paidTrail := byID[claudeID].History
	if len(paidTrail) != len(before[claudeID].History)+1 ||
		paidTrail[len(paidTrail)-1].Kind != ledger.EventPaid {
		t.Errorf("claude history = %+v, want one %q event appended", paidTrail, ledger.EventPaid)
	}
	if paidTrail[len(paidTrail)-1].Interest != byID[claudeID].Interest {
		t.Errorf("paid event interest = %+v, want the frozen accrual %+v",
			paidTrail[len(paidTrail)-1].Interest, byID[claudeID].Interest)
	}
	if len(byID[codexID].History) != len(before[codexID].History) {
		t.Errorf("codex history grew on an unrelated paydown: %+v", byID[codexID].History)
	}

	// Settled debt is settled: re-paying errors and the interest stops
	// accruing even as more commits stack on top.
	if _, err := errRun(t, NewPaydownCmd(), claudeID); err == nil {
		t.Error("paying down an already-paid entry should fail")
	}
	seedCommit(t, dir, "docs: human writes docs", "", "", "2026-10-04T09:00:00+08:00",
		map[string]string{"human.go": "package demo\n"})
	runCmd(t, NewBoardCmd())

	byID = entryByID(t, dir)
	if got := byID[claudeID].Interest; got.DependentCommits != 1 || got.BlastRadius != 1 {
		t.Errorf("paid entry interest = %+v, want frozen at the paydown accrual (1/1)", got)
	}
	if got := byID[codexID].Interest; got.DependentCommits != 1 {
		t.Errorf("open entry interest = %+v, want 1 dependent commit", got)
	}
	// The refused re-paydown and later reads must not append to the trail —
	// a settled entry freezes its history along with its interest.
	if got := byID[claudeID].History; len(got) != len(paidTrail) {
		t.Errorf("claude history = %+v, want it frozen at %d events", got, len(paidTrail))
	}

	// Unknown ids fail with a clear error.
	if _, err := errRun(t, NewPaydownCmd(), "nosuchentry000"); err == nil {
		t.Error("paydown of an unknown id should fail")
	}
}

func TestRefinanceBundlesEntries(t *testing.T) {
	t.Run("older then newer", func(t *testing.T) {
		dir, baseHash, claudeHash, codexHash := seedRepo(t)
		t.Chdir(dir)
		claudeID, codexID := gitrepo.EntryID(claudeHash), gitrepo.EntryID(codexHash)

		runCmd(t, NewInitCmd())
		out := runCmd(t, NewRefinanceCmd(), claudeID, codexID)

		byID := entryByID(t, dir)
		if len(byID) != 3 {
			t.Fatalf("ledger has %d entries, want 3 with the bundled entry", len(byID))
		}
		if byID[claudeID].Status != ledger.StatusRefinanced || byID[codexID].Status != ledger.StatusRefinanced {
			t.Errorf("source statuses = %q, %q, want both refinanced", byID[claudeID].Status, byID[codexID].Status)
		}
		if !strings.Contains(out, "Bundled "+claudeID+" and "+codexID+" into") {
			t.Fatalf("refinance output = %q", out)
		}

		var bundle ledger.Entry
		for _, e := range byID {
			if e.ID != claudeID && e.ID != codexID {
				bundle = e
			}
		}
		if bundle.Status != ledger.StatusOpen {
			t.Errorf("bundle status = %q, want open", bundle.Status)
		}
		if bundle.Change != baseHash+".."+codexHash {
			t.Errorf("bundle change = %q, want %s..%s", bundle.Change, baseHash, codexHash)
		}
		if bundle.SavepointRef != byID[claudeID].SavepointRef {
			t.Errorf("bundle savepoint = %q, want the older entry's %q", bundle.SavepointRef, byID[claudeID].SavepointRef)
		}
		if bundle.CommitTime != byID[claudeID].CommitTime {
			t.Errorf("bundle commit_time = %q, want the older entry's %q", bundle.CommitTime, byID[claudeID].CommitTime)
		}
		if got := strings.Join(bundle.Files, ","); got != "claude.go,shared.go,codex.go" {
			t.Errorf("bundle files = %q, want the union of both entries", got)
		}
		if bundle.Interest.TouchedFiles != 3 || bundle.Interest.DependentCommits != 0 || bundle.Interest.BlastRadius != 0 {
			t.Errorf("bundle interest = %+v, want 3 touched files and nothing stacked yet", bundle.Interest)
		}
		// The refinance appended an event to each source naming the bundle it
		// joined, and the bundle's own trail starts with its first accrual.
		for _, src := range []ledger.Entry{byID[claudeID], byID[codexID]} {
			last := src.History[len(src.History)-1]
			if last.Kind != ledger.EventRefinanced || !strings.Contains(last.Detail, bundle.ID) {
				t.Errorf("%s history = %+v, want a %q event naming bundle %s", src.ID, src.History, ledger.EventRefinanced, bundle.ID)
			}
		}
		if len(bundle.History) == 0 || bundle.History[0].Kind != ledger.EventAccrued {
			t.Errorf("bundle history = %+v, want it to start with an %q event", bundle.History, ledger.EventAccrued)
		}
	})

	t.Run("argument order does not matter", func(t *testing.T) {
		dir, _, claudeHash, codexHash := seedRepo(t)
		t.Chdir(dir)
		runCmd(t, NewInitCmd())
		runCmd(t, NewRefinanceCmd(), gitrepo.EntryID(codexHash), gitrepo.EntryID(claudeHash))

		byID := entryByID(t, dir)
		if len(byID) != 3 {
			t.Fatalf("ledger has %d entries, want 3", len(byID))
		}
		for _, e := range byID {
			if e.ID != gitrepo.EntryID(claudeHash) && e.ID != gitrepo.EntryID(codexHash) && e.Status != ledger.StatusOpen {
				t.Errorf("bundle status = %q, want open", e.Status)
			}
		}
	})
}

func TestRefinanceRejectsBadInput(t *testing.T) {
	dir, _, claudeHash, _ := seedRepo(t)
	t.Chdir(dir)
	claudeID := gitrepo.EntryID(claudeHash)

	runCmd(t, NewInitCmd())

	if _, err := errRun(t, NewRefinanceCmd(), claudeID, claudeID); err == nil {
		t.Error("refinancing an entry with itself should fail")
	}
	// Unknown id, and settled entries cannot be refinanced.
	runCmd(t, NewPaydownCmd(), claudeID)
	if _, err := errRun(t, NewRefinanceCmd(), claudeID, "nosuchentry000"); err == nil {
		t.Error("refinancing an unknown id should fail")
	}
}

func TestDefaultRestoresSavepointTree(t *testing.T) {
	dir, _, _, codexHash := seedRepo(t)
	t.Chdir(dir)
	codexID := gitrepo.EntryID(codexHash)

	runCmd(t, NewInitCmd())
	headBefore := strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD"))

	out := runCmd(t, NewDefaultCmd(), codexID)
	if !strings.Contains(out, "Defaulted") {
		t.Fatalf("default output = %q", out)
	}

	// The savepoint-tree restore discards exactly the Codex change and
	// commits the restored tree — the working tree ends clean.
	tree := gitCmd(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(tree, "codex.go") {
		t.Errorf("restored tree still has codex.go:\n%s", tree)
	}
	for _, want := range []string{"base.go", "claude.go", "shared.go"} {
		if !strings.Contains(tree, want) {
			t.Errorf("restored tree lost %s:\n%s", want, tree)
		}
	}
	if _, err := os.Stat(dir + "/codex.go"); !os.IsNotExist(err) {
		t.Error("codex.go still on disk after the restore")
	}
	if dirty := gitCmd(t, dir, "status", "--porcelain", "-uno"); dirty != "" {
		t.Errorf("working tree not clean after default: %q", dirty)
	}
	if head := strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD")); head == headBefore {
		t.Error("default did not commit the restored tree")
	}

	byID := entryByID(t, dir)
	if byID[codexID].Status != ledger.StatusDefaulted {
		t.Errorf("codex status = %q, want defaulted", byID[codexID].Status)
	}
	// The default appended a defaulted event as the trail's last record.
	def := byID[codexID].History
	if len(def) == 0 || def[len(def)-1].Kind != ledger.EventDefaulted {
		t.Errorf("codex history = %+v, want a %q event appended", def, ledger.EventDefaulted)
	}
	if _, err := errRun(t, NewDefaultCmd(), codexID); err == nil {
		t.Error("defaulting an already-defaulted entry should fail")
	}
}

func TestDefaultRootCommitUsesEmptyTree(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.name", "Human Dev")
	gitCmd(t, dir, "config", "user.email", "human@example.com")
	gitCmd(t, dir, "config", "commit.gpgsign", "false")
	seedCommit(t, dir, "feat: claude bootstraps the repo",
		"Co-Authored-By: Claude <noreply@anthropic.com>", "", "2026-10-02T09:00:00+08:00",
		map[string]string{"r.go": "package demo\n"})
	t.Chdir(dir)

	runCmd(t, NewInitCmd())
	byID := entryByID(t, dir)
	if len(byID) != 1 {
		t.Fatalf("ledger has %d entries, want 1", len(byID))
	}
	var entry ledger.Entry
	for _, e := range byID {
		entry = e
	}
	if entry.SavepointRef != "" {
		t.Fatalf("root-commit entry savepoint ref = %q, want empty", entry.SavepointRef)
	}

	runCmd(t, NewDefaultCmd(), entry.ID)

	if tree := gitCmd(t, dir, "ls-tree", "-r", "--name-only", "HEAD"); strings.TrimSpace(tree) != "" {
		t.Errorf("restored tree = %q, want the empty tree", tree)
	}
	if dirty := gitCmd(t, dir, "status", "--porcelain", "-uno"); dirty != "" {
		t.Errorf("working tree not clean after default: %q", dirty)
	}
	if _, err := os.Stat(dir + "/r.go"); !os.IsNotExist(err) {
		t.Error("r.go still on disk after the restore")
	}
}

func TestDefaultRefusesDirtyWorktree(t *testing.T) {
	dir, _, claudeHash, _ := seedRepo(t)
	t.Chdir(dir)
	claudeID := gitrepo.EntryID(claudeHash)

	runCmd(t, NewInitCmd())
	if err := os.WriteFile(dir+"/claude.go", []byte("package demo // uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := errRun(t, NewDefaultCmd(), claudeID); err == nil {
		t.Fatal("default with a dirty working tree should fail")
	}

	// Nothing was restored or settled.
	if _, err := os.Stat(dir + "/claude.go"); err != nil {
		t.Fatalf("claude.go vanished: %v", err)
	}
	byID := entryByID(t, dir)
	if byID[claudeID].Status != ledger.StatusOpen {
		t.Errorf("claude status = %q, want still open", byID[claudeID].Status)
	}
}
