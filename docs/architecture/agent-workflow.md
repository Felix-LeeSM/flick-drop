# Agent Workflow

BurnLink uses directory-local `AGENTS.md` files and temporary work notes instead
of a full `memory/` tree.

The goal is that a request like "implement this feature" is converted into a
repeatable GitHub workflow before code changes begin.

## Entry Rule

1. Read root `AGENTS.md`.
2. Read the nearest `AGENTS.md` for files being changed.
3. Read matching `docs/work/active/*.md` when one exists.
4. Read the architecture document named by root `AGENTS.md` for the affected
   surface.

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

Agents must not start implementation on `main`. If `main` is checked out, create
a topic branch first.

## Milestones

Milestones represent release-sized product slices, not individual PRs.

Initial milestone shape:

- `M0: Repository Foundation`
- `M1: Local Secret MVP`
- `M2: Worker and Cleanup`
- `M3: OCI Object Storage`
- `M4: k3s Deployment`

Each milestone should have:

- goal
- included scope
- non-goals
- expected verification level

## Issues

Issues are the default unit of work. A normal issue should be small enough for
one PR.

Required issue content:

- goal
- scope
- acceptance criteria
- affected areas
- required reading
- test plan
- out of scope

When a user request does not map to an existing issue, create or draft the issue
before implementation. Do not use issue text as a substitute for updating
durable architecture docs when a decision becomes permanent.

## Branches

Branch names should include the issue number.

```text
feature/<issue-number>-<slug>
fix/<issue-number>-<slug>
docs/<issue-number>-<slug>
chore/<issue-number>-<slug>
```

## Pull Requests

Every PR must:

- link an issue with `Closes #number`, `Fixes #number`, or `Resolves #number`
- have a milestone
- use the PR template sections
- have no unchecked checklist items before merge
- stay within the default PR size target unless a size exception is documented
- have exactly one `type:*` label
- have at least one `area:*` label
- have exactly one `risk:*` label
- pass `Repo checks`
- pass `NATS smoke`
- pass `Review gate`

The PR body should carry acceptance criteria and verification evidence. Durable
decisions still belong in `docs/architecture/` or `docs/runbook/`.

Default PR size target:

- 10 changed files or fewer.
- 500 changed lines or fewer.
- one issue, one behavior change, one review story.

Hard review-gate threshold:

- more than 20 changed files or more than 1,000 changed lines requires
  `risk:high`
- large PRs must include a non-empty size exception in the `## PR Size` section

Prefer splitting work before using a size exception. Good exceptions include
initial scaffold, generated lockfile churn, or a mechanical rename that is
easier to review as one unit.

## Labels

Label families:

- `type:*`: what kind of work this is.
- `area:*`: which repo/product surfaces are affected.
- `risk:*`: expected review depth.
- `review:*`: current review decision.

`Review gate` derives required area and risk labels from changed paths. If a PR
changes sensitive areas such as workflows, CI scripts, contracts, deploy assets,
storage, or security code, it must use `risk:medium` or `risk:high`.

## Review Gate

BurnLink starts with label-based review because a single GitHub account cannot
approve its own PR for native required reviews.

Review has two parts:

1. spawn a reviewer subagent with `.agents/skills/pr-review/SKILL.md`
2. record the review result as a PR comment before applying review labels

The gate is implemented by `.github/workflows/review-gate.yml` and
`scripts/ci/review-gate.sh`.

The workflow uses `pull_request_target` but only checks GitHub metadata:

- PR title/body
- milestone
- labels
- changed file paths
- PR size
- label event timestamp
- review comment timestamp

It does not checkout, build, or execute untrusted PR code. Checkout is pinned to
the base branch commit so the gate script comes from already-reviewed main.

Required review labels:

- `review:approved` is required.
- `review:changes-requested` blocks merge.
- `review:approved` must be applied after the latest approving subagent review
  comment.

If a new commit is pushed after approval, the existing subagent review comment
no longer satisfies the gate because it references the previous head SHA. Review
the new head, add a new review comment, then remove and re-apply
`review:approved`.

Required review comment:

- a PR comment containing `## BurnLink Subagent Review` is required
- the comment must include `Decision: approve`
- the comment must include `Head: <current PR head SHA>`
- the comment must be written by an owner, member, or collaborator
- `review:approved` must be applied after that approving subagent review comment

The main agent may orchestrate review, summarize findings, and update PR
metadata, but it must not treat its own implementation pass as the review. A
separate reviewer subagent should inspect the PR in read-only mode using the
repo-owned review skill.

The review gate publishes an explicit `Review gate` commit status to the PR head
SHA. Branch protection should require that status context, not the
`pull_request_target` workflow job name.

When BurnLink has another maintainer or bot account that can perform real
reviews, the repository can move from label-based review to native required
approving reviews.

## Work Notes

Use `docs/work/active/<date>-<slug>.md` only for work that needs durable
side-channel context:

- multi-step implementation
- decision comparison
- cross-service contract change
- security/storage/env change
- verification evidence that should be preserved

When the work finishes:

- move durable conclusions to `docs/architecture/` or `docs/runbook/`
- move useful history to `docs/work/done/`
- delete disposable scratch from `docs/work/tmp/`

## Memory Tree Deferral

Do not introduce a project-wide `memory/` tree yet. Add it later only if local
guides become too large or rules start repeating across several directories.
