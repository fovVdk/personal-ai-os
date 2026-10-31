# Personal AI OS V1 Implementation Plan

## Repository and environment findings

- The repository is a clean Go 1.22 module skeleton; domain directories contain only package comments and there are no existing implementations or tests to preserve.
- The installed toolchain is Go 1.27.3 on darwin/arm64. Keep the module language target at Go 1.22 and do not upgrade toolchains.
- Docker CLI 29.6.2 and Docker Compose v5.3.1 are installed. Daemon availability must be checked before attempting infrastructure startup.
- The checkout is located at the configured GOPATH root, which makes Go print `ignoring go.mod in $GOPATH`; run module commands with a temporary GOPATH outside the checkout rather than changing user environment.
- The workspace does not currently have Git metadata, so changes cannot be reviewed with `git diff` or committed from this directory.

## Architecture decisions

1. Keep a modular monolith and preserve the existing `internal/` domain boundaries.
2. Build transport and infrastructure adapters around domain interfaces; no provider, agent-framework, or database types should leak into core business rules.
3. Use PostgreSQL/pgx for durable application state and ordered SQL migrations. Keep unit tests independent of Docker; gate database integration tests on an explicitly supplied test DSN.
4. Centralize configuration, logging, request IDs, API errors, readiness, lifecycle, and graceful shutdown before adding business APIs.
5. Enforce policy before tool side effects. Approval records are bound to a canonical parameter hash, expire, and cannot authorize changed parameters.
6. Use deterministic mock model/agent implementations so core workflows do not require credentials or external services.
7. Deliver in verified increments; explicitly record incomplete modules rather than representing interfaces or stubs as shipped functionality.

## Phases and dependency order

| Phase | Scope | Key deliverables | Verification |
| --- | --- | --- | --- |
| 0 | Repository inspection | This plan, toolchain and baseline findings | Baseline build/test |
| 1 | Runtime foundation | Env config, structured logging, pgx pool, migration runner/schema, Chi HTTP server, health/readiness, graceful shutdown, deployment templates | `go build ./...`, `go test ./...`, handler/config tests |
| 2 | Core domains | Identity primitives, Goal and Task state machines, events, audit interfaces and persistence | State transition and repository tests |
| 3 | Controlled autonomy | Structured planner, bounded supervisor, execution/checkpoints, A0-A3 policy, parameter-bound approvals | Policy, approval, retry, and workflow tests |
| 4 | Model and agents | Provider-neutral model gateway, mock/OpenAI-compatible/Ollama providers, registry/runtime and mock agents | Mock provider and adapter tests |
| 5 | Tools | Registry/schema, policy-gated tool executor, safe built-ins, MCP client boundary | Authorization-before-side-effect tests |
| 6 | Memory and knowledge | CRUD/search, provenance, chunking, embedding/retrieval contracts and pgvector migration support | CRUD/search and retrieval tests |
| 7 | Nodes and observability | Node registry/health, tracing and metrics interfaces | Domain and instrumentation tests |
| 8 | Delivery | Compose, Makefile, OpenAPI, deployment/recovery docs, integration/e2e/failure-recovery tests | Full test suite and documented smoke checks |

## Initial implementation slice

Start with Phase 1 plus the smallest useful vertical slice from Phase 2: durable Goal/Task APIs and tested state transitions. This validates the DB/API boundaries before adding autonomy. Do not silently use in-memory state as production persistence. If PostgreSQL is unavailable locally, unit tests still run and DB-backed checks remain explicitly marked as not run.
