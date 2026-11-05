# Architecture Overview

## System shape

Personal AI OS uses a modular-monolith boundary: the Go Core owns durable state, policy, approvals and execution authority. The model and agents are treated as untrusted reasoning adapters; tools only perform concrete operations after the core authorization gate.

```text
HTTP API -> Goal/Task repositories -> PostgreSQL
                          |
                    Policy -> Approval
                          |
              Execution -> Agent/Tool adapters
                          |
              Event outbox -> audit/observers
```

The runnable vertical slices include Goal/Task persistence, structured criteria, deterministic Evaluation, verifier-backed artifact/reference criteria, versioned Plan Proposals, idempotent Supervisor Decisions, durable Worker execution, approval, persistent Goal Jobs, Outbox-driven wakeups, immutable Source Snapshots and the centralized Tool Gateway. HTTP and background Job triggers reuse the same bounded Supervisor turn.

## Data and lifecycle invariants

- Goals are independent of chat and agent sessions and have a UUID owner relation.
- Goal completion requires a fresh Evaluation for the current Goal/Task/Execution facts, all required structured criteria verified, no unsettled Tasks, and internally derived Evidence. The PostgreSQL transition rechecks current Criterion Verification rows and Artifact hashes while holding the Goal lock.
- `ArtifactVerifier` is deterministic and independent of ModelAgent for FILE_STORAGE_DIR-relative regular files: it enforces a size cap, Markdown encoding/headings where requested, and a SHA-256 reference. The completion gate reopens the path and rechecks the hash. A hash proves byte integrity only, not semantic correctness or source truth.
- Required source references are linked to immutable raw/normalized snapshots. The reference verifier checks exact allowlisted URL binding, HTTP success, raw hash, normalization integrity, freshness, exact quote presence and visible `source://ID`/URL/quote inclusion in the report. It deliberately does not claim semantic statement support.
- Execution Evidence has a database UUID; Criterion Verification rows bind Goal/Goal timestamp, Criterion ID/version, Verifier type/version, result, hash list and FK links to real Execution Evidence.
- Tasks reference Goals, use independent IDs and have explicit transitions.
- Task creation/state changes and Execution evidence completion acquire a shared Goal row lock. Goal completion acquires the exclusive row lock; this serializes facts and status transitions across the completion gate.
- Plan Proposals are advisory. Core row-locks Goal, checks Base Plan Version, expiry, DAG, required Criterion mapping, Task budget and existing Task identity before atomically inserting the Plan Version, Task additions, Audit and Outbox. Existing completed/active Tasks and Evidence are retained; active Tasks cannot be omitted; no-progress proposals are rejected.
- Evaluations and Decisions share a transaction. Decision fingerprint is derived from criteria versions/evidence hashes, Task states, waits, blockers and budgets; duplicate snapshots reuse a single Decision record.
- A3 actions and configured approval-required actions cannot invoke a tool without an approved request bound to the exact tool, action, parameters and expiry.
- Retry is bounded by persistent Task attempt counters. Execution runs and evidence are persisted before Task completion; the Supervisor reconciles a crash between those commits. The in-memory idempotency guard is not a substitute for a durable idempotency claim before external operations.
- HTTP input is bounded, unknown JSON fields are rejected, API errors avoid exposing database internals, and logs must never include credential values.

## Persistence

PostgreSQL 16 plus pgvector is provided by Compose. SQL migrations are embedded, ordered, transactionally applied and checksum-verified. Schema additions are append-only migration files. JSONB is used for evolving policies and criteria; core identity and lifecycle fields remain relationally constrained.

Migrations 0011–0013 add Evaluation history, structured Goal criteria, Criterion Verification/Evidence relations, Plan Versions/Proposals, Supervisor Decisions and Task PlanKey/Criterion mapping. Migration 0014 adds persistent Goal Jobs with lease/fencing/retry state; 0015 adds Outbox dispatch leases/retry/dead-letter and idempotent wake receipts; 0016 adds immutable Source Snapshots and Source Reference bindings. The Evaluation payload is a snapshot for retrieval; completion authority is rechecked against normalized Verification/Evidence links, source snapshot integrity/freshness and current artifact bytes.

## Security posture

The API supports an environment-provided API key with constant-time comparison. It is intentionally open when the key is empty for local development, and production configuration rejects an empty key. Never expose the development configuration on an untrusted network. No arbitrary shell tool exists. `source.capture` is registered only with an operator-configured exact host allowlist, runs through the Tool Gateway, rejects non-public DNS results and rechecks every redirect; fetched content is treated as untrusted text and never executed. Future tool integrations must call the centralized policy gate before any side effect.

## Planned extension boundaries

- `internal/model/provider`: provider-specific HTTP clients behind a model gateway.
- `internal/agent/adapter`: framework-specific translations, with no Eino types in domain packages.
- `internal/tool/mcp`: MCP transport adapter only; registration never confers permissions.
- `internal/event`: persistent Outbox dispatcher and idempotent Goal Job wake consumer; payloads are signals only and consumers reread PostgreSQL state.
- `internal/source`: allowlisted public-source Fetcher, immutable snapshot/reference contracts, and deterministic structure/quote verifier.
- `internal/verification`: Core deterministic file plus source-reference verifier; semantic report support remains an independent-review boundary.
- `internal/observability`: structured logging; tracing/metrics instrumentation remains incomplete.

## Phase 12.3 scope boundary

The Goal Job worker runs one bounded shared Supervisor turn per claim and uses PostgreSQL leases/fencing; the Outbox dispatcher persists delivery claims, retries and wake receipts. `artifact.write` is Gateway-only and derives its single allowed path from a persisted, confirmed Goal Criterion. `source.reference.add` binds the exact snapshot quote to that same criterion. Optional `report.export` writes only to a separately configured root and is an external Risk-2 action requiring explicit Approval over the criterion ID/version, artifact path and current SHA-256. A deterministic Goal Job research integration scenario and a separate real effect/response-loss Worker-crash test are now present in source, but could not be rerun in the current environment because the Go module proxy timed out; they remain NOT VERIFIED until `make test-autonomous-e2e` passes. These are not yet one combined scenario with the same Goal/Task/Execution across unknown effect, checkpoint takeover, Core restart and final completion. Live source capture requires operator allowlist and was not run; semantic report support remains an independent-review boundary. Hosted Go 1.25 CI also remains pending.
