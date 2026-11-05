# Test and Validation Report

Date: 2026-11-05 (updated after Phase 12.3 implementation work)

## Phase 12.3 final verification addendum

- `make fmt`, `make test`, `make build`, `make vet`: passed after Phase 12.3 code changes.
- `go test -race ./...`: passed using Go 1.27.3 darwin/arm64.
- `make build-arm64`: passed.
- `make test-integration`: passed on isolated PostgreSQL 16/pgvector 0.8.7; all 16 migrations applied and injected migration rollback/retry passed. Coverage includes Goal Job claim/fencing/restart, Outbox lease expiry/redelivery/Approval wake/retry/dead-letter, Source Snapshot raw-data roundtrip/reference verification/immutability, Worker crash/checkpoint recovery, and PostgreSQL/Core restart.
- Docker Compose config and YAML parse for Compose, config template and OpenAPI: passed.
- Hosted Go 1.25 CI: pending; local Go 1.27.3 results are not substituted for hosted CI.
- Complete single-scenario Autonomous Research Goal E2E: NOT VERIFIED; see [Autonomous Goal E2E report](AUTONOMOUS_GOAL_E2E_REPORT.md).

## Phase 12.3.4 work-in-progress addendum (2026-11-04)

- Implemented after the baseline above: criterion-scoped atomic `artifact.write`, immutable `source.reference.add` Tool binding, and opt-in Risk-2 `report.export` with API Approval over exact criterion ID/version/path/SHA-256. A Goal Job-driven deterministic research E2E with API approval continuation and a separate real HTTP side-effect/Worker subprocess crash/fresh-Core unknown-recovery test are authored. Added `make test-autonomous-e2e` and retained opt-in `make test-source-live`.
- Targeted offline tests passed with `GOPROXY=off`: `internal/source`, `internal/tool/builtin`, `internal/app/config`, and `internal/supervisor`.
- Whole-repository and PostgreSQL E2E reruns are BLOCKED before compilation: module proxy requests for `github.com/jackc/pgx/v5` and `github.com/go-chi/chi/v5` timed out. The new integration tests are NOT VERIFIED; don't infer success from their source code.
- `make test-source-live` without operator URL/allowlist correctly returned BLOCKED; no live source was fetched.
- Full single-scenario Phase 12.3.4 closure (unknown effect + Worker takeover + Core restart + final research Goal completion) remains NOT VERIFIED; hosted Go 1.25 CI also pending.

## Documentation and comment coverage addendum

