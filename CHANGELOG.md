# Changelog

All notable changes to this project are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-11-17

Reliability release for the local ledger: four reproduced correctness fixes,
no new surface. The team ledger server moves to v0.3.

### Fixed

- Agent commit file sets are read from git's NUL-separated `--name-only`
  output. The previous `--name-status` parsing stored a rename as the single
  path `old<TAB>new` and non-ASCII paths quoted with octal escapes, so an entry
  that renamed a file did not own the new name and the next commit editing that
  file counted as blast radius beyond the entry. Renames now record the
  destination path, and Chinese/other non-ASCII paths are stored verbatim.
- `debtflow init --agents` is remembered in `.debtflow/config.json`, so the
  board's auto-import pass and later inits keep using the narrowed pattern
  list. Previously the next `debtflow board` fell back to the default patterns
  and re-imported exactly the commits init had excluded, which is the case the
  documented `--agents` narrowing exists to prevent.
- `debtflow board` orders rows by the commit instant instead of the RFC3339
  text. Commits carry their own UTC offset, so a repo mixing agent commits from
  CI (UTC) and a developer laptop (`+08:00`) could show the newest debt first;
  oldest debt is now first regardless of offset.
- A refinanced bundle's file set is the whole range between the older entry's
  range start and the newer entry's tip, not just the union of the two source
  entries. Files changed by non-entry commits inside the bundled range are part
  of the review and no longer read as blast radius beyond the bundle.

### Changed

- The roadmap now states that v0.2 is the local-ledger reliability release; the
  team ledger server is planned for v0.3. README (both locales) and the product
  site carry the same statement.

## [0.1.0] - 2026-10-14

First release: the complete v0.1 ledger — register, accrue, settle — in a
single binary.

### Added — m1_register_debt

- `debtflow init` detects agent-authored commits (author / Co-Authored-By
  substring matching, default patterns `claude, codex, cursor`, configurable
  via `--agents`), records each as a ledger entry and writes a savepoint ref
  (`refs/debtflow/<id>`) pointing at the pre-change tree.
- `debtflow board` renders the ledger table (entry, age, dependent commits,
  touched files, blast radius, status), oldest debt first.
- Append-only JSONL ledger at `.debtflow/ledger.jsonl`, auto-gitignored;
  writes go through a temp file and rename so a crash cannot tear a line.

### Added — m2_accrue_interest

- Interest is recomputed from the git graph on every read — `board` and the
  settle verbs all accrue; there is no separate accrue command.
- Dependent commits are counted along the entry's first-parent line, anchored
  at the merge base, so commits folded in by side-branch merges do not count;
  blast radius is the distinct files those commits touch beyond the entry's
  own file set.
- New agent commits are auto-imported as fresh entries whenever the board is
  read on a repo that already has a ledger.
- Interest snapshots are appended to each entry's history as `accrued` events,
  so the ledger doubles as an interest audit trail.

### Added — m3_settle_debt

- `debtflow paydown <id>` marks a reviewed entry paid and freezes its interest.
- `debtflow refinance <id1> <id2>` bundles two open entries into one review:
  both sources are marked refinanced and a fresh open entry covers their
  combined range, anchored at the older entry's savepoint.
- `debtflow default <id>` restores the recorded savepoint tree (a wholesale
  tree restore, not a range revert) and commits it; the entry is marked
  defaulted and its stacked commits are discarded along with it.
- Every verb appends its event to the entry history; settled entries' interest
  stays frozen at the last accrual.

### Added — polish

- Bilingual README (Chinese primary, English sibling).
- Hand-authored demo figure `docs/demo-ledger-board.svg` built from a real
  end-to-end run, recorded in `docs/demo-results.json`, plus a seed script
  (`examples/seed-demo-repo.sh`) that rebuilds the demo repository.
- CI workflow (build, vet, test) and a goreleaser release pipeline publishing
  linux/darwin/windows binaries for amd64/arm64 with checksums.
- Version lockstep: the CLI version default is pinned to the repo `VERSION`
  file by a test; goreleaser injects the tagged version at release time.

[0.2.0]: https://github.com/SuperMarioYL/debtflow/releases/tag/v0.2.0
[0.1.0]: https://github.com/SuperMarioYL/debtflow/releases/tag/v0.1.0
