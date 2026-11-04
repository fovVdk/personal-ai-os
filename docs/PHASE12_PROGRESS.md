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

1. Supervisor still creates a Plan only when a Goal has no persisted Tasks. It does not yet evaluate Evidence against Goal success criteria or create a durable decision record for continue/stop/wait/replan.
2. Dynamic plan replacement/reconciliation is not implemented; add it transactionally and version plans before accepting model-proposed replans.
3. Independent artifact/result verification for real Agents is not implemented; current ModelAgent final output is intentionally unverified and leaves Task incomplete.
4. Model-call/token usage for unsuccessful and approval-waiting runs is not fully included in cross-turn budgets. Tool invocation counts are persisted; a later increment should add idempotent per-execution model usage events/checkpoints.
5. General MCP OAuth/credential-provider support and side-effect tools remain disabled.
6. Multi-Agent delegation contracts are not implemented.

## Verification

- Unit tests cover exact alias→Registry binding, unknown alias rejection, request/result message roundtrip, bounded Tool Calls, exact Approval checkpoint resume, and rehydration of completed tool results without checkpointing Tool content.
- Provider tests cover OpenAI-compatible and Ollama Tool Call/tool-result wire serialization.
- Real PostgreSQL integration checks migration 0010 and confirms durable execution completion persists Model/Tool Call usage, while MCP Tool Invocation records drive cross-turn Tool budget usage.
- Full acceptance requested after the implementation increment: `make fmt`, `make test`, `make build`, `make vet`, `go test -race ./...`, `make build-arm64`, and isolated `make test-integration`.
