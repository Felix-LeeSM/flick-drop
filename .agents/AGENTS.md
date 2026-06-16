# `.agents/` Guide

This directory is for repo-owned agent skills or thin wrappers if they become
useful.

Current policy:

- Do not copy a full memory tree from another project.
- Add a skill only when repeated BurnLink work needs a reusable method.
- Skill bodies live under `.agents/skills/<name>/SKILL.md`.
- Keep skills repo-agnostic where practical and link to BurnLink docs for local
  policy.
- Directory-local `AGENTS.md` files and `docs/work/*` are the first-line
  workflow mechanism.

Agent workflow principles:

- Keep the main assistant responsible for repo state, final decisions, and
  verification.
- Reusable skills should encode stable workflow, not one-off project memory.
- If a rule belongs to a code or docs boundary, put it in that directory's
  `AGENTS.md` instead of hiding it in a skill.
- Do not require agents to read a large memory tree before ordinary edits.
