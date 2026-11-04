# Phase 12.3.3 — Source Snapshot & Reference Verification

## Status: IMPLEMENTED for controlled capture and deterministic structural verification

### Delivered

- Migration `0016_source_snapshots.sql` stores immutable source snapshots (raw bytes, normalized text, original/final URLs, status, MIME, UTC retrieval time, SHA-256, fetcher version and Goal/Task metadata) and immutable report-reference bindings.
- Snapshot/reference records reject UPDATE; each fetch produces a new record. Raw content is preserved in PostgreSQL for deterministic re-verification.
- `source.capture` is enabled only with an operator-provided exact `HTTP_FETCH_ALLOWED_HOSTS` allowlist and is executed via the existing Tool Gateway using trusted Goal/Task/Execution/Step context.
- Fetcher supports HTTP(S) only, no proxy, public-address DNS checks and IP-pinned dialing, revalidates every allowed redirect (maximum five), restricts ports and MIME types, caps body size at 2 MiB, and uses a 15-second overall timeout.
- Snapshot verification recomputes content hash and normalized text, checks successful HTTP status, exact original/final URL binding, quote hash and exact quote occurrence, and snapshot freshness.
- Required report artifacts must visibly contain the `source://<snapshot-id>` link, bound URL and exact quote. The verifier emits auditable source-reference Evidence. Semantic claim support is `requires_independent_review`, never inferred from URL or lexical match.
- Owner-scoped APIs list/get snapshots and create/list report citation bindings.

### Verification

- Unit tests cover URL allowlist/port/scheme, local/private/reserved IP blocks, MIME filtering, script removal from HTML normalization, quote/URL mismatch and semantic-verification boundary.
- PostgreSQL integration tests passed in the full isolated acceptance and cover migration rollback/retry, raw-content roundtrip, snapshot/reference CRUD, hash/freshness checks, fabricated URL/quote rejection and immutable update rejection.
- Artifact verifier tests cover visible citation binding and failure when cited report content is removed.

### Limits

- Real external-source capture requires operator configuration and a reachable allowlisted public host; no external source fetch was performed as part of this coding session.
- The current exact quote check is lexical/structural only. It does not prove that a report claim is logically entailed, that the source is authoritative or independent, or that fetched content is factually accurate.
- Raw content is stored in PostgreSQL with a 2 MiB cap; large-scale/object-storage retention is future work.
