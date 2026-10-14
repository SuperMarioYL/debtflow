package ledger

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

// An entry's history must survive the JSONL round trip byte for byte — the
// trail is the audit record, not derived state.
func TestEventHistoryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	entry := Entry{
		ID: "cccc55556666", Change: "h2..h3", Status: StatusOpen,
		CommitTime: "2026-10-03T09:00:00+08:00", Files: []string{"d.go"},
		Interest: Interest{DependentCommits: 1, TouchedFiles: 1, BlastRadius: 2},
		History: []Event{
			{Kind: EventImported, At: at, Interest: Interest{TouchedFiles: 1}},
			{Kind: EventAccrued, At: at.Add(time.Minute),
				Interest: Interest{DependentCommits: 1, TouchedFiles: 1, BlastRadius: 2}},
		},
	}
	if err := Append(dir, entry); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded[0], entry) {
		t.Errorf("round trip lost history:\n got %+v\nwant %+v", loaded[0], entry)
	}
	// Re-saving the loaded entry must reproduce the identical line — a read
	// that adds nothing must not change the file.
	if err := Save(dir, loaded); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again[0], entry) {
		t.Errorf("Save rewrote the history line:\n got %+v\nwant %+v", again[0], entry)
	}
}

// The Mark* transitions pair every status change with its appended event, and
// each event freezes the interest as of the moment it was recorded.
func TestVerbEventsPairWithStatus(t *testing.T) {
	e := Entry{
		ID: "dddd77778888", Status: StatusOpen,
		Interest: Interest{DependentCommits: 2, TouchedFiles: 3, BlastRadius: 1},
	}
	e.RecordEvent(EventImported, "")
	if e.Status != StatusOpen || len(e.History) != 1 {
		t.Fatalf("after RecordEvent: status %q, history %d events", e.Status, len(e.History))
	}
	if got := e.History[0]; got.Kind != EventImported || got.Interest != e.Interest || got.Detail != "" {
		t.Errorf("imported event = %+v, want kind %q with the registration interest", got, EventImported)
	}

	e.MarkPaid()
	if e.Status != StatusPaid || len(e.History) != 2 || e.History[1].Kind != EventPaid {
		t.Errorf("after MarkPaid: status %q, history %+v", e.Status, e.History)
	}
	if got := e.History[1]; got.Interest != e.Interest {
		t.Errorf("paid event interest = %+v, want the frozen accrual %+v", got.Interest, e.Interest)
	}

	e.MarkRefinanced("bundle000111")
	if e.Status != StatusRefinanced || len(e.History) != 3 || e.History[2].Kind != EventRefinanced {
		t.Errorf("after MarkRefinanced: status %q, history %+v", e.Status, e.History)
	}
	if got := e.History[2].Detail; !strings.Contains(got, "bundle000111") {
		t.Errorf("refinanced event detail = %q, want it to name the bundle", got)
	}

	e.MarkDefaulted()
	if e.Status != StatusDefaulted || len(e.History) != 4 || e.History[3].Kind != EventDefaulted {
		t.Errorf("after MarkDefaulted: status %q, history %+v", e.Status, e.History)
	}
}

// An accrual snapshot only earns a history event when the interest actually
// moved — unchanged reads must not grow the trail.
func TestAccrueToAppendsOnlyOnChange(t *testing.T) {
	e := Entry{Interest: Interest{TouchedFiles: 2}}

	e.accrueTo(Interest{TouchedFiles: 2})
	if len(e.History) != 0 {
		t.Errorf("unchanged accrual grew the history: %+v", e.History)
	}

	next := Interest{DependentCommits: 1, TouchedFiles: 2, BlastRadius: 3}
	e.accrueTo(next)
	if e.Interest != next {
		t.Errorf("interest = %+v, want %+v", e.Interest, next)
	}
	if len(e.History) != 1 || e.History[0].Kind != EventAccrued || e.History[0].Interest != next {
		t.Errorf("moved accrual must append the snapshot as an accrued event, got %+v", e.History)
	}
}

// A Save that changes nothing must leave the ledger file untouched — reading
// the board does not rewrite the ledger.
func TestSaveNoopLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, Entry{ID: "eeee99990000", Status: StatusOpen}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ledgerDir, ledgerFile)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, loaded); err != nil {
		t.Fatal(err)
	}
	same, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, same) {
		t.Error("Save with no changes should not replace the ledger file")
	}

	loaded[0].Status = StatusPaid
	if err := Save(dir, loaded); err != nil {
		t.Fatal(err)
	}
	changed, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(same, changed) {
		t.Error("Save with changes should replace the ledger file through temp+rename")
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
