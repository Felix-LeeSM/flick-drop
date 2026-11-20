---
type: Architecture
title: Agent Workflow
description: GitHub issue/PR workflow, label-based review gate rules, OKF v0.2 document format conventions, and document size limits enforced in CI.
tags: [architecture, workflow, okf, review-gate]
status: stable
---

# Agent Workflow

Flick uses directory-local `AGENTS.md` files, OKF v0.2 metadata/indexes, and temporary work notes instead of a full `memory/` tree.

The goal is that a request like "implement this feature" is converted into a repeatable GitHub workflow before code changes begin.

## Entry Rule

1. Read root `AGENTS.md`.
2. Read the nearest `AGENTS.md` for files being changed.
3. Use root `index.md` (and subdirectory `index.md` files) to locate relevant architecture docs, runbooks, and active work notes.
4. Read matching `docs/work/active/*.md` when one exists.
5. Read the architecture document named by root `AGENTS.md` for the affected surface.

## Default Work Path

```text
user request
  -> inspect current repository and GitHub state
  -> find or create milestone
  -> find or create issue
  -> define acceptance criteria
  -> create branch from latest main
  -> implement within issue scope
  -> run local checks
  -> open PR
  -> pass GitHub checks
  -> pass label-based review gate
  -> squash merge
```

Agents must not start implementation on `main` and must not commit directly to local `main`. If `main` is checked out, create a topic branch first. Open one PR per work unit; if a unit has no existing issue, create a tracking issue so the PR satisfies `Closes/Fixes/Resolves #number`.

## Milestones

Milestones represent release-sized product slices, not individual PRs.

Initial milestone shape (the GitHub milestone titles):

- `M0: Repository Foundation`
- `M1: Local Secret MVP`
- `M2: Worker and Cleanup`
- `M3: OCI Object Storage`
- `M4: k3s Deployment`

Product milestones from M8 onward are described in [ROADMAP.md](../ROADMAP.md); list every milestone with its state using `gh api 'repos/Felix-LeeSM/flick-drop/milestones?state=all'`.

Each milestone should have: goal, included scope, non-goals, and expected verification level.

## Issues

Issues are the default unit of work. A normal issue should be small enough for one PR and include goal, scope, acceptance criteria, affected areas, required reading, test plan, and out of scope.

When a user request does not map to an existing issue, create or draft the issue before implementation. Do not use issue text as a substitute for updating durable architecture docs when a decision becomes permanent.

## Branches

Branch names should include the issue number: `feature/<issue-number>-<slug>`, `fix/<issue-number>-<slug>`, `docs/<issue-number>-<slug>`, `chore/<issue-number>-<slug>`.

## Pull Requests

Every PR must:

- target `main` as base (`scripts/ci/review-gate.sh` rejects non-`main` bases)
- link an issue with `Closes #number`, `Fixes #number`, or `Resolves #number`
- have a milestone
- use all PR template sections (`## Summary`, `## Linked Issue`, `## Acceptance Criteria`, `## PR Size`, `## Test Plan`, `## Review Notes`)
- have no unchecked `- [ ]` checklist items before merge
- stay within the default PR size target (≤10 changed files, ≤500 changed lines) unless a size exception is documented
- have exactly one `type:*` label, at least one `area:*` label, and exactly one `risk:*` label
- pass `Repo checks`, `NATS smoke`, and `Review gate`

The PR body should carry acceptance criteria and verification evidence. Durable decisions still belong in `docs/architecture/` or `docs/runbook/`.

The default PR size target is one issue, one behavior change, one review story. Hard review-gate size threshold: more than 20 changed files or more than 1,000 changed lines requires `risk:high` and a non-empty `Size exception:` entry in `## PR Size`.

Prefer splitting work before using a size exception. Good exceptions include initial scaffold, generated lockfile churn, or a mechanical rename that is easier to review as one unit.

## Labels

Label families: `type:*`, `area:*`, `risk:*`, `review:*`.

`scripts/ci/review-gate.sh` derives required `area:*` and `risk:*` labels from changed paths. Pre-compute all required labels from `gh pr diff --name-only` when opening the PR to avoid round-trips:

