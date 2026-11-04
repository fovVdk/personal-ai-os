# Personal AI OS — Phase 12.2.3–12.2.6
# Independent Verification, Dynamic Planning & Autonomous Supervisor

## 0. 执行指令

本任务是 Personal AI OS Phase 12.2 的连续实施任务。

要求一次性完成以下四个子阶段，不需要每完成一个子阶段就等待用户确认：

- Phase 12.2.3 — Independent Result Verifier
- Phase 12.2.4 — Dynamic Planner
- Phase 12.2.5 — Supervisor Controller
- Phase 12.2.6 — End-to-End Autonomous Goal Acceptance

必须按照子阶段顺序实施。

每个子阶段完成后：
1. 执行对应单元测试。
2. 执行必要的 PostgreSQL 集成测试。
3. 检查领域模型、状态迁移和事务边界。
4. 更新阶段进度。
5. 再进入下一子阶段。

遇到不影响核心架构的实现问题，应自主调查并修复，不要中途停止等待用户确认。

如果遇到无法安全解决的架构冲突、数据迁移风险、权限边界问题或外部副作用不确定性，应保留安全状态，记录问题及原因，不得通过降低安全要求来让测试通过。

最终要求完成全部可实现功能、测试和文档。不能将尚未实现的能力标记为完成。

---

# 一、当前系统基线

当前 Personal AI OS 已完成 Phase 0–11，以及 Phase 12.1 和 Phase 12.2.1–12.2.2。

已经存在的核心能力：

### 1. Core Architecture

- Go Core 模块化单体。
- Go 最低版本 1.25.0。
- PostgreSQL + pgvector。
- Goal-first 领域模型。
- Goal → Subgoal → Plan / Milestone → Task → Task Step → Agent / Tool → Execution → Evidence。
- Goal、Task 和 Execution 持久化。
- Audit / Outbox。
- API Key 认证和优雅关闭。

### 2. Execution Runtime

- 持久化 Execution Queue。
- Worker Pool。
- Lease / Heartbeat。
- Fencing Token。
- Checkpoint。
- Run / Step 幂等记录。
- 崩溃恢复。
- Execution 取消。
- 重启恢复。

### 3. Tool Fabric

- Tool Registry。
- Tool Gateway。
- Native Tools。
- MCP Manager。
- MCP Streamable HTTP。
- 受控 stdio。
- Policy Engine。
- Approval Manager。
- Tool Invocation 持久化。
- unknown 状态处理。
- 统一 Audit / Outbox。

### 4. Agent Runtime

- ModelAgent。
- OpenAI-compatible Tool Calling。
- Ollama Tool Calling。
- 结构化 Tool Call。
- Core 注入 Gateway。
- Agent 不直接执行工具。
- 工具结果通过 Gateway 重建。
- Goal 级 Tool Call Usage。
- Agent 最终答复标记为未验证。

### 5. Goal Evaluation

已完成：

- 确定性 Goal Evaluator。
- Evaluation 历史。
- Evaluation 与 Audit / Outbox 同事务持久化。
- 审批等待、外部条件、失败和阻塞识别。
- 未知进度返回 null。
- Goal Success Criteria 结构化模型。
- Criteria 版本管理。
- 用户确认来源由 Core 记录。
- 当前 Evaluation 新鲜度校验。
- Goal 完成门禁。
- 移除所有 Task 完成就自动完成 Goal 的逻辑。

当前支持的结构化 Criteria：

- verified_evidence_count
- verified_task_completion

当前尚未配置真正独立的生产 Artifact Verifier，因此 ModelAgent 输出不会被标记为已验证。

### 6. 当前阶段状态

Phase 12.2.1 和 12.2.2 已完成。

数据库目前已有 12 个 Migration，最新为：

0012_structured_goal_criteria.sql

实际实施前必须检查当前代码，不得假设上述实现与描述完全一致。

---

# 二、本阶段最终目标

将当前系统从：

能够执行 Task，并对 Goal 进行事实评估。

升级为：

能够围绕 Goal 持续执行以下闭环：

1. 评估 Goal 当前状态。
2. 检查每项成功标准。
3. 收集和验证真实 Evidence。
4. 判断哪些标准尚未满足。
5. 根据实际差距动态调整计划。
6. 将新计划交给现有执行系统。
7. 处理审批、失败、外部条件和 Worker 崩溃。
8. 重新评估 Goal。
9. 独立验证完成结果。
10. 只有所有必需标准真正满足，才完成 Goal。

最终自主循环：

