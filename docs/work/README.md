---
type: Work Guide
title: Work Notes Guide
description: Structure and lifecycle rules for active, done, and tmp work notes under docs/work/.
tags: [docs, work-notes]
status: stable
---

# Work Notes

This directory replaces sprint artifacts for Flick.

- `active/`: in-progress work notes the agent should read before editing.
- `done/`: completed work notes worth keeping as history.
- `tmp/`: disposable explorations, comparisons, and sketches.

Use a work note when a task has several decisions, multiple implementation
steps, or verification evidence that should survive context changes.

Suggested shape:

```markdown
# <date>-<slug>

## Goal

## Scope

## Decisions

## Checks

## Open Questions
```
