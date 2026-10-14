#!/usr/bin/env bash
# Seed a scratch repository with agent commits so you can try debtflow
# without touching a real project. Creates ./debtflow-demo-repo, then run:
#
#   cd debtflow-demo-repo && debtflow init && debtflow board
set -euo pipefail

REPO="${1:-debtflow-demo-repo}"
mkdir -p "$REPO" && cd "$REPO"
git init -q -b main .
git config user.name "Demo Human"
git config user.email "human@example.com"

mk() { mkdir -p "$(dirname "$1")"; cat > "$1"; }

# baseline (human)
mk README.md <<'EOF'
# debtflow-demo

Scratch repository for trying debtflow: a tiny ledger service whose
unreviewed agent commits pile up as review debt.
EOF
mk go.mod <<'EOF'
module demo/ledger

go 1.24
EOF
mk service/ledger.go <<'EOF'
package service

type Ledger struct{ Items []string }

func (l *Ledger) Add(item string) { l.Items = append(l.Items, item) }

func (l *Ledger) Count() int { return len(l.Items) }
EOF
mk service/ledger_test.go <<'EOF'
package service

import "testing"

func TestAdd(t *testing.T) {
	l := &Ledger{}
	l.Add("x")
	if l.Count() != 1 {
		t.Fatal("want 1 item")
	}
}
EOF
mk api/handler.go <<'EOF'
package api

import "demo/ledger/service"

var store = &service.Ledger{}

func Handle(item string) int {
	store.Add(item)
	return store.Count()
}
EOF
git add -A
GIT_AUTHOR_NAME="Demo Human" GIT_AUTHOR_EMAIL="human@example.com" \
GIT_COMMITTER_NAME="Demo Human" GIT_COMMITTER_EMAIL="human@example.com" \
GIT_AUTHOR_DATE="2026-10-11T10:00:00+08:00" GIT_COMMITTER_DATE="2026-10-11T10:00:00+08:00" \
  git commit -q -m "chore: baseline ledger service"

# agent(name, email, date, subject): commit whatever is staged as an agent
agent() {
  GIT_AUTHOR_NAME="$1" GIT_AUTHOR_EMAIL="$2" GIT_COMMITTER_NAME="$1" GIT_COMMITTER_EMAIL="$2" \
  GIT_AUTHOR_DATE="$3" GIT_COMMITTER_DATE="$3" git commit -q -m "$4"
}

# agent commit 1 — Claude adds interest support (4 files)
mk service/ledger.go <<'EOF'
package service

type Ledger struct{ Items []string }

func (l *Ledger) Add(item string) { l.Items = append(l.Items, item) }

func (l *Ledger) Count() int { return len(l.Items) }

// Interest returns points accrued beyond the first ten items — the way
// deferred review work compounds in this toy service.
func (l *Ledger) Interest() int {
	if n := l.Count() - 10; n > 0 {
		return n
	}
	return 0
}
EOF
mk service/ledger_test.go <<'EOF'
package service

import "testing"

func TestAdd(t *testing.T) {
	l := &Ledger{}
	l.Add("x")
	if l.Count() != 1 {
		t.Fatal("want 1 item")
	}
}

func TestInterestEmpty(t *testing.T) {
	l := &Ledger{}
	if l.Interest() != 0 {
		t.Fatal("empty ledger carries no interest")
	}
}
EOF
mk api/handler.go <<'EOF'
package api

import "demo/ledger/service"

var store = &service.Ledger{}

func Handle(item string) int {
	store.Add(item)
	return store.Count() + store.Interest()
}
EOF
mk README.md <<'EOF'
# debtflow-demo

Scratch repository for trying debtflow: a tiny ledger service with a count
of items, accrued interest and a small API handler.
EOF
git add -A
agent "Claude" "claude@anthropic.com" "2026-10-12T09:30:00+08:00" "feat: accrue interest beyond ten items"

# agent commit 2 — Cursor Agent exposes an HTTP route (2 files)
mk api/routes.go <<'EOF'
package api

import "net/http"

func Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/count", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}
EOF
mk service/ledger.go <<'EOF'
package service

type Ledger struct{ Items []string }

func (l *Ledger) Add(item string) { l.Items = append(l.Items, item) }

func (l *Ledger) Count() int { return len(l.Items) }

// Interest returns points accrued beyond the first ten items, plus one for
// every duplicate — deferred review work compounds in this toy service.
func (l *Ledger) Interest() int {
	n := l.Count() - 10
	if n < 0 {
		n = 0
	}
	return n
}
EOF
git add -A
agent "Cursor Agent" "agent@cursor.sh" "2026-10-13T10:00:00+08:00" "feat: expose count over http"

# agent commit 3 — Codex covers routes and adds a health endpoint (3 files)
mk api/routes_test.go <<'EOF'
package api

import "testing"

func TestRoutesSmoke(t *testing.T) {
	if Routes() == nil {
		t.Fatal("routes must not be nil")
	}
}
EOF
mk api/handler.go <<'EOF'
package api

import (
	"net/http"

	"demo/ledger/service"
)

var store = &service.Ledger{}

func Handle(item string) int {
	store.Add(item)
	return store.Count() + store.Interest()
}

func Health(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}
EOF
mk README.md <<'EOF'
# debtflow-demo

Scratch repository for trying debtflow: a tiny ledger service with a count
of items, accrued interest, an HTTP surface and a small API handler.
EOF
git add -A
agent "Codex" "codex@openai.com" "2026-10-13T16:00:00+08:00" "test: cover routes and add health endpoint"

echo "Seeded $REPO with 1 human + 3 agent commits."
echo "Next: cd $REPO && debtflow init && debtflow board"
