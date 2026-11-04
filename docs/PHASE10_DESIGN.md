# Phase 10 Design — Durable Execution Runtime

Date: 2026-10-31

## Current execution architecture

Before Phase 10 the system was a synchronous modular monolith. `Supervisor.Turn` loaded Goal/Task state, generated a plan, transitioned a selected Task to `running`, invoked an in-process Agent, wrote execution/evidence, and separately transitioned the Task. Phase 10 keeps that compatibility path for unit/embedding use, while application startup now runs the PostgreSQL-backed Worker Pool. Legacy `execution_runs` success records remain readable and are not rewritten.

## Existing capabilities

- PostgreSQL transaction/advisory-lock/checksum migration runner. Historical migration files must remain unchanged because checksums are stored and enforced.
- Durable Goal/Task state machines, persisted attempt count, transactional Audit/Outbox mutation recording.
- `execution_runs` with unique legacy `idempotency_key`, input hash, result, attempt, status and timestamps; `execution_evidence` rows.
- Bounded in-process retry and coalescing `IdempotencyGuard` (not durable).
- Supervisor reconciles a persisted successful execution if the Task completion transition was interrupted.
- Task dependency DAG and explicit `waiting_approval` / `waiting_external` statuses.
- PostgreSQL-backed Run/Step idempotency, Checkpoint, lease and state-history schema from migration 0008.

## Gaps and correctness risks

- Durable claim, heartbeat, fencing, checkpoint, retry-wait, and periodic recovery are implemented for the startup Worker Pool. The legacy `Recoverable` path is disabled when the durable runtime is injected.
- The API/runtime does not enable production side-effect Tools; all external Tool results still need downstream idempotency and independent verification.
- Task/Evidence/Execution completion is atomically committed by the durable store; Task planning remains a separate transaction.
- Recovery requeues only an expired `claimed` run that had not started. An expired `running` operation becomes `unknown` unless the Agent explicitly declares both retry safety and idempotency.

## Proposed components

1. `internal/execution`: provider-neutral persisted run, claim, lease, checkpoint, result, executor and worker-pool contracts/runtime. It owns worker lifecycle, heartbeat, poll/recovery loop, concurrency and cancellation.
2. `internal/app/store`: PostgreSQL implementation. `Enqueue` uses a transaction and durable scoped idempotency record. `Claim` uses `FOR UPDATE SKIP LOCKED`, increments a monotonically increasing fencing token and claims the Task atomically. Heartbeat/checkpoint/complete/fail/cancel writes condition on worker ID, token and unexpired lease. Completion, Evidence, Task terminal state, Audit and Outbox are committed together.
3. `cmd/paios`: assembles one bounded worker pool, starts it before serving requests, runs recovery through the worker loop, and shuts the pool down before closing PostgreSQL.
4. `Supervisor`: retains Goal planning, dependency ordering and budget decisions. With the durable runtime injected, it submits/waits for an execution and never makes lease/claim SQL decisions. The legacy synchronous path remains available to unit tests and non-runtime embeddings.
5. API: only operationally useful read/status/cancel routes are added; no API exposes raw checkpoint payloads or worker secrets.

## State mapping and transitions

Legacy rows remain readable: `succeeded` maps to the new successful terminal state; `running`, `waiting_approval`, `waiting_external`, `retrying`, and `failed` are retained as compatible labels. New runtime rows use:

```text
queued -> claimed -> running -> succeeded
                         |       -> failed
                         |       -> retry_wait -> claimed
                         |       -> waiting_approval (no execution proceeds)
                         |       -> unknown (unsafe outcome / lease loss)
queued/retry_wait -> cancelled
expired lease + retry-safe -> queued (next claim gets a higher fencing token)
expired lease + unsafe side effect -> unknown (manual reconciliation; no automatic retry)
```

Only a claim may assign `worker_id`; each successful new claim increments `fencing_token`. A terminal run is immutable to normal worker operations. Every state change records a reason/error class and timestamps. Task `running` is set atomically with claim; failed safe attempts move Task to `retrying`; successful completion atomically marks Task `completed`. Approval waits do not invoke the executor.

