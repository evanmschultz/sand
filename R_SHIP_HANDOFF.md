# R-SHIP-SAND — Handoff to Dev

**Date:** 2026-05-30
**Tillsyn refinement:** `3a770b48-1a9a-41b1-a31a-aad7961d4b27` (R-SHIP-SAND)
**Source-of-truth sibling:** `ta` (architecture) + `tillsyn` (CASCADE_METHODOLOGY.md canon)
**Memory rule:** `feedback_no_sibling_git_mutations` — orch wrote files only; ALL git is yours.

sand is an existing Go-only sibling. Orch sync'd the agent architecture (Batch 1) + canonicalized the magefile (Batch 2). No git touched.

---

## Batch 1 — agent infrastructure (cp from ta, byte-identical)

| Path | Source |
|---|---|
| `bin/agent-dispatch.sh` + `bin/agent-audit-toon.py` | ta |
| `.claude/hooks/ta_action_gate.py` + `.claude/hooks/post_tooluse_agent_audit.py` | ta |
| `.claude/agents/<persona>/settings.json` × 7 (Go-only) | ta (`mcp__tillsyn__*` stripped) |
| `.claude/agents/<persona>.md` × 7 | ta (Path B 2.2.A — tillsyn refs inert) |
| `.claude/settings.json` | PostToolUse Agent matcher added (PreToolUse Bash already present) |
| `CASCADE_METHODOLOGY.md` | tillsyn (sha256 `87708e81…`) |

## Batch 2 — magefile canonicalized

`magefile.go` + `magefile_test_targets.go` now expose the canonical 12-target shape (verified via `mage -l`). sand's mage/sh style + `Install` (binary + backends/chains seed) preserved — NO laslig dependency added.

- **Renames:** `Fmt`→`Format`, `FmtCheck`→`FormatCheck`, `Check`→`CI`. Hyphenated aliases preserved (`fmt`, `fmt-check`, `format-check`, `check`).
- **New targets:** `FormatFile`, `VetPkg`, `RacePkg`, `Race`, `Cover`. `TestFunc` changed from 1-arg `(pattern)` + `TA_TEST_PKG` env → 2-arg `(pkg, testName)` with `-race -count=1`. `TestPkg` now `go test -count=1 <pkg>` (was `./`-prefix-only).
- **CI body:** FormatCheck → Vet → Cover (race+cover) → Tidy.

`mage -l` lists 14 targets: the canonical 12 + Install + Cover.

---

## Verify + commit (YOUR hands)

```sh
cd /Users/evanschultz/Documents/Code/hylla/sand/main
git status                       # review what changed
mage ci                          # FormatCheck + Vet + Cover(race+cover) + Tidy — must pass green
# optional: smoke a persona — dispatch ta-go-builder, confirm git commit is hook-blocked
git add bin/ .claude/ CASCADE_METHODOLOGY.md magefile.go magefile_test_targets.go R_SHIP_HANDOFF.md
git commit -m "chore: sync agent infra from ta + canonical magefile (12-target shape)"
git push origin main
gh run watch --exit-status       # confirm CI green
```

**Hylla ingest** (after CI green):
```
mcp__hylla__hylla_ingest(source_url="https://github.com/evanmschultz/sand.git", ref="<SHA>", branch="main", enrichment_mode="full_enrichment", stream=true)
```

Then tell orch the SHA + ingest task id so R-SHIP-SAND closes.

## Note: CLAUDE.md

**sand's `CLAUDE.md` was caveman'd in the P5 pass (2026-05-30): 23,254 → ~22,292 chars.** Fixed: 3 dangling refs to the now-deleted `../ta/main/docs/cascade-methodology.md` (methodology refs → in-repo `CASCADE_METHODOLOGY.md`; the project-docs companion ref → `../ta/main/docs/cascade-reference.md`); collapsed the bloated rule-2 atomicity essay → terse; dropped the doubled record-id-convention reminder (the full rule already lives once in the Cascade-managed-development section). No dated headers / stale personas / droplet-auto_spawn claims were present. If sand's `CLAUDE.md` is a ta record (`agents_md` schema), run `ta index rebuild` once after pulling. Stage `CLAUDE.md` with this commit.

## Note: GH workflow

The magefile canonicalization renamed targets (`Fmt`→`Format`, `FmtCheck`→`FormatCheck`, `Check`→`CI`) with the hyphenated/old aliases preserved (`fmt`, `fmt-check`, `format-check`, `check`), so an existing `.github/workflows/` calling `mage check` still works via the alias. Still — **verify `.github/workflows/` calls the canonical `mage ci`** (the gate) and that `mage ci` is green in CI after push. Note `TestFunc` is now 2-arg `(pkg, name)` and `TestPkg` is a plain `go test`; if a workflow invokes those directly, update accordingly. Orch did NOT touch your workflow — this is your check.

## Pre-existing WIP

Orch did not touch any uncommitted work that was already in your tree. Review `git status` and stage selectively if you have unrelated WIP.
