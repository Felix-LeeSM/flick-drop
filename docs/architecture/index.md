# Architecture

* [Advertising Experiment](advertising-experiment.md) - Trust boundaries, static sponsor slot contract, publisher policy checks, and aggregate measurement rules for M10.
* [Agent Workflow](agent-workflow.md) - GitHub issue/PR workflow, label-based review gate rules, OKF v0.2 document format conventions, and document size limits enforced in CI.
* [CI and Testing](ci-testing.md) - PR checks, local entry points, Go and Playwright browser test suites, synthetic canary, image publish, and CLI release workflows.
* [Database Schema](database-schema.md) - PostgreSQL schema, ownership split between flick_api and flick_worker databases, request and reconciliation tables, and dialect rules.
* [Deployment Target](deployment-target.md) - Generic k3s Kubernetes resources, Traefik retry/transport setup, initial OCI Always Free resource budgets, and multi-replica scaling boundaries.
* [Environment Contract](env-contract.md) - Public contract for all runtime, build-time, CLI, PostgreSQL, Valkey, NATS, and named S3-compatible storage environment variables.
* [Event Contract](event-contract.md) - NATS JetStream stream/consumer settings, job payload schema, outbox publishing and retention, worker lease/retry semantics, and reconciliation acks.
* [Implementation Choices](implementation-choices.md) - Implementation defaults for SvelteKit, i18n, Go HTTP/chi, PostgreSQL 17, Valkey, AWS SDK v2 S3-compatible storage, containers, IDs, and age/PBKDF2 crypto.
* [Live Relay Architecture and Security](live-relay.md) - Valkey-backed rendezvous coordinator, pod-to-pod encrypted streaming, waiting-room confirmation, rate limiting, and security boundaries for Live Transfer (/api/pipe/*).
* [Public Guide](public-guides.md) - Structure, canonical URL, crawler directives, 301 redirects, and sponsor boundary for the /guides/ page.
* [One-Time Request Links](request-links.md) - Authority model, key custody, age encryption format, HTTP contract, large-upload reservations, and cleanup semantics for M9 request links.
* [Security Model](security-model.md) - Client-side encryption, Model A/B access control, one-time request security, log masking, web response headers, sender management, and residual risk boundaries.
* [Service Topology](service-topology.md) - Runtime services (flick-web, flick-api, flick-worker, nats, postgres, valkey), live-relay pod-to-pod flow, and outbox reliability.
* [Storage Model](storage-model.md) - PostgreSQL vs. named S3-compatible object storage thresholds, capacity-weighted placement, request storage, deletion semantics, and late-upload reconciliation.
* [Telemetry Architecture](telemetry.md) - Structured logging, Prometheus metrics, scrape-time database collectors, worker metrics listener, and OpenTelemetry tracing design for internal/telemetry.
