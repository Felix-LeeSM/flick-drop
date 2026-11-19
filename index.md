---
okf_version: "0.2"
---

# Agent Guide

* [Flick Agent Guide](AGENTS.md) - Root guide for Flick project shape, directory principles, required reading, hard security rules, and development standards.
* [.agents Guide](.agents/AGENTS.md) - Policy for repo-owned agent skills, PR review subagent contract, and keeping workflow rules in directory-local guides.
* [.github Guide](.github/AGENTS.md) - GitHub Actions workflow boundaries, branch protection, synthetic canary budget, and metadata-only review gate policy.
* [cmd Guide](cmd/AGENTS.md) - Rules for executable entrypoints under cmd/, keeping main.go thin and delegating domain logic to internal packages.
* [cmd/flick Guide](cmd/flick/AGENTS.md) - Entrypoint rules, input/passphrase security, default host policy, and release workflow for the flick CLI client.
* [cmd/flick-api Guide](cmd/flick-api/AGENTS.md) - Process bootstrap order, database ownership, and directory rules for the flick-api HTTP server entrypoint.
* [cmd/flick-worker Guide](cmd/flick-worker/AGENTS.md) - Process bootstrap order, worker database ownership, and NATS consumer entrypoint rules for flick-worker.
* [contracts Guide](contracts/AGENTS.md) - Ownership and security rules for shared HTTP OpenAPI, age format, credential payload, NATS event, and internal API contracts.
* [deploy Guide](deploy/AGENTS.md) - Generic k3s manifests, multi-replica rollout rules, opt-in monitoring, resource budgets, and public-safe deployment boundaries.
* [internal Guide](internal/AGENTS.md) - Go internal package map, API vs. worker database ownership rules, and package boundary principles.
* [internal/clientcrypto Guide](internal/clientcrypto/AGENTS.md) - Go client-side age encryption, Model A PBKDF2 access proof, fragment parity with web/src/lib/crypto, and golden vector contract rules.
* [internal/config Guide](internal/config/AGENTS.md) - Environment variable parsing and validation boundaries without side effects for internal/config.
* [internal/db Guide](internal/db/AGENTS.md) - PostgreSQL connection setup, advisory-locked idempotent migrations, UTC timestamptz scanning, and read-only storage usage aggregation.
* [internal/events Guide](internal/events/AGENTS.md) - NATS JetStream subjects, outbox publishing and retention pruning, idempotent consumers, and W3C trace propagation rules.
* [internal/flickcli Guide](internal/flickcli/AGENTS.md) - CLI HTTP client, share-link parsing, send/open parity with web, and destructive-open ordering rules.
* [internal/httpapi Guide](internal/httpapi/AGENTS.md) - HTTP routing, middleware, DTO mapping, transaction boundary limits, and internal-token rules for public and internal API handlers.
* [internal/piperelay Guide](internal/piperelay/AGENTS.md) - Boundary, hard security invariants, Valkey/streaming rules, and test prerequisites for the Live Transfer (/api/pipe/*) coordinator package.
* [internal/requests Guide](internal/requests/AGENTS.md) - One-time request lifecycle, envelope validation, immutable large-object finalize, and request-prefix reconciliation rules.
* [internal/secrets Guide](internal/secrets/AGENTS.md) - Secret lifecycle states, expiration reaper, atomic access-attempt lockout, sender management revocation, and object reconciliation rules.
* [internal/storage Guide](internal/storage/AGENTS.md) - S3-compatible object storage adapter, presigned PUT and OCI CORS constraints, named-backend registry, and reconciliation listing rules.
* [internal/telemetry Guide](internal/telemetry/AGENTS.md) - Boundary, file layout, and privacy/cardinality invariants for structured logging, Prometheus metrics, and OpenTelemetry tracing in internal/telemetry.
* [internal/usage Guide](internal/usage/AGENTS.md) - Transactional daily aggregate usage_counters writer rules and privacy boundaries in the flick_api database.
* [internal/worker Guide](internal/worker/AGENTS.md) - Worker job execution, flick_worker database ownership, idempotent retries, object-key masking, and cleanup acknowledgement rules.
* [scripts Guide](scripts/AGENTS.md) - Shared CI, dev, smoke, and one-time ops script boundaries, static structure checks, and credential exclusion rules.
* [tests Guide](tests/AGENTS.md) - Cross-component integration, browser e2e, and non-secret fixture boundaries under tests/.
* [web Guide](web/AGENTS.md) - Directory structure, security invariants, request-link rules, design/prototyping gotchas, and i18n testing policy for the SvelteKit frontend.

# Architecture

* [Flick Service Concept](flick-service-concept.md) - Original product concept kept for orientation; docs/architecture/* is the source of truth.

# Contract

* [age Format v1](contracts/age-format-v1.md) - Client-side hybrid age encryption format, Model A passphrase wrapping, Model B fragment keys, interop vectors, and API/live-relay wire mapping.
* [Worker Cleanup Internal API](contracts/internal-api.md) - Internal token-authenticated HTTP endpoints for inline payload cleanup and job-ID-fenced object reconciliation acknowledgements.
* [Sender Management Contract v1](contracts/sender-management-v1.md) - Accountless sender management capability tokens, status/revocation HTTP surface, opened-means-forgotten semantics, and late-PUT object reconciliation.

# Reference

* [Monitoring Stack (Opt-In)](deploy/base/monitoring/README.md) - Lightweight k3s monitoring add-ons (kube-state-metrics, node-exporter, VictoriaMetrics, Loki, Alloy, alloy-cadvisor, Grafana), log privacy, and dashboard layout.
* [Web i18n Guide](web/src/lib/i18n/README.md) - English, Korean, and Japanese catalog structure, English-skeleton wording rules, and user-perspective terminology under web/src/lib/i18n/.

# Subdirectories

* [docs](docs/index.md) - Architecture decisions, operational runbooks, roadmap, and active work notes.