```text
                  GOAL
                    |
                    v
             Goal Evaluator
                    |
                    v
             Supervisor
                    |
          +---------+---------+
          |                   |
          v                   v
    Goal Satisfied?       Goal Incomplete
          |                   |
          v                   v
    Result Verifier      Analyze Gap
          |                   |
          v                   v
    Verification       Dynamic Planner
          |                   |
     +----+----+              v
     |         |         Plan Proposal
     v         v              |
  Verified  Rejected          v
     |         |         Validate Plan
     v         +----------+   |
Complete Goal            |   v
                         | Commit Plan
                         |   |
                         |   v
                         | Task Manager
                         |   |
                         |   v
                         | Execution Manager
                         |   |
                         |   v
                         | Agent Runtime
                         |   |
                         |   v
                         | Tool Gateway
                         |   |
                         |   v
                         + Re-evaluate Goal
```

必须确保整个循环可持久化、可恢复、可审计、可停止。

---

# 三、不可违反的核心架构原则

## 3.1 Goal-first

Goal 不属于 Agent。

Task 不属于对话。

Plan 不属于一次模型调用。

Agent 不拥有 Goal 的最终状态。

所有核心对象必须能够跨对话和系统重启持续存在。

## 3.2 Core 是事实和权限的最终控制者

模型只能提出：

- 计划。
- Task。
- Tool Call。
- 结果解释。
- 验证建议。
- 重新规划建议。

模型不能：

- 直接修改 Goal 状态。
- 直接修改 Task 最终状态。
- 直接写入可信 Evidence。
- 直接声明 Evidence 已验证。
- 绕过 Policy。
- 绕过 Approval。
- 直接提交 Plan。
- 自行认定 Goal 完成。

所有重要状态变化由 Go Core 领域服务执行。

## 3.3 Evidence 与 Verification 必须分离

必须区分：

- Raw Evidence：原始证据。
- Evidence Metadata：证据元数据。
- Verification：验证记录。
- Verified Evidence：经过 Core 接受的验证结果。
- Evaluation：Goal 标准的整体评估结果。

Evidence 的存在不等于 Evidence 有效。

Evidence 有效不等于 Evidence 满足 Criterion。

Criterion 满足不等于 Goal 已完成。

Goal 完成必须由 Core 完成最终状态门禁。

## 3.4 不承诺无法保证的 Exactly-once

继续遵守现有执行系统原则：

- 数据库状态通过事务、幂等和 Fencing Token 保护。
- 外部副作用通过下游幂等、去重、对账和 unknown 状态处理。
- 不得把外部工具执行描述为天然 Exactly-once。
- unknown Invocation 不得简单转换为 failed 后自动重放。
- 重新连接 MCP Server 不代表之前的外部动作没有发生。

## 3.5 保持现有安全体系

权限等级继续保持：

- A0：读取、搜索、分析、查询。
- A1：创建笔记、整理知识、创建任务。
- A2：修改文件、发送消息、普通设备控制，根据策略决定是否审批。
- A3：付款、重要删除、门锁、安防、机器人高风险动作，必须人工审批。

Policy Engine 独立于模型。

Planner 不能提升权限。

Agent 不能直接决定审批通过。

重新规划后，动作参数发生实质变化必须重新审批。

---

# 四、Phase 12.2.3 — Independent Result Verifier

## 4.1 目标

建立真正独立于 ModelAgent 的验证系统。

验证器必须能够检查：

- Task 是否实际完成。
- Artifact 是否存在。
- Artifact 内容是否满足要求。
- Evidence 是否真实存在。
- Evidence 是否来源可信。
- Evidence 是否支持指定 Criterion。
- Evidence 是否过期。
- Evidence 是否与当前 Goal / Task / Criterion 版本匹配。

不能仅检查 Evidence ID 是否存在，也不能直接信任 Agent 的最终文本。

## 4.2 验证架构

建议实现：

```text
Evidence Collector
       |
       v
Evidence Repository
       |
       v
Verification Service
       |
       +---- Deterministic Verifier
       |
       +---- Artifact Verifier
       |
       +---- Schema Verifier
       |
       +---- External State Verifier
       |
       +---- Model-assisted Verifier (optional)
       |
       v
Verification Record
       |
       v
Core Evidence Acceptance
       |
       v
Goal Evaluator
```

先检查现有 Evidence 模型和存储能力。

如果已经具备相同能力，应扩展，不要重复建设。

## 4.3 Verifier 接口

建议设计统一接口：

```go
type Verifier interface {
    Type() string
    Version() string
    Supports(criterion SuccessCriterion) bool

    Verify(
        ctx context.Context,
        input VerificationInput,
    ) (VerificationResult, error)
}
```

具体类型和字段以当前项目 Domain 设计为准。

Verifier 必须通过 Core 的受控服务访问数据。

不得将任意数据库连接、Shell 或不受限制的文件系统访问直接注入模型。

## 4.4 第一批确定性 Verifier

至少实现以下能力。

### A. Evidence Count Verifier

对应：

verified_evidence_count

验证：

- Evidence 真实存在。
- Evidence 已通过 Core Verifier。
- Evidence 属于当前 Goal。
- Evidence 满足指定类型和来源要求。
- Evidence 未过期。
- 同一 Evidence 不得重复计数。
- 被撤销或失效的 Evidence 不得计入。