- Go source/test files were scanned: every file has a bilingual file/Package description and every Go line-comment block has a Chinese counterpart; compiler directives are explained by adjacent bilingual comments.
- README now documents current design principles, control/execution planes, modules, lifecycle and evidence flow, migration strategy, setup/deployment, Tool/Approval boundaries, and explicit limitations.
- Historical SQL migrations were intentionally not edited because their checksums are verified on startup; their English/Chinese purpose is listed in [Migration Catalog](MIGRATION_CATALOG.md).
- `make fmt` and `git diff --check` passed. Targeted offline packages (`source`, `builtin`, `tool/gateway`, `config`, `supervisor`, `planner`, `verification`) passed. The full `make test` attempt was blocked during dependency setup by `proxy.golang.org` timeouts fetching Chi and pgx; no whole-repository test/build pass is claimed for the latest workspace.

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
| Phase 12.2.1 Evaluator unit/API coverage | Passed | Covers missing criteria, unknown percentage, approvals, failures, dependency gaps, exhausted budgets, unverified claims, persisted evaluation history and the authenticated API endpoints. |
| Migration 0011 Evaluation persistence | Passed | Fresh PostgreSQL 16/pgvector 0.8.7 applied all eleven migrations; injected migration failure rolled back and retried cleanly. Evaluation snapshot/history and transactional Audit/Outbox assertions passed. |
| Goal completion safety regression | Passed | Durable task-recovery test confirms all verified Tasks lead to `awaiting_verification`, not automatic Goal completion. |
| Phase 12.2.2 typed criteria and API | Passed | Validates `verified_evidence_count` and `verified_task_completion`, rejects unknown types/methods and invalid parameters, stamps API-authored criteria as user-confirmed, increments versions on revision, and migrates legacy strings only by explicit typed-criteria PATCH. |
| Core-owned verifier boundary | Passed | Durable executor test proves an Agent cannot self-attest `Verified=true`; without a Core-injected verifier execution fails. With a verifier, only its Evidence and method are persisted. PostgreSQL execution stores/recovery also reject missing verifier provenance. |
| Phase 12.2.2 Goal completion transaction | Passed | Fresh PostgreSQL evaluation matches required criteria to verified execution evidence; a Task change invalidates the old evaluation, caller-supplied prose is ignored, and completion Evidence links back to the Evaluation/Criterion. |
| Migration 0012 typed criteria | Passed | Fresh PostgreSQL 16/pgvector 0.8.7 applied all twelve migrations; failure rollback/retry, structured criteria roundtrip, evaluation, completion gating and cleanup passed. |
| Phase 12.2.2 full acceptance | Passed | `make fmt`, `make test`, `make build`, `make vet`, `go test -race ./...`, `make build-arm64`, `make test-integration`, Compose parse and Compose/config/OpenAPI YAML parse all passed. Integration includes Worker crash/checkpoint recovery and database/Core restart tests. |
| Phase 12.2.3 Artifact Verifier | Passed | Real filesystem tests verify root-relative Markdown existence/size/heading rules and SHA-256; traversal/missing path rejected. Real PostgreSQL execution/evaluation records verifier provenance and FK Evidence links. Content mutation after Evaluation is rejected by rehashing under Goal completion transaction; restoring verified bytes permits completion. |
| Phase 12.2.4 Versioned Plan Proposal | Passed | Real PostgreSQL commits Plan v1 and v2; v2 retains a completed Task without re-execution and atomically adds a dependent Task. Concurrent proposal with stale base is marked conflict. Proposal with no executable additions is rejected and current version retained. |
| Phase 12.2.5 Decision persistence | Passed | Evaluation and deterministic Decision are saved in one transaction with Audit/Outbox; repeated identical facts reuse one Decision. Decision fingerprint includes criteria versions/evidence hashes, Task status, blockers and budgets.
| Phase 12.2.6 autonomous Goal E2E | Partial / not claimed complete | PostgreSQL integration exercises artifact verification/hash invalidation, Plan conflict/retention, failure recovery, Worker crash/checkpoint and Core/database restart as separate fixtures. It does not execute one full technical research Goal with authentic reference snapshots, semantic report verification, approval during the same controller loop and final restart. Source snapshot collector, production report Agent and durable background Supervisor Job are not implemented. |
| Phase 12.2.x final combined regression | Passed | Latest local `make fmt`, `make test`, `make build`, `make vet`, `go test -race ./...`, `make build-arm64`, YAML validation and isolated `make test-integration` passed. Fresh PostgreSQL applied migrations 0001–0013 and covered Verification/Evidence FK, stale Artifact Hash rejection, Plan v1→v2 retention/conflict/no-progress behavior, idempotent Decisions, Worker crash/checkpoint and PostgreSQL/Core restart. |

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
- Conservative Goal Evaluator recommendations from lifecycle, task states/dependencies, Core-verifier-backed execution evidence, and model/tool budgets; legacy free-form criteria remain unknown and completion percentage remains unset
- Typed Goal Success Criteria validation/versioning and deterministic evidence-count/verified-task rules, plus stale Evaluation invalidation and transactionally derived Goal completion Evidence
- Core-owned `ResultVerifier` boundary; Agent self-reported `Verified` is ignored, missing verifier provenance is not accepted for Task recovery or Goal criterion satisfaction
- Core filesystem ArtifactVerifier, content hash/criterion version binding, persisted Verification history, Goal completion rehash check, versioned Plan proposal stale/conflict/no-progress behavior, retained completed Task and idempotent Decision history
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
