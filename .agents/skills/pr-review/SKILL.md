---
name: pr-review
description: Use when reviewing a BurnLink pull request, especially as a spawned reviewer subagent. Produces a structured, read-only review against BurnLink's issue, milestone, label, security, directory, contract, and verification rules.
---

# BurnLink PR Review

Use this skill for PR review. The reviewer is read-only unless the user
explicitly asks for fixes.

## Inputs

Required context:

- repository path
- PR number or branch
- linked issue
- milestone
- changed files

Read first:

1. `AGENTS.md`
2. nearest `AGENTS.md` files for changed paths
3. `docs/architecture/agent-workflow.md`
4. `docs/architecture/ci-testing.md`
5. architecture docs named by root `AGENTS.md` for affected surfaces

## Review Checks

Check these in order:

1. Scope: PR matches the linked issue and milestone.
2. Metadata: type, area, risk, and review labels match changed paths.
3. Security: no plaintext, passphrases, derived keys, credentials, or private
   deployment values are exposed.
4. Ownership: API owns `api.db`; worker owns `worker.db`; NATS carries IDs and
   safe metadata only.
5. Contracts: OpenAPI/event/env/docs are updated with behavior changes.
6. Directory rules: new directories have clear owners and local `AGENTS.md`
   where required.
7. Tests: local and GitHub checks match the risk and affected areas.
8. Regression risk: identify missing tests, unsafe defaults, or operational
   ambiguity.

## Output

Return a concise review with this shape:

```text
## BurnLink Subagent Review

Decision: approve | changes-requested

Findings:
- severity: file:line - concrete issue

Metadata:
- issue:
- milestone:
- labels:
- checks:

Residual Risk:
- ...
```

If there are no blocking findings, say `Decision: approve` and list any
non-blocking residual risk. Do not apply GitHub labels yourself. The
orchestrating agent or maintainer records the review comment and controls
`review:*` labels.
