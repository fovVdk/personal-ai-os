# Phase 12.3.4 — Autonomous Research Goal E2E Closure

## Status: NOT VERIFIED / BLOCKED

A complete Phase 12.3.4 autonomous research Goal E2E is not claimed complete.

### Verified independently

- PostgreSQL Goal Job claim/fencing/wake/lifecycle and Core restart persistence.
- Outbox retry/dead-letter, lease recovery, duplicate receipt, coalesced wake and Approval-triggered wake.
- Source Snapshot and Reference persistence, structural quote/URL/hash/freshness checks, and ArtifactVerifier report binding.
- Existing durable Worker crash/checkpoint recovery and PostgreSQL/Core restart acceptance.

### Missing from one integrated scenario

No test currently combines all required steps in one autonomous research Goal lifecycle: a production-safe report writer/Research Agent, allowlisted real external retrieval and snapshot creation, report/reference registration, Approval event continuation, Tool Invocation `unknown`, Worker crash and lease takeover, Core restart, final independent Evaluation and evidence-gated Goal completion.

The local environment did not provide an explicitly authorized real source host or real report-generation/semantic-review adapter for such an acceptance. No source was fetched and no success was fabricated. Mock Agents remain unsuitable as the sole proof of a real research/report E2E.

### Required next work

1. Add a bounded report-writer adapter whose output is stored beneath the configured artifact root and cannot self-attest verification.
2. Define a trusted research workflow that uses only operator-allowlisted sources through Tool Gateway.
3. Add an integration scenario with an explicitly approved public source and deterministic independent evidence/reference checks, including Approval, unknown outcome, Worker crash/takeover and Core restart.
4. Run and record the full single-Goal acceptance, then verify current Goal state from PostgreSQL/API.

Hosted Go 1.25 CI is also pending and must be confirmed separately.
