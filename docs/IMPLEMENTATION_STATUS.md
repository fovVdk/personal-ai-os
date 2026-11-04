# Implementation Status

Updated: 2026-10-31

## Current checkpoint

- Documentation pass: Go source, tests, and operational templates carry English/Chinese comments. Historical SQL migration files remain byte-for-byte unchanged because their raw-content checksums are verified on startup; schema purposes and constraints are documented in README. README documents current architecture, module boundaries, data flow, routes, persistence, security/configuration, and incomplete capabilities.
- Phase 0 repository/environment inspection: complete.
- Phase 9 database persistence integration verification: complete. See [Database integration report](DATABASE_INTEGRATION_REPORT.md).
- Phase 10 Durable Execution Runtime: implementation and migration 0008 are complete; the final fresh-volume `make test-integration` passed, including full Go/race/vet/build, migrations/fault injection, Worker concurrency/fencing/checkpoints/cancellation, and PostgreSQL + new Core restart recovery. Details and limits: [Phase 10 implementation report](PHASE10_IMPLEMENTATION_REPORT.md).
- Environment: Go 1.27.3 darwin/arm64; module directive remains Go 1.22. Docker Engine 29.6.2 and Docker Compose 5.3.1 are available.
- The `pgvector/pgvector:pg16` pull succeeded on retry (arm64 digest recorded in the database report); pgvector extension 0.8.7 was verified. The dedicated test database container and named data volume were removed after the successful run. Existing Compose services were left untouched.
- The checkout is located at the configured GOPATH root. The Makefile sets an external temporary GOPATH so Go Modules work correctly.
- Latest checks passed: `make fmt`, `make test`, `make build`, `make vet`, full `go test ./...` and `go test -race ./...` while test PostgreSQL was available, PostgreSQL migration/integration tests, and Compose test-profile parsing.

## Completed implementation

- Runtime: validated env configuration (including execution/model-call/tool-call/token/cost budgets and secure API key rules), JSON slog, pgx pool, embedded ordered SQL migrations with transaction/advisory lock/checksum checks, Chi HTTP server, request IDs, timeout/recovery, API-key middleware, health/readiness endpoints and signal-driven graceful shutdown.
- Durable Goal/Task slice: UUID owner-scoped Goal CRUD and PATCH, Task CRUD/list, transactional dependency-aware plan task insertion, Goal/Task state machine endpoints, evidence-gated Goal completion, transactional event outbox and audit inserts for Goal/Task create/update/transition.
- Policy primitives: A0-A3 deterministic decisions, approvals bound to exact tool/action/parameter hash and expiry, a tool execution guard that rejects side effects until approval is valid.
- Execution slice: bounded exponential retry, in-process concurrent idempotency primitive, persisted execution runs/evidence, attempt-count-bounded Task retries, and Supervisor reconciliation of interrupted `running`/`retrying` work including the evidence-saved-before-task-completion crash window. There is still no atomic durable idempotency claim before an external operation or independent background recovery worker.
- Planning and Agent slice: deterministic validated structured plans with DAG dependency checks; generated tasks are persisted atomically. Context-aware Model Planner strictly rejects unknown fields, extra JSON, cross-goal output, invalid DAGs and inconsistent estimates. General, Research, and Planning Mock Agents are registered and listed through API.
- Model slice: provider-neutral Gateway, deterministic Mock, OpenAI-compatible Chat Completions and Ollama `/api/chat` providers, token usage, bounded timeouts/response sizes, provider fallback, and strict Tool-call allowlist/JSON schema/required-field/value validation. Runtime config selects Mock/OpenAI-compatible/Ollama; real model keys stay in environment. Tool-call results remain proposals only; no model output automatically executes tools. JSON tool schemas are intentionally restricted to the implemented subset and unsupported schema keywords fail closed.
- Supervisor slice: bounded per-Goal budget for steps/model calls/tool calls/tokens/cost/runtime/retries; model-call/token usage is persisted in its own ledger across turns; deterministic task-to-agent routing; dependency-aware task execution; persistent attempt counter; recovery checks for Running/Retrying work and for evidence saved before the task status update. Mock outputs explicitly use `unverified` and cannot complete Tasks/Goals; the HTTP API surfaces this as 409 rather than claiming success.
- Approval slice: create/list/get/approve/reject API and PostgreSQL persistence; creation and decisions write audit/outbox records. Approval execution gate verifies exact action/parameters/expiry before tool invocation.
- Memory/Knowledge/Node slice: provenance/category/confidence validation, Memory PostgreSQL CRUD/soft deletion/full-text search, text document ingestion, rune-safe overlapping chunking, content deduplication, PostgreSQL full-text cited search, Node registration/list/detail/heartbeat repository; pgvector extension and flexible vector columns are provisioned but embeddings/vector ranking are not wired.
- Tests: Goal and Task state transitions; policy; approval hash/expiry and API flow; retry/idempotency; tool approval gate; config safety; Goal/Task/Planner/Supervisor/Approval/Agent/Node HTTP/domain behavior; provider/Tool-call schema/value validation; Knowledge chunking; PostgreSQL migrations, transaction rollback, core persistence, constraints, concurrent approvals, and restart recovery.
- Delivery: Makefile, example environment/YAML configuration, PostgreSQL/pgvector + opt-in Ollama Compose manifest, OpenAPI, architecture/implementation docs, test report and `.gitignore` for secrets/runtime/build output.

## Not yet implemented

- Eino adapter; Model fallback is interface-level and does not yet include retry/circuit-breaker/health policy.
- Safe built-in tool registry (time/query/note/allowlisted-file), user-facing tool approval association, and MCP integration. Tool authorization primitives are tested but there are no production side-effect tools.
- Execution runs/Evidence are durable and recovery handles common commit-order crash windows, but there is no independent background recovery worker, per-step checkpoint, or durable idempotency-key claim before an external operation.
- Memory embedding generation/pgvector retrieval; Knowledge APIs currently accept text only (no file/object storage ingestion).
- Identity/RBAC/API token storage beyond the local owner and one shared API key; Node heartbeat exists at repository level but no external heartbeat route or liveness expiry worker is wired.
- OpenTelemetry, event outbox publisher, audit query/retention policy.
- Manual PostgreSQL integration tests require `PAIOS_TEST_DATABASE_URL` pointing to a dedicated database whose name contains `test`. `make test-integration` owns its isolated Compose database and removes only its dedicated container and named test volume.
- Phase 10 adds migration 0008, PostgreSQL durable enqueue/claims/leases/fencing/checkpoints/step idempotency, Worker Pool/recovery scanner, Supervisor runtime integration, and Execution status/cooperative cancellation APIs. A real PostgreSQL restart test verifies expired claimed work is re-fenced, safely re-claimed, and terminalized without treating unverified Mock output as success. Remaining limitations are listed in the implementation report.
- No dedicated real-model E2E suite yet.

## Next actions

1. Keep the current mock workflow honest: do not mark a real Goal done from mock prose; add an explicit verified artifact/evidence contract before changing that behavior.
2. Implement per-step checkpoints and atomic durable idempotency claims before enabling side-effect tools.
3. Add permission-bound built-in tools/MCP, then connect a verifier to model-produced outputs and human approvals.
4. Add Memory vectors/Knowledge ingestion, then Node/observability and full E2E/failure-recovery coverage.
