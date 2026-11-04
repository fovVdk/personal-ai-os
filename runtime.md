# Personal AI OS — Phase 10：Durable Execution Runtime

## 项目背景

Personal AI OS 已完成 Phase 0–9。

当前已经具备：

- Go Core 基础设施
- PostgreSQL 持久化
- Goal / Task / Execution
- Planner / Supervisor
- Policy / Approval
- Mock Agent
- Model Gateway
- Memory / Knowledge / Node
- Audit / Outbox 基础能力

Phase 9 已完成真实 PostgreSQL 集成验收。

相关报告：

- DATABASE_INTEGRATION_REPORT.md
- IMPLEMENTATION_STATUS.md
- TEST_REPORT.md

本阶段的目标不是增加新的 AI 功能，而是把现有 Execution 系统升级为真正可靠的持久化后台执行引擎。

必须首先阅读现有代码和文档，理解已有设计，禁止重复建设已有能力。

---

# 一、总体目标

建立 Durable Execution Runtime，使系统具备：

1. 持久化任务认领。
2. 多 Worker 并发执行。
3. Worker 心跳。
4. 执行租约。
5. Worker 崩溃检测。
6. 未完成任务自动恢复。
7. Checkpoint 持久化。
8. 执行超时与重试。
9. 幂等执行控制。
10. 防止失效 Worker 覆盖新执行结果。
11. Supervisor 与后台 Worker 协作。
12. 服务重启后自动恢复执行。

本阶段不引入 Temporal，不重写现有 Execution 模型。

优先在现有 PostgreSQL 和 Go 架构上实现。

# 二、第一步：审查现有实现

在修改代码前，检查：

- Execution 状态定义。
- Task 状态定义。
- Execution Repository。
- Task Repository。
- Supervisor。
- Execution Manager。
- 当前 Checkpoint 实现。
- 当前 Retry 实现。
- 当前 Idempotency 实现。
- 数据库 Migration。
- 现有测试。

创建：

docs/PHASE10_DESIGN.md

记录：

1. 当前执行架构。
2. 已有能力。
3. 缺失能力。
4. 新增组件。
5. 状态转换。
6. 数据库修改。
7. Worker 协作流程。
8. 故障恢复策略。

避免重复创建已有字段或表。

# 三、Execution 状态机

检查现有状态定义，设计完整状态转换。

至少考虑：

- pending
- queued
- claimed
- running
- waiting_approval
- retry_wait
- succeeded
- failed
- cancelled
- unknown

要求：

1. 状态转换必须有明确规则。
2. 非法状态转换必须被拒绝。
3. 终态不能被普通 Worker 随意修改。
4. 每次状态变化需要记录时间和原因。
5. 失败状态需要保存错误分类。
6. 需要审批时不能继续执行。
7. 外部副作用结果不确定时不能盲目重试。

如果现有状态名称不同，优先兼容现有模型，并记录映射关系，不要为了统一命名而破坏已有数据。

# 四、Durable Claim

实现持久化执行认领。

要求：

1. Worker 只能认领符合执行条件的 Execution。
2. 同一个 Execution 同一时刻只能存在一个有效执行者。
3. 认领操作必须通过数据库事务实现。
4. 支持多个 Worker 并发竞争。
5. 认领失败不能导致数据状态损坏。
6. 支持 Lease Expiration。
7. 认领信息必须持久化。

优先考虑 PostgreSQL SELECT FOR UPDATE SKIP LOCKED。

新增或调整必要的数据结构：

- worker_id
- lease_until
- claimed_at
- heartbeat_at
- fencing_token

具体字段根据当前 Schema 设计，避免重复。

# 五、Lease 和 Heartbeat

实现：

1. Worker 定期发送 Heartbeat。
2. Heartbeat 延长有效 Lease。
3. Worker 停止工作后 Lease 最终过期。
4. Lease 过期后允许其他 Worker 重新认领。
5. 旧 Worker 不能在 Lease 失效后继续修改 Execution。
6. 所有写入必须验证当前 fencing_token。

必须通过测试验证旧 Worker 无法覆盖新 Worker 的执行结果。

# 六、Worker Runtime

建立独立的后台执行组件。

建议职责：

- Worker Manager
- Worker Pool
- Execution Claim Service
- Lease Manager
- Heartbeat Manager
- Execution Dispatcher
- Recovery Scanner

具体包名和目录以当前项目为准，不要为了形式化而过度拆分。

要求：

1. 支持配置 Worker 数量。
2. 支持最大并发数。
3. 支持优雅关闭。
4. 关闭时停止领取新任务。
5. 正在执行的任务按策略完成或安全中断。
6. Context Cancellation 必须向下传递。
7. 避免 goroutine 泄漏。
8. Worker 崩溃后能够被检测和恢复。