- `.github/*` → `area:github`
- `.github/workflows/*`, `scripts/ci/*` → `area:ci` + `risk:medium` or `risk:high`
- `.github/workflows/review-gate.yml`, `scripts/ci/review-gate.sh`, `.agents/skills/pr-review/SKILL.md` → `risk:high`
- `docs/architecture/security-model.md`, `contracts/age-format-v1.md`, `internal/clientcrypto/*`, `internal/secrets/*`, `web/src/lib/crypto/*` → `area:security` + `risk:medium` or `risk:high`
- `internal/storage/*` → `area:storage` + `risk:medium` or `risk:high`
- `contracts/*` → `area:contracts` + `risk:medium` or `risk:high`
- `deploy/*` → `area:deploy` + `risk:medium` or `risk:high`
- `cmd/flick-api/*`, `internal/httpapi/*` → `area:api`
- `cmd/flick-worker/*`, `internal/worker/*` → `area:worker`
- `web/*` → `area:web`
- `docs/*` → `area:docs`

## Review Gate

Flick uses label-based review because a single GitHub account cannot approve its own PR for native required reviews. Review results are always posted as PR comments.

1. Spawn a separate reviewer subagent in read-only mode with `.agents/skills/pr-review/SKILL.md`.
2. Post the review result as a PR comment containing `## Flick Subagent Review`, `Decision: approve`, and `Head: <current PR head SHA>` (authored by an owner, member, or collaborator).
3. Apply `review:approved` **after** the approving comment (`review:changes-requested` blocks merge).

The main agent may orchestrate review, summarize findings, and update PR metadata, but it must not treat its own implementation pass as the review. If a new commit is pushed after approval, the existing review comment no longer satisfies the gate because it references the previous head SHA: review the new head, add a new review comment, then remove and re-apply `review:approved`.

The gate is implemented by `.github/workflows/review-gate.yml` and `scripts/ci/review-gate.sh`. Since 2026-11-16 (`#431`), it triggers on `pull_request` (because `pull_request_target` hit startup failures after a GitHub incident) while still checking out only the base commit so untrusted PR head code is never executed. The gate checks only GitHub metadata (PR title/body, milestone, labels, changed file paths, PR size, label event timestamp, review comment timestamp); it does not build or execute PR code. It publishes the required `Review gate` commit status to the PR head SHA. Branch protection should require that status context, not the job name of `.github/workflows/review-gate.yml`. When Flick has another maintainer or bot account that can perform real reviews, the repository can move from label-based review to native required approving reviews.

Operational notes for PR merges and races:

