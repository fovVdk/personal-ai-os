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

The runnable vertical slices include Goal/Task persistence and lifecycle APIs, structured planning to transactional Task creation, approval APIs and parameter-bound policy primitives, Agent profile registry, Memory CRUD, text Knowledge ingestion/cited keyword retrieval, Model Gateway providers, and a bounded Supervisor turn. Mock Agent outputs are unverified and cannot transition Tasks/Goals to success. The verifier for real agent outputs, independent recovery worker, per-step checkpoint service and durable idempotency claim before external operations are not yet connected.

## Data and lifecycle invariants

- Goals are independent of chat and agent sessions and have a UUID owner relation.
- Goal completion requires evidence and is not authorized by model prose alone.
- Tasks reference Goals, use independent IDs and have explicit transitions.
- A3 actions and configured approval-required actions cannot invoke a tool without an approved request bound to the exact tool, action, parameters and expiry.
- Retry is bounded by persistent Task attempt counters. Execution runs and evidence are persisted before Task completion; the Supervisor reconciles a crash between those commits. The in-memory idempotency guard is not a substitute for a durable idempotency claim before external operations.
- HTTP input is bounded, unknown JSON fields are rejected, API errors avoid exposing database internals, and logs must never include credential values.

## Persistence

PostgreSQL 16 plus pgvector is provided by Compose. SQL migrations are embedded, ordered, transactionally applied and checksum-verified. Schema additions are append-only migration files. JSONB is used for evolving policies and criteria; core identity and lifecycle fields remain relationally constrained.

The currently implemented repositories persist Goals, Tasks, Approval decisions, Memories and Knowledge documents/chunks. Goal/Task/Memory/Approval/Knowledge mutations are recorded in the transactional outbox and audit table on relevant write paths. Migrations also provision execution evidence and vector columns, but durable execution runners, embeddings and vector retrieval are not implemented yet.

## Security posture

The API supports an environment-provided API key with constant-time comparison. It is intentionally open when the key is empty for local development, and production configuration rejects an empty key. Never expose the development configuration on an untrusted network. No arbitrary shell tool exists. Future tool integrations must call the centralized policy gate before any side effect.

## Planned extension boundaries

- `internal/model/provider`: provider-specific HTTP clients behind a model gateway.
- `internal/agent/adapter`: framework-specific translations, with no Eino types in domain packages.
- `internal/tool/mcp`: MCP transport adapter only; registration never confers permissions.
- `internal/event`: event contracts and outbox publisher; consumers must be idempotent.
- `internal/observability`: logging now, tracing/metrics instrumentation later.
