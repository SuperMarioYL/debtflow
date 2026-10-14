package render

import (
	"strings"
	"testing"
)

func TestBoardRendersLedgerRows(t *testing.T) {
	rows := []BoardRow{
		{ID: "a1b2c3d4e5f6", Age: "3d", DependentCommits: 2, TouchedFiles: 4, BlastRadius: 5, Status: "open"},
		{ID: "b2c3d4e5f6a1", Age: "1d", DependentCommits: 0, TouchedFiles: 1, BlastRadius: 0, Status: "paid"},
	}
	out := Board(rows)
	for _, want := range []string{
		"ENTRY", "AGE", "DEPENDENT COMMITS", "TOUCHED FILES", "BLAST RADIUS", "STATUS",
		"a1b2c3d4e5f6", "3d", "b2c3d4e5f6a1", "1d",
		"open", "paid",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("board output missing %q:\n%s", want, out)
		}
	}
	// Rows must appear in the order given — the cli passes oldest first.
	if strings.Index(out, "a1b2c3d4e5f6") > strings.Index(out, "b2c3d4e5f6a1") {
		t.Errorf("board rows out of order:\n%s", out)
	}
}

func TestBoardEmpty(t *testing.T) {
	if got := Board(nil); got != "" {
		t.Errorf("Board(nil) = %q, want empty string", got)
	}
}