必须定义清楚 Count 的计算口径。

不能将原始 Tool Invocation 数量直接当成有效 Evidence 数量。

### B. Task Completion Verifier

对应：

verified_task_completion

验证：

- Task 真实存在。
- Task 属于当前 Goal。
- Task 状态为允许验证的最终状态。
- Execution 有合法记录。
- 必要 Evidence 已经存在。
- 相关 Verification 通过。
- Task 对应的验收条件已经满足。

不能只根据 Task status=succeeded 判定。

### C. Artifact Existence Verifier

验证指定 Artifact：

- 存在。
- 路径在授权范围内。
- 文件类型正确。
- 文件大小符合限制。
- 能够读取。
- Hash 可计算。
- Evidence 与 Artifact 关联正确。

### D. Artifact Content Verifier

针对允许的 Artifact 类型实现：

- JSON Schema 验证。
- Markdown 结构验证。
- 必需章节检查。
- 表格列检查。
- 引用字段检查。
- 指定字段和内容约束检查。

不要在本阶段实现任意复杂的自然语言真实性判断。

对于语义性内容，先返回 insufficient_evidence 或 inconclusive。

### E. Reference Verifier

验证报告引用：

- 引用记录存在。
- URL 或来源标识有效。
- 引用对应实际内容或抓取记录。
- 引用不是 Agent 凭空生成的。
- 必要时检查来源抓取时间。
- 能够将引用关联到报告中的对应部分。

不能仅因为 Markdown 中出现一个 URL 就认定来源有效。

如果当前 Knowledge 系统没有可靠的来源快照机制，应记录为限制，不要伪造验证能力。

## 4.5 Verification Result

统一结果至少包括：

- Verification ID
- Goal ID
- Task ID（可选）
- Criterion ID
- Goal Version
- Criterion Version
- Evidence IDs
- Evidence Hashes
- Verifier Type
- Verifier Version
- Verification Status
- Verification Reason
- Verified At
- Expires At（可选）
- Verification Metadata

状态建议：

- verified
- rejected
- insufficient_evidence
- inconclusive
- pending

只有 verified 且满足 Core 接受策略的结果才可以成为有效验证结果。

## 4.6 验证来源与信任等级

必须区分：

| 来源 | 信任规则 |
|---|---|
| Core 确定性规则 | 可以产生确定性验证 |
| Artifact Hash | 证明内容一致，不证明内容真实 |
| JSON Schema | 证明结构合法，不证明语义正确 |
| 外部系统状态查询 | 根据来源、权限和新鲜度评估 |
| 独立模型辅助验证 | 只能提供非确定性辅助结论 |
| 原 Agent 自我声明 | 不可信 |

模型辅助验证必须：

- 明确标记 model_assisted。
- 记录模型 Provider 和模型标识。
- 记录 Prompt Version。
- 记录输入 Evidence 引用。
- 保存结构化验证结果。
- 不得伪装成确定性验证。
- 不得单独满足需要确定性验证的 Criterion。

如果某个 Criterion 需要人工确认，应保留 pending 或转入人工审批流程。

## 4.7 Verification 新鲜度和失效

验证必须绑定：

- Goal Version。
- Criterion Version。
- Evidence Hash。
- Artifact Hash。
- Verifier Version。

如果 Criterion 修改、Artifact 内容变化或 Evidence 被撤销：

- 旧 Verification 保留历史。
- 旧 Verification 不再用于当前完成判定。
- 需要重新验证。

对于无法检测内容变化的外部资源，必须明确设置有效期或要求重新查询。

不能无限期复用旧验证。

## 4.8 完成门禁

Goal Completion Gate 必须使用 Core 持久化的可信 Verification。

禁止：

- Agent 自报 verified。
- HTTP 调用者提交自然语言证明。
- Planner 直接将 Criterion 标记为满足。
- 使用已过期 Evaluation。
- 使用已过期 Verification。
- 使用其他 Goal 的 Evidence。
- 使用其他版本的 Criterion 验证结果。

验证通过后重新执行 Goal Evaluation。

只有当前 Evaluation 中所有 Required Criteria 均满足，且完成门禁检查通过，才允许 Goal 完成。

## 4.9 测试要求

至少覆盖：

- Evidence 不存在。
- Evidence 重复。
- Evidence 不属于当前 Goal。
- Evidence 未通过验证。
- Evidence 已撤销。
- Evidence 过期。
- Artifact 不存在。
- Artifact 路径越权。
- Artifact Hash 变化。
- JSON Schema 不符合。
- 必需章节缺失。
- 引用记录不存在。
- 引用与内容不匹配。
- Criterion Version 变化。
- Goal Version 变化。
- Verifier Version 变化。
- 模型辅助验证不能冒充确定性验证。
- 原 Agent 声明 verified 被拒绝。
- Verification 历史不可篡改。
- Goal Completion Gate 拒绝无效验证结果。