## Database changes (new forward migration only)

- Extend `execution_runs` with persisted input/payload, worker identity, claim/lease/heartbeat times, `fencing_token`, `available_at`, attempt limit, error classification, retry safety and cancellation request. Preserve the existing unique legacy key and existing status data.
- Add `execution_idempotency` with unique `(scope, key)`, input hash and run reference. Same scope/key + same hash replays the run; same key + different hash returns conflict.
- Add `execution_checkpoints`: run ID, ordered sequence, step ID, JSON progress/completed steps, external state reference, schema version, fencing token and UTC creation time. Unique `(run_id, sequence)`; writes require the current lease/fence. Checkpoint payloads contain references/metadata only; secrets and binary blobs are excluded.
- Add indexes for claim/recovery scans. Migration 0008 is append-only; do not edit 0001–0007.

## Worker collaboration flow

1. Supervisor creates/persists a Task DAG and submits an execution payload only after dependencies and policy are satisfied.
2. Durable enqueue atomically checks scoped idempotency and creates a queued run.
3. Each worker claims one eligible row using `SKIP LOCKED`; in the same DB transaction it increments the fencing token, records worker/lease metadata, and starts/increments the associated Task.
4. A heartbeat extends the lease on a bounded cadence. A lost lease/token cancels the local executor context. Every checkpoint/result write checks `(run_id, worker_id, fencing_token, lease_until > now)`.
5. Safe failures are scheduled with bounded retry/backoff. Unsafe timeout/lease-loss outcomes become `unknown` and are not automatically repeated. Cancellation is cooperative; no new work is claimed once shutdown begins.
6. A periodic recovery scanner requeues expired claims that had not started; expired in-flight work is `unknown` unless explicitly retry-safe and idempotent. Recovery fencing and state changes are recorded with Audit/Outbox events.
7. On graceful shutdown, workers stop claiming, cancel/await in-flight work to a configured deadline, and leave safe/unknown recovery state durable before the database pool closes.

## Failure/retry/idempotency policy

PostgreSQL transactions and unique constraints provide at-most-one active lease and durable request replay, not exactly-once external side effects. Same scope/key + same canonical request hash replays a run; changed payload, Agent, retry policy or safety declaration conflicts. Step claims are execution/step scoped. Agent idempotency is opt-in through `IdempotencyDeclaration`; unknown Agent failures are treated as uncertain and do not replay automatically. Retries are bounded by persisted attempts.

## Verification gate

The Worker requires explicit `VerifiedResult` and evidence; the PostgreSQL completion method independently checks the flag and the result payload's `verified` assertion. Mock Agent is intentionally unverified and cannot mark a Task complete. A future real-world result requires an independent verifier, not just model prose.

## Test plan

- Unit tests for state transitions, retry classification, key/hash behavior, lease loss cancellation, graceful stop, and checkpoint ordering.
- PostgreSQL concurrency tests: two workers contend for one queued row; lease expiry and new fencing token; stale heartbeat/checkpoint/completion rejected; unique replay/conflict; approval/wait states do not execute.
- Fault injection with isolated PostgreSQL: rollback checkpoint writes, stop/restart during a claimed run, recovery scans repeated safely, DB interruption, cancellation and uncertain outcomes.
- Dependency tests ensure Supervisor does not dispatch a Task before predecessor evidence.
- Full Go/race/vet/build and isolated `make test-integration`; the implementation report records the current incomplete startup-recovery restart assertion and requires a passing full rerun before Phase 10 acceptance.

## Resource and operational limits

Worker count, max in-flight runs, lease duration, heartbeat interval, poll interval, retry limit and shutdown grace are explicit bounded settings. Initial pool size is a configuration cap; PostgreSQL `SKIP LOCKED` prevents duplicate claims across processes. Metrics/logging report worker lifecycle, queue depth, lease loss, retries and unknown outcomes without logging secrets or checkpoint contents.
