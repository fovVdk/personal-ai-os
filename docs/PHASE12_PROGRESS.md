# Phase 12 Progress — Model Tool Calls Through the Core Gateway

Date: 2026-11-04

## Phase 12 objective

Phase 12 turns the existing plan-and-execute system toward Goal-driven autonomy. The core rule remains: model outputs are proposals; Go Core owns Task/Goal state, permission decisions, approvals, execution and verification.

This increment implements one narrow vertical slice: a bounded, resumable ModelAgent can receive provider-structured Tool Calls, resolve only opaque aliases from a trusted per-run catalog, call the Core-injected ToolCaller, receive correlated tool results, and continue a bounded model loop.

## Implemented

- Added `internal/agent/adapter.ModelAgent` for configured OpenAI-compatible/Ollama Providers. Mock mode continues using the explicitly unverified MockAgent.
- Tool catalog entries contain a stable opaque alias mapped only in Go to an exact `(provider, ToolID, version)` Registry identity. Catalog construction filters for explicit grants plus enabled/healthy tools. Remote descriptions are labeled untrusted. Model-supplied identity strings cannot choose a provider or version.
- All dispatch goes through `runtime.ToolCaller` injected by the Durable Worker; the existing Gateway remains responsible for Policy, Approval, active Execution Lease/Fencing, schema checks, durable idempotency, result persistence and Audit/Outbox.
- Extended provider-neutral model messages to carry `assistant.tool_calls` and correlated `tool_call_id` / `tool_name` tool results. OpenAI-compatible and Ollama adapters serialize their respective message formats. Provider tests cover a complete assistant-call/tool-result roundtrip.
- ModelAgent has explicit bounds for model rounds, model calls, tool calls, output tokens, request timeout, model tool schema/input, Tool result size and checkpoint size. Provider ToolCall JSON is canonicalized and mapped via an exact server-generated alias.
- Execution checkpoint stores conversation metadata, exact pending Tool identity alias/arguments/Step/idempotency key and Approval ID. Tool result bodies are omitted from checkpoints; after recovery, completed result messages are reconstructed through same-key Gateway replay. This keeps the Tool Gateway as the result authority and avoids copying tool content into Agent checkpoints.
- If a Gateway call waits for Approval, the ModelAgent saves the exact proposal and returned Approval ID, then surfaces the durable `approval_required` failure. After approval/requeue, it repeats the same Step/key/arguments; changed aliases/versions or parameters cannot inherit approval.
- ModelAgent completion remains `OutcomeUnverified`/`Verified=false`. Model-generated prose and tool outputs never complete Tasks/Goals; a Core-owned independent verifier is still required.
- Durable execution results carry Core-observed Tool Call count. Supervisor sums it across Tasks in a turn, and migration `0010_goal_tool_usage.sql` adds the per-Goal Tool Call ledger. Usage lookup also considers durable `tool_invocations`, so approved/unknown/failed tool attempts remain charged across turns without double-counting completed durable runs.

## Safety decisions

- Registering a Tool is not permission. Only the explicit Native allowlist and configured MCP Server tool-name allowlist enter the model catalog; the Gateway independently checks grants on every call.
- A3 MCP tools still require Approval regardless of model request or annotations.
- Server annotations such as `readOnlyHint` or `idempotentHint` do not alter policy or replay guarantees.
- MCP output is untrusted data, never an instruction. Errors/timeouts/unknown results are not retried blindly.
- Tool Call models cannot transition Goal/Task state or set `Verified`; those fields are enforced by Core/runtime.

## Current limitations / next Phase 12 increments

1. Supervisor now consumes Evaluation and persists Decisions, but it is still synchronous/request-triggered; a durable background Goal job/event wakeup controller remains.
2. Plan Versions and additive Plan Proposals exist. General modify/remove/cancel/compensation diffs are unsupported; active Tasks must be retained, and no-progress proposals stop safely.
3. Deterministic local Artifact Existence/Markdown-heading/Hash verification exists. Authentic source snapshot/reference checks and semantic report verification do not; ModelAgent text is still unverified.
4. Model-call/token usage for unsuccessful and approval-waiting runs is not fully included in cross-turn budgets. Tool invocation counts are persisted; a later increment should add idempotent per-execution model usage events/checkpoints.
5. General MCP OAuth/credential-provider support and side-effect tools remain disabled.
6. Multi-Agent delegation contracts are not implemented.

## Phase 12.2.3–12.2.6 implementation status (partial foundation)

