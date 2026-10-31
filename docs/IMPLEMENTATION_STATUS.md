# Implementation Status

Updated: 2026-10-31

## Current checkpoint

- Documentation pass: Go source, tests, and operational templates now carry English/Chinese comments. Historical SQL migration files were intentionally left byte-for-byte unchanged because their raw-content checksums are verified on startup; their schema purposes and constraints are documented bilingually in README. README now documents current architecture, module boundaries, data flow, routes, persistence, security/configuration, verified checks, and explicitly incomplete capabilities.

- Phase 0 repository/environment inspection: complete.
- Current `make build`, `make test`, `make vet`, and `go test -race ./...`: passed. Unit and handler tests cover implemented slices; database integration tests are present and skip unless a dedicated `PAIOS_TEST_DATABASE_URL` is set.
- Environment: Go 1.27.3 darwin/arm64; module directive remains Go 1.22. Docker Engine 29.6.2 and Docker Compose 5.3.1 are available.
- Note: the checkout is the GOPATH root and has no `.git` metadata. Module commands require an external temporary GOPATH to avoid Go's warning.
- Pulling `pgvector/pgvector:pg16` exceeded three minutes and was cancelled; the image is not available locally. The attempt used isolated `/tmp/paios-postgres-test-20261031` storage, so no repository or user data was touched. Database migrations/integration checks are not yet confirmed.

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
- Tests: Goal and Task state transitions; policy; approval hash/expiry and API flow; retry/idempotency; tool approval gate; config safety; Goal/Task/Planner/Supervisor/Approval/Agent/Node HTTP/domain behavior; provider/Tool-call schema/value validation; Knowledge chunking.
- Delivery: Makefile, example environment/YAML configuration, PostgreSQL/pgvector + opt-in Ollama Compose manifest, OpenAPI, architecture/implementation docs, test report and `.gitignore` for secrets/runtime/build output.

## Not yet implemented

- Eino adapter; Model fallback is interface-level and does not yet include retry/circuit-breaker/health policy.
- Safe built-in tool registry (time/query/note/allowlisted-file), user-facing tool approval association, and MCP integration. Tool authorization primitives are tested but there are no production side-effect tools.
- Execution runs/Evidence are durable and recovery handles common commit-order crash windows, but there is no independent background recovery worker, per-step checkpoint, or durable idempotency-key claim before an external operation.
- Memory embedding generation/pgvector retrieval; Knowledge APIs currently accept text only (no file/object storage ingestion).
- Identity/RBAC/API token storage beyond the local owner and one shared API key; Node heartbeat exists at repository level but no external heartbeat route or liveness expiry worker is wired.
- OpenTelemetry, event outbox publisher, audit query/retention policy.
- Database integration tests run only when `PAIOS_TEST_DATABASE_URL` is set; no dedicated full approval/memory/knowledge integration or real-model E2E suite yet.

## Next actions

1. Finish the isolated PostgreSQL+pgvector image pull, apply migrations and run integration tests using a dedicated disposable test database.
2. Keep the current mock workflow honest: do not mark a real Goal done from mock prose; add an explicit verified artifact/evidence contract before changing that behavior.
3. Implement per-step checkpoints and atomic durable idempotency claims before enabling side-effect tools.
4. Add permission-bound built-in tools/MCP, then connect a verifier to model-produced outputs and human approvals.
5. Add Memory vectors/Knowledge ingestion, then Node/observability and full E2E/failure-recovery coverage.
