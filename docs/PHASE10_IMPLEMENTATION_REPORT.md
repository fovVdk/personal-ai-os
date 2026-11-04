# Phase 10 Implementation Report — Durable Execution Runtime

Date: 2026-10-31

## Summary

Phase 10 adds a PostgreSQL-backed durable execution queue and worker runtime to the existing modular monolith. It preserves the legacy synchronous Supervisor path for compatibility/tests, but the application startup now wires the durable runtime, supervised Worker Pool, lease recovery scanner, and durable execution APIs. Historical migrations 0001–0007 were not modified; migration 0008 adds runtime state and constraints.

The runtime provides durable idempotency, multi-worker claims, leases, heartbeats, fencing tokens, step claims, ordered checkpoints, retry/unknown classification, cooperative cancellation, Recovery scanning, Worker shutdown, execution status, and Supervisor integration. An independent `verified` assertion is required at both Executor and PostgreSQL completion boundaries: Mock Agent prose remains unverified and cannot mark a Task complete.

## New components

- `internal/execution/runtime.go`: durable contracts, state/status and failure classes, bounded Worker Pool, heartbeat loop, cancellation, Checkpoint progress, periodic Recovery Scanner, bounded retry backoff and worker status.
- `internal/app/store/durable_executions.go`: PostgreSQL Enqueue, atomic claim, lease heartbeat, fencing validation, result/Evidence/Task transaction, cancellation, Recovery, state history, and verified durable run lookup.
- `internal/app/store/execution_steps.go`: persisted per-step idempotency claims.
- `cmd/paios/durable_executor.go`: adapter from a durable claim to the framework-neutral registered Agent. It forwards persisted checkpoints and step claims; only a Result with explicit verification and Evidence can succeed.
- `internal/supervisor/durable_runtime.go`: Supervisor-facing enqueue/status polling adapter.
- `internal/execution/runtime_test.go`, `internal/execution/worker_test.go`: config/classification/retry/shutdown and Worker behavior tests.
- PostgreSQL tests in `tests/integration/postgres_test.go`: durable claim, double-worker race, idempotency, checkpoint, fencing, lease recovery, safe/unknown replay, transaction rollback, background worker/cancellation, and restart/recovery behavior.

## Modified modules

- `internal/agent/runtime`: added resumable Agent/checkpoint/step claim contracts and explicit optional idempotency declaration. Mock Agent implements resumable checkpoints and declares safe replay because it has no external side effects; Mock result remains `unverified`.
- `internal/supervisor`: optional durable enqueue/poll execution path; adds durable verified results to completion accounting; disables the legacy unleased `Recoverable` transition loop when durable runtime is enabled.
- `cmd/paios`: starts configured Worker Pool and recovery loop; shuts down Workers before database close; supplies durable execution storage, Agent adapter and worker status.
- `internal/api`: `GET /api/v1/executions/{id}`, `POST /api/v1/executions/{id}/cancel`; existing Task cancel delegates to durable cancellation. `GET /api/v1/supervisor/status` includes Worker snapshot.
- `internal/app/config`, `.env.example`, `configs/config.example.yaml`, Makefile, Compose, OpenAPI and documentation: worker/lease/recovery configuration and test support.

## Migration 0008

`0008_durable_execution_runtime.sql` extends `execution_runs` with JSON input payload, Agent ID, worker ID, claim/lease/heartbeat timestamps, monotonic fencing token, available time, attempt budget, retry safety, explicit idempotency declaration, cancellation flag, error class, state reason and transition time. It expands the compatible status constraint without modifying legacy rows. Added tables:

- `execution_idempotency`: unique scope/key, parameter hash and run link. Same hash returns the existing run; changed payload/policy hash is rejected.
- `execution_checkpoints`: sequence, step, bounded JSON progress, completed step list, external reference, schema version and fencing token; unique `(execution_run_id, sequence)`.
- `execution_step_idempotency`: step/key/input hash unique constraints.
- `execution_state_history`: append-only state transitions with Worker, fencing token, reason, error classification and UTC timestamp.

## Worker, claim, lease, fencing

