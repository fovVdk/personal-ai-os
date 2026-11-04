# Phase 12.3.1 — Persistent Autonomous Goal Runtime

## Status: IMPLEMENTED; PostgreSQL integration coverage passed

### Delivered

- Migration `0014_goal_jobs.sql` stores durable Goal Jobs, one live Job per Goal, stage, run/retry budgets, next/last run, wake flag and lifecycle timestamps.
- Goal Job worker uses atomic PostgreSQL `FOR UPDATE SKIP LOCKED` claims, lease owner/expiry, heartbeat and monotonic fencing tokens. An expired lease can be reclaimed; stale workers cannot heartbeat or finish.
- A Job claim invokes exactly one bounded turn of the existing Supervisor. The runtime does not directly invoke Agents/Tools, and HTTP handlers enqueue or control Jobs rather than running long loops.
- Create/wake, history, get, pause, resume and cancel APIs are documented in OpenAPI. Pausing/canceling fences active workers.
- Per-turn status maps approval and external waits into non-hot polling states; worker/run/retry limits are persisted.

### Verification

- Unit tests cover Job record lease invariants and Supervisor status mapping.
- PostgreSQL tests cover competing workers, fencing, expired lease takeover, wake coalescing, approval wait, pause/resume/cancel, and Job persistence across two Core process starts.
- Final full acceptance `make test-integration` passed after migration 0016, together with `go test -race ./...`, `make build-arm64`, Compose config parsing and YAML parsing. PostgreSQL restart and Worker crash/checkpoint acceptance also passed.

### Limits

- One claim runs one bounded Supervisor turn; this is a durable runtime, not an unrestricted autonomous loop.
- Jobs are created only by an explicit API request. Events do not silently opt every active Goal into autonomy.
- Hosted Go 1.25 CI result is pending and is not claimed from local Go 1.27.3 results.
