# Test and Validation Report

Date: 2026-11-04 (updated after Phase 12 ModelAgent Tool Call integration)

## Environment

- macOS, darwin/arm64
- Go `go1.27.3`; module minimum is Go 1.25.0 for official MCP SDK v1.8.0.
- Go 1.25.0 toolchain download was attempted through the configured proxy but timed out at the toolchain storage redirect; the installed Go 1.27.3 toolchain runs the module. Native darwin/arm64 build is separately verified.
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
| `make build-arm64` | Passed | Cross-builds Personal AI OS for macOS darwin/arm64 |
| Go 1.25.0 CI acceptance | Added | `.github/workflows/ci.yml` pins exact Go 1.25.0, asserts the toolchain version, runs full tests/race/PostgreSQL acceptance and separate macOS ARM64 stdio/ARM64 verification. Hosted CI result is pending its first run. |
| Docker Compose config parse | Passed | `docker compose --env-file .env.example -f deployments/compose/docker-compose.yml config -q` |
| YAML syntax validation | Passed | Compose, example config, and OpenAPI YAML parsed |
| Bilingual documentation audit | Passed | Go source headers/public API comments and operational templates reviewed; new MCP contracts include bilingual GoDoc where applicable |
| Historical migration checksum preservation | Passed | SQL migration contents were left unchanged so existing raw-file checksums remain stable; migration design is documented in README |
| PostgreSQL migration/integration run | Passed | Phase 9 used isolated PostgreSQL 16/pgvector 0.8.7; empty-database migrations, injected rollback/retry, core DB integration, full tests/race tests, and database + fresh Core restart persistence all passed. Details: [database report](DATABASE_INTEGRATION_REPORT.md) |
| Phase 10 Durable Runtime acceptance | Passed | Fresh-volume `make test-integration` completed Migration 0008/fault rollback, full Go/race/vet/build, real Worker claim/fencing/checkpoint/idempotency/cancellation tests, and PostgreSQL restart + fresh Core startup recovery. An unverified Mock result safely ends failed rather than claiming success. See [Phase 10 report](PHASE10_IMPLEMENTATION_REPORT.md). |
| Eight-step worker crash acceptance | Passed | One Goal/five dependent Tasks and two Workers; the first subprocess was SIGKILLed after step-1 Checkpoint; the second waited for lease expiry, reclaimed with higher fencing, and resumed without repeating step 1. After Go Core restart, the Mock result was safely rejected; a verified Worker then continued the remaining Task. Audit/Execution/Evidence showed no false completion, and a fresh Core process read final state via HTTP. |
| Phase 11 Tool Fabric unit + PostgreSQL acceptance | Passed | Registry/Gateway/Policy/Native filesystem and SSRF boundary tests; PostgreSQL 0009 migration, exact Approval binding/consumption, approved Execution requeue, concurrent idempotent invocation, replay/conflict, Audit, and expired-lease `unknown` recovery. See [Phase 11 report](PHASE11_IMPLEMENTATION_REPORT.md). |
| Official MCP SDK v1.8.0 build and real Server suite | Passed | Downloaded through `127.0.0.1:18780`; module target raised to Go 1.25.0. Real SDK Client/Server Streamable HTTP tests cover connect/discovery/schema/call/replay/approval/error/timeout/schema update/disable/restart/reconnect. |
| Real stdio subprocess lifecycle and failure recovery | Passed | Official SDK stdio server runs as a child test process; disable/enable and forced process exit produce `unknown`, auto-restart/discovery, and successful subsequent invocation. Race test repeated five times on darwin/arm64. |
| MCP + PostgreSQL Gateway E2E | Passed | Fresh isolated DB test uses a real official SDK MCP Server and exercises Gateway approval, Execution requeue/fencing, durable Invocation result replay, Audit and Outbox. |
| Phase 12 ModelAgent unit/provider tests | Passed | Bounded model Tool Call roundtrips through the Core-injected Gateway caller; opaque alias binding, approval checkpoint/replay, and checkpoint exclusion/reconstruction of tool result content pass. OpenAI-compatible and Ollama message wire mappings pass. |
| Migration 0010 + Goal Tool Call usage | Passed | Fresh PostgreSQL 16/pgvector 0.8.7 integration applies all ten migrations, validates failure rollback/retry, and checks durable Execution usage plus Gateway invocation-based cross-turn Tool Call accounting. |
| Phase 12 full acceptance after latest edits | Passed | `make fmt`, `make test`, `make build`, `make vet`, `go test -race ./...`, and `make test-integration`; integration target also runs full package/race/vet tests, Worker crash/checkpoint recovery, and PostgreSQL restart/fresh-Core persistence acceptance. |

