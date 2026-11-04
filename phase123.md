# Phase 12.3 — Autonomous Runtime & E2E Closure

你现在继续开发 Personal AI OS Go Core 项目。

本阶段目标：在现有 Phase 0–12.2.6 的实现基础上，完成真正可持续运行的 Autonomous Goal Runtime，建立后台 Goal Job、事件驱动唤醒、真实来源快照与引用验证，并通过完整的 Autonomous Research Goal E2E 测试。

**这不是一次简单的功能追加，而是从“具备自主决策能力”走向“具备持续自主运行能力”的关键阶段。**

必须先阅读现有代码、架构文档、Migration、API、测试报告和 IMPLEMENTATION_STATUS.md，确认实际实现状态。不要假设文档与代码完全一致。

---

# 一、必须遵守的架构原则

## 1. Goal-first 架构

保持现有核心层级：

Goal → Subgoal → Plan/Milestone → Task → Task Step → Agent/Tool → Execution → Evidence

约束：

- Goal 是最高级管理对象，不属于任何 Agent。
- Task 独立于对话和 Agent 生命周期。
- Supervisor 是 Goal 生命周期的决策协调者。
- Agent 只能执行授权范围内的工作，不能决定系统权限。
- Model 不能自行声明 Goal 完成。
- Goal 完成必须经过独立 Evaluator、Verifier 和有效 Evidence 验证。
- Goal、Task、Execution、Decision 必须支持跨进程重启恢复。

## 2. 控制平面与执行平面分离

继续保持：

- Go Core：Goal、Task、Policy、Approval、Supervisor、Job、Event、Audit。
- Execution Runtime：Worker、Agent、Tool Gateway、MCP、外部工具。
- Python 只承担必要的 AI 专项能力，不重写 Go Core。
- 不引入不必要的微服务。

## 3. 权限与安全

沿用现有 A0–A3 权限体系：

- A0：读取、查询、搜索、分析。
- A1：创建笔记、知识、任务。
- A2：修改文件、发送消息、普通设备控制，依授权策略执行。
- A3：付款、重要删除、门锁、安防及高风险机器人操作，必须人工审批。

必须保证：

- 所有外部操作通过现有 Tool Gateway。
- 不允许 Agent 绕过 Policy、Approval 或 Execution Lease。
- 不允许模型提高自身权限。
- 审批必须绑定具体动作和参数。
- 参数发生实质变化时必须重新审批。
- 不允许用 Fencing Token 假装实现外部副作用 Exactly-once。
- 对执行结果不确定的外部操作，必须保留 unknown 状态并支持对账或人工处理。

---

# 二、Phase 12.3.1 — Persistent Goal Job Runtime

## 目标

将当前由 HTTP 请求触发的 Supervisor，升级为可以长期运行的后台 Goal Job Runtime。

当前 Supervisor 仍然主要由 HTTP 请求触发。本阶段必须解决这个问题。

## 实现要求

### 1. Goal Job 持久化

设计独立的 Goal Job 数据模型，至少包含：

- Job ID
- Goal ID
- Job 状态
- 当前运行阶段
- Lease Owner
- Lease Expiry
- Heartbeat
- Fencing Token
- Next Run At
- Last Run At
- Last Error
- Retry Count
- Created At / Updated At

Job 状态必须明确区分：

- pending
- running
- waiting_approval
- waiting_external
- retry_wait
- completed
- failed
- cancelled

具体状态可根据现有领域模型调整，但必须有清晰、可验证的状态迁移规则。

### 2. Goal Job Worker

实现后台 Goal Job Worker：

- 启动时扫描待执行 Job。
- 支持多个 Worker 并发运行。
- 使用 PostgreSQL 原子认领机制。
- 使用 Lease、Heartbeat 和 Fencing Token。
- Lease 过期后允许其他 Worker 接管。
- 旧 Worker 不得覆盖新 Worker 的状态。
- 支持 Core 重启后恢复。
- 支持优雅关闭。
- 支持执行预算、最大重试次数和退避策略。

必须防止同一个 Goal 被多个 Worker 同时执行有效的 Supervisor 决策循环。

### 3. Supervisor 集成

不要另外实现一套 BackgroundSupervisor。

HTTP 触发和后台触发必须复用同一个 Supervisor 服务及决策逻辑。

Supervisor 每次运行必须：

