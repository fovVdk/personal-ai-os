# Database Integration Report

Date: 2026-10-31

## Executive summary

Phase 9 database persistence acceptance completed against a real PostgreSQL container with pgvector. The verification used a dedicated Compose project, loopback-only port, separate test user/database, and a named test-only Docker volume. It did not stop, alter, or reuse the user's existing Compose services or database volumes. The test target removed only its own container and named volume when it completed.

Results:

- PostgreSQL 16 + pgvector started and reported healthy.
- All seven migrations applied to a newly created empty database in lexical order; the migration ledger recorded seven versions and 64-character SHA-256 checksums.
- Applying migrations a second time succeeded without adding versions or changing the schema.
- A deliberately injected DDL failure rolled back both schema changes and migration-version records; after removing the failure, all seven migrations applied successfully.
- Real DB integration tests passed for Goal, Task, Execution, Evidence, Approval, Policy gate, Audit, Outbox, Memory, Knowledge, and Node.
- Audit/Outbox failures injected inside transactions left no partial Goal/Execution writes and did not commit an Approval decision.
- PostgreSQL container restart followed by a fresh Go Core process preserved and served all prepared records.
- Full `go test ./...`, `go test -race ./...`, `go vet ./...`, `make build`, and Compose configuration validation passed.

## Database environment

| Item | Observed value |
| --- | --- |
| Host | macOS darwin/arm64; Docker Desktop Linux/arm64 |
| Docker CLI / Engine | 29.6.2 |
| Compose | 5.3.1 |
| Image | `pgvector/pgvector:pg16` |
| Pulled image digest | `sha256:7b822b0aac60967beb1ea5e576b8602c94c300a157d187f385ae3e0da199b90a` |
| pgvector extension | 0.8.7 |
| Test Compose project | `paios-dbtest` |
| Test service/port | `postgres-test`, `127.0.0.1:55432` |
| Database/user | `paios_test` / `paios_test` |
| Test storage | Named disposable volume `paios-integration-test-data` |

### Image pull diagnosis

The image was not present locally at the start of this phase. A direct `docker pull pgvector/pgvector:pg16` then completed successfully, downloaded all layers, and returned the digest above. Docker Desktop was running with the `desktop-linux` context on arm64; no registry mirror was configured. No image substitution or database version change was necessary. The previous slow pull did not reproduce on retry.

The repository's regular PostgreSQL service and other running containers were not started, stopped, or modified. The test service uses an explicitly isolated Compose project/profile, a different port, credentials, database name, and named volume.

## Migration validation

Migrations are embedded SQL files applied by `migrations.Apply` under one PostgreSQL transaction and a transaction-scoped advisory lock. The runner records SHA-256 checksums and rejects changes to a previously applied file. Historical SQL migration files were not edited in this phase.

Verified on an empty test volume:

1. `schema_migrations` was absent/empty before application (0 recorded versions).
2. Ordered files `0001_core.sql` through `0007_model_usage.sql` applied successfully.
3. Recorded versions were exactly:
   - `0001_core.sql`
   - `0002_execution_approval_memory.sql`
   - `0003_goal_evidence.sql`
   - `0004_knowledge_vectors.sql`
   - `0005_task_attempts.sql`
   - `0006_nodes.sql`
   - `0007_model_usage.sql`
4. Each stored checksum had 64 hexadecimal characters.
5. A repeated `migrations.Apply` succeeded and left exactly seven migration records.
6. `pg_extension` reported pgvector 0.8.7. Representative Goal/Task/recovery/full-text/Node indexes were present.
7. On a separate disposable database, an event trigger deliberately failed migration execution at index creation. The test confirmed the `users` table and `schema_migrations` table/versions were rolled back. After removing the injected trigger, the same database accepted all seven migrations.

The tests also exercise representative foreign-key, unique, and check constraints. They do not exhaustively enumerate every constraint or PostgreSQL version/upgrade path.

## Database integration coverage

The suite is opt-in via `PAIOS_TEST_DATABASE_URL`; `make test-integration` supplies its isolated test DSN automatically. The integration helper rejects database names that do not contain `test`. The Make target first removes only the explicitly named disposable test container/volume, then recreates them.