- **Head SHA changes and stacked PRs:** Because `main` requires branches to be up to date (`strict`), rebasing or updating a branch changes the head SHA and invalidates earlier `Head: <sha>` comments. For stacked PRs (each targeting `main`), after squash-merging the lower PR, rebase the next PR with `git rebase --onto origin/main <old-base-tip>`, verify with `git range-diff`, post a new approving comment with the new head SHA, then remove and re-apply `review:approved` (wait 30+ seconds between remove and add or use REST `DELETE`/`POST` on `/issues/{n}/labels` so the unlabeled event does not race the new labeled run).
- **Missing PR checks (`workflow_dispatch` caveat):** Checks triggered via `gh workflow run CI --ref <branch>` attach to the commit check-runs API, not the PR's `statusCheckRollup`, so branch protection stays blocked. Retrigger PR-context CI without changing the head SHA using `gh pr close <n> && gh pr reopen <n>`.
- **Merging from a git worktree:** Omit `--delete-branch` with `gh pr merge --squash` when working inside a linked worktree (`main` is already checked out in the primary worktree; GitHub auto-deletes merged head branches remotely). Auto-merge is disabled in this repository, so `gh pr merge --auto` is rejected; wait for checks and merge directly.
- **Labels that silently do not change:** `gh pr label` / `gh pr remove-label` sometimes do nothing (output that looks like `gh pr view` is the sign). Use REST `DELETE /repos/{owner}/{repo}/issues/{n}/labels/{label}`, wait 30+ seconds, then `POST /repos/{owner}/{repo}/issues/{n}/labels`, and verify the label set in the response.
- **Reading a gate failure:** the job summary shows only script errors; the failing condition is in the Validate step log. Read it with `gh run view <run> --log | rg review-gate` to tell a real validation failure from a label/comment timing race.
- **Stacked PR marked "merged" (#346):** a stacked PR whose base is a feature branch is marked merged by GitHub when its head commit is pushed into that base branch (the merge commit is the head SHA, and nothing reaches `main`). Check exact remote branch names with `git branch -r` before pushing to another branch.
- **Manual gate bypass:** Never post a manual `Review gate` commit status without explicit user approval for that specific incident.

## Work Notes

Use `docs/work/active/<date>-<slug>.md` only for work that needs durable side-channel context (multi-step implementation, decision comparison, cross-service contract change, security/storage/env change, or verification evidence). When finished, move durable conclusions into `docs/architecture/` or `docs/runbook/`, move useful history to `docs/work/done/`, and delete disposable scratch from `docs/work/tmp/`.

## Memory Tree Deferral

Do not introduce a project-wide `memory/` tree. Durable project facts live in directory-local `AGENTS.md` files, `docs/architecture/`, and `docs/runbook/`, located through root `index.md`. A harness-local agent memory (for example Claude Code's auto-memory directory) keeps only facts about the agent harness itself, never project rules or decisions; move any project fact found there into the owning `AGENTS.md` or doc.

## OKF v0.2 Format and Document Size Limits

Agent-facing Markdown documents in this repository follow Open Knowledge Format (OKF) v0.2:

- Every governed document starts with a single YAML frontmatter block on line 1 with key order `type`, `title`, `description`, and optional `tags` and `status` (`draft`, `stable`, or `deprecated`).
- Root `index.md` carries only `okf_version: "0.2"` in its frontmatter; subdirectory `index.md` files have no frontmatter. Every governed document is listed in its nearest ancestor `index.md` as `* [Title](relative/path.md) - Description`.
- `log.md` files record dated history under `## YYYY-MM-DD` headings in reverse chronological order so `AGENTS.md` files stay free of dated changelogs.

### Type Vocabulary

| `type` | Used for |
| --- | --- |
| `Agent Guide` | Directory-local and root `AGENTS.md` guides |
| `Architecture` | Durable design and contract docs in `docs/architecture/*.md` |
| `Contract` | Protocol and interface specifications in `contracts/*.md` |
| `Runbook` | Operational procedures in `docs/runbook/*.md` |
| `Roadmap` | Product scope and milestones (`docs/ROADMAP.md`) |
| `Reference` | Technical reference READMEs and historical orientation docs (`deploy/base/monitoring/README.md`, `web/src/lib/i18n/README.md`, `flick-service-concept.md`) |
| `Work Guide` | Work-note directory guide (`docs/work/README.md`) |
| `Work Note` | Active multi-step notes (`docs/work/active/*.md`) |
| `Ops Note` | Draft notes staged outside git for the private ops repo |
| `Memory` | Harness-only Claude Code memory files |

### Exempt Paths

The paths below are excluded from OKF frontmatter and `index.md` checks (`scripts/ci/agent-docs.sh` is the enforcement twin of this table):

| Path | Reason |
| --- | --- |
| `README.md` | Public product introduction for humans and GitHub visitors |
| `.github/pull_request_template.md` | GitHub copies the file into every PR body, and `scripts/ci/review-gate.sh` parses that body |
| `.agents/skills/*/SKILL.md` | Codex skill validator rejects unknown frontmatter keys |
| `web/**` (except `web/AGENTS.md` and `web/src/lib/i18n/README.md`) | Frontend application sources and local prototype files |
| `docs/work/done/**` | Archived historical records preserved byte-for-byte |

### Document Size Limits

`scripts/ci/agent-docs.sh` (invoked from `scripts/ci/all.sh`) enforces the repository rows. The two `.agent-memory/` rows cover a git-excluded local directory that CI never sees; agents check them by hand when editing that directory. Never delete rules or rationale to fit a limit — split the document and link to the detail doc instead.

| Target | Limit | Reason |
| --- | --- | --- |
| Sum of `AGENTS.md` chain from root to deepest directory | 24 KiB (24,576 B) | Leaves headroom under Codex's default 32 KiB `project_doc_max_bytes` truncation ceiling |
| Root `AGENTS.md` | 10 KiB (10,240 B) | Loaded at the start of every session |
| Single subdirectory `AGENTS.md` | 8 KiB (8,192 B) | Move detailed specs to `docs/architecture/` and link from the local guide |
| Single `docs/**/*.md` document | 32 KiB (32,768 B) | Split oversized specs into topic-focused documents |
| `.agent-memory/MEMORY.md` (local harness) | 150 lines, 8 KiB | Claude Code auto-loads only the first 200 lines of `MEMORY.md` |
| Single `.agent-memory/*.md` file (local harness) | 2 KiB | Larger files indicate multiple facts that should be split or moved into repo docs |
