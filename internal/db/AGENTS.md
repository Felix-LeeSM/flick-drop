# `internal/db/` Guide

This package owns SQLite setup, migrations, transactions, and low-level database
helpers.

Directory structure:

- Keep connection setup, migration execution, and transaction helpers separate
  by file.
- If embedded migrations become large enough for a `migrations/` directory, add
  a local `AGENTS.md` there before adding SQL files.
- Do not add domain-specific repositories here unless the repository boundary is
  explicitly documented.

Rules:

- Enable WAL, foreign keys, and busy timeout consistently for every SQLite
  connection.
- Preserve API-owned and worker-owned database separation.
- Do not store plaintext secret content, passphrases, derived keys, real
  credentials, or production identifiers in migrations or fixtures.