---

# 五、Phase 12.2.4 — Dynamic Planner

## 5.1 目标

建立基于真实 Goal Evaluation 和 Verification 结果的动态规划系统。

Planner 不再只是根据 Goal 生成一次完整 Task 列表。

它必须能够：

- 生成初始计划。
- 识别未满足的 Success Criteria。
- 分析失败和阻塞原因。
- 生成补充 Task。
- 替换不再适用的 Task。
- 调整未执行 Task。
- 修改依赖关系。
- 保留已完成 Task。
- 根据新 Evidence 更新计划。
- 产生可追溯的 Plan Version。

## 5.2 Plan Version

实现版本化 Plan。

每个 Plan Version 至少包括：

- Plan ID
- Goal ID
- Version
- Parent Version
- Status
- Planning Reason
- Trigger Type
- Evaluation ID
- Verification IDs
- Created At
- Created By

每次被接受的计划变更必须创建新版本。

不得覆盖历史计划。

## 5.3 Plan Proposal

Planner 输出提案，不直接提交。

建议结构：

```text
PlanProposal
  ├── Proposal ID
  ├── Goal ID
  ├── Base Plan Version
  ├── Proposed Version
  ├── Trigger
  ├── Reason
  ├── Added Tasks
  ├── Modified Tasks
  ├── Retained Tasks
  ├── Cancelled Tasks
  ├── Dependency Changes
  ├── Criteria Mapping
  ├── Estimated Budget
  ├── Required Permissions
  └── Created At
```

Proposal 状态：

- proposed
- accepted
- rejected
- expired
- conflict

必须保存所有提案及其接受、拒绝或冲突历史。

## 5.4 计划变更规则

### 已完成 Task

- 默认保留。
- 不得重新执行。
- 不得删除原 Execution。
- 不得删除原 Evidence。
- 如果 Evidence 过期，新增验证 Task。
- 如果外部事实发生变化，应明确创建新的补充 Task。

### 运行中 Task

- 不得直接删除。
- 检查 Execution、Lease 和 Invocation 状态。
- 必要时调用现有取消机制。
- 有外部副作用可能性的 Task 必须先处理 unknown。
- 取消失败时不能假定执行已经停止。

### 失败 Task

根据失败原因决定：

- 重试。
- 替换。
- 增加补偿 Task。
- 转人工处理。
- 停止 Goal。

必须使用现有重试预算。

### 等待审批 Task

- 保留原审批状态。
- 不允许用旧审批授权修改后的动作。
- 参数实质变化必须重新审批。

### 等待外部条件 Task

- 保留等待条件。
- 不得无限创建重复等待 Task。
- 条件满足后通过事件触发重新评估。

## 5.5 Criteria Mapping

每个 Task 应尽可能关联一个或多个 Success Criteria。

Planner 必须解释：

- 这个 Task 为什么存在。
- 它要满足哪个 Criterion。
- 它需要什么 Evidence。
- 它的成功条件是什么。
- 它是否有外部副作用。
- 它需要什么权限。
- 它预计消耗多少预算。

不要创建与 Goal 成功标准没有关系的无意义 Task。

## 5.6 DAG 校验

新 Plan 必须通过 Core DAG 校验：

- 无循环依赖。
- 依赖节点存在。
- 依赖关系合法。
- Task 状态迁移合法。
- 不会重新执行已完成 Task。
- 不会破坏运行中 Task。
- 不会丢失历史 Evidence。
- 不会绕过审批。
- 不会超过预算。
- 不会产生权限升级。

## 5.7 Plan Commit

由 Core 负责接受和提交 Proposal。

提交步骤：

1. 锁定或版本校验 Goal。
2. 检查 Base Plan Version。
3. 检查 Goal 当前状态。
4. 检查 Proposal 是否过期。
5. 校验 DAG。
6. 校验 Task 状态。
7. 校验 Criteria Mapping。
8. 校验预算。
9. 校验权限。
10. 保存新 Plan Version。
11. 保存 Plan Diff。
12. 保存 Task 变更。
13. 写入 Audit。
14. 写入 Outbox。
15. 提交事务。

必须处理并发 Planner：

如果 Base Plan Version 已改变，不得覆盖新版本。

旧 Proposal 应标记 conflict 或要求重新规划。

## 5.8 模型参与规划的边界

模型可以生成结构化 Plan Proposal。

Core 必须验证：

- JSON Schema。
- Task 数量。
- 依赖 DAG。
- Criteria Mapping。
- 权限。
- 预算。
- Task 类型。
- 资源约束。

模型不能生成可直接执行的任意 Shell 命令。

模型不能绕过 Tool Gateway。

模型不能修改数据库事实。

