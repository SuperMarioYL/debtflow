package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionMatchesVERSIONFile pins the CLI version default to the repo's
// VERSION file, so the two cannot drift apart: one version, declared once in
// VERSION, echoed here as the default and injected by goreleaser at release
// time. It reads a plain file — no binaries required.
func TestVersionMatchesVERSIONFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	want := strings.TrimSpace(string(raw))
	if version != want {
		t.Errorf("cli version default is %q but the VERSION file says %q — keep them in lockstep", version, want)
	}
}
