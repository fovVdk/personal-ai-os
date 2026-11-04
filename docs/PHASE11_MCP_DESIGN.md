# Phase 11 MCP Design — Official SDK Adapter

Date: 2026-11-04

## Scope and compatibility decision

The project minimum Go version is raised from 1.22 to 1.25 as explicitly directed by `agentsmcp.md`; official `github.com/modelcontextprotocol/go-sdk` v1.8.0 is pinned. SDK and project build/test are verified with the installed Go 1.27.3 darwin/arm64 toolchain. An exact Go 1.25.0 toolchain download was attempted but timed out at the Go toolchain storage redirect. No unrelated direct dependencies are upgraded; `golang.org/x/sync` is raised to v0.20.0 as required by the SDK module graph.

SDK types are confined to `internal/tool/mcp`. The adapter registers server-provided tools into the existing provider-qualified Tool Registry. The MCP handler can only be reached from Tool Gateway after policy, approval, durable Invocation reservation, lease/fencing, and schema checks.

## Server and transport model

`Manager` owns a set of operator-configured server identities and lifecycle state. A server config is immutable during one manager registration and specifies:

- stable Server ID and display Name;
- one Transport: `streamable_http` or `stdio`;
- for HTTP: an absolute endpoint URL, exact allowed host names, TLS verification, bounded client timeouts, redirects forbidden, and a dialer that resolves/checks every address and dials the checked public/explicit-local address;
- for stdio: an absolute executable path, separately specified argv (never shell syntax), absolute working directory, and an explicit environment map. No model/Agent input can alter these fields;
- enabled state and exact MCP tool names allowed to receive a Policy grant. The list is operator configuration, not server metadata.

Server health exposes ID/name/transport/enabled/connection state/server version/last connected/last error/tool discovery time. Periodic bounded health/discovery checks detect disconnected servers; reconnect uses bounded exponential backoff and re-runs initialization/discovery. Disable/unregister closes sessions and disables their previously registered tool identities. Runtime status omits endpoint, argv, environment and credentials.

## Discovery and trust rules

- The SDK client advertises empty optional capabilities; it does not offer filesystem Roots, LLM Sampling, or Elicitation to an MCP server.
- Tool names, descriptions, schemas, output schemas and annotations are untrusted. Annotation hints never set risk, permission, idempotency, or verification. Tool changes are re-discovered under a per-server lock; active handler execution and schema refresh are serialized to prevent a pending approval from executing under a changed implementation.
- Only a deliberately narrow JSON Schema subset supported by the core Registry is accepted. Unknown validation keywords, nested/compound schemas, excessive sizes and invalid names fail closed. Schema, description, upstream server version, configured transport and execution bounds form the Tool version fingerprint; a semantic/runtime change produces a new version and disables the old version.
- Every MCP tool is classified as external, risk A3, non-idempotent by default. Exact operator allowlisting only provides a grant; the central Policy still requires human approval for A3. The server cannot modify Go Core grants.
- MCP output is bounded and normalized into the Gateway's strict `{result:string}` contract. `IsError` responses are returned as handler errors, causing durable `unknown`, because an external server's error cannot prove that no side effect occurred.

## Execution

An `mcpToolHandler` captures server ID + exact original MCP tool name + discovered Tool version, not a mutable name lookup. It obtains the current healthy session, adds an operation timeout, sends the input object through SDK `ClientSession.CallTool`, bounds content, and returns a normalized JSON result. It never accepts a Gateway Request, Approval, Execution lease, or model identity itself. These are enforced by the existing Gateway/store.

## Test architecture

Tests run a real official SDK MCP Server using `StreamableHTTPHandler` on a local `httptest.Server`, and connect it through the real SDK `StreamableClientTransport` (not a fake MCP protocol/client). They cover connect/initialize, discover/schema registration, Gateway-only call, no-grant/approval gate, schema rejection, MCP tool errors, timeout/cancellation, invalid content, changing schema/version, disable/unregister, endpoint allowlist/SSRF/redirect rejection, server stop/restart/reconnect, and Audit/Invocation assertions.

A real PostgreSQL integration test composes the SDK MCP Server, Registry/Manager, Gateway, approval continuation, replacement Worker fencing, and durable Invocation/Audit/Outbox replay.

Stdio is implemented as an explicit `exec.Cmd` from operator configuration, using SDK `CommandTransport`, and is unit-tested for validation/no-shell/argv/env behavior. A real SDK stdio server runs as a child Go test process to verify connect/discovery, disable/enable, forced process crash, `unknown` invocation outcome, automatic process restart/reconnect and a successful post-recovery call. No general shell runner is exposed.
