# Phase 11 Design — Tool Fabric & Secure Execution

Date: 2026-11-04 (updated with MCP implementation)

## Current baseline and boundaries

The repository currently has a model `ToolCall` proposal and restricted JSON schema validation, a standalone A0–A3 `internal/tool/policy.Executor`, PostgreSQL approvals, and Phase 10 Execution/Step claims. Phase 11 now includes a trusted registry, the Go Core Gateway, durable Invocation storage, read-only Native tools, and an official MCP Go SDK v1.8.0 client adapter. The project's minimum Go version is 1.25.0.

The model/provider package remains a proposal source only. It never receives native handler or MCP client references. The only invocation entry point is the Tool Gateway, which derives caller context from a Worker-owned durable Execution lease. Registry metadata does not grant permission. Default grants cover explicitly selected read-only built-ins only; all mutations/high-risk tools remain denied unless an operator policy and bound Approval allow them.

## Components

1. `internal/tool/registry`: versioned immutable `Definition` metadata, stable provider-qualified Tool ID, schemas, source/type, risk, declared idempotency, timeout/input/output bounds, environment and health/enabled states. Re-registering the same ID/version with changed semantics is a conflict; publishing a changed definition requires a new version.
2. `internal/tool/gateway`: `Gateway.Execute`, canonical JSON input hashing, registry and schema validation, trusted authorization resolver, lease validation, durable invocation protocol, constrained handler execution, result validation, timeout/cancellation and safe unknown outcomes.
3. `internal/app/store/tool_invocations.go`: transactional invocation/approval creation and one-time approval consumption; durable replay/conflict lookup; invocation state/result/Evidence persistence with Audit and Outbox.
4. `internal/tool/builtin`: time, Memory search, Knowledge search, allowlisted filesystem read/list, and outbound HTTP fetch with strict host/protocol/redirect/body limits. Filesystem root must be trusted and not concurrently modified by an untrusted local process. No Shell, writes/deletes, payment, device or robot controls.
5. MCP remains a separate provider adapter to be added only after the Gateway contract is stable and official SDK compatibility is verified. MCP-originated tool metadata/results are untrusted and must pass the same Gateway checks.

## Request identity and authorization

`ToolRequest` includes Goal, Task, Execution, Step, Agent, Tool ID, exact Tool version, canonical JSON arguments, stable idempotency key, and optional Approval ID. Worker ID/fencing/lease are supplied by the durable Worker progress adapter, never accepted from model-generated arguments. Caller autonomy and grants come from a trusted Authorizer, not from the Tool definition or model request.

The Gateway sequence is:

1. Validate identity fields and fetch exact registered ID/version; require enabled/healthy.
2. Enforce payload size/depth and both registered JSON schemas; canonicalize arguments and hash semantic Tool ID/version/arguments.
3. Validate active Goal/Task/Execution association and live Worker lease/fencing token.
4. Resolve trusted Agent grants; run the central Policy Engine. No grant means deny. Profile risk cannot grant permission.
5. If approval is required and absent, transactionally persist a pending, context-bound Approval plus `waiting_approval` invocation and return without calling the handler. Durable Worker persists its run/task as `waiting_approval`.
6. On approval, requeue the same Execution. On retry after approval, reload Approval under a row lock; check user/task/execution/step/tool/version/argument/context hashes, expiry, unconsumed status and active lease; atomically consume approval and mark invocation running.
7. For non-approval calls, atomically create/reserve the invocation before external work. Same execution/key/hash returns stored success, reports in-progress/unknown, or rejects mismatched hashes.
8. Revalidate lease just before handler dispatch; invoke with a bounded context; validate bounded result against output schema; transactionally store result/Evidence/status/Audit/Outbox.
9. Timeout/lease loss after dispatch is unknown unless the exact Tool version declares idempotency and supports the stable invocation key. Never infer exactly-once for generic HTTP/MCP tools.

## Approval binding and lifecycle

Extend the existing approval record (forward migration only) with Tool version, Execution ID, Step ID, Agent ID, invocation context hash, and `consumed_at`. Legacy approvals remain readable but cannot authorize a Gateway call because required context fields are absent. Approval creation remains pending-only. Approval is consumed exactly once in the same transaction that transitions its waiting invocation to running. Changed input, tool version, caller or Execution context invalidates the old approval.

## Durable invocation schema

Migration 0009 adds `tool_invocations`, keyed by UUID, with Goal/Task/Execution foreign keys, Step/Agent/Worker/Fencing identity, stable Tool ID/provider/version, canonical arguments and hash, scoped idempotency key, status (`waiting_approval`, `running`, `succeeded`, `failed`, `unknown`, `cancelled`), approval link, bounded result JSON, error classification/reason, timestamps and unique `(execution_id,idempotency_key)`. Tool idempotency capability is immutable Registry metadata; ambiguous active Tool invocations force enclosing Execution into `unknown` even if an Agent declared idempotency.

Recovery of an expired non-idempotent Tool invocation becomes `unknown` and prevents the enclosing Execution from being blindly replayed. A durable invocation is not claimed to give external exactly-once semantics.

## Phase breakdown

### 11.1 Registry + Gateway foundation — implemented

Implement immutable versioned metadata, trusted grants, canonical schemas, active lease/context checks, durable invocation rows, and no-side-effect denial/approval results. Unit and PostgreSQL tests cover registration/version conflicts, disabled/health states, schema validation and missing grants.

### 11.2 Secure Runtime + approvals/idempotency — implemented for current Native invocation contract

Implement transactionally bound approval/consumption, `SKIP LOCKED`/unique invocation replay, lease/fencing validation before/after dispatch, output validation, error/unknown recovery, and timeout/cancellation. Tests use handlers with counters to assert denial/approval/replay behavior. External calls cannot be part of a PostgreSQL transaction; ambiguous outcomes remain unknown.

### 11.3 MCP + read-only tools — Native tools implemented; MCP blocked

Native read-only handlers (`time.now`, `filesystem.read/list`, `knowledge.search`, `memory.search`, and opt-in `http.fetch`) are implemented and covered by tests. SDK downloads succeed through the supplied HTTP proxy `127.0.0.1:18780`. However, inspected official stable releases from v0.1.0 through v1.0.0 require Go 1.23, while v1.8.0 requires Go 1.25; the project target remains Go 1.22. No dependency was added and no fake MCP protocol was written. MCP server registration/connection/discovery/call/reconnect and real server integration remain unimplemented until the Go target decision is made.

## Test and security plan

- Unit: registry immutability, version identity, schema, canonical hashing, policy mapping, URL/path validation, size/time limits.
- Real PostgreSQL: invocation idempotency/replay/conflict, two-worker same invocation, Approval consumption and binding, transaction rollback, expired lease/fencing, unknown invocation recovery, Audit/Outbox consistency.
- Native read-only handlers: actual temporary-root filesystem fixtures with traversal/symlink escape cases; Memory/Knowledge database adapters; HTTP fetch uses local controlled test servers plus private/redirect/oversize denial.
- MCP: only report real server interoperability after an actual compatible SDK and local test server run. Otherwise mark not run/not implemented.
- Regression: full `make fmt`, `make test`, `make build`, `make vet`, `go test -race ./...`, and isolated `make test-integration`.