# 七、Checkpoint

检查现有 Checkpoint 设计并补齐持久化能力。

Checkpoint 至少需要支持：

- Execution ID
- Step ID
- Checkpoint Sequence
- 执行进度
- 已完成步骤
- 中间状态引用
- 创建时间
- 必要的版本信息

要求：

1. Checkpoint 必须持久化。
2. 同一个 Execution 的 Checkpoint 必须有明确顺序。
3. 恢复时从最后一个有效 Checkpoint 开始。
4. 不允许旧 Worker 覆盖新 Checkpoint。
5. 敏感信息不得明文写入 Checkpoint。
6. 大型文件或二进制数据应保存为外部引用，不要直接塞进数据库。

# 八、幂等机制

重点实现：

1. Task 层面的幂等。
2. Execution 层面的幂等。
3. Step 层面的幂等。
4. Tool 调用层面的幂等声明。

要求：

- 幂等 Key 必须具有明确作用域。
- 数据库约束必须防止重复创建。
- 不能只依赖内存 Map。
- 服务重启后幂等记录仍然有效。
- 相同 Key 和相同参数可以识别为重复请求。
- 相同 Key 但不同参数必须拒绝。
- 外部副作用需要支持 Idempotency Key 或明确的不确定结果处理。

不得将任意外部调用描述为天然 Exactly-once。

# 九、自动恢复机制

服务启动后：

1. 查询未完成 Execution。
2. 检查 Lease 状态。
3. 识别过期执行。
4. 判断是否可以安全恢复。
5. 恢复符合条件的 Execution。
6. 不可安全恢复的进入 unknown 或人工核实状态。
7. 记录 Recovery Audit。

后台 Recovery Scanner 应周期性运行。

恢复过程必须幂等，重复扫描不能破坏状态。

# 十、Supervisor 集成

将 Supervisor 与 Durable Execution Runtime 连接。

Supervisor 负责：

- 监控 Goal。
- 识别可执行 Task。
- 根据依赖关系调度 Task。
- 管理执行预算。
- 监控执行结果。
- 根据 Evidence 更新 Goal。
- 失败时判断是否需要重新规划。
- 遇到权限限制时等待审批。

Supervisor 不应直接持有数据库执行细节。

执行状态、认领、Lease 和恢复由 Execution Runtime 负责。

自主循环必须具有：

- 最大迭代次数。
- 最大执行时长。
- Token 预算。
- 工具调用预算。
- 停止条件。
- 用户暂停机制。
- 取消机制。

# 十一、故障注入测试

必须设计真实数据库测试。

至少覆盖：

1. 两个 Worker 同时认领同一个 Execution。
2. Worker 认领后立即崩溃。
3. Worker 执行中失去心跳。
4. Lease 过期后新 Worker 接管。
5. 旧 Worker 恢复后尝试写入。
6. Checkpoint 保存过程中数据库连接中断。
7. 服务重启后自动恢复。
8. 重复提交相同幂等 Key。
9. 相同幂等 Key 使用不同参数。
10. 执行过程中收到取消信号。
11. 多个 Task 存在依赖关系。
12. 执行过程中需要人工审批。
13. 执行结果不确定时阻止盲目重试。
14. 数据库短暂不可用后恢复。

必须使用真实 PostgreSQL 验证关键并发和事务行为。

# 十二、API 和运维

检查是否需要增加：

- Worker 状态查询。
- Execution 状态查询。
- Execution 取消。
- Execution 重试。
- Execution 暂停与恢复。
- 系统运行状态。

只增加真正需要的 API。

完善：

- 配置文件。
- Docker Compose。
- Makefile。
- 日志。
- Audit。
- Metrics 基础指标。

# 十三、测试与验收

执行：

- gofmt
- go test ./...
- go test -race ./...
- go vet ./...
- make build
- make test
- make test-integration
- PostgreSQL 故障注入测试
- Worker 并发测试
- 服务重启恢复测试

所有测试必须实际执行。

# 十四、文档与交付

创建：

docs/PHASE10_DESIGN.md
docs/PHASE10_IMPLEMENTATION_REPORT.md

更新：

docs/IMPLEMENTATION_STATUS.md
TEST_REPORT.md

报告必须明确：

1. 新增模块。
2. 修改模块。
3. 数据库 Migration。
4. Worker 架构。
5. Claim 与 Lease 实现。
6. Checkpoint 实现。
7. 幂等策略。
8. 恢复策略。
9. 故障注入测试。
10. 性能和并发限制。
11. 未解决问题。

本阶段完成后暂停。

不要开始 MCP、Eino、Embedding 或 Home Assistant 开发。

等待下一阶段指令。