# Phase 11 MCP Implementation Report

Date: 2026-11-04

## Summary

Implemented the official MCP Go SDK v1.8.0 adapter and wired it into the existing Tool Registry and Tool Execution Gateway. The project minimum Go version is now 1.25.0, as requested by `agentsmcp.md`; no unrelated direct dependency upgrades were made. The adapter supports Streamable HTTP and explicitly configured stdio commands. MCP tools are untrusted, classified as external A3/non-idempotent by default, operator-allowlisted by exact tool name, and still require human approval through the existing Gateway.

## SDK and Go compatibility

- Downloaded official `github.com/modelcontextprotocol/go-sdk` v1.8.0 through HTTP proxy `127.0.0.1:18780`.
- SDK module declares `go 1.25.0`; repository `go.mod` declares the same minimum.
- Development toolchain is Go 1.27.3 on darwin/arm64. Exact Go 1.25.0 toolchain download was attempted but timed out at the Go toolchain storage redirect; the installed toolchain successfully ran project tests with the Go 1.25 module minimum.
- `go.mod` pins SDK v1.8.0 and its required indirect dependency graph; direct Chi and pgx versions were not changed.
- SDK-specific types are isolated in `internal/tool/mcp` and `internal/api` exposes only the manager's local status structures.

## Adapter and server lifecycle

- `Manager` validates immutable operator-owned `ServerConfig`, starts one cancelable connection/reconnect worker per configured server, performs MCP initialize/discovery, tracks server version/connection/last-connected/error/tool count/discovery time, and closes sessions during disable, removal and Core shutdown.
- Streamable HTTP uses the official `mcp.StreamableClientTransport`, an exact host allowlist, TLS 1.2+, redirects disabled, no environment proxy, explicit IP resolution and direct dialing only to checked public IPs. Plain HTTP is permitted only to loopback for local integration/development.
- Official SDK logging is directed to `io.Discard`; status and application errors use fixed redacted messages so endpoint/query details and subprocess stderr are not written to ordinary logs.
- Stdio uses official `mcp.CommandTransport` and `exec.Command` with an operator-provided absolute executable and separate argv, explicit working directory and environment map. Shell interpreters are rejected, stderr is discarded to avoid secret leakage, and Agent/model data never enters process configuration. No arbitrary shell executor exists.
- Periodic health/discovery checks detect server disconnects and tool-list changes; reconnect attempts use bounded exponential backoff and repeat schema discovery. Enable/disable/reconnect/unregister are available under authenticated `/api/v1/mcp/servers` routes. Unregistered/disabled Tools remain visible only as disabled metadata and cannot resolve for Gateway dispatch.

## Discovery and trust boundary

- MCP tools are normalized into provider-qualified identities under `mcp.<server-id>`, with a stable Tool ID derived from the exact remote name and a version hash over server version, description, transport, timeout/size policy, and normalized input/output schemas.
- Only bounded object-root JSON Schema with required fields, primitive properties, and `additionalProperties:false` is supported. Unknown keywords, nested/compound constructs, oversized schema, invalid names and excessive pagination fail closed.
- Unsupported output schemas are rejected. When an output schema is declared, `StructuredContent` is required and validated; otherwise only bounded MCP content is normalized. MCP errors, timeout/cancellation, invalid/oversized results become uncertain errors; Gateway persists `unknown` and does not blindly retry.
- Server annotations/hints never set risk, autonomy, permission or idempotency. MCP tools always receive risk level A3 and `IdempotencyNone`; exact operator allowlisting only provides a grant, and central Policy still mandates Approval.
- Tool refresh disables existing versions before rediscovery. A changed definition gets a new identity version and old versions are disabled, invalidating approvals tied to the previous version. Per-server refresh locking prevents an old registered handler from switching to a newly discovered Tool implementation mid-call.

## Real MCP Server verification

