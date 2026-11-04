# Phase 11 Implementation Report — Tool Fabric & Secure Execution

Date: 2026-11-04 (updated after MCP integration)

## Executive summary

Phase 11 Tool Fabric and MCP Client are implemented for the current transport scope. The Go Core now owns a versioned Tool Registry, a single Gateway dispatch path, a trusted-grant Policy adapter, fenced durable Invocation/approval storage, bounded Native read-only handlers, authenticated Tool metadata and MCP lifecycle endpoints, an official MCP SDK v1.8.0 adapter, and Agent Runtime's structured Gateway-only caller.

The project minimum Go version is 1.25.0, as explicitly directed by `agentsmcp.md`, and SDK v1.8.0 is pinned. Real official SDK Server/Client Streamable HTTP tests and a real PostgreSQL MCP→Gateway→Approval→Invocation→Audit/Outbox test pass. Remaining limits include MCP OAuth/credentials, a dedicated stdio subprocess integration test, and automatic model ToolCall orchestration. No production side-effect tools are registered.

## Phase 11.1 — Registry + Gateway foundation

- Added `internal/tool/registry`: provider-qualified `(ProviderID, ToolID, Version)` identity; immutable Definition snapshots; native/MCP/remote Source values; type, risk, declared idempotency, timeout, size limits, execution environment, schemas, enabled state and health state.
- Duplicate exact identities are rejected; changed semantics must publish a new version. MCP/remote tools start with unknown health and do not resolve until healthy. Registration never grants permission.
- The supported JSON Schema subset is bounded and fail-closed; arguments are checked before handler resolution/dispatch, results are checked before persistence.
- Added `internal/tool/gateway`: request binding to Goal/Task/Execution/Step/Agent/exact Tool version/idempotency/arguments and Worker Lease; canonical argument hash includes Tool identity/version.
- Added metadata-only `GET /api/v1/tools` and `GET /api/v1/tools/{provider}/{tool}/{version}`. No HTTP dispatch route exposes handlers or bypasses Durable Worker Lease checks.

## Phase 11.2 — Policy, Approval, idempotency, secure runtime

- Added `PolicyAuthorizer` + trusted GrantResolver. Missing grant is default-deny; Main explicitly grants only selected read-only built-ins at A0. The Policy Engine independently requires approval for A3 risk, including when caller autonomy is lower.
- Gateway verifies the current Execution lease before authorization, at reservation, before dispatch, and after dispatch. PostgreSQL rechecks Task/Goal association, Worker ID, fencing token, active lease, cancellation, and owner.
- Added migration `0009_tool_fabric.sql` with `tool_invocations`, unique `(execution_id,idempotency_key)`, exact context identity, arguments/hash/result/status/error, Approval link and recovery indexes. Existing migration files 0001–0008 were not edited.
- Approval requests bind user/task/tool/provider-qualified ID/version/normalized argument hash/Execution/Step/Agent/idempotency context, expire, and are consumed once atomically when the invocation transitions to running. Approval repository now rejects creation with any status other than pending.
- Same key + same hash returns a durable result/status; same key + changed request is rejected. Transaction advisory locks serialize reservation attempts. An active Tool invocation prevents automatic execution retry after Lease expiry; it and its enclosing Execution become `unknown`. Pending/unconsumed Approval is revoked when execution expires.
- Approved waiting executions requeue through the approval manager. The Worker uses a higher fencing token and the same Step/Tool idempotency record. A resumable Agent must repeat the same structured Step/key/arguments; a changed call cannot reuse approval.
- Agent Runtime receives only a `ToolCaller` interface. Durable Worker injects Lease identity from its claim, calls Phase 10 Step claim with the same version-bound request hash, and counts tool attempts in Core rather than trusting Agent-reported usage.
- Any handler error, lease loss after dispatch, oversized/invalid result, or inability to durably persist a tool result is conservative `unknown`; no blind external retry is inferred. This does not claim generic exactly-once behavior.

## Phase 11.3 — Native tools and MCP status

Implemented registered read-only Native tools:

- `time.now` returns UTC RFC3339Nano.
- `filesystem.read` and `filesystem.list` operate only beneath an existing configured `FILE_STORAGE_DIR`; traversal and resolved symlink escapes are rejected, reads are regular-file-only and capped at 512 KiB, listings are capped at 1,000 entries.
- `memory.search` and `knowledge.search` use existing owner-scoped repositories and bound limits.
- Optional `http.fetch` is registered only with an exact `HTTP_FETCH_ALLOWED_HOSTS` allowlist. It accepts HTTP(S), rejects credentials/unsupported ports, disables redirects/proxy, resolves and dials only public addresses, blocks private/special-use ranges and caps response size at 1 MiB. Empty allowlist disables registration.
- No Shell, filesystem write/delete, payment, door/security, Home Assistant, or robot Tool is registered.