1. 读取最新 Goal 状态。
2. 获取当前 Evaluation。
3. 检查 Evaluation 是否过期。
4. 检查待处理 Approval。
5. 检查 Execution 和 Tool Invocation 状态。
6. 根据现有 Decision 机制决定下一步。
7. 必要时提交新的 Plan Proposal。
8. 在权限允许时执行后续 Task。
9. 更新 Job 状态和下一次运行时间。
10. 持久化决策和审计记录。

waiting_approval 和 waiting_external 不应通过无意义的高频轮询消耗资源。

### 4. API

增加必要的 API：

- 创建或启动 Goal Job。
- 查询 Goal Job。
- 查询 Goal Job 运行历史。
- 暂停和恢复 Goal Job。
- 取消 Goal Job。
- 查询 Goal 当前自主运行状态。

具体路径遵循现有 API 命名规范。

HTTP API 负责创建 Job、查询状态和控制生命周期，不负责直接执行长时间 Supervisor 循环。

### 5. 测试

必须覆盖：

- 两个 Worker 同时竞争一个 Goal。
- Worker 获取 Lease 后异常退出。
- Lease 过期后的接管。
- Fencing Token 递增。
- 旧 Worker 无法提交结果。
- Core 重启后 Job 恢复。
- Goal 取消后的执行阻止。
- waiting_approval 状态不被重复执行。
- 多个 Job 并发运行。
- 优雅关闭和重新启动。

---

# 三、Phase 12.3.2 — Event-driven Wakeup

## 目标

建立事件驱动的 Goal Job 唤醒机制。

不要让 Goal Job 依靠高频轮询才能继续执行。

## 实现要求

建立：

Outbox → Event Dispatcher → Goal Job Wakeup → Supervisor Resume

### 1. 事件来源

至少支持以下事件：

- Approval Approved
- Approval Rejected
- Execution Completed
- Execution Failed
- Execution Cancelled
- Tool Invocation Resolved
- Tool Invocation Unknown
- External Condition Satisfied
- Plan Proposal Accepted
- Goal Updated

只处理真实存在且可以从 Core 状态中验证的事件。

不要为了凑功能虚构事件来源。

### 2. Outbox Dispatcher

实现持久化 Outbox Dispatcher：

- 从 PostgreSQL 读取待发布事件。
- 支持多 Dispatcher 并发。
- 事件至少一次投递。
- 支持投递失败重试。
- 支持 Dispatcher 崩溃恢复。
- 支持重复投递。
- 支持消费状态持久化。
- 支持死信或明确的终止失败状态。

### 3. Event Consumer

事件消费必须具备幂等性。

同一事件被投递多次时：

- 不得重复执行相同的 Supervisor 决策。
- 不得重复创建 Plan Proposal。
- 不得重复创建 Goal Job。
- 不得重复产生外部副作用。

多个事件同时唤醒同一个 Goal 时，必须合并唤醒请求，不能并发启动多个有效的 Supervisor 循环。

### 4. 重要边界

事件只是唤醒信号，不是可信业务状态。

Supervisor 被唤醒后必须重新读取数据库中的真实状态，不能仅根据事件 Payload 做决策。

不能因为收到 Execution Completed 事件，就直接相信 Task 已完成。

最终状态仍然必须经过现有 Evidence、Evaluator 和 Verifier 流程。

### 5. 测试

覆盖：

- Outbox 发布成功。
- Dispatcher 发布后崩溃。
- 同一事件重复投递。
- 消费者处理过程中崩溃。
- 重启后重新消费。
- 多个事件同时唤醒一个 Goal。
- 不同 Goal 并行唤醒。
- Approval 事件触发 Supervisor 继续执行。
- Execution 完成事件触发后续任务。
- Goal 已取消时忽略无效唤醒。

---

# 四、Phase 12.3.3 — Source Snapshot & Reference Verification

## 目标

为自主研究任务建立真实、可追溯、可验证的外部来源采集能力。

当前 Artifact Verifier 可以验证文件存在、大小、Markdown 标题和 SHA-256，但这不等于报告中的引用真实有效。

本阶段必须补齐来源快照和引用验证。

## 1. Source Snapshot 数据模型

每个来源至少持久化：

- Source ID
- Original URL
- Final URL
- HTTP Status
- MIME Type
- Retrieved At
- Content Hash
- Raw Content 或可恢复的原始内容存储引用
- Normalized Content
- Fetcher Version
- Source Metadata
- Associated Goal / Task
- Created At

来源快照必须不可变。