This phase126.md request asked for the four remaining stages in sequence. A useful safety-preserving foundation was implemented and exercised, but the full 62-step autonomous research-report acceptance is **not complete**.

### Independent Result Verifier

- Added Core-owned `verification.ArtifactVerifier`, confined to the configured FILE_STORAGE_DIR via `os.Root` and safe local-relative paths. It checks regular-file existence/readability, bounded size, Markdown UTF-8/extension, required headings and computes SHA-256. It ignores ModelAgent summary and caller claims.
- Artifact criteria carry relative path, required sections and byte ceiling. Verified Evidence stores criterion/version, verifier type/version, artifact reference and content hash in the authoritative PostgreSQL `execution_evidence` row.
- Goal completion re-opens the current artifact and checks its SHA-256 against the Verification record, inside the completion gate, while Goal locking serializes Task/Evidence mutations. Changed files fail closed; matching hash plus fresh Verification permits completion.
- Typed evidence-count and task rules require real FK-linked Evidence UUIDs from Goal-scoped Core-verified executions; duplicate UUIDs count once. Criterion Verification records and evidence FK links are immutable history in migration 0013.
- Not implemented: real reference/source snapshot collector, URL provenance checks, JSON Schema/Table/content policy framework, semantic truth verification. Markdown headings do not prove three distinct options, complete comparison dimensions or authentic citations.

### Dynamic Planner / Supervisor decisions

- Migration 0013 stores Plan Versions, fingerprinted Plan Proposals, Task PlanKey/Criterion mapping and Supervisor Decision history.
- Initial Supervisor plans use proposal validation + transactional commit, not a bypassing task insert path. The current Plan API and proposal history are available; replan proposals can be manually accepted or rejected with an audit reason.
- Proposal commit row-locks Goal, checks Goal status/base version/expiry/DAG/required criterion mappings/task budget, preserves existing Task rows/Evidence/Executions, refuses changes to existing PlanKey task content/mapping, retains every active task, and rejects stale or no-progress proposals. Plan version cap is 10; Goal Task cap is 200.
- Evaluations persist an idempotent deterministic Decision derived from criterion/evidence hashes, Task status, blockers and budgets. Supervisor Turn waits for approval/external conditions, stops on budget/terminal states, can commit a bounded replan for concrete failed/blocked/evidence-gap facts, and completes a Goal only after verifier-backed criteria and file freshness checks.
- Not implemented: a durable background Supervisor Job queue/Event wakeup worker, automatic re-evaluation after approval/outbox events, user-adjustable budgets, broad Task cancellation/compensation changes, multi-instance lease/fencing for Supervisor cycles, or a full no-progress streak policy. The controller remains a bounded HTTP-triggered synchronous cycle.

### End-to-end acceptance boundary

- Real PostgreSQL integration separately validates Verification persistence and modified-artifact rejection; versioned Plan v1→v2 commit, completed Task retention, stale concurrent proposal conflict and no-progress rejection; migration rollback; worker crash/checkpoint recovery; and database/Core restart.
- These are not a single full Goal scenario using five real source snapshots, approval/recovery/unknown MCP action, report semantic validation and final completion. No source snapshot acquisition mechanism or production report generator is present. We do not claim that full autonomous Goal execution is complete.

### Final combined validation

- Passed after latest code: `make fmt`, `make test`, `make build`, `make vet`, `go test -race ./...`, `make build-arm64`, Docker Compose configuration validation and YAML parsing of Compose/config/OpenAPI.
- Passed fresh-volume `make test-integration` on PostgreSQL 16/pgvector 0.8.7: migrations 0001–0013 with injected failure rollback/retry; structured Goal Criteria; persisted Verification and exact Evidence FK links; file hash mutation rejected at completion then matching bytes accepted; Goal row lock and stale Evaluation checks; Plan v1→v2 commit preserving a completed Task; stale concurrent proposal conflict; no-progress proposal rejection; idempotent Decision; MCP Gateway approval/replay; Worker crash/checkpoint recovery; PostgreSQL restart and fresh Core process recovery.
- Hosted Go 1.25 CI is configured but its cloud result was not observed in this session. Local checks used Go 1.27.3 because local 1.25 toolchain fetch has previously timed out.

## Phase 12.2.1 — Goal Evaluator baseline

