---
type: Agent Guide
title: cmd/flick Guide
description: Entrypoint rules, input/passphrase security, default host policy, and release workflow for the flick CLI client.
tags: [agent-guide, go, cli]
---

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

- Flag and subcommand parsing (`flag`, not a CLI framework — the surface is
  `send`, `open`, `request`, `submit`, `receive`, `live`, `join`, and
  `version`).
- Live-transfer commands (`cmd/flick/main.go` `runLive`, `runJoin`) confirm the
  receiver by its 3-digit number: interactive on a terminal, `-confirm auto`
  (first waiting row) or `-confirm <nnn>` (that number) for scripts, and a
  refusal before `/init` when there is neither a terminal nor `-confirm`, so
  no room is burned.
- Terminal interaction: passphrase prompts, TTY detection, exit codes.
- Rendering results for humans and for `-json`.

Request-link commands (`cmd/flick/main.go` `runRequest`, `runSubmit`,
`runReceive`) print the two links a request produces with labels on stderr and
the bare links on stdout, and `flick receive -` (or a pipe) reads the
retrieval link from stdin so the identity in its fragment stays out of shell
history.

Encryption, HTTP calls, and share-link handling belong in
`internal/clientcrypto` and `internal/flickcli`.

## Default host

- `cmd/flick/main.go:defaultAPIURL` stays `http://localhost:8080` (matching
  `DEFAULT_API_BASE_URL` in `web/src/lib/api/secrets.ts`). Never hardcode a
  remote deployment host in source so third-party `go install` users do not
  accidentally send secrets to someone else's server. Operators and users set
  `FLICK_URL` in their shell or pass `-url` (the public demo
  `https://flick.makana.cc/` in `README.md` is the only named domain exception).

## Input rules

- **A passphrase is never a flag value.** Command arguments are visible to every
  process through `ps` and land in shell history. Read it from the terminal
  without echo, or from `FLICK_PASSPHRASE` for scripted use.
- **Decrypted values never reach stdout by default.** `flick open`,
  `flick receive`, and `flick join` save the payload to a reserved 0600 file
  (`internal/flickcli/open.go:reserveAutoDestination`) and print only its
  path, so secrets stay out of terminal scrollback, pipes, and agent or CI
  logs. `-reveal` opts into stdout — byte-exact text bytes, formatted
  credential values — and is what makes `flick open -reveal <link> >
  secret.txt` write the secret. A file secret is always saved to disk, also
  under `-reveal`, because stdout cannot be reserved for a streamed file.
  `cmd/flick/main.go:reportOpened` is the one renderer for all three
  commands, and `cmd/flick/main.go:reportOpenedResult` is its `-json` form,
  which keeps stdout to exactly one JSON object.
- Under `-json` (`cmd/flick/main.go:reportOpenedResult`) stdout is exactly one
  object. A credential's `title`, `notes`, and non-secret field values are in
  it with or without `-reveal`; secret-field values and text are in it only
  together with `-reveal`.
- Prompts, status lines, and the masked credential summary
  (`internal/flickcli/credential.go:SummaryLines`) go to stderr.
- A consumed text or credential payload whose write failed is rescued to a
  fresh 0600 file in the OS temp directory
  (`cmd/flick/main.go:writeRescueFile`), never dropped. `flick open`,
  `flick receive`, and `flick join` report an opened payload through
  `cmd/flick/main.go:finishOpened`, which calls `cmd/flick/main.go:rescue`
  when the error carries `internal/flickcli/open.go:ErrWriteAfterConsume`; a
  failed `-reveal` stdout write is marked that way too, because the consumed
  payload then exists only in this process. A streamed file secret has no
  second copy: a disk that fails mid-stream loses it
  (`internal/flickcli/AGENTS.md`).
- `cmd/flick/main.go:run` ignores `SIGPIPE` so a write to a pipe whose reader
  is gone returns an error instead of ending the process, and that error is
  returned: a share or request link, a saved path, or a live code that stdout
  could not take (`cmd/flick/main.go:printLine`) ends the command with exit 1
  instead of exit 0.
- Under `-reveal` piped text is byte-exact — no trailing newline is added
  unless stdout is a terminal. A credential prints as `Label: value` lines
  with one trailing newline (`internal/flickcli/credential.go:RevealText`).
- `flick send -credential <path|->` sends a structured credential as JSON
  validated against `contracts/credential-payload.schema.json`
  (`internal/flickcli/credential.go:ParseCredentialInput`); an invalid payload
  fails before the create request (`internal/flickcli/send.go:Send`).
- `flick send` with nothing to send and a terminal on stdin asks for the parts
  the command line left out, one at a time. It asks only about those: a flag
  that was typed is never re-asked, and a pipe or an argument means the caller
  already decided, so nothing is asked there. The secret itself is read without
  echo for the same reason it is not a flag value.

## Releases

Binaries are built by `.github/workflows/release-cli.yml` on a `cli/v*` tag.
Server images publish on `v*` tags and are deliberately a separate namespace, so
a CLI release does not republish `flick-api`, `flick-worker`, and `flick-web`.

`version` is stamped with `-ldflags "-X main.version=..."` at release time; a
`go install` build falls back to the module version from the build info.