同一个 URL 在不同时间抓取，应产生不同快照记录。

不能仅保存 URL，然后在未来验证时重新访问网页并把新内容当成原始证据。

## 2. Source Fetcher

实现受控的来源采集器。

要求：

- 只允许 HTTP/HTTPS。
- 限制响应体大小。
- 设置连接、读取和整体超时。
- 限制重定向次数。
- 对每次重定向重新执行安全检查。
- DNS 解析后检查目标 IP。
- 拒绝 localhost、私有 IP、链路本地地址和内部网络地址。
- 防止 DNS Rebinding。
- 禁止访问云元数据地址。
- 校验 MIME Type。
- 不执行下载内容中的脚本。
- 不允许网页内容作为系统指令执行。

URL 白名单和策略必须由 Core 管理，不能让模型自行决定安全边界。

如果已有 `http.fetch` 工具，评估复用现有 Gateway 和 Policy，不要另建绕过现有安全体系的 HTTP 执行入口。

## 3. Reference Verification

建立报告引用与 Source Snapshot 的关联机制。

至少支持：

- 引用 URL 是否存在。
- 是否关联已持久化快照。
- 快照是否成功获取。
- 引用内容 Hash 是否一致。
- 来源是否过期。
- 报告引用与来源内容是否存在可验证关联。

必须区分：

- URL 可访问。
- 来源已成功采集。
- 来源快照已保存。
- 报告引用指向正确快照。
- 报告陈述得到来源支持。

这些不是同一件事。

不要声称仅凭 URL 可访问就证明报告内容真实。

## 4. 语义验证边界

第一版可以实现确定性的引用结构验证和基础内容关联验证。

如果无法可靠判断某段报告陈述是否被来源支持，必须标记为需要进一步验证。

不能让 LLM 自己判断自己生成的报告完全正确，然后直接赋予 Verified 状态。

必须保留 Verifier 的独立性和可审计性。

---

# 五、Phase 12.3.4 — Autonomous Goal E2E Closure

## 目标

建立一个真正完整的自主研究 Goal 测试场景。

不要只测试孤立 API 或 Mock Agent。

必须使用真实 PostgreSQL、真实持久化 Job、真实事件流程、真实 Source Snapshot 和 Artifact Verifier。

## 端到端场景

创建一个研究型 Goal，例如：

“研究某个指定开源机器人项目的架构、核心硬件、软件依赖和自主运行能力，生成一份包含真实来源引用的技术研究报告。”

测试必须覆盖以下完整生命周期：

1. 创建 Goal。
2. 设置结构化 Success Criteria。
3. 创建 Goal Job。
4. 后台 Worker 自动启动。
5. Supervisor 读取 Goal 并生成计划。
6. Planner 创建 Plan Proposal。
7. Plan Proposal 经过校验并被接受。
8. Task 被持久化并进入执行队列。
9. Agent 通过 Tool Gateway 获取研究所需信息。
10. 外部来源被真实采集并形成 Source Snapshot。
11. Source Snapshot 和报告引用建立关联。
12. 至少触发一次需要人工审批的操作。
13. 审批完成后，通过事件唤醒 Goal Job。
14. 至少模拟一次 Tool Invocation unknown。
15. 至少模拟一次 Worker 崩溃。
16. Lease 过期后由其他 Worker 接管。
17. 至少模拟一次 Go Core 重启。
18. 恢复 Job、Execution、Checkpoint 和事件处理状态。
19. Supervisor 根据最新 Evaluation 判断是否需要重新规划。
20. 生成最终研究报告。
21. Artifact Verifier 验证报告文件。
22. Reference Verifier 验证报告引用和 Source Snapshot。
23. Evaluator 验证所有必需 Success Criteria。
24. Supervisor 根据验证结果生成最终 Decision。
25. Goal 只有在所有必需标准满足时才允许完成。

## 必须证明的结果

- Goal 跨 Core 重启持续存在。
- Goal Job 跨 Core 重启持续存在。
- 不会因为 Worker 崩溃而丢失执行进度。
- 不会因为事件重复投递而重复执行外部操作。
- 不会因为模型自报成功而完成 Goal。
- 报告引用可以追溯到真实来源快照。
- 报告修改后旧 Artifact Hash 验证失效。
- 来源快照变化或引用失效时，旧验证不能继续作为有效依据。
- 旧 Evaluation 不能被当作当前有效 Evaluation。
- 未通过必需标准时，Goal 不得完成。
- 最终 Goal 完成可以通过 API 和数据库状态独立核对。

