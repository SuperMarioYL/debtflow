**English** | [简体中文](./README.md)

<div align="center">

# Debtflow

**Turn unreviewed coding-agent changes into interest-bearing review debt.**

[![Go](https://img.shields.io/badge/go-1.24%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![Release](https://img.shields.io/github/v/release/SuperMarioYL/debtflow)](https://github.com/SuperMarioYL/debtflow/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/SuperMarioYL/debtflow/ci.yml?branch=main&label=CI)](https://github.com/SuperMarioYL/debtflow/actions/workflows/ci.yml)
[![Platform](https://img.shields.io/badge/platform-macOS%20%C2%B7%20Linux%20%C2%B7%20Windows-lightgrey)](https://github.com/SuperMarioYL/debtflow/releases)
[![License](https://img.shields.io/badge/license-MIT-blue)](./LICENSE)

<img src="https://readme-typing-svg.demolab.com?font=JetBrains+Mono&weight=600&size=21&duration=3200&pause=1100&color=D29922&center=true&vCenter=true&width=640&height=56&multiline=false&repeat=true&lines[]=Every%20unreviewed%20agent%20change%20quietly%20accrues%20interest&lines[]=dependent%20commits%20up%20%C2%B7%20touched%20files%20up%20%C2%B7%20blast%20radius%20up&lines[]=paydown%20%C2%B7%20refinance%20%C2%B7%20default" alt="Every unreviewed agent change quietly accrues interest; the three interest columns: dependent commits, touched files, blast radius; paydown · refinance · default" width="640" />

Your coding agents now write more code in a day than you can review: every agent change nobody has looked at quietly accrues interest — dependent commits pile on top of it, its blast radius keeps growing. Debtflow records these unreviewed changes as entries in an interest-bearing debt ledger, and three verbs — paydown, refinance, default — decide whether each debt is settled by review, bundled into one review, or wiped by a rollback.

Single binary · no daemon · no network dependency · no LLM anywhere in the accrual

[Demo](#-demo) · [Quickstart](#-quickstart) · [Commands](#-commands) · [Pricing](#-pricing) · [Roadmap](#-roadmap)

</div>

## <img src="https://cdn.jsdelivr.net/npm/@tabler/icons@3.34.1/icons/outline/chart-line.svg" height="22" alt="">&nbsp;Demo

The figure below is the real output of `debtflow board` on a seeded repository (the full command log and output live in [docs/demo-results.json](docs/demo-results.json)). Three panels: register, accrue, settle.

<p align="center">
  <img src="docs/demo-ledger-board.svg" alt="The debtflow board demo, three terminal panels. Panel one: debtflow init imports three unreviewed agent commits as open ledger entries, with the columns entry, age, dependent commits, touched files, blast radius and status. Panel two: a fourth agent commit lands stacked on the oldest entry; the next board read shows its interest risen (dependent commits 2 to 3, blast radius 2 to 4) and the new commit auto-imported as a fresh open entry. Panel three: after debtflow paydown, refinance and default, the final board shows paid, refinanced, open and defaulted entries at once, and git status confirms the working tree was restored clean." width="720" />
</p>

Panel two is the whole point: between the two `board` reads nothing was run, yet the oldest debt's dependent commits rose from 2 to 3 and its blast radius from 2 to 4 — as long as nobody reviews, interest keeps accruing on its own.

## <img src="https://cdn.jsdelivr.net/npm/@tabler/icons@3.34.1/icons/outline/scale.svg" height="22" alt="">&nbsp;Why interest-bearing debt

An unreviewed agent change is not a static thing. Every day it sits in the repo, other commits stack on top of it: more code comes to depend on it, the files it touched get touched again, and by the time you look, the review is no longer just the original diff.

Existing tools model it as a static list anyway: in a PR queue, a two-week-old diff with ten commits stacked on it looks exactly like one that landed yesterday. You cannot see which debt is compounding.

Debtflow uses a different accounting model — **every unreviewed agent commit is an interest-bearing debt**:

| Verb | Meaning |
|---|---|
| `debtflow init` | Scan the repo for untracked agent commits and record each as a debt entry (each with a savepoint ref pointing at the pre-change tree) |
| `debtflow board` | Print the ledger: each entry's age and three interest columns, oldest debt first |
| `debtflow paydown <id>` | Reviewed and cleared — the interest freezes at its current reading |
| `debtflow refinance <id1> <id2>` | Bundle two small debts into one review — both are marked refinanced and a fresh entry covers their combined range |
| `debtflow default <id>` | Default: restore the pre-change tree from the savepoint and commit it — the debt is wiped, along with everything stacked on it |

The "interest" is not a metaphor. It is three machine-checkable numbers, recomputed from the git graph on every read:

| Column | Meaning |
|---|---|
| DEPENDENT COMMITS | Commits stacked on the change along the first-parent line up to HEAD (anchored at the merge base, so commits folded in by side-branch merges do not count) |
| TOUCHED FILES | Files in the change's own diff |
| BLAST RADIUS | Files touched by those stacked commits beyond the change's own file set |

There is deliberately no LLM in the loop: interest must be machine-checkable for the ledger to be trusted. Git is the single source of truth; the ledger is derived state you can rebuild from history at any time.

## <img src="https://cdn.jsdelivr.net/npm/@tabler/icons@3.34.1/icons/outline/bolt.svg" height="22" alt="">&nbsp;Install

Requires Go 1.24+ (or grab a [release binary](https://github.com/SuperMarioYL/debtflow/releases) for macOS / Linux / Windows on amd64 / arm64):

```bash
go install github.com/SuperMarioYL/debtflow@latest
```

## <img src="https://cdn.jsdelivr.net/npm/@tabler/icons@3.34.1/icons/outline/terminal-2.svg" height="22" alt="">&nbsp;Quickstart

Rather not try it on a real repo? The bundled seed script builds a scratch repository with three agent commits ([examples/seed-demo-repo.sh](examples/seed-demo-repo.sh)):

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/SuperMarioYL/debtflow/main/examples/seed-demo-repo.sh)
cd debtflow-demo-repo

# 1. Register the debt: scan for agent commits, record each entry + savepoint ref
debtflow init

# 2. Read the ledger: three open debts, oldest first
debtflow board
```

Now land one more agent commit stacked on top and read the ledger again — interest rises, the new commit is auto-imported:

```bash
cat > service/interest.go <<'EOF'
package service

// Rate is the per-item interest rate of the toy ledger.
const Rate = 1
EOF
git add -A
GIT_AUTHOR_NAME="Claude" GIT_AUTHOR_EMAIL="claude@anthropic.com" \
GIT_COMMITTER_NAME="Claude" GIT_COMMITTER_EMAIL="claude@anthropic.com" \
  git commit -q -m "feat: make the interest rate a constant"

debtflow board   # the oldest debt's interest rose; Auto-imported 1 new agent commit(s)
```

Then settle:

```bash
debtflow paydown <id>            # reviewed it — clear the debt
debtflow refinance <id1> <id2>   # bundle two changes into one review
debtflow default <id>            # roll back to the savepoint — wipe the debt
```

On your own repository: `cd your-repo && debtflow init && debtflow board`. Any git host works — GitHub, Gitee, self-hosted GitLab, anything — because debtflow only talks to local git history.

## <img src="https://cdn.jsdelivr.net/npm/@tabler/icons@3.34.1/icons/outline/list-details.svg" height="22" alt="">&nbsp;Commands

| Command | What it does |
|---|---|
| `debtflow init` | Scan for untracked agent commits, record each as a ledger entry with a savepoint ref |
| `debtflow board` | Print the ledger table (age + three interest columns + status), auto-import new agent commits, reaccrue every open entry |
| `debtflow paydown <id>` | Mark a reviewed entry paid; its interest freezes |
| `debtflow refinance <id1> <id2>` | Bundle two open entries into one, covering their combined range |
| `debtflow default <id>` | Restore the savepoint tree and commit it; the entry is marked defaulted |

Agent detection is case-insensitive substring matching over the author name / email and Co-Authored-By trailers, defaulting to `claude, codex, cursor`. If a human teammate would match by accident (say, Claudine), narrow the list:

```bash
debtflow init --agents claude,codex,cursor-agent
```

Two known edges, worth knowing before you use the verbs:

- `default` restores the **whole savepoint tree** — it is not a range revert, so commits stacked on top of the defaulted entry are discarded along with it. That is the point (stop the bleeding), but think before defaulting the oldest debt.
- The ledger lives in a local `.debtflow/ledger.jsonl` (auto-added to .gitignore); v0.1 has no sync or shared view — that is what the v0.2 team ledger server is for.

## <img src="https://cdn.jsdelivr.net/npm/@tabler/icons@3.34.1/icons/outline/components.svg" height="22" alt="">&nbsp;How it works

One binary, three internal packages, one external dependency: git itself.

```
debtflow (single binary)
├── gitrepo     — shells out to local git: first-parent walking, agent-commit
│                 detection, savepoint recording and restore, merge-base anchoring
├── ledger      — append-only JSONL store + pure accrual functions (deterministic, replayable)
└── cli/render  — cobra commands + the lipgloss board table renderer
```

- **Savepoints**: registering a debt writes a ref (`refs/debtflow/<id>`) pointing at the tree immediately before the change. `default` restores that tree instead of doing a range revert — a range revert would skip the range start.
- **Append-only ledger**: one line per entry in `.debtflow/ledger.jsonl`. Entries are never deleted or reordered; state changes are appended to the entry's history as events (imported / accrued / paid / refinanced / defaulted), and the interest fields only carry the latest state those events describe. Writes go through a temp file and a rename so a crash cannot tear a line; a read that changed nothing leaves the file untouched.
- **Every read is an accrual pass**: `board` and all three verbs recompute open entries' interest before doing their work — there is no separate accrue command to remember.

## <img src="https://cdn.jsdelivr.net/npm/@tabler/icons@3.34.1/icons/outline/message-circle-question.svg" height="22" alt="">&nbsp;FAQ

**Is this the same thing as review-inbox tools (like Pinrail)?**
No. An inbox queues items flatly for blocking approval; the items do not change. Debtflow does the accounting — interest grows over time, and paydown / refinance / default are ledger verbs, not inbox checkboxes. They compose: the inbox decides what to review now, the ledger shows what deferring is costing.

**Why not use an LLM to judge risk?**
On purpose. An LLM's judgment is not reproducible; a ledger's numbers must be machine-checkable. These three interest columns are deterministic math over the git graph — anyone re-running gets the same numbers. Pair Debtflow with whatever tool judges the diff itself.

**Isn't this just a git-log script?**
You could write one — the accrual logic is deterministic by design. What Debtflow ships is the manual work you would otherwise maintain yourself: savepoint refs, the append-only ledger, the three settle verbs, and auto-importing new agent commits on every read.

## <img src="https://cdn.jsdelivr.net/npm/@tabler/icons@3.34.1/icons/outline/building-bank.svg" height="22" alt="">&nbsp;Pricing

The solo CLI is open source and free; the ledger lives in your own repository. The team problem is that the ledger is not on one page — who owes the most debt, whether the team's review liability grew this week — a local file cannot answer that.

The **team ledger server** planned for v0.2 upgrades the local ledger into a shared team view:

| | Solo CLI (v0.1, shipped) | Team ledger server (v0.2, planned) |
|---|---|---|
| Shape | Open source single binary | Hosted service (self-hosted edition open source and free) |
| Ledger | Local `.debtflow/ledger.jsonl` | Shared team ledger |
| Debt assignment, merge-gate hook | — | Included |
| Price | Free | ¥79/seat/month (free up to 5 seats), ¥790/seat/year; $15/seat/month globally |

The path: let the solo CLI earn its place inside a team first, then open a 14-day hosted pilot; if no pilot team converts, the server gets cut — that decision rule is written into the roadmap. Want in on the pilot, or have thoughts on pricing? Say so in [Discussions](https://github.com/SuperMarioYL/debtflow/discussions).

## <img src="https://cdn.jsdelivr.net/npm/@tabler/icons@3.34.1/icons/outline/rocket.svg" height="22" alt="">&nbsp;Roadmap

- [x] **v0.1 (current)** — `init` / `board` / `paydown` / `refinance` / `default`; savepoint refs; interest recomputed from the git graph on every read; append-only JSONL ledger
- [ ] **v0.2** — team ledger server: shared ledger, debt assignment, merge-gate hook; hosted paid tier (see [Pricing](#-pricing))

<p align="center"><sub><a href="./LICENSE">MIT</a> © 2026 SuperMarioYL</sub></p>
