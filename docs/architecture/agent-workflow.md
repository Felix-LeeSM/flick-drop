# Agent Workflow

BurnLink uses directory-local `AGENTS.md` files and temporary work notes instead
of a full `memory/` tree.

## Entry Rule

1. Read root `AGENTS.md`.
2. Read the nearest `AGENTS.md` for files being changed.
3. Read matching `docs/work/active/*.md` when one exists.
4. Read the architecture document named by root `AGENTS.md` for the affected
   surface.

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
