package ledger

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadAppendSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if Exists(dir) {
		t.Error("no ledger should exist in a fresh directory")
	}
	entries, err := Load(dir)
	if err != nil || entries != nil {
		t.Fatalf("Load on a missing ledger = %v, %v; want nil, nil", entries, err)
	}

	first := Entry{
		ID: "aaaa11112222", Change: "p1..h1",
		SavepointRef: "refs/debtflow/aaaa11112222", Status: StatusOpen,
		CommitTime: "2026-10-01T09:00:00+08:00", Files: []string{"a.go", "b.go"},
	}
	second := Entry{
		ID: "bbbb33334444", Change: "h1..h2", Status: StatusOpen,
		CommitTime: "2026-10-02T09:00:00+08:00", Files: []string{"c.go"},
	}
	if err := Append(dir, first); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, second); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Error("Append should have created the ledger file")
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d entries, want 2", len(loaded))
	}
	if !reflect.DeepEqual(loaded[0], first) || !reflect.DeepEqual(loaded[1], second) {
		t.Errorf("round trip lost data:\n got %+v\nwant %+v", loaded, []Entry{first, second})
	}

	// Save persists status changes and recomputed interest onto the same
	// lines — one line per entry, no duplicates from the earlier Appends.
	loaded[0].Status = StatusPaid
	loaded[0].Interest = Interest{DependentCommits: 3, TouchedFiles: 2, BlastRadius: 5}
	if err := Save(dir, loaded); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 {
		t.Fatalf("after Save the ledger has %d entries, want 2", len(again))
	}
	if again[0].Status != StatusPaid || again[0].Interest != (Interest{DependentCommits: 3, TouchedFiles: 2, BlastRadius: 5}) {
		t.Errorf("Save did not persist the updates: %+v", again[0])
	}
	if !reflect.DeepEqual(again[1], second) {
		t.Errorf("Save clobbered an untouched entry: %+v", again[1])
	}
}

func TestLoadRejectsCorruptLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ledgerDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ledgerDir, ledgerFile), []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("Load should fail on a corrupt ledger line")
	}
}

func TestChangeTip(t *testing.T) {
	cases := []struct{ change, want string }{
		{"abc123..def456", "def456"},
		{"rootonly", "rootonly"},
	}
	for _, c := range cases {
		if got := ChangeTip(c.change); got != c.want {
			t.Errorf("ChangeTip(%q) = %q, want %q", c.change, got, c.want)
		}
	}
}

func TestSaveLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, Entry{ID: "aaaa11112222", Status: StatusOpen}); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, []Entry{{ID: "aaaa11112222", Status: StatusPaid}}); err != nil {
		t.Fatal(err)
	}
	names, err := os.ReadDir(filepath.Join(dir, ledgerDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if strings.HasSuffix(n.Name(), ".tmp") {
			t.Errorf("temp file %s left behind", n.Name())
		}
	}
}
