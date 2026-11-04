# Verification Design

## Trust boundary

- Agent and Model outputs are proposals. `Result.Verified` supplied by an Agent is ignored by Core.
- `ResultVerifier` is injected by Go Core into the execution boundary. A result is accepted only when the verifier returns `Verified=true`, a non-empty verifier method, and verifier-produced Evidence.
- Persisted execution Evidence receives a UUID in `execution_evidence`. Evaluations link to these real UUIDs through `criterion_verification_evidence`; they never trust Evidence IDs embedded in a model response.
- Verification rows bind Goal timestamp, Criterion ID/version, verifier type/version, result/reason, Evidence hashes and linked Evidence IDs. Histories are retained and Audit/Outbox are written transactionally.

## Deterministic verifiers

`verification.ArtifactVerifier` uses a Core-configured FILE_STORAGE_DIR and Go's rooted filesystem APIs. It accepts only local relative paths. Supported checks are regular-file existence/readability, bounded byte size, UTF-8 Markdown extension and exact required headings. It records SHA-256 as an integrity hash, not a truth claim.

At Goal completion PostgreSQL locks the Goal, requires current Goal/Criteria versions and a ready Evaluation, verifies every required Criterion has a satisfied persisted Verification and linked real Evidence, confirms all Tasks are terminal, and asks the configured Artifact freshness verifier to reread and rehash artifact files. Concurrent Task state/Evidence writes acquire a shared Goal lock, preventing them from racing through the completion gate.

Evidence-count criteria count unique persisted Evidence UUIDs bound to the exact Criterion ID/version, only from Core-verified executions. Task-completion criteria additionally require the referenced Task belongs to the Goal, is completed, is mapped to that Criterion, and has matching verified Evidence.

## Limits

- Hash equality proves that bytes have not changed since verification; it does not prove semantic correctness or truth.
- Markdown checks currently validate required headings only. There is no general schema/table/section semantic engine.
- The system has no source snapshot/reference ingestion service. Arbitrary URLs are not accepted as proof of source authenticity.
- The production ModelAgent has no general-purpose independent verifier. The configured local ArtifactVerifier can verify existing files, but no safe Agent artifact-writer/source collector is currently registered.
- Model-assisted verification is intentionally not accepted for deterministic criteria.
