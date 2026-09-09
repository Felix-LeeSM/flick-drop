# `cmd/flick/` Guide

This directory is the `flick` command-line client entrypoint.

Directory structure:

- `main.go`: parse flags, read terminal input, dispatch into `internal/flickcli`,
  format output.
- `AGENTS.md`: local process rules.
- Do not add subdirectories here.

Unlike `flick-api` and `flick-worker`, this is a short-lived user-facing
process, not a service. It has no config file, no database, and no telemetry.

## What belongs here

- Flag and subcommand parsing (`flag`, not a CLI framework — the surface is two
  commands).
- Terminal interaction: passphrase prompts, TTY detection, exit codes.
- Rendering results for humans and for `-json`.

Encryption, HTTP calls, and share-link handling belong in
`internal/clientcrypto` and `internal/flickcli`.

## Input rules

- **A passphrase is never a flag value.** Command arguments are visible to every
  process through `ps` and land in shell history. Read it from the terminal
  without echo, or from `FLICK_PASSPHRASE` for scripted use.
- Prompts and status lines go to stderr; the payload goes to stdout. That is
  what makes `flick open <link> > secret.txt` write only the secret.
- Piped stdout stays byte-exact — no trailing newline is added unless stdout is
  a terminal.

## Releases

Binaries are built by `.github/workflows/release-cli.yml` on a `cli/v*` tag.
Server images publish on `v*` tags and are deliberately a separate namespace, so
a CLI release does not republish `flick-api`, `flick-worker`, and `flick-web`.

`version` is stamped with `-ldflags "-X main.version=..."` at release time; a
`go install` build falls back to the module version from the build info.