Filesystem limitation: standard-library path resolution and symlink checks protect traversal and symlink escape, but do not provide an OS-level `openat`/Root handle against a malicious local process concurrently mutating the configured root. Keep that root owned by the trusted service/operator and not writable by untrusted local users.

MCP SDK v1.8.0 is used through the supplied module proxy. The project minimum is Go 1.25.0. Streamable HTTP uses exact host allowlists, TLS for remote endpoints, redirect blocking, DNS/IP checks, timeouts and bounded reconnect. Stdio accepts only operator-configured absolute command/argv/workdir/environment and rejects shell interpreters. MCP tool descriptions, schemas, outputs and annotations are untrusted; only a narrow schema subset is accepted; all MCP Tools are A3/non-idempotent by default and require Approval. Tool versions include server version and schema fingerprints; changed versions disable the old Tool and invalidate old Approval bindings.

## Database and API changes

- Forward migration 0009 extends `approvals` with Tool Version, Execution ID, Step ID, Agent ID, invocation-context hash and `consumed_at`; adds `tool_invocations` with execution-scoped idempotency and recovery indexes.
- Approval APIs now trigger guarded Durable Execution continuation after a context-bound Approval is approved. Generic legacy approvals with no Execution binding remain unaffected.
- OpenAPI and README document the metadata endpoints and current limits.
- No schema changes were made to historical migrations 0001–0008.

## Verification

Environment: macOS darwin/arm64; installed Go toolchain 1.27.3, module minimum Go 1.25.0; Docker Engine 29.6.2; Compose 5.3.1; isolated PostgreSQL 16/pgvector 0.8.7.

Latest final verification:

- `make fmt` — passed.
- `make test` — passed across all packages.
- `make build` — passed.
- `make vet` — passed.
- `make test-integration` — passed on a fresh isolated database/volume. The target ran empty-database migrations 0001–0009, injected migration failure/rollback/retry, all repository tests, race tests, vet/build, Tool Invocation approval/idempotency/recovery tests, the Phase 10 Worker crash/checkpoint acceptance, and database/Core restart verification.
- Real SDK Streamable HTTP tests — passed: Client connection/initialization, discovery, Gateway-gated invocation, Approval-before-call, duplicate replay without duplicate server call, invalid input, remote error/timeout uncertainty, Tool schema version changes, disable/unregister, and actual local MCP Server stop/restart/reconnect.
- Real PostgreSQL MCP E2E — passed in `make test-integration`: actual SDK Server → MCP manager → versioned Registry → A3 Policy/Approval → Durable Execution Lease/Fencing → PostgreSQL Invocation/Audit/Outbox → replay.
- Real PostgreSQL Tool tests verified: approval binding mismatch by Step is denied; approval consumes once; approval queues a waiting run and a replacement Worker claims it with higher fencing; concurrent identical Begin calls have one winner and one replay; completed result replays; changed hash conflicts; rejected approval cannot begin; expired Lease plus a running Tool invocation forces Tool and Execution `unknown`; Audit records are present.
- Native unit tests verified filesystem traversal/symlink escape rejection and loopback HTTP destination denial. No public-network HTTP fetch was attempted.
- Exact Go 1.25.0 toolchain download was attempted with the configured proxy but timed out while fetching the Go toolchain archive from its storage redirect. All tests passed with installed Go 1.27.3 while the module minimum is set to Go 1.25.0. SDK and project ARM64 cross-build validation are tracked separately.
- `go.mod`/`go.sum` pin official SDK v1.8.0 and required indirect modules; Chi and pgx direct versions are unchanged.

The `make test-integration` target removed only its dedicated `paios-dbtest` container and `paios-integration-test-data` volume; unrelated Compose services were not targeted.

## Remaining work / blockers

1. MCP OAuth/HTTP credentials and a secret-provider integration are not implemented; authenticated remote servers are not enabled.
2. A dedicated real stdio subprocess MCP Server interoperability suite has not been run; stdio configuration is validated and uses the SDK CommandTransport.
3. Exact Go 1.25.0 toolchain artifact could not be downloaded through the current proxy path; code is verified using Go 1.27.3 with the Go 1.25.0 module minimum. A local ARM64 build remains an explicit validation item.
4. Automatic orchestration of model-provider ToolCall proposals into structured Agent calls is not implemented. The Agent Runtime contract is ready, but current Mock Agents do not call Tools.
5. Approval continuation requires a resumable Agent to repeat the identical Step/idempotency request; a generic persisted tool-result injection/replay loop into arbitrary Agents is not implemented.
6. Policy grants are explicit operator configuration; persistent per-user/per-Agent authorization administration is not implemented.
7. Side-effectful Native/MCP/Remote handlers, OTel tracing, remote sandboxing and external verifier workflows remain intentionally disabled.
8. Memory/Knowledge search results are returned in the Tool result envelope as serialized JSON text; a richer nested output-schema subset can be added later without broadening permissions.

No Eino, Home Assistant, Xiaozhi or ROS 2 work was started, per Phase 11 scope.