---

# 六、Phase 12.2.5 — Supervisor Controller

## 6.1 目标

建立真正的 Goal-driven Supervisor Decision Loop。

当前 Supervisor 如果只负责提交 Execution 或查询状态，应扩展其能力，不要创建第二套并行 Supervisor。

Supervisor 负责：

- 读取 Goal。
- 读取当前 Plan。
- 运行 Goal Evaluator。
- 根据 Evaluation 决定下一步。
- 调用 Verifier。
- 调用 Dynamic Planner。
- 提交合格 Plan。
- 选择 Ready Tasks。
- 通过现有 Execution Manager 执行。
- 等待审批和外部条件。
- 恢复未完成的自主循环。

Supervisor 是决策编排器，不是工具执行器。

## 6.2 统一决策状态

建议支持：

- continue_execution
- replan
- verify_completion
- wait_approval
- wait_condition
- budget_exhausted
- stop
- escalate

状态转换必须有明确条件。

不允许模型自行设置状态。

## 6.3 决策流程

```text
Load Goal
    |
Load Current Plan
    |
Evaluate Goal
    |
    +-- Required Criteria Missing
    |       |
    |       v
    |   Analyze Gap
    |       |
    |       v
    |   Dynamic Planner
    |
    +-- Failed Task
    |       |
    |       v
    |   Analyze Failure
    |       |
    |       v
    |   Replan or Retry
    |
    +-- Pending Approval
    |       |
    |       v
    |   Wait Approval Event
    |
    +-- Pending External Condition
    |       |
    |       v
    |   Wait Condition Event
    |
    +-- All Required Criteria Potentially Satisfied
            |
            v
       Result Verifier
            |
       +----+----+
       |         |
       v         v
    Verified  Insufficient
       |         |
       v         v
  Complete Goal Replan / Continue
```

注意：

Evaluation 中的状态只能作为 Supervisor 决策输入。

最终状态变更必须由 Core Domain Service 执行。

## 6.4 持久化 Supervisor Decision

每轮决策必须持久化。

至少记录：

- Decision ID
- Goal ID
- Goal Version
- Plan Version
- Evaluation ID
- Decision Type
- Decision Reason
- Proposal ID
- Verification IDs
- Related Execution IDs
- Decision Status
- Created At
- Completed At

决策必须支持：

- 幂等。
- 查询。
- 审计。
- 恢复。
- 并发保护。
- 超时处理。

不要把 Supervisor 的全部状态只放在内存中。

## 6.5 持久化自主循环

Supervisor 必须有可恢复的后台工作机制。

优先复用现有 PostgreSQL 持久化队列、Worker 和 Outbox。

不应在 HTTP 请求中无限同步执行整个 Goal。

建议使用：

```text
Goal Created / Resumed
        |
        v
Supervisor Job
        |
        v
Decision Cycle
        |
        v
Persist Decision
        |
        +---- Execute Tasks
        |
        +---- Replan
        |
        +---- Verify
        |
        +---- Wait Event
        |
        +---- Stop
```

每个决策循环都应该有边界。

需要新的外部事件时，保存等待状态并释放 Worker，不要占用 Worker 无限等待。

事件发生后，通过 Outbox / Event Handler 恢复对应 Goal。

## 6.6 执行预算

复用现有 Goal Tool Usage。

增加或完善 Core 强制执行的预算：

- 最大 Supervisor Decision 次数。
- 最大 Replanning 次数。
- 最大 Task 数量。
- 最大模型调用次数。
- 最大 Tool Call 次数。
- 最大 Token 数量。
- 最大执行时间。
- 最大失败重试次数。

预算不能只由模型自觉遵守。

预算配置必须有默认值、上限和持久化记录。

达到预算时：

- 停止自动执行。
- 持久化原因。
- 保留现有状态。
- 输出可解释原因。
- 允许授权用户调整预算后恢复。

## 6.7 防止无限循环

必须实现：

- 最大决策轮数。
- 最大重规划次数。
- 相同 Evaluation 不重复生成相同 Proposal。
- 相同 Proposal 不重复提交。
- 相同失败原因不能无限重试。
- 没有新事实时不能无限调用模型。
- 连续无进展时进入 stop 或 escalate。

可以记录 Goal Progress Fingerprint：

由当前 Goal Version、Plan Version、Criteria 状态、Task 状态、Evidence 状态和预算状态构成。

如果连续多个决策周期 Fingerprint 不变，应停止无意义循环。

## 6.8 并发和恢复

必须考虑：

- 同一个 Goal 被多个 Worker 同时处理。
- Supervisor Worker 崩溃。
- Core 重启。
- Lease 过期。
- 旧 Worker 恢复。
- Planner 并发生成 Proposal。
- Evaluation 并发执行。
- Approval 在等待期间通过。
- Tool Invocation 在恢复时处于 unknown。

