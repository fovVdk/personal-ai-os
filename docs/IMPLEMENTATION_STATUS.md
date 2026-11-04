# Implementation Status

Updated: 2026-10-31

## Current checkpoint

- Documentation pass: Go source, tests, and operational templates carry English/Chinese comments. Historical SQL migration files remain byte-for-byte unchanged because their raw-content checksums are verified on startup; schema purposes and constraints are documented in README. README documents current architecture, module boundaries, data flow, routes, persistence, security/configuration, and incomplete capabilities.
- Phase 0 repository/environment inspection: complete.
- Phase 9 database persistence integration verification: complete. See [Database integration report](DATABASE_INTEGRATION_REPORT.md).
- Phase 10 Durable Execution Runtime and eight-step worker crash acceptance: complete. Fresh-volume PostgreSQL verification SIGKILLed a Worker after Checkpoint persistence, confirmed Lease-expiry takeover and checkpoint resume, completed a five-Task dependency chain, safely rejected unverified Mock output after Core restart, then continued with a verified test Worker. Full Go/race/vet/build and Core restart HTTP reads passed. Details/limits: [Phase 10 implementation report](PHASE10_IMPLEMENTATION_REPORT.md).
- Environment: Go 1.27.3 darwin/arm64; module directive remains Go 1.22. Docker Engine 29.6.2 and Docker Compose 5.3.1 are available.
- The `pgvector/pgvector:pg16` pull succeeded on retry (arm64 digest recorded in the database report); pgvector extension 0.8.7 was verified. The dedicated test database container and named data volume were removed after the successful run. Existing Compose services were left untouched.
- The checkout is located at the configured GOPATH root. The Makefile sets an external temporary GOPATH so Go Modules work correctly.
- Latest checks passed: `make fmt`, `make test`, `make build`, `make vet`, full `go test ./...` and `go test -race ./...` while test PostgreSQL was available, PostgreSQL migration/integration tests, and Compose test-profile parsing.

## Completed implementation

- Runtime: validated env configuration (including execution/model-call/tool-call/token/cost budgets and secure API key rules), JSON slog, pgx pool, embedded ordered SQL migrations with transaction/advisory lock/checksum checks, Chi HTTP server, request IDs, timeout/recovery, API-key middleware, health/readiness endpoints and signal-driven graceful shutdown.
- Durable Goal/Task slice: UUID owner-scoped Goal CRUD and PATCH, Task CRUD/list, transactional dependency-aware plan task insertion, Goal/Task state machine endpoints, evidence-gated Goal completion, transactional event outbox and audit inserts for Goal/Task create/update/transition.
- Policy primitives: A0-A3 deterministic decisions, approvals bound to exact tool/action/parameter hash and expiry, a tool execution guard that rejects side effects until approval is valid.
- Execution slice: bounded exponential retry, legacy in-process idempotency primitive, and the Phase 10 PostgreSQL Durable Runtime with scoped request/step idempotency, multi-worker claim, lease/heartbeat/fencing, ordered Checkpoint, safe recovery, Audit/Outbox state history and background Worker Pool. Arbitrary external effects are not exactly-once; uncertain non-idempotent operations become `unknown`.
- Planning and Agent slice: deterministic validated structured plans with DAG dependency checks; generated tasks are persisted atomically. Context-aware Model Planner strictly rejects unknown fields, extra JSON, cross-goal output, invalid DAGs and inconsistent estimates. General, Research, and Planning Mock Agents are registered and listed through API.
- Model slice: provider-neutral Gateway, deterministic Mock, OpenAI-compatible Chat Completions and Ollama `/api/chat` providers, token usage, bounded timeouts/response sizes, provider fallback, and strict Tool-call allowlist/JSON schema/required-field/value validation. Runtime config selects Mock/OpenAI-compatible/Ollama; real model keys stay in environment. Tool-call results remain proposals only; no model output automatically executes tools. JSON tool schemas are intentionally restricted to the implemented subset and unsupported schema keywords fail closed.
- Supervisor slice: bounded per-Goal budget for steps/model calls/tool calls/tokens/cost/runtime/retries; model-call/token usage is persisted in its own ledger across turns; deterministic task-to-agent routing; dependency-aware durable task execution and evidence reconciliation. Mock outputs explicitly use `unverified` and cannot complete Tasks/Goals; the Durable Worker and HTTP API do not claim simulated work succeeded.
- Approval slice: create/list/get/approve/reject API and PostgreSQL persistence; creation and decisions write audit/outbox records. Approval execution gate verifies exact action/parameters/expiry before tool invocation.
- Memory/Knowledge/Node slice: provenance/category/confidence validation, Memory PostgreSQL CRUD/soft deletion/full-text search, text document ingestion, rune-safe overlapping chunking, content deduplication, PostgreSQL full-text cited search, Node registration/list/detail/heartbeat repository; pgvector extension and flexible vector columns are provisioned but embeddings/vector ranking are not wired.
- Tests: Goal and Task state transitions; policy; approval hash/expiry and API flow; retry/idempotency; tool approval gate; config safety; Goal/Task/Planner/Supervisor/Approval/Agent/Node HTTP/domain behavior; provider/Tool-call schema/value validation; Knowledge chunking; PostgreSQL migrations, transaction rollback, core persistence, constraints, concurrent approvals, restart recovery, and the full Worker crash/Checkpoint/five-dependent-Task acceptance scenario.
- Delivery: Makefile, example environment/YAML configuration, PostgreSQL/pgvector + opt-in Ollama Compose manifest, OpenAPI, architecture/implementation docs, test report and `.gitignore` for secrets/runtime/build output.

## Not yet implemented

- Eino adapter; Model fallback is interface-level and does not yet include retry/circuit-breaker/health policy.
- Safe built-in tool registry (time/query/note/allowlisted-file), user-facing tool approval association, and MCP integration. Tool authorization primitives are tested but there are no production side-effect tools.
- No production side-effect Tool runner or independently verified real Agent is enabled. The worker runtime and Step idempotency contracts are implemented, but future tools must provide their own external idempotency guarantee and verifier before automatic replay is safe.
- Memory embedding generation/pgvector retrieval; Knowledge APIs currently accept text only (no file/object storage ingestion).
- Identity/RBAC/API token storage beyond the local owner and one shared API key; Node heartbeat exists at repository level but no external heartbeat route or liveness expiry worker is wired.
- OpenTelemetry, event outbox publisher, audit query/retention policy.
- Manual PostgreSQL integration tests require `PAIOS_TEST_DATABASE_URL` pointing to a dedicated database whose name contains `test`. `make test-integration` owns its isolated Compose database and removes only its dedicated container and named test volume.
- No dedicated real-model E2E suite yet.

## Next actions

1. Keep the current mock workflow honest: do not mark a real Goal done from mock prose; add an explicit verified artifact/evidence contract before changing that behavior.
2. Connect independently verified artifacts/results to the Worker success gate before reporting real-world completion.
3. Add permission-bound built-in tools/MCP with explicit external idempotency and Approval linkage before enabling side effects.
4. Add Memory vectors/Knowledge ingestion, then Node/observability and full E2E/failure-recovery coverage.