A real local MCP Server using the official SDK `StreamableHTTPHandler` and the real SDK `StreamableClientTransport` is tested in `internal/tool/mcp/manager_test.go`. A separate child process runs the official SDK `Server.Run` + `StdioTransport` in `internal/tool/mcp/stdio_integration_test.go`. Neither transport uses a fake MCP protocol/client.

Passed test coverage:

1. Real SDK Client connection/initialization and tool discovery.
2. Tool schema conversion and exact provider/version registration.
3. Agent/model cannot call a handler directly: only the Gateway test invokes it.
4. A3 approval is persisted/requested before the MCP server sees a call; after approval Gateway performs the real SDK call.
5. Same successful invocation replay does not repeat the server-side counter/action.
6. Invalid parameters are rejected before reaching the MCP Server.
7. MCP error, request timeout, explicit parent-context cancellation and schema-invalid structured output map to Gateway `unknown`.
8. Server tool schema/version changes create a new Tool version and disable the old version.
9. Disable/enable/reconnect/unregister lifecycle and HTTPS/allowlist/shell-command validation.
10. A local real HTTP MCP Server process is stopped and restarted at the same endpoint; health discovery detects the disconnect, reconnects and discovers the new definition.
11. A real stdio child process is connected, discovered, disabled/closed, restarted, and re-enabled; invoking its crash Tool exits the child process, yields an `unknown` Gateway outcome, triggers Manager restart/discovery, and a subsequent Tool call succeeds through the recovered process.

A separate real PostgreSQL integration test `TestPostgresRealMCPGatewayApprovalAuditAndReplay` composes the SDK MCP Server, manager, Registry, A3 Policy, one-time Approval, PostgreSQL Invocation store, Durable Execution Lease/Fencing, result replay, Audit and Outbox. It is included in `make test-integration` and passes on the isolated database.

## Verification results

- `make fmt` — passed.
- `make test` — passed.
- `make build` — passed.
- `make vet` — passed.
- `GOPATH=/tmp/paios-gopath GO111MODULE=on go test -race ./...` — passed.
- `make test-integration` — passed on a fresh isolated PostgreSQL 16/pgvector 0.8.7 database, including new real MCP + Gateway + Approval + Audit/Outbox test, existing migration rollback, Worker crash/recovery and Core/database restart tests.
- `make build-arm64` — passed, producing the darwin/arm64 binary using Go 1.27.3.
- Real stdio subprocess lifecycle/recovery test with race detector, repeated five times — passed on macOS darwin/arm64.
- Docker Compose parse and YAML syntax validation — passed after final MCP config/OpenAPI updates.
- `.github/workflows/ci.yml` pins exact Go 1.25.0, asserts `go env GOVERSION`, runs test/build/vet/race/PostgreSQL integration acceptance, and separately runs stdio lifecycle/recovery tests plus ARM64 build on macOS. Hosted workflow has been configured and syntax-checked but its first remote GitHub Actions run is pending.

## Explicit remaining limits

- MCP HTTP credentials/OAuth are not implemented. HTTP MCP endpoints use HTTPS outside loopback, and configured hosts/credentials are never logged. Add a dedicated secret-provider integration before authenticated remote servers are required.
- The dedicated real stdio subprocess MCP server lifecycle/crash-recovery test passes locally and is repeated five times in the Go 1.25.0 macOS CI job. The PostgreSQL-backed full MCP Invocation test uses Streamable HTTP; stdio and PostgreSQL are independently covered rather than combined in one test.
- No production MCP server configuration is enabled by default. Configure `MCP_SERVERS_JSON` using a local `.env`/secret injection outside Git. MCP tool allowlists are operator-controlled; server claims never grant permission.
- MCP tool results are treated as untrusted text/JSON. Prompt-injection/content sanitization is outside the protocol layer and must remain the Agent's untrusted-input responsibility.
- No Eino, Home Assistant, Xiaozhi, ROS 2, arbitrary shell, filesystem write/delete or other side-effect tool work was started.