使用现有 Lease、Fencing Token、事务、幂等和版本控制。

不能只依靠进程内 Mutex 保证跨实例正确性。

---

# 七、Phase 12.2.6 — End-to-End Autonomous Goal Acceptance

这是本阶段最终验收。

必须使用真实 PostgreSQL。

不能只通过 Mock Repository 完成端到端测试。

## 7.1 验收 Goal

建立一个完整的技术研究 Goal：

收集指定技术主题的资料，比较至少三个方案，生成一份具有可追溯引用的技术报告。

Success Criteria：

1. 至少收集 5 个符合要求的有效来源。
2. 至少比较 3 个技术方案。
3. 比较维度完整。
4. 每个主要结论都有来源引用。
5. 报告 Artifact 实际存在。
6. 报告结构符合要求。
7. 所有必需 Criterion 均有有效 Verification。
8. 报告最终版本与验证时的 Artifact Hash 一致。

对于引用真实性，应使用真实抓取记录、来源快照或现有 Knowledge 证据。

如果当前系统没有真实外部来源抓取能力，可以使用测试专用 MCP Server / Native Tool 提供可验证来源快照。

不能通过随意构造 URL 来伪造来源真实性。

## 7.2 故障注入场景

必须按以下流程测试：

### 第一阶段：初始规划

1. 创建 Goal。
2. 创建结构化 Success Criteria。
3. 用户确认 Criteria。
4. Supervisor 启动。
5. Evaluator 识别 Goal 尚未满足。
6. Planner 创建初始 Plan。
7. Core 校验并提交 Plan。
8. Supervisor 提交 Ready Tasks。

### 第二阶段：正常执行

9. 第一个工具调用成功。
10. 收集真实 Evidence。
11. Core Verifier 验证 Evidence。
12. 对应 Task 完成。
13. 第二个工具调用失败。
14. Evaluator 识别失败和缺失标准。

### 第三阶段：动态重新规划

15. Planner 分析失败原因。
16. 创建新的 Plan Proposal。
17. 新 Proposal 保留已经完成的 Task。
18. 不重复执行已经完成的外部副作用。
19. 增加补充 Task。
20. Core 校验 DAG、权限和预算。
21. 提交新的 Plan Version。

### 第四阶段：审批

22. 新 Task 需要 A2 或 A3 权限。
23. Gateway 创建审批请求。
24. Supervisor 进入 wait_approval。
25. 审批通过。
26. 原 Execution 使用原参数和幂等键恢复。
27. 参数变化时必须重新审批。
28. 验证旧审批不能授权新动作。

### 第五阶段：崩溃恢复

29. Worker 在工具执行过程中崩溃。
30. 使 Lease 过期。
31. 旧 Worker 的写入被 Fencing Token 拒绝。
32. 重新启动 Go Core。
33. Supervisor 自动恢复。
34. 正确读取 Execution 和 Invocation 状态。
35. 对 unknown Invocation 进行安全处理。
36. 不得盲目重放可能已发生的外部副作用。

### 第六阶段：Artifact 生成

37. Agent 生成最终报告。
38. Agent 声称 Goal 已经完成。
39. Core 不接受 Agent 自报完成。
40. Artifact Verifier 检查报告是否存在。
41. 检查 Artifact Hash。
42. 检查 Markdown 结构。
43. 检查至少三个方案。
44. 检查必需比较维度。
45. 检查来源引用关联。
46. 检查引用是否有真实来源快照。

### 第七阶段：验证失败与补充规划

47. 故意使某项 Criterion 缺少 Evidence。
48. Verifier 返回 insufficient_evidence。
49. Goal 保持未完成。
50. Evaluator 识别未满足标准。
51. Planner 创建补充 Task。
52. 执行补充 Task。
53. 收集新 Evidence。
54. 重新执行 Verifier。

### 第八阶段：最终完成

55. 所有必需 Criterion 均验证通过。
56. Evaluation 重新生成。
57. 检查 Evaluation 与 Goal / Criterion / Evidence 版本一致。
58. Core Completion Gate 验证全部条件。
59. Goal 完成。
60. 查询 Goal、Task、Plan、Execution、Evidence、Verification、Evaluation、Decision、Audit 和 Outbox。
61. 确认数据一致。
62. 重启 Go Core。
63. 确认 Goal 完成状态和全部历史记录仍然存在。

## 7.3 必须验证的异常情况

另外测试：

- 缺失 Evidence。
- 重复 Evidence。
- 过期 Evidence。
- Artifact 内容变化。
- Criterion Version 变化。
- Goal Version 变化。
- Verification 失效。
- 不存在的 Evidence ID。
- 不可信 Agent 自报结果。
- 矛盾 Evidence。
- Plan DAG 循环。
- Plan Version 冲突。
- 旧 Proposal 提交。
- 并发 Supervisor。
- 并发 Planner。
- Worker 崩溃。
- Core 重启。
- Approval 等待。
- unknown Invocation。
- 预算耗尽。
- 最大重规划次数。
- 无进展循环。
- Goal 用户取消。
- 外部服务长期不可用。

