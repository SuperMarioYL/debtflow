// The JSONL store: an append-only line file at .debtflow/ledger.jsonl inside
// the tracked repo — one Entry per line, derived state over git history. It
// is local state, never committed.

package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The ledger lives at .debtflow/ledger.jsonl inside the tracked repo — one
// Entry per line, derived state over git history. It is local state, never
// committed.
const (
	ledgerDir  = ".debtflow"
	ledgerFile = "ledger.jsonl"
)

// Load reads the ledger file. A missing file is an empty ledger, not an
// error — debtflow init creates it.
func Load(root string) ([]Entry, error) {
	f, err := os.Open(filepath.Join(root, ledgerDir, ledgerFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("corrupt ledger line: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, sc.Err()
}

// Append appends one entry as a single JSON line.
func Append(root string, e Entry) error {
	if err := os.MkdirAll(filepath.Join(root, ledgerDir), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, ledgerDir, ledgerFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// Save rewrites the ledger file with the given entries. New entries always
// enter the ledger through Append; Save is how recomputed interest, appended
// history events, and verb status changes are persisted onto the lines
// already there. The ledger stays append-only in spirit: entries are never
// deleted or reordered, and every state change is recorded inside the entry's
// history as an appended event — the interest and status fields only carry
// the latest state those events describe. A write goes through a temp file
// and a rename so a crash cannot tear a line in half, and a pass that changed
// nothing leaves the file untouched: reading the board does not rewrite the
// ledger.
func Save(root string, entries []Entry) error {
	var buf bytes.Buffer
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	path := filepath.Join(root, ledgerDir, ledgerFile)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, buf.Bytes()) {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(root, ledgerDir), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Exists reports whether the repo already has a ledger file — debtflow init
// has run. The board only auto-imports on top of an existing ledger; on a
// repo that never opted in it stays a pure read.
func Exists(root string) bool {
	_, err := os.Stat(filepath.Join(root, ledgerDir, ledgerFile))
	return err == nil
}