Mock Agent 可以用于单元测试，但不能作为完整自主研究 E2E 的唯一执行主体。

如果真实模型、真实外部数据源或人工审批环境受限，必须明确标记测试边界，不得伪造完整 E2E 成功。

---

# 六、数据库与代码质量要求

1. 所有新增数据库结构必须使用版本化 Migration。
2. Migration 必须支持事务回滚和失败重试。
3. 重要状态变化必须与 Audit、Outbox 在同一事务中提交。
4. 所有异步任务必须具备持久化状态。
5. 所有重要操作必须具备幂等策略。
6. 所有 Worker 必须正确处理 Context Cancel。
7. 必须正确处理 PostgreSQL 并发和事务隔离。
8. 不允许通过内存 Map 充当持久化队列。
9. 不允许依赖进程内 Channel 保证事件不丢失。
10. 不允许把模型返回内容直接写入可信 Evidence。
11. 不允许绕过现有领域层直接修改数据库状态。
12. 不允许为通过测试而降低现有安全检查。

保持现有 Go 项目编码风格、目录边界和依赖管理方式。

不要大规模重构已经稳定的 Phase 0–12.2.6。

---

# 七、测试与验收标准

每个子阶段必须分别验收。

至少执行：

```bash
make fmt
make test
make build
make vet
go test -race ./...
make build-arm64
make test-integration
```

如果项目命令名称与上述不同，以实际 Makefile 为准，但必须覆盖相同的验证范围。

还必须检查：

- Docker Compose 配置。
- Migration 顺序和 checksum。
- Migration 失败回滚和重试。
- PostgreSQL 容器重启。
- Go Core 重启。
- Worker 崩溃恢复。
- 并发竞争。
- 重复事件消费。
- 外部副作用 unknown 状态。
- OpenAPI 与实际 API 一致。
- ARM64 构建。
- 现有 Phase 0–12.2.6 测试没有回归。

Hosted Go 1.25 CI 必须单独确认实际执行结果，不能用本地 Go 1.27 测试代替 Go 1.25 CI 通过。

---

# 八、实施顺序与交付规则

严格按以下顺序实施：

1. Phase 12.3.1：Persistent Goal Job Runtime
2. Phase 12.3.2：Event-driven Wakeup
3. Phase 12.3.3：Source Snapshot & Reference Verification
4. Phase 12.3.4：Autonomous Goal E2E Closure

每完成一个子阶段：

- 更新 IMPLEMENTATION_STATUS.md。
- 更新架构文档。
- 更新 OpenAPI。
- 编写独立测试。
- 运行对应验收。
- 记录新增 Migration。
- 记录尚未完成的工作。
- 输出具体文件变更清单。

不要一次性提交一个巨大、无法独立验收的实现。

如果发现现有架构存在阻碍本阶段实现的问题，先说明问题、影响范围和修复方案，不要擅自进行大规模重构。

遇到外部依赖不可用、真实模型无法调用、CI 未运行或真实来源无法采集时，明确标记为 BLOCKED 或 NOT VERIFIED，不得伪造成功。

## 最终交付物

至少包含：

- `IMPLEMENTATION_STATUS.md`
- `AUTONOMOUS_RUNTIME_REPORT.md`
- `EVENT_DRIVEN_RUNTIME_REPORT.md`
- `SOURCE_SNAPSHOT_REPORT.md`
- `AUTONOMOUS_GOAL_E2E_REPORT.md`
- 完整 Migration 清单
- 更新后的 OpenAPI
- 新增单元测试、集成测试和 E2E 测试

最终报告必须区分：

- 已实现
- 已通过单元测试
- 已通过真实 PostgreSQL 集成测试
- 已通过故障注入测试
- 已通过完整 E2E
- 已通过 Hosted CI
- 尚未验证
- 当前阻塞

**本阶段最终验收目标：Personal AI OS 不再只是一个可以规划和执行任务的 Go Core，而是一个能够在后台持续管理 Goal、根据事件自主恢复执行、在进程重启和 Worker 崩溃后继续工作，并通过独立证据验证 Goal 是否真正完成的 Autonomous Runtime。**

完成 Phase 12.3 之前，不要进入 Multi-Agent 扩展，不要为了展示能力提前接入机器人硬件，也不要增加不必要的新模型框架。

优先把现有自主执行闭环做扎实。