- Workers poll an indexed eligibility query and claim using one transaction with `FOR UPDATE SKIP LOCKED`.
- Claim atomically moves the Execution to `claimed`, increments the fencing token, writes worker/lease/heartbeat, moves the Task to `running`, and records state history plus Audit/Outbox.
- `Start`, heartbeat, step claim, Checkpoint, failure, and completion verify the worker ID, fencing token, status and unexpired lease. A lease is refreshed at a cadence less than half its configured duration.
- Stale Workers cannot save checkpoints, heartbeat, or complete after lease handoff. Completion also locks/updates the Task in the same transaction as result, Evidence, model usage, state history, Audit and Outbox.
- Recovery uses `SKIP LOCKED` and acts only on expired claim/run leases. Claimed-but-not-started work is safe to enqueue again. Expired Running work becomes `unknown` unless the Agent explicitly declares idempotency; explicitly idempotent retry is bounded by the persisted attempt limit. Repeated scans only select active claim/run states, so they are idempotent.

## Checkpoints and idempotency

- Checkpoints have unique monotonic per-run sequence and are serialized under the execution row lock with current lease/fencing validation.
- Checkpoint payloads are capped at 64 KiB, completed steps at 1024, sensitive key names are rejected, and large data/secrets must remain external references/secret store values.
- Enqueue verifies SHA-256 against the canonical JSON payload and normalizes identity over Task, Agent, attempt limit, retry safety, explicit idempotency flag and payload hash.
- Durable idempotency lives in PostgreSQL, not an in-memory Map. Repeated scoped key + same hash returns the same run; same key with changed request hash conflicts.
- Step claims are bound to execution, step ID, key and hash; conflicting key/input reuse is rejected.
- This is not arbitrary exactly-once execution. Non-idempotent External effects that may have occurred before a timeout/lease loss enter `unknown`/`waiting_external` and are not replayed. A downstream side-effect tool must itself support stable idempotency or a verifier/manual reconciliation flow.

## State mapping and transitions

The new execution row status set includes `pending`, `queued`, `claimed`, `running`, `waiting_approval`, `waiting_external`, `retrying`, `retry_wait`, `succeeded`, `failed`, `cancelled`, and `unknown`. Legacy `execution_runs` status values remain permitted by the compatibility constraint. Task state is coordinated transactionally: claim starts it, retryable recovery leaves it retryable for claim, verified success completes it, ordinary permanent failure fails it, unsafe uncertain outcome waits externally.

Terminal success writes require independent verification and Evidence. An unverified Agent result is converted to failure and never runs Goal completion. A cancellation request while running is cooperative; the heartbeat returns `ErrLeaseLost`/cancellation and the Worker records an uncertain outcome. Cancellation of queued/claimed/pre-approval work is deterministic; unknown/waiting-external work cannot be rewritten as cancelled.

## Supervisor and API

Supervisor remains responsible for Goal/task DAG, budgets, policy and presentation; it submits a durable request and polls persisted run state. It does not own PostgreSQL claims or lease SQL. Verified durable execution IDs are considered when reconciling Goal evidence. Current startup registers Mock agents; they produce unverified output, so a durable Worker correctly fails rather than claims task success. This is intentional until an independent verifier is integrated.

- `GET /api/v1/executions/{id}`: returns local-owner execution status and metadata.
- `POST /api/v1/executions/{id}/cancel`: cancels queued/claimed runs or requests cancellation of a running execution; unknown outcomes return conflict.
- Existing `POST /api/v1/tasks/{id}/cancel` is coordinated with durable execution state.
- Existing `GET /api/v1/supervisor/status` reports Worker count/capacity/active/stopping snapshot.

The API remains under shared API Key authentication with existing request ID, body size, JSON and timeout controls.

## PostgreSQL failure-injection tests

Real PostgreSQL integration tests exercise:

