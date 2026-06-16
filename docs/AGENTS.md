# `docs/` Guide

Use docs as the durable source of truth for decisions, not as a dump of every
agent step.

Routing:

- `architecture/`: durable technical decisions and contracts.
- `runbook/`: operational procedures.
- `work/active/`: temporary notes for in-progress multi-step work.
- `work/done/`: completed notes worth keeping.
- `work/tmp/`: sketches, comparisons, and disposable exploration.
- `ROADMAP.md`: MVP scope, sequencing, and future milestones.

CI and test strategy belongs in `architecture/ci-testing.md`. Test run output
does not belong in durable docs unless it is evidence for an active work note.

Implementation defaults such as frontend adapter, Go router, SQLite driver,
object storage SDK, container base, and ID format belong in
`architecture/implementation-choices.md`.

Do not create sprint directories by default. Create a work note only when the
task needs a side-channel plan, decision log, or evidence record.

When a temporary decision becomes durable, move the final decision into
`architecture/` or `runbook/` and delete or archive the temporary note.

Documentation principles:

- Public docs should describe BurnLink as a production-facing open-source
  self-hosted product.
- Document residual risk plainly. Do not overclaim physical deletion,
  anonymity, or perfect secrecy.
- Prefer one durable decision in `architecture/` over repeated explanations in
  temporary notes.
- Directory-local `AGENTS.md` files carry working rules; avoid creating a
  separate principle document unless the rules outgrow this structure.
