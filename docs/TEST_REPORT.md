# Test and Validation Report

Date: 2026-10-31

## Environment

- macOS, darwin/arm64
- Go `go1.27.3`; module language target remains Go 1.22
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
| Docker Compose config parse | Passed | `docker compose --env-file .env.example -f deployments/compose/docker-compose.yml config -q` |
| YAML syntax validation | Passed | Compose, example config, and OpenAPI YAML parsed |
| Bilingual documentation audit | Passed | All 100 Go files have bilingual file headers; public GoDoc and existing Go comments were translated; operational templates and README reviewed |
| Historical migration checksum preservation | Passed | SQL migration contents were left unchanged so existing raw-file checksums remain stable; migration design is documented in README |
| PostgreSQL migration/integration run | Not run | Docker Engine works, but pulling `pgvector/pgvector:pg16` exceeded the available session time; no database container/migration execution was confirmed |

## Automated test areas

- Goal and Task state transitions; evidence-gated completion
- Policy A0–A3, approval expiry/hash binding, and rejection of tool side effects while approval is pending
- Retry/idempotency and Supervisor execution/evidence/recovery windows
- Supervisor cross-turn persisted model call/token budget accounting
- Configuration validation and API authentication, validation, size limits, Goal/Task/Planner/Approval/Node flows
- Mock/OpenAI-compatible/Ollama Model Gateway behavior and untrusted tool-call schema/allowlist validation
- Structured planner DAG/schema validation; Knowledge text chunking and provenance checks

PostgreSQL integration tests are gated by `PAIOS_TEST_DATABASE_URL` and use the explicitly supplied test database. Set it to a disposable database before running database integration tests. Do not point it at user or production data.
