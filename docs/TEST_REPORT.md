# Test and Validation Report

Date: 2026-10-31

## Environment

- macOS, darwin/arm64
- Go `go1.27.3`; module language target remains Go 1.22
- Docker CLI 29.6.2 and Docker Compose 5.3.1 are installed
- The checkout itself is inside GOPATH; repository Make targets force `/tmp/paios-gopath` so Go modules are honored

## Completed checks

| Check | Result | Notes |
| --- | --- | --- |
| `make fmt` | Passed | `gofmt` on Go source |
| `make test` | Passed | All runnable package tests pass |
| `make build` | Passed | Produces `bin/paios` (ignored by Git) |
| `make vet` | Passed | `go vet ./...` |
| `go test -race ./...` | Passed | Run with external GOPATH |
| Docker Compose config parse | Passed | `docker compose --env-file .env.example -f deployments/compose/docker-compose.yml config -q` |
| YAML syntax validation | Passed | Compose, example config, and OpenAPI YAML parsed |
| Bilingual documentation audit | Passed | All 100 Go files have bilingual file headers; public GoDoc and existing Go comments were translated; operational templates and README reviewed |
| Historical migration checksum preservation | Passed | SQL migration contents were left unchanged so existing raw-file checksums remain stable; migration design is documented in README |
| PostgreSQL migration/integration run | Passed | Phase 9 used isolated PostgreSQL 16/pgvector 0.8.7; empty-database migrations, injected rollback/retry, core DB integration, full tests/race tests, and database + fresh Core restart persistence all passed. Details: [database report](DATABASE_INTEGRATION_REPORT.md) |
| Phase 10 Durable Runtime acceptance | Passed | Fresh-volume `make test-integration` completed Migration 0008/fault rollback, full Go/race/vet/build, real Worker claim/fencing/checkpoint/idempotency/cancellation tests, and PostgreSQL restart + fresh Core startup recovery. An unverified Mock result safely ends failed rather than claiming success. See [Phase 10 report](PHASE10_IMPLEMENTATION_REPORT.md). |
| Eight-step worker crash acceptance | Passed | One Goal/five dependent Tasks and two Workers; the first subprocess was SIGKILLed after step-1 Checkpoint; the second waited for lease expiry, reclaimed with higher fencing, and resumed without repeating step 1. After Go Core restart, the Mock result was safely rejected; a verified Worker then continued the remaining Task. Audit/Execution/Evidence showed no false completion, and a fresh Core process read final state via HTTP. |

Latest Phase 10 integration run also fixed an SQL issue in `VerifiedTaskIDs`: PostgreSQL requires `ORDER BY` expressions in a `SELECT DISTINCT` query to match the selected expression. The query now orders by the selected text-cast Task ID; the restarted Supervisor safely returns HTTP 409 for the unverified Mock result, after which a verified Worker completes the pending Task.

## Automated test areas

- Goal and Task state transitions; evidence-gated completion
- Policy A0–A3, approval expiry/hash binding, and rejection of tool side effects while approval is pending
- Retry/idempotency and Supervisor execution/evidence/recovery windows
- Supervisor cross-turn persisted model call/token budget accounting
- Configuration validation and API authentication, validation, size limits, Goal/Task/Planner/Approval/Node flows
- Mock/OpenAI-compatible/Ollama Model Gateway behavior and untrusted tool-call schema/allowlist validation
- Structured planner DAG/schema validation; Knowledge text chunking and provenance checks
- Real PostgreSQL CRUD, foreign-key/unique/check constraints, execution/evidence persistence, model usage, concurrent approval decisions, injected Audit/Outbox transaction failures, Memory/Knowledge/Node stores, and database migration history.
- PostgreSQL container restart followed by a fresh Core process reading Goal, Task, Execution, Evidence, Approval, Memory, Knowledge, Node, Audit, and Outbox state.
- Eight-step Worker crash acceptance: two active Worker instances contend for a run; one subprocess is force-killed after step-1 Checkpoint; the second waits for Lease expiry, takes over with a higher fencing token, resumes from that Checkpoint without a duplicate step claim, completes the dependency chain, restarts Go Core with the last Task pending, safely rejects unverified Mock output, then resumes the Task using a verified test Worker. Audit/Execution/Evidence and non-completed Goal state are checked; a second fresh Core process reads final persisted state over HTTP.

PostgreSQL integration tests are gated by `PAIOS_TEST_DATABASE_URL` and reject database names without `test`. `make test-integration` creates an isolated project/database/volume and cleans only its own resources. Never point manual integration tests at user or production data. See [database report](DATABASE_INTEGRATION_REPORT.md) for precise scope and remaining limitations.
