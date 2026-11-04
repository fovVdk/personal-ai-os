# Supervisor Controller Design

## Current decision cycle

Supervisor reads persisted Goal, Tasks, Execution Evidence, usage budgets, current Plan Version and Evaluation. `supervisor.NewDecision` deterministically maps Evaluation advice to one of: continue, replan, verify completion, wait approval, wait condition, budget exhausted, stop or escalate. A fingerprint captures Goal timestamp, Criterion versions/status/evidence hashes, Task status, waits/blockers and budgets. PostgreSQL uniqueness makes repeated identical decisions idempotent.

Evaluation and its Decision row are persisted in one transaction with Audit/Outbox. Supervisor turn is bounded by the existing execution runtime, model/tool/token/cost/retry budgets, task limit and maximum Plan Version. Approval/external/unknown states return control rather than holding a Worker indefinitely. Verified required criteria cause a fresh Core completion-gate call; the Goal repository rechecks evidence, Artifact hashes and terminal Task states under the Goal row lock.

For an existing plan, concrete failed/blocked Tasks or insufficient typed evidence can cause a bounded Proposal. The proposal is committed through the existing Core Plan repository, then Supervisor reloads Tasks and submits work through the existing Durable Execution runtime/Worker/Tool Gateway. No parallel task engine or model-owned state writer is used.

## Recovery and concurrency

- Plan commits use Goal row locks and version comparison; stale proposals cannot overwrite a newer active plan.
- Goal completion uses an exclusive Goal lock. Task creation, Task transitions and execution Evidence completion take compatible shared Goal locks, serializing facts against completion.
- Existing Worker lease/fencing/checkpoint/idempotency and Tool Invocation `unknown` semantics remain authoritative.
- Decision fingerprints are stable across Evaluation UUIDs and include facts so unchanged turns reuse the same Decision; Task state/evidence/budget changes produce a new fingerprint.

## Known gaps

The controller is currently synchronous and request-triggered. There is no durable Supervisor Job queue, Outbox/event wakeup consumer, automatic continuation after Approval, configurable decision/replan budget API, or persistent no-progress streak policy. Plan cap and Task cap provide hard stops, but this is not yet a continuously running autonomous Goal loop.
