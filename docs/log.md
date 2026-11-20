# Documentation Change Log

## 2026-11-19

* **OKF & Size Limits:** Adopted Open Knowledge Format v0.2 frontmatter, `index.md` files, and CI size/link enforcement (`scripts/ci/agent-docs.sh`). Split `docs/architecture/live-relay.md` and `docs/architecture/telemetry.md` out of oversized guides (`internal/piperelay/AGENTS.md`, `internal/telemetry/AGENTS.md`, and `docs/architecture/security-model.md`).
* **Agent Knowledge Migration:** Moved agent-agnostic development, testing, and product rules from harness-local memory into directory `AGENTS.md`, `docs/architecture/`, `docs/runbook/local-dev.md`, and `docs/ROADMAP.md`.

## 2026-11-16

* **Design Decision:** Rejected full visual redesign prototypes (D1–D5, C0–C4) and kept the existing teal-and-serif design system, prioritizing mobile overflow fixes (`#419`).

## 2026-11-10

* **Usage Stats:** Retired public `/stats` and `GET /api/stats` (`#364`, followed by `usage.Store.Report` removal in `#403`); usage reporting now lives in `/metrics` and Grafana while `usage_counters` stays write-only.