Latest Phase 10 integration run also fixed an SQL issue in `VerifiedTaskIDs`: PostgreSQL requires `ORDER BY` expressions in a `SELECT DISTINCT` query to match the selected expression. The query now orders by the selected text-cast Task ID; the restarted Supervisor safely returns HTTP 409 for the unverified Mock result, after which a verified Worker completes the pending Task.

## Automated test areas

- Goal and Task state transitions; evidence-gated completion
- Policy A0–A3, approval expiry/hash binding, and rejection of tool side effects while approval is pending
- Retry/idempotency and Supervisor execution/evidence/recovery windows
- Supervisor cross-turn persisted model call/token budget accounting
- Configuration validation and API authentication, validation, size limits, Goal/Task/Planner/Approval/Node flows
- Mock/OpenAI-compatible/Ollama Model Gateway behavior and untrusted tool-call schema/allowlist validation
- ModelAgent bounded assistant Tool Call/tool-result loops, exact alias-to-Registry identity mapping, Approval checkpoint continuation, and result reconstruction from durable Gateway replay without checkpointing result payloads
- Goal Tool Call budget aggregation from durable Invocation records and persisted Execution usage
- Tool Registry stable provider/Tool/version identity, schema validation, health/enabled state, and default-deny Policy grants
- Tool Gateway Lease/Fencing gates, canonical Tool-version argument hash, Approval binding and one-time consumption, Core-observed budget, output validation, and unknown outcomes
- Native `time.now`, authorized-root filesystem traversal/symlink tests, owner-scoped Memory/Knowledge search registration, HTTP allowlist/private destination denial, and authenticated Tool metadata endpoints
- Real PostgreSQL Tool Invocation approval/requeue/fencing, parallel identical reservation, durable result replay, changed-parameter conflict, rejected approval, Audit/Outbox and Lease-expiry unknown recovery
- Official MCP SDK v1.8.0 real Client ↔ `StreamableHTTPHandler` Server lifecycle, tool discovery/versioning, Gateway-only call, default-deny/A3 Approval gates, input/output Schema rejection, error/timeout/cancellation unknown status, disable/unregister and Server process restart/reconnect
- PostgreSQL-backed MCP end-to-end approval continuation, new fencing claim, exactly-one server-side invocation, durable replay, Audit/Outbox
- Structured planner DAG/schema validation; Knowledge text chunking and provenance checks
- Real PostgreSQL CRUD, foreign-key/unique/check constraints, execution/evidence persistence, model usage, concurrent approval decisions, injected Audit/Outbox transaction failures, Memory/Knowledge/Node stores, and database migration history.
- PostgreSQL container restart followed by a fresh Core process reading Goal, Task, Execution, Evidence, Approval, Memory, Knowledge, Node, Audit, and Outbox state.
- Eight-step Worker crash acceptance: two active Worker instances contend for a run; one subprocess is force-killed after step-1 Checkpoint; the second waits for Lease expiry, takes over with a higher fencing token, resumes from that Checkpoint without a duplicate step claim, completes the dependency chain, restarts Go Core with the last Task pending, safely rejects unverified Mock output, then resumes the Task using a verified test Worker. Audit/Execution/Evidence and non-completed Goal state are checked; a second fresh Core process reads final persisted state over HTTP.

PostgreSQL integration tests are gated by `PAIOS_TEST_DATABASE_URL` and reject database names without `test`. `make test-integration` creates an isolated project/database/volume and cleans only its own resources. Never point manual integration tests at user or production data. See [database report](DATABASE_INTEGRATION_REPORT.md) for precise scope and remaining limitations.

Exact Go 1.25.0 toolchain download locally timed out at the toolchain storage redirect; project tests passed on installed Go 1.27.3 with the Go 1.25.0 module minimum. CI now runs the exact Go 1.25.0 acceptance. Real Streamable HTTP and stdio MCP servers are covered; OAuth/credential-provider integration remains unimplemented. See [MCP implementation report](PHASE11_MCP_IMPLEMENTATION_REPORT.md).