- two workers concurrently claiming the same queued Execution (exactly one wins);
- durable same-key replay and changed-input conflict;
- persisted step claim replay/conflict;
- checkpoints sequence 1/2, latest checkpoint recovery, secret-like field rejection, failure injection during checkpoint insert with full rollback;
- lease expiry, recovery idempotency, new claim and larger fencing token;
- stale Worker checkpoint/complete rejection after handoff;
- A3 approval gate, Task dependencies and prior existing transaction failure cases;
- Worker Pool claim/heartbeat/checkpoint/verified completion and context cancellation to unknown;
- migration 0008 from a new empty volume, repeat application, injected DDL rollback and clean retry;
- PostgreSQL container restart and fresh Go Core process reading persisted Goal/Task/Execution/Evidence/Approval/Memory/Knowledge/Node/Audit/Outbox;
- a separately prepared expired claimed run for startup recovery after database + Core restart.

`make test-integration` creates/removes only the dedicated `paios-dbtest` container and `paios-integration-test-data` volume. The primary existing Compose project and its services/data are not touched.

## Verification results

On the current macOS darwin/arm64 environment with Docker Desktop Linux/arm64, Docker Engine 29.6.2, Compose 5.3.1, PostgreSQL 16 image `pgvector/pgvector:pg16`, and pgvector extension 0.8.7:

- `make fmt`: passed.
- `make test`: passed.
- `make build`: passed.
- `make vet`: passed.
- `go test -race ./...`: passed.
- Isolated `make test-integration` on a fresh empty named volume: passed. This target ran all migration, database integration, full repository Go tests, race tests, vet/build, Worker Pool concurrency/fencing/checkpoint/cancellation tests, and both database/Core restart tests.
- Migration verification began with zero recorded versions, applied all eight migrations, tested a deliberate DDL rollback/retry, and verified pgvector 0.8.7.
- The startup-recovery fixture verified fencing increased from 5 to 6 after the expired claim was recovered and claimed by a new Worker. The unverified Mock Agent result correctly left the run/Task failed rather than claiming success. PostgreSQL then restarted and a fresh Core process successfully read persisted records.
- The first startup-recovery assertion observed the intermediate `retry_wait` state too early; its wait condition was fixed to wait for both recovery history and a terminal Execution status before asserting. The final clean-volume run passed.
- `get_errors`: no workspace diagnostics at the most recent check before documentation edits.
- Compose test profile and YAML parsing were verified in Phase 9 and the test service configuration remained parseable during Phase 10 runs.

## Known limits / remaining work

1. The repository has no production Agent that returns independently verified results; Mock is intentionally unverified. The safe completion gate exists, but an independent real-world verifier/artifact protocol is outside this phase.
2. Recovery automatically requeues an expired claim that had not started. An expired running operation becomes `unknown` unless the Agent explicitly declares both retry safety and idempotency; generic errors default to uncertain.
3. Checkpoint sensitive-key rejection is heuristic by field name; opaque credential material under innocuous names cannot be detected. Agents must checkpoint only references to secret stores/artifact storage.
4. No production side-effect Tool runner is enabled. Step idempotency is available to resumable Agent/Tool adapters, but does not prove exactly-once external actions.
5. No dedicated metrics backend or OTel export was added; Worker operational status is available through Supervisor status and structured logs.
6. Execution waiting for Approval is persisted, but an approval-triggered continuation dispatcher is still pending. Current Mock workflow does not invoke external effects.
7. Repeated Supervisor turn submissions are protected by stable Task-scoped keys and persisted input hashes; callers must retain the same Task and request intent for replay to resolve to the original run.

## Changed files

- New: `migrations/0008_durable_execution_runtime.sql`
- New: `internal/execution/runtime.go`, `runtime_test.go`, `worker_test.go`
- New: `internal/app/store/durable_executions.go`, `execution_steps.go`
- New: `internal/supervisor/durable_runtime.go`
- New: `cmd/paios/durable_executor.go`
- Expanded: `internal/agent/runtime/runtime.go`, `mock.go`; `internal/supervisor/supervisor.go`; `internal/app/config/config.go`; `internal/api/server.go`, `supervisor.go`, `executions.go`; `cmd/paios/main.go`; `tests/integration/postgres_test.go`; `deployments/compose/docker-compose.yml`; `Makefile`; `configs/config.example.yaml`; `.env.example`; `docs/openapi.yaml`.
