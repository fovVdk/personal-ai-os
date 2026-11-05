# Phase 12.3.4 Implementation Design & File Plan

Date: 2026-11-04

## Implementation progress

- Slice A implemented: private controlled `RoundTripper` and package-private DNS resolver seams; deterministic Fetcher unit tests passed offline. Live target `make test-source-live` is opt-in and was not run because no explicit URL/host allowlist was supplied.
- Slice B implemented: Gateway-only criterion-scoped atomic `artifact.write`, `source.reference.add`, and optional exact-hash report export to a separate root requiring Approval.
- Slice C authored: PostgreSQL-backed deterministic Goal Job research test exercises the shared Supervisor, durable Execution Worker, Gateway, source/reference/artifact services, API Approval and Goal completion. It has not been run after current edits.
- Slice D authored separately: a real Worker subprocess triggers a controlled external HTTP side effect, is killed before response, and a fresh Core process must recover the Execution/Invocation as unknown with no replay. It has not been run, and is not yet combined with Slice C.
- Current test blocker: `proxy.golang.org` timed out while fetching pgx/Chi. Targeted standard-library-only packages passed with `GOPROXY=off`; whole database integration/E2E compile remains unverified.

## Review outcome

The Phase 12.3.1–12.3.3 components are present, but code inspection confirms they are separate vertical slices rather than an autonomous research pipeline:

- `JobWorker.RunOne` claims one durable Goal Job and calls the existing bounded `Supervisor.Turn`; it does not call Agents or Tools directly.
- `Supervisor.Turn` uses the real Planner, persists Plan Proposal/Tasks, selects a registered Agent, and routes durable execution through the Execution Worker and Core Tool Gateway.
- `source.capture` is a Gateway-only contextual Tool. It persists snapshots associated with the current Goal and Task. It does not create report-reference bindings.
- `ArtifactVerifier` verifies a file already written under the configured file root and checks registered Source References. There is no authorized artifact-writing Tool or report-generation service.
- Goal completion requires current Core Evidence, Evaluations, verification records, matching criterion version, current Artifact Hash and fresh source snapshots. Agent self-attestation is rejected.
- Tool Gateway approvals bind exact tool/version, input hash, execution, step, agent and idempotency key. Existing `source.capture` is not an approval action and read-only research will not be mislabeled as high-risk.
- PostgreSQL recovery marks active Tool Invocation and Execution `unknown` after a lost lease; it does not assume success or replay a non-idempotent external effect.
- Existing crash acceptance tests worker checkpoint/lease recovery and Core restart separately. Existing Source Snapshot tests use persisted fixtures; they are not proof of external live-network capture.
- Migrations 0014–0016 already supply the required Goal Job, Outbox and Source Snapshot tables; Phase 12.3.4 should not add schema until a concrete missing durable fact is identified.

## Safety blockers to the requested full closure

1. No controlled report writer exists. Adding arbitrary file write is unsafe; any writer must derive its only allowed path from a persisted, confirmed Goal Criterion and use bounded atomic writes under the configured root.
2. No Core-owned pipeline currently binds Source Snapshot IDs, exact source quotations, report citations, report writing and ArtifactVerifier Evidence within a durable Execution result. Reference creation currently has an API path, not an execution Tool path.
3. No production action suitable for an Approval node is registered. Research reads remain A0/A1. A release/publish action must be a distinct, precisely parameterized adapter; inventing a high-risk read to exercise approvals would violate Policy semantics.
4. A real external-effect/response-loss/Worker-kill test is possible with a dedicated side-effect ledger service and a PostgreSQL Gateway invocation, but is not present today. A manually written `unknown` row is not equivalent.
5. A deterministic local network test server cannot be treated as live-source verification. The live test must require an exact HTTPS URL whose hostname appears in an operator-provided allowlist and must persist its real response in the isolated PostgreSQL test DB.

## Incremental implementation plan

### Slice A — independently verifiable source-fetch acceptance