---

# 八、数据库和 API 要求

开始前先检查现有 Schema。

不要机械地为每个概念创建一张新表。

优先复用现有：

- goals
- tasks
- executions
- evidence
- approvals
- tool_invocations
- goal_evaluations
- goal_success_criteria
- audit
- outbox

如果需要新增逻辑实体，可以考虑：

- verifications
- verification_evidence
- plans
- plan_versions
- plan_proposals
- plan_changes
- supervisor_decisions
- supervisor_jobs
- goal_budgets

具体以现有数据库设计为准。

要求：

- 外键完整。
- 唯一约束合理。
- 状态约束明确。
- 并发写入安全。
- 事务边界正确。
- 历史记录保留。
- Migration 失败可以回滚。
- Migration 不破坏已有数据。
- Migration 重复执行行为正确。

API 只暴露真正需要的管理能力。

可能需要：

### Verification

- 创建或触发验证。
- 查询 Verification。
- 查询 Criterion 验证状态。

### Plan

- 查询当前 Plan。
- 查询 Plan Version。
- 查询 Plan Proposal。
- 接受或拒绝 Proposal。

### Supervisor

- 查询当前决策状态。
- 查询决策历史。
- 启动或恢复 Goal。
- 暂停或取消 Goal。
- 手动触发重新评估。

### Budget

- 查询 Goal Budget。
- 在授权下调整预算。

所有 API 必须使用现有认证和权限机制。

更新 OpenAPI。

不要创建可以让调用者直接提交可信 Verification 或绕过 Core 完成门禁的接口。

---

# 九、技术和工程约束

1. 保持 Go 1.25.0 最低版本。
2. 不随意升级工具链。
3. 继续使用 PostgreSQL。
4. 继续使用现有 Repository 和 Domain Service 模式。
5. 继续使用现有 Execution Manager。
6. 继续使用现有 Worker Pool。
7. 继续使用现有 Tool Gateway。
8. 继续使用现有 Policy Engine 和 Approval Manager。
9. 继续使用现有 ModelAgent。
10. 不重写 MCP Manager。
11. 不引入第二套 Task Engine。
12. 不引入 Temporal。
13. 不为了本阶段引入新的 Agent Framework。
14. 不提前实现 Multi-Agent。
15. 不将 Agent Framework 类型泄漏进 Core Domain。
16. 不把所有逻辑堆进 Supervisor。
17. 不使用进程内状态代替持久化状态。
18. 不使用模型输出代替数据库事实。
19. 不以降低安全策略的方式修复测试。
20. 保持 ARM64 构建兼容性。

推荐继续使用：

- Go：Core Domain、Verifier、Planner、Supervisor、Policy。
- PostgreSQL：持久化状态、历史和队列。
- Python：未来可选的专用 AI / 多模态服务，不在本阶段强行引入。
- OpenTelemetry：观测和指标。

---

# 十、可观测性

增加必要的结构化日志和 OpenTelemetry 指标。

至少覆盖：

### Verification

- Verification Count。
- Verification Duration。
- Verification Success Rate。
- Verification Rejection Count。
- Insufficient Evidence Count。
- Stale Verification Count。

### Planning

- Plan Proposal Count。
- Plan Commit Count。
- Plan Conflict Count。
- Replanning Count。
- Retained Task Count。
- Added Task Count。
- Rejected Proposal Count。

### Supervisor

- Decision Count。
- Decision Cycle Duration。
- Goal Progress Duration。
- Goal Completion Duration。
- Blocked Goal Count。
- Budget Exhaustion Count。
- No Progress Stop Count。
- Recovery Count。

日志不得包含：

- API Key。
- OAuth Token。
- MCP 凭据。
- 敏感文件内容。
- 不必要的个人数据。

---

# 十一、最终测试要求

每个子阶段必须执行相应测试。

最终必须执行：

```bash
make fmt
make test
make build
make vet
go test -race fovvdk.
make build-arm64
make test-integration
```

另外执行：

- Compose 配置校验。
- YAML 校验。
- OpenAPI 校验。
- Migration 顺序和 checksum 检查。
- Migration 失败回滚测试。
- 真实 PostgreSQL 集成测试。
- Worker 崩溃恢复。
- Go Core 重启恢复。
- 并发测试。
- ARM64 构建。

CI 应继续覆盖精确 Go 1.25.0。

如果 CI 云端尚未运行，不得声称云端验收已经通过。

不得删除原有测试来规避失败。

不得将只有 Mock 的测试描述成真实 PostgreSQL 集成测试。

---

# 十二、文档交付

完成后更新：

