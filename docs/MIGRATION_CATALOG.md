# Database Migration Catalog | 数据库迁移目录

This catalog explains the schema evolution without modifying historical SQL files. Applied migration bytes are checksum-verified at startup, so old migrations must remain immutable; schema changes are added as new, ordered files.

本目录说明数据库结构的演进，但不修改历史 SQL 文件。程序启动时会校验已应用迁移的内容 Hash，因此旧迁移必须保持不可变；新结构只能追加新的有序迁移文件。

| Version | English purpose | 中文用途 |
| --- | --- | --- |
| 0001 `0001_core.sql` | Creates local identity and the core Goal, Task, and dependency relations. | 创建本地身份，以及 Goal、Task 和任务依赖核心关系。 |
| 0002 `0002_execution_approval_memory.sql` | Adds approvals, executions, execution evidence, transactional Outbox, audit records, and personal memories. | 增加审批、执行、执行证据、事务性 Outbox、审计记录和个人记忆。 |
| 0003 `0003_goal_evidence.sql` | Persists evidence attached to Goal completion. | 持久化 Goal 完成时关联的证据。 |
| 0004 `0004_knowledge_vectors.sql` | Enables pgvector and adds document/chunk storage with an embedding column. | 启用 pgvector，并增加文档、分块及 Embedding 列。 |
| 0005 `0005_task_attempts.sql` | Adds durable Task attempt counters. | 增加持久化 Task 尝试计数。 |
| 0006 `0006_nodes.sql` | Adds extensible local/distributed Node identity and health state. | 增加可扩展的本地/分布式 Node 身份及健康状态。 |
| 0007 `0007_model_usage.sql` | Tracks per-Goal model call and token usage. | 按 Goal 记录模型调用次数及 Token 用量。 |
| 0008 `0008_durable_execution_runtime.sql` | Adds execution leases, fencing, checkpoints, recovery metadata, and step idempotency. | 增加执行租约、Fencing、Checkpoint、恢复元数据及步骤幂等记录。 |
| 0009 `0009_tool_fabric.sql` | Persists Tool Invocations and binds one-time Approval to exact execution context. | 持久化 Tool Invocation，并将一次性审批绑定到精确 Execution 上下文。 |
| 0010 `0010_goal_tool_usage.sql` | Adds Core-observed Tool Call counts to the per-Goal autonomy budget. | 将 Core 实际观察到的 Tool Call 数量加入每 Goal 自主预算。 |
| 0011 `0011_goal_evaluations.sql` | Persists immutable Evaluation snapshots and indexes their history. | 持久化不可变 Evaluation 快照并为历史查询建立索引。 |
| 0012 `0012_structured_goal_criteria.sql` | Adds versioned structured Success Criteria and a JSONB search index. | 增加版本化结构化成功标准及 JSONB 检索索引。 |
| 0013 `0013_autonomy_records.sql` | Persists criterion verification/evidence links, versioned plans/proposals, and idempotent Supervisor Decisions. | 持久化标准验证与证据关联、版本化计划/提案及幂等 Supervisor Decision。 |
| 0014 `0014_goal_jobs.sql` | Adds persistent Goal Jobs, one-live-Job constraint, leases, heartbeats, fencing, retries, and wake coalescing. | 增加持久 Goal Job、单活动 Job 约束、租约、心跳、Fencing、重试和唤醒合并。 |
| 0015 `0015_outbox_dispatch_leases.sql` | Adds Outbox claim/retry/dead-letter state and idempotent Goal Job wake receipts. | 增加 Outbox 认领/重试/死信状态及幂等 Goal Job 唤醒回执。 |
| 0016 `0016_source_snapshots.sql` | Stores immutable raw/normalized source snapshots and exact report citation bindings. | 保存不可变原始/规范化来源快照及精确报告引用绑定。 |

## Migration rules | 迁移规则

- Migration order is the lexicographic order of the numbered `.sql` files embedded into the Go binary.
- Migrations are applied under a PostgreSQL advisory transaction lock and one transaction; the filename and SHA-256 checksum are recorded in `schema_migrations`.
- Do not edit any migration that may have been applied. Append a new version instead.
- Test clean application, injected failure rollback, clean retry, and checksum consistency against disposable PostgreSQL only.

- 迁移按嵌入 Go 二进制的编号 `.sql` 文件字典序执行。
- 迁移在 PostgreSQL advisory transaction lock 下以事务执行；文件名和 SHA-256 Hash 写入 `schema_migrations`。
- 可能已经应用的迁移不可修改；结构调整必须追加新版本。
- 全新应用、注入失败回滚、清洁重试及 Hash 一致性检查仅针对一次性 PostgreSQL 测试库执行。