- Add a private `RoundTripper` seam to `internal/source.Fetcher` for deterministic tests without weakening production DNS/IP pinning or allowlist behavior.
- Add table-driven deterministic tests for success, allowed/denied redirects, context timeout, unsupported MIME, body limit, hash/normalization and URL/DNS/IP constraints.
- Add an opt-in PostgreSQL live source test that refuses non-HTTPS URLs and hosts absent from an explicit allowlist; it persists the fetched response and checks status, final URL, retrieval time and hash.
- Add `make test-source-live` which requires explicit `PAIOS_LIVE_SOURCE_URL` and `PAIOS_LIVE_SOURCE_ALLOWED_HOSTS`, starts only the disposable integration DB, and runs only the live test.

Files: `internal/source/fetcher.go`, `internal/source/fetcher_test.go`, `tests/integration/source_live_test.go`, `Makefile`, `.env.example`, `docs/SOURCE_SNAPSHOT_REPORT.md`, `docs/IMPLEMENTATION_STATUS.md`.

### Slice B — safe report writer and durable reference-binding boundary

- Define a narrowly scoped Core report artifact writer. It accepts a Goal/Task/Execution context and content, looks up the current Goal and confirmed criterion itself, permits only that criterion's one relative artifact path, enforces size/Markdown policy, and writes via root-confined atomic replacement. No caller-supplied path may expand authority.
- Define a Reference binding operation usable by the Core execution boundary, enforcing same Goal/Task/snapshot owner plus URL and exact quote; the model cannot directly mint Evidence.
- The writer/reference binding must be exposed only through Tool Gateway, with a durable Tool Invocation and required Execution lease. Any APIs are review/metadata only.
- Tests must prove path traversal, cross-Goal Task/snapshot, unconfirmed/changed criteria and oversized writes are rejected.

Files expected after Slice A design review: `internal/source` or a new `internal/research` service, `internal/tool/builtin/register.go`, `cmd/paios/durable_executor.go`, focused unit and PostgreSQL tests, OpenAPI only if a user-facing endpoint is added.

### Slice C — one deterministic autonomous Goal pipeline

- Register a deterministic Research Agent adapter only for explicitly configured/test profile; it may call `source.capture` and the restricted writer/reference operation exclusively through the Gateway.
- Create a three-stage typed Plan (capture, analyze/write, verify/review) using the existing Planner/Plan Proposal and Task criterion mapping.
- Run a real PostgreSQL Goal Job/Worker/Outbox in an integration acceptance. Do not manually call Supervisor HTTP between stages.
- Add a separate, real publish/export Tool with exact action/parameter approval only if an actual safe destination and reversible/idempotent contract are selected. Until then Approval lifecycle is tested as a separate guarded action, and full publish-approved research closure remains NOT VERIFIED.

### Slice D — failure injection and unknown external invocation

- Use a dedicated test side-effect service that durably records one operation by an idempotency key, then blocks before sending a response.
- A real Worker subprocess invokes it via the PostgreSQL-backed Tool Gateway; the test kills the Worker after the service recorded the effect but before response delivery.
- Restart/recovery must mark Invocation and Execution unknown and leave the external effect count at one. Replay with the same non-idempotent request must be refused pending reconciliation/approval.
- Combine Goal Job, Outbox, Execution checkpoint, Worker lease takeover and Core restart in the research E2E once Slices B–C exist.

Files expected: new opt-in integration subprocess helper/side-effect server, `tests/integration`, Makefile target, and final report.

## Acceptance claim policy

At each slice, report `IMPLEMENTED`, `UNIT TESTED`, `POSTGRESQL INTEGRATION TESTED`, `FAILURE INJECTION TESTED`, `AUTONOMOUS E2E VERIFIED`, `LIVE SOURCE CAPTURE VERIFIED`, `HOSTED CI VERIFIED`, `BLOCKED`, and `NOT VERIFIED` separately. No localhost/fake transport test can set LIVE SOURCE CAPTURE VERIFIED. The full Phase 12.3.4 status remains NOT VERIFIED until one integrated run covers pipeline, genuine source snapshot, approval action, unknown invocation crash, Worker/Core recovery, final artifact/reference checks, current Evaluation/Decision and Goal completion.
