# Phase 12.3.4 — Autonomous Research Goal E2E Closure

## Status: IMPLEMENTATION PARTIAL; AUTONOMOUS E2E NOT VERIFIED

The requested full Phase 12.3.4 acceptance is **not claimed complete**. New deterministic integration and fault-injection test code has been added, but the latest Go test attempt could not compile database-dependent packages because the module proxy timed out downloading `pgx` and Chi. The new E2E and crash tests therefore remain **NOT VERIFIED**.

## Implemented pipeline slice

The authored deterministic research flow is:

`Goal Job → shared Supervisor → validated Plan Proposal/Task → durable Execution Worker → Gateway-backed Research Agent → source.capture → source.reference.add → artifact.write → Core Artifact/Reference Verifier → persisted Criterion Evaluation → evidence-gated Goal completion`.

- `artifact.write` accepts no caller-supplied path. It reads a confirmed required Artifact Criterion from the persisted Goal, checks the running Task is mapped to that Criterion, applies a byte/UTF-8 bound, and atomically writes only beneath the configured artifact root.
- `source.reference.add` uses Gateway-verified Goal/Task identity and derives the artifact path from the same confirmed Criterion; repository checks bind the exact URL and normalized quote to the immutable snapshot.
- Optional `report.export` is a distinct external Tool, registered only when `REPORT_EXPORT_DIR` is configured and separate from `FILE_STORAGE_DIR`. It copies only bytes whose SHA-256, Criterion ID/version and artifact path match the exact approved Tool parameters. Core policy config sets this to Risk 2/A2 plus ApprovalRequired; source reading remains A1 and does not require Approval.
- Approval API response invokes `ResumeApproval`; Outbox carries the persisted `approval.approved` event to wake the existing Goal Job. The test asserts the waiting Job does not hot-loop and that approved parameter hashes remain identical.
- The deterministic research fixture is a local controlled HTTP server and a test-only `source.capture` adapter. It is not a public Internet source and cannot satisfy live-source acceptance.

## New acceptance code

- `tests/integration/autonomous_e2e_test.go` runs a real PostgreSQL Goal Job, Supervisor, Worker Pool, Outbox Dispatcher, Tool Gateway, Source Snapshot repository, Citation repository, ArtifactVerifier, Evaluator, API Approval handler and final Goal transition. It asserts the Goal reaches `completed`, the Task and evidence/criterion/version links persist, the export copy matches the verified report hash, and a `complete_goal` Decision exists.
- The same test records a real Risk-2 report export Approval, checks that Goal Job `RunCount` is stable while waiting, approves through the API handler, and relies on the persisted approval event/wakeup to continue.
- `tests/integration/unknown_invocation_test.go` creates an actual HTTP side effect in a controlled external-service ledger, blocks the response, kills a real Worker subprocess, then starts a fresh Core process. It expects exactly one effect and persisted Tool Invocation/Execution `unknown`, Task `waiting_external`, and no automatic replay.
- `internal/source/source_test.go` has deterministic Fetcher tests for normal response/hash/time, allowed and rejected redirects, redirect limits, timeout, MIME/body limits and public/private DNS outcomes. These unit tests passed locally with `GOPROXY=off`.
- `make test-autonomous-e2e` runs the deterministic research E2E and the separate unknown-effect process-crash test against its disposable PostgreSQL database.

## Verification matrix

| Requirement | Status | Evidence / boundary |
| --- | --- | --- |
| Criterion-scoped report writer and reference binding | IMPLEMENTED; UNIT TESTED | `internal/tool/builtin/artifacts_test.go`; Core verifies independently |
| Approval-bound report export | IMPLEMENTED; UNIT TESTED | `internal/tool/builtin/report_export_test.go`; exact criterion/version/path/hash and risk policy |
| Deterministic HTTP Fetcher boundary | UNIT TESTED | Fetcher unit tests passed with `GOPROXY=off`; transport/DNS test seams do not disable production checks |
| Full PostgreSQL autonomous research E2E | NOT VERIFIED | Authored in `tests/integration/autonomous_e2e_test.go`; not run after latest changes |
| Approval continuation through API + Outbox + Goal Job | NOT VERIFIED | Authored as part of the unrun E2E |
| External effect → lost response → Worker process kill → unknown recovery | NOT VERIFIED | Authored in `tests/integration/unknown_invocation_test.go`; not run |
| Live public source capture | BLOCKED | No `PAIOS_LIVE_SOURCE_URL` or exact host allowlist was supplied; `make test-source-live` correctly refuses to run |
| Worker checkpoint takeover and Core restart in the *same research Goal* | NOT VERIFIED | Existing Phase 10 crash acceptance and new unknown-effect restart test are separate fixtures; not yet combined with report completion |
| Real model E2E / semantic claim support | NOT VERIFIED | Deterministic test Agent only; semantic support requires an independent reviewer |
| Hosted Go 1.25 CI | NOT VERIFIED | Must be confirmed from a hosted workflow run |

## Current validation limitation

The latest `make test` / E2E attempts failed during dependency setup, before compiling the new integration code: `proxy.golang.org` timed out while fetching `github.com/jackc/pgx/v5` and `github.com/go-chi/chi/v5`. Do not interpret that as a test failure in application behavior, but also do not report the new E2E as passed. The previous Phase 12.3 full PostgreSQL acceptance passed before these current Phase 12.3.4 edits.

## Next steps

1. Restore module proxy/cache access and run `make test`, then `make test-autonomous-e2e`; fix any actual failures.
2. Run `make test-integration`, race tests, ARM64 build, Compose/OpenAPI validation.
3. Configure an explicitly authorized public HTTPS source and run `make test-source-live`; otherwise keep live-source status BLOCKED/NOT VERIFIED.
4. Combine Worker Checkpoint takeover/Core restart with the same research Goal lifecycle before claiming the complete Phase 12.3.4 closure.