- Added a deterministic `EvaluateGoalSnapshot` over persisted Goal, Task, Execution Evidence and Goal usage records. The result distinguishes approval waits, external-condition waits, failures, explicit blocks, task dependency gaps, exhausted budgets, missing criteria, ready-for-verification and terminal Goals.
- The evaluator never treats Task count as a completion percentage. Legacy free-form `SuccessCriteria` remain `unknown`; `completion_percentage` remains null unless a future rule can compute it reliably.
- Evaluation records carry Goal timestamp, explicit unknown Plan version, task/evidence/blocker summaries, budget usage, recommended action and reason. Evaluation is advisory and does not directly mutate Goal/Task state.
- Migration `0011_goal_evaluations.sql` persists immutable evaluation snapshots. Store writes the snapshot, `goal.evaluated` Audit row and Outbox event transactionally. Authenticated API endpoints are `POST /api/v1/goals/{id}/evaluate` and `GET /api/v1/goals/{id}/evaluations`.
- Removed the Supervisor shortcut that completed a Goal merely because all Tasks had verified executions. Once all Tasks are complete, the turn reports `awaiting_verification`; explicit independent criterion verification is still required before Goal completion.
- The evaluator currently cannot see expired Worker leases or unresolved Gateway `unknown` invocations unless those facts are reflected in Task state. The next controller/verifier increment must incorporate these durable execution/tool facts before making automated decisions.

## Phase 12.2.2 — Typed, verifiable Goal Success Criteria

- Added `goal.SuccessCriterion`, persisted as `goals.structured_success_criteria` by migration `0012_structured_goal_criteria.sql`. The old `success_criteria: []string` remains for compatibility but is explicitly unverifiable; create requests cannot mix formats, while PATCH with typed criteria clears the legacy field.
- Each criterion has stable ID, name/description, type, required flag, validation method, typed parameters, source, owner-confirmation marker, version and creation timestamp. The Core stamps API-authored criteria as user-confirmed; clients cannot designate model-originated criteria as already confirmed through the API. Updating an existing criterion ID increments its version.
- Supported deterministic rules: `verified_evidence_count` requires a minimum number of evidence items from optional allowed sources and can require a non-empty reference; `verified_task_completion` requires each named Task to be Completed with a Core-verified execution and matching evidence. Unsupported criterion types/methods fail closed.
- Example rule: `{ "id": "sources", "name": "Three cited sources", "type": "verified_evidence_count", "required": true, "validation_method": "verified_execution_evidence", "min_evidence": 3, "evidence_sources": ["research"], "require_reference": true }`. A pass means three matching items exist in a Core-verifier-approved Execution; it does not mean model prose claimed three sources.
- Evaluation now reports each structured criterion as satisfied/insufficient/unknown/unsupported, explains the match/missing facts, and includes references to the verified execution evidence. Model/Agent final prose and arbitrary Goal evidence do not count as criterion evidence.
- Goal completion is gated twice: the authenticated API requires a fresh Evaluation for the current Goal version, and PostgreSQL `Transition` rechecks the latest Evaluation freshness against Goal, Task and Execution timestamps and all required criteria. Stored Goal completion Evidence is derived from the Evaluation ID and satisfied criterion/evidence references, not from caller-submitted natural language.
- Added Core-injected `agentruntime.ResultVerifier`. Durable and non-durable Supervisor paths ignore Agent `Verified` claims and fail closed without an independently injected verifier. Verified execution records include `VerificationMethod`; legacy/missing provenance is excluded from Goal criteria checks and durable recovery.
- No production Artifact Verifier is wired yet. Thus typed criteria describe what must be true and what evidence would justify it, but actual ModelAgent results remain unverified and cannot satisfy them until a trusted verifier implementation is added.

## Verification

- Unit tests cover exact alias→Registry binding, unknown alias rejection, request/result message roundtrip, bounded Tool Calls, exact Approval checkpoint resume, and rehydration of completed tool results without checkpointing Tool content.
- Provider tests cover OpenAI-compatible and Ollama Tool Call/tool-result wire serialization.
- Real PostgreSQL integration checks migrations 0001–0013, typed criteria, Evidence UUID/Verification FK links, Artifact Hash revalidation at completion, Goal lock/stale-state gate, Plan versions/proposal conflict and no-progress handling, Decision idempotence/history, Audit/Outbox, plus MCP Gateway and worker/Core restart recoveries.
- Full acceptance passed: `make fmt`, `make test`, `make build`, `make vet`, `go test -race ./...`, `make build-arm64`, isolated `make test-integration` (including fresh PostgreSQL 16/pgvector, migration rollback, Worker crash/checkpoint recovery and Core/database restart), Docker Compose parse, and YAML parse for Compose/config/OpenAPI.