| Area | Verified against PostgreSQL |
| --- | --- |
| Goal | Create, Get, Update, activate, evidence-backed completion, timestamp/progress persistence and evidence reload |
| Task | Create, ready/running/completed transitions, attempt count/timestamp, listing, dependency-plan rollback, and recovery query for Running tasks |
| Execution | Save and query a verified execution, execution evidence, model-usage ledger, and unique idempotency-key constraint |
| Evidence | Goal evidence and execution evidence persistence and association |
| Approval | Create/Get/decision persistence, Approval→Task FK, two concurrent contradictory decisions yield exactly one success, and failed audit write rolls the decision back |
| Policy | A3 resolves to `RequireApproval`; tool is not invoked without approval; changed parameters are rejected; the exact approved parameter set can invoke the test tool |
| Audit | Mutation records are checked alongside Goal/Task/Execution/Approval writes; injected audit failure verifies transaction rollback |
| Event / Outbox | Outbox rows are checked with corresponding mutations and after restart; injected failures verify no orphan event for failed Goal/Execution transactions |
| Memory | Create, full-text search, update, soft delete/not-found behavior, and category check constraint |
| Knowledge | Text document/chunk import, content-hash duplicate rejection, metadata/list and cited full-text search |
| Node | Register, identity upsert, offline reset, heartbeat state/time persistence and retrieval |
| Migration | Empty database, ordered version/checksum ledger, idempotent re-application, pgvector availability, and injected transactional rollback/retry |
| Service/DB restart | Goal, Task, Execution, Evidence, Approval, Memory, Knowledge, Node, Audit and Outbox are read by a freshly launched Go Core after restarting PostgreSQL |

### Transaction consistency observations

- Goal create writes the Goal, its Outbox row, and its Audit row in one transaction. An injected Audit insert error left all three absent.
- `CreateTasks` writes all planned tasks, dependencies, and mutation journal entries transactionally. An unknown dependency after an earlier task insert left no partial tasks.
- `ExecutionStore.Save` writes the Execution Run, Evidence, optional Model Usage, Outbox, and Audit in one transaction. An injected Audit error left no Run/Evidence rows.
- Approval decisions use `SELECT ... FOR UPDATE`, then update status plus journal entries in one transaction. An injected Audit error left the Approval pending; a subsequent two-writer race committed exactly one decision and its journal entries.
- The service restart test stored a verified Execution/Evidence and completed Goal/Task data before a PostgreSQL container restart, then read the state through a newly launched Go Core process.

The database does not currently persist a separate Task→Execution transaction spanning both operations. The Supervisor/store workflow currently commits execution evidence and Task status through separate transactions with recovery reconciliation for common interruption windows. A durable pre-side-effect idempotency claim and background recovery worker remain unimplemented.

## Commands and reproducibility

- `make db-test-up`: starts only the disposable integration PostgreSQL service.
- `make db-test-down`: stops/removes only that service container.
- `make db-test-clean`: removes the test service and the named disposable test volume.
- `make test-integration`: builds the service; recreates isolated test DB; runs empty-database migration validation; runs `go test ./...`, `go test -race ./...`, `go vet ./...`, and `make build`; runs DB integration tests; restarts PostgreSQL; launches a fresh service process to verify persistence; cleans the isolated test container and volume.

`PAIOS_TEST_DATABASE_URL` can be overridden when running the integration package manually, but it must point to a dedicated disposable database whose name contains `test`. Do not point it at a user or production database. Manual tests do not automatically drop a caller-supplied database; only the Make target owns and removes the dedicated Compose test volume.

## Findings and changes made

1. Earlier image pull timeout was transient; retry succeeded with the configured official pgvector image/tag. No image change was made.
2. The prior integration test only covered basic Goal/Task operations and contained non-fatal cleanup omissions. It was replaced with fixture-scoped integration coverage and explicit cleanup against a test-named database.
3. The Compose file previously had no separately isolated integration DB service. A `test` profile now defines `postgres-test` with a separate port, database/user/password, Compose project and named test-only volume.
4. Make targets were added for isolated test DB start/stop/cleanup and the complete database integration/restart workflow. The integration target never uses Compose `down` on the application project.
5. PostgreSQL tests exposed and corrected a recovery test expectation (only Running/Retrying tasks are returned), and the restart scenario was adjusted to follow valid Goal transitions and inspect Goal evidence through the detail endpoint.
6. Historical migration SQL files remain byte-for-byte unchanged to preserve the migration runner's stored checksum compatibility.

## Not verified / remaining limitations

- No exhaustive property-based test for every DDL constraint or every concurrency interleaving.
- No migration test against an existing production-sized historical database or an upgrade from every earlier schema snapshot.
- PostgreSQL server restart was tested, but abrupt power-loss/filesystem corruption scenarios were not simulated.
- The integration suite verifies transaction rollback with isolated temporary test triggers; it does not provide production SQL-level audit/outbox read APIs or an outbox publisher.
- Policy decisions are deterministic Go logic, not rows in a separate Policy table. Approval persistence and the tool gate are tested together, but production side-effect tools are not currently enabled.
- No vector embeddings are generated, and no vector similarity query is implemented; pgvector extension/column availability alone does not provide semantic retrieval.
- There is no independent background recovery worker or durable idempotency claim before an external operation.
