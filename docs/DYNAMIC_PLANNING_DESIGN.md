# Dynamic Planning Design

## Data model

Migration 0013 persists `plan_versions`, `plan_proposals`, `tasks.plan_key`, and `tasks.criterion_ids`. A Proposal includes Goal ID, base/proposed version, trigger/reason, Evaluation ID, stable fingerprint, structured Plan, retained Task IDs, status and expiry.

## Proposal lifecycle

1. Supervisor loads current Goal facts and a persisted Evaluation.
2. Planner receives confirmed criteria, unmet criterion reasons, failed/blocked Task summaries and constraints.
3. Core validates Goal ownership, Plan DAG, exact Goal/criterion mappings, task budget, next monotonically increasing version and proposal fingerprint.
4. Proposal is stored without effect. An authorized accept/reject API or the bounded initial-plan controller resolves it.
5. Accept locks the Goal row, checks Goal lifecycle, base Plan Version, expiry, current criteria and DAG/task budgets, then atomically creates new Tasks/dependencies, supersedes the old Version, accepts the Proposal and writes Audit/Outbox.

## Preservation and stop rules

- Completed Tasks and their Execution/Evidence history are never deleted or re-executed by plan commit.
- Existing PlanKey Task title/description/Criterion mapping cannot be mutated in place.
- Every non-terminal Task must be represented in a revised DAG; active work cannot be silently removed.
- A stale base is marked conflict. Re-accepting an already accepted Proposal returns its current committed version idempotently.
- A Proposal adding no executable Task is rejected as `no_plan_progress`; Plan Versions are capped at 10 and Goal Tasks at 200.
- Changed Tool parameters/approval contexts remain controlled by the existing Tool Gateway, not Plan metadata.

## Limits

The current commit supports additive Task DAG proposals and retaining prior work. It does not yet support a general safe remove/modify/cancel/compensation diff. Unknown external side effects must be resolved separately and are never inferred from plan state. The HTTP-triggered Supervisor is not a durable background planning job.
