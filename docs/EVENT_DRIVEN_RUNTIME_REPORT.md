# Phase 12.3.2 — Event-driven Goal Job Wakeup

## Status: IMPLEMENTED; real PostgreSQL delivery tests added

### Delivered

- Migration `0015_outbox_dispatch_leases.sql` persists dispatch owner/expiry, retry time/count, last error, dead-letter timestamp and per-event Goal Job wake receipts.
- Dispatcher uses PostgreSQL claims with `SKIP LOCKED`, at-least-once delivery, lease expiry recovery, bounded exponential retry and terminal dead-letter audit.
- Event processing, idempotent wake receipt, Job wake/coalescing and Outbox acknowledgement share one transaction.
- Supported mappings are restricted to actual persisted Goal, Task, Approval, Execution, Tool Invocation, accepted Plan and Source Snapshot aggregates. Current DB status is reread; event payload is not trusted.
- A wake only targets an existing explicitly started Goal Job. Multiple event wakes coalesce and cannot launch multiple concurrent Supervisor loops for the same Goal.

### Verification

- PostgreSQL tests cover Goal update dispatch, expired dispatcher lease takeover, duplicate event redelivery, wake receipt uniqueness, running-job coalescing, actual Approval-approved event wake, terminal/cancelled Goal ignore, injected wake failure, retry and dead-letter audit.
- Migration rollback/retry and the complete runtime integration suite passed under `make test-integration` on the isolated PostgreSQL 16/pgvector database; the final run also passed package race tests, Worker crash/checkpoint acceptance and database/Core restart verification.

### Limits

- This dispatcher is an internal PostgreSQL consumer for Goal Job wakeups, not a general external pub/sub publisher.
- A full consumer-process crash while running business logic is represented by transactional failure/retry and receipt replay tests, not by an external broker deployment.