- IMPLEMENTATION_STATUS.md
- PHASE12_PROGRESS.md
- TEST_REPORT.md
- ARCHITECTURE.md
- README.md
- OpenAPI 文档
- 必要的数据库设计文档

建议新增：

- VERIFICATION_DESIGN.md
- DYNAMIC_PLANNING_DESIGN.md
- SUPERVISOR_DESIGN.md

报告必须明确区分：

- 已实现。
- 单元测试通过。
- 真实 PostgreSQL 集成测试通过。
- 故障注入通过。
- Worker 恢复通过。
- Core 重启恢复通过。
- ARM64 构建通过。
- CI 已配置。
- CI 云端实际通过。
- 尚未实现。
- 已知限制。

不能仅写一句“所有测试通过”。

必须说明关键测试覆盖的实际行为。

---

# 十三、最终验收标准

Phase 12.2.3–12.2.6 只有满足以下条件才能标记完成：

## Verifier

1. 存在独立于 ModelAgent 的 Verification Service。
2. 至少实现 Evidence Count、Task Completion、Artifact Existence、Artifact Content 的确定性验证。
3. 支持来源引用验证，或者明确记录当前能力限制。
4. Verification 绑定 Goal、Criterion 和 Evidence 版本。
5. 过期验证不能用于 Goal 完成。
6. Agent 自报 verified 不被信任。
7. Verification 历史可追溯。

## Dynamic Planner

8. 支持 Plan Version。
9. 支持 Plan Proposal。
10. 支持增量重新规划。
11. 已完成 Task 不被无条件重复执行。
12. 不破坏历史 Execution 和 Evidence。
13. 新 Plan 通过 DAG、权限和预算检查。
14. 并发 Proposal 不会互相覆盖。

## Supervisor

15. 能够消费 Goal Evaluation。
16. 能够调用 Result Verifier。
17. 能够调用 Dynamic Planner。
18. 能够通过现有 Execution Manager 推进 Task。
19. 能够处理 Approval 和外部等待。
20. 决策状态持久化。
21. 能够跨 Worker 和 Core 重启恢复。
22. 预算由 Core 强制执行。
23. 无进展时能够停止。
24. 不会无限调用模型。

## End-to-End

25. 真实 PostgreSQL 端到端测试通过。
26. 失败后可以动态重新规划。
27. 已完成 Task 不重复执行。
28. 审批参数绑定有效。
29. unknown Invocation 安全恢复。
30. Artifact 可以被独立验证。
31. Evidence 不足时 Goal 不完成。
32. 所有必需 Criterion 通过后 Goal 才完成。
33. Core 重启后全部状态正确恢复。
34. Audit / Outbox 完整。
35. 原有测试全部通过。

---

# 十四、实施完成后的系统能力目标

本阶段完成后，系统应具备以下能力：

用户提出一个长期 Goal。

系统能够：

1. 理解 Goal 的成功标准。
2. 将 Goal 分解为可执行 Task。
3. 生成初始计划。
4. 自主执行 Task。
5. 调用 Native Tool 和 MCP Tool。
6. 收集执行结果和 Evidence。
7. 验证实际产物。
8. 发现缺失和失败。
9. 动态调整计划。
10. 保留已经完成的工作。
11. 请求必要的人工审批。
12. 在系统重启后恢复执行。
13. 独立验证最终结果。
14. 判断 Goal 是否真正完成。

整个过程由 Go Core 控制状态、权限、预算和事实记录。

模型负责推理和提出建议，不负责决定系统是否有权执行，也不负责自行宣布成功。

**本阶段完成后，单 Goal 的自主执行闭环才算真正建立。**

---

# 十五、执行策略

现在开始：

1. 检查完整代码库和当前分支状态。
2. 检查 Phase 12.2.1、12.2.2 的实际实现。
3. 检查 Goal、Task、Evidence、Evaluation、ModelAgent、Supervisor、Execution Manager 和 Tool Gateway。
4. 输出简短差距分析。
5. 立即开始 Phase 12.2.3。
6. 按 12.2.3 → 12.2.4 → 12.2.5 → 12.2.6 顺序连续实施。
7. 每个阶段完成后测试并修复。
8. 不需要等待用户逐阶段批准。
9. 完成全部可实现内容后执行最终验收。
10. 更新所有报告和架构文档。

最终交付时，提供：

- 实际完成的功能清单。
- 新增和修改的核心文件。
- Migration 清单。
- API 变化。
- 关键架构决策。
- 测试命令及实际结果。
- 故障注入结果。
- 已知限制。
- 尚未通过的 CI 项目。
- Phase 12.2 最终完成状态。

如果某项能力因为缺少外部基础设施而无法真实实现，必须明确说明，不能用 Mock 结果冒充生产能力。

**最终目标不是让模型看起来能够自主工作，而是让系统真正具备可验证、可恢复、受权限约束的 Goal-driven Autonomy。**