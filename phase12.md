# Personal AI OS — Phase 12.2
# Goal Evaluation & Dynamic Replanning

## 一、阶段目标

在现有 Phase 0–12.1 基础上，实现 Goal 驱动的进度评估、独立结果验证、动态重新规划和 Supervisor 决策控制。

当前系统已经具备：

- Goal / Subgoal / Task 持久化与生命周期管理。
- Task DAG、依赖关系和执行记录。
- PostgreSQL 持久化执行队列、Lease、Heartbeat、Fencing Token、Checkpoint 和 Worker 恢复。
- Policy Engine、Approval Manager、Tool Registry、Tool Gateway。
- MCP、Native Tool 统一工具执行链路。
- ModelAgent 结构化 Tool Call 往返。
- Goal 级 Tool Call 用量持久化。
- 工具结果通过 Gateway 重建，不将工具执行事实写入 Agent Checkpoint。
- 模型最终答复标记为未验证，不能直接完成 Task 或 Goal。

本阶段需要解决：

1. 系统如何判断 Goal 当前真实进度？
2. 如何判断一个 Task 的执行结果是否真正满足要求？
3. 如何判断 Evidence 是否足以支持完成结论？
4. 当执行失败、环境变化或原计划不再适用时，如何动态重新规划？
5. 如何避免重新规划导致已完成 Task 被重复执行？
6. 如何让 Supervisor 在继续执行、重新规划、等待审批、等待条件、验证完成和停止之间自主决策？

最终目标：

让 Supervisor 能够围绕长期 Goal，持续评估实际状态、调整计划、验证结果，并在授权范围内继续推进任务。

---

## 二、必须遵守的架构原则

### 2.1 Goal-first 架构不可改变

核心层级保持：

Goal → Subgoal → Plan / Milestone → Task → Task Step → Agent / Tool → Execution → Evidence

不得将 Goal 归属到某个 Agent。

不得将 Task 归属到某次对话。

不得让 Agent 成为 Goal 或 Task 的最终状态管理者。

Goal、Task、Plan 和 Execution 必须能够跨对话、跨 Worker、跨 Go Core 重启持续存在。

### 2.2 模型不是事实来源

必须严格区分：

- Model Output：模型生成的内容。
- Agent Result：Agent 提交的执行结果。
- Evidence：可追溯的结果证据。
- Verification Result：独立验证结论。
- Task Completion：Core 确认的 Task 完成状态。
- Goal Completion：Core 确认的 Goal 完成状态。

任何模型输出都不能直接改变 Task 或 Goal 的最终完成状态。

模型声称完成、Planner 认为完成、Agent 返回 success，都不构成完成依据。

### 2.3 Policy Engine 独立于模型

继续遵守现有权限模型：

- A0：读取、搜索、分析、查询。
- A1：创建笔记、整理知识、创建任务。
- A2：修改文件、发送消息、普通设备控制，根据授权策略决定是否审批。
- A3：付款、重要删除、门锁、安防、机器人高风险动作，必须人工审批。

本阶段不得修改权限等级的基本含义。

Planner 可以提出动作，但不能授予动作权限。

重新规划不能提高工具权限，也不能绕过审批。

审批必须绑定具体动作和参数。实质性参数变化必须重新审批。

### 2.4 事实状态与决策状态分离

PostgreSQL 中已经持久化的 Goal、Task、Execution、Evidence、Approval 和 Tool Invocation 是事实来源。

Evaluator 和 Planner 的输出是决策建议，不是事实状态。

所有状态变化必须经过 Go Core 的领域服务、事务和状态机校验。

不得让模型直接生成 SQL 或直接修改数据库。

### 2.5 不破坏现有执行系统

优先复用：

- Goal / Task Domain。
- Supervisor。
- Planner。
- ModelAgent。
- Execution Manager。
- Worker Pool。
- Tool Gateway。
- Policy Engine。
- Approval Manager。
- Evidence。
- Audit / Outbox。
- Goal Tool Usage。

不要另建一套并行的 Task Engine、工具执行系统或审批机制。

不要把现有 Go Core 重构成微服务。

---

## 三、Phase 12.2 总体模块

本阶段实现以下四个核心模块：

1. Goal Evaluator
2. Result Verifier
3. Dynamic Planner
4. Supervisor Controller

推荐职责边界：

### 3.1 Goal Evaluator

负责评估 Goal 当前状态：

- 成功标准是否满足。
- Subgoal 完成情况。
- Task 完成情况。
- 当前阻塞项。
- Evidence 完整性。
- 执行预算使用情况。
- 当前计划是否仍然有效。
- 是否需要重新规划。

Evaluator 不负责执行工具，不负责直接完成 Goal。

### 3.2 Result Verifier

负责验证 Task 和 Goal 的结果：

- Evidence 是否存在。
- Evidence 是否关联正确的 Goal / Task。
- Evidence 是否可追溯。
- Evidence 是否满足对应的验收标准。
- Evidence 是否过期或已经失效。
- 是否存在矛盾证据。
- 验证方法是否足够独立。

Verifier 不应该简单调用原 Agent 并让其再次确认自己是否正确。

优先使用可确定性验证的方式，例如：

- 文件是否存在及其内容校验。
- 数据库记录是否符合预期。
- 工具返回的结构化数据是否符合 Schema。
- HTTP 状态和响应内容是否满足约束。
- 任务输出是否符合明确的验收规则。
- 多个独立来源之间是否一致。

对于无法确定性验证的复杂语义结果，可以使用独立模型评估，但必须标记为模型辅助验证，不能将其等同于确定性证明。

### 3.3 Dynamic Planner

负责根据当前 Goal 状态生成或调整计划：

- 创建初始计划。
- 识别计划不可执行的原因。
- 根据执行结果调整 Task DAG。
- 增加必要的 Task。
- 调整尚未执行的 Task。
- 处理失败 Task。
- 处理新增条件和新 Evidence。
- 保留已经完成的 Task。
- 生成新的 Plan Version。

Planner 只生成计划提案，不直接修改持久化执行事实。

### 3.4 Supervisor Controller

负责组织自主决策循环：

- 加载 Goal 和当前计划。
- 调用 Evaluator。
- 根据评估结果调用 Planner 或 Verifier。
- 决定继续执行、重新规划、等待审批、等待外部条件、验证完成或停止。
- 将决策持久化。
- 将符合条件的 Task 提交给现有 Execution Manager。

Supervisor 不应绕过现有 Worker、Execution Manager 和 Tool Gateway。

---

## 四、第一部分：Goal 成功标准模型

首先检查现有 Goal 数据结构。

如果当前已经存在 Goal Success Criteria 或等价字段，优先扩展现有模型，不要重复创建。

如果不存在，则增加明确的结构化成功标准。

建议支持：

- Criterion ID
- Name
- Description
- Criterion Type
- Required
- Validation Method
- Expected Value / Constraint
- Evidence Requirements
- Created At
- Version

成功标准必须能够持久化，并且可以被 Evaluator 和 Verifier 读取。

需要区分：

- 必须满足的标准。
- 可选标准。
- 仅供参考的条件。

Goal 的成功条件必须明确。

不能只依靠 Goal 的自然语言描述，让模型自行决定何时完成。

如果用户创建 Goal 时没有明确成功标准：

- 可以由 Planner 生成成功标准提案。
- 必须记录来源为模型生成。
- 不能自动视为用户确认。
- 对于重要 Goal，应通过现有审批或确认机制取得用户认可。
- 在标准尚未明确时，不得自动完成 Goal。

---

## 五、第二部分：Goal Evaluator

新增独立的 Goal Evaluator。

建议设计为：

EvaluateGoal(ctx, goalID) → EvaluationResult

EvaluationResult 至少包含：

- Goal ID
- Evaluation ID
- Goal Version
- Plan Version
- Evaluation Status
- Completion Percentage（如果能够可靠计算）
- Completed Criteria
- Unmet Criteria
- Blocked Tasks
- Failed Tasks
- Pending Approvals
- Pending External Conditions
- Evidence Summary
- Budget Summary
- Recommended Action
- Reason
- Created At

Recommended Action 建议支持：

- continue
- replan
- wait_approval
- wait_condition
- verify_completion
- stop
- escalate

注意：

Recommended Action 只是 Evaluator 的建议，最终由 Supervisor 根据状态机和 Policy 决定。

### 5.1 进度评估原则

不能简单使用已完成 Task 数量 / 总 Task 数量作为 Goal 完成百分比。

原因：

- Task 的重要性可能不同。
- 某些 Task 是可选的。
- 某些 Task 只是辅助步骤。
- 某些 Task 完成并不意味着成功标准满足。

应优先根据 Success Criteria 和 Evidence 计算完成情况。

如果当前没有足够信息计算百分比，应允许返回 unknown，而不是生成虚假的精确百分比。

### 5.2 识别阻塞状态

Evaluator 至少应识别：

- 依赖 Task 未完成。
- Task 执行失败。
- Approval 等待中。
- Execution Lease 超时。
- 外部服务不可用。
- 工具调用处于 unknown。
- 预算不足。
- 成功标准缺少 Evidence。
- 当前 Plan 无法继续。

Evaluator 必须区分暂时阻塞和永久失败。

---

## 六、第三部分：Result Verifier

新增独立的 Result Verifier。

建议设计为：

VerifyTask(ctx, taskID) → VerificationResult

VerifyGoal(ctx, goalID) → VerificationResult

VerificationResult 至少包含：

- Verification ID
- Target Type
- Target ID
- Verification Status
- Criteria Results
- Evidence References
- Verification Method
- Verification Source
- Confidence（仅在有合理定义时使用）
- Failure Reasons
- Created At

Verification Status 建议：

- verified
- rejected
- insufficient_evidence
- inconclusive
- pending

### 6.1 验证约束

1. 不能仅根据 Agent 最终文本认定成功。
2. 不能仅根据 Task 状态为 succeeded 认定 Goal 完成。
3. 必须验证 Evidence 与成功标准之间的关联。
4. Evidence 必须具有来源和关联关系。
5. 必须识别过期 Evidence。
6. 必须允许多个 Evidence 支持一个 Criterion。
7. 必须允许一个 Evidence 支持多个 Criterion。
8. 必须处理矛盾 Evidence。
9. 无法验证时保持未完成状态。
10. Verifier 不得直接绕过 Goal / Task 状态机。

对于模型辅助验证：

- 使用独立的验证上下文。
- 尽量避免将原 Agent 的结论直接作为验证依据。
- 保存验证模型、验证提示版本、输入 Evidence 引用和结构化结果。
- 明确标记 model_assisted。
- 不能将模型置信度直接当作事实证明。

### 6.2 Evidence 不可伪造

Verifier 只能引用系统中真实存在的 Evidence。

不得允许模型凭空生成 Evidence ID。

不得允许模型通过自然语言声明某个外部操作已经完成。

对于外部副作用，必要时通过实际状态查询、下游系统对账或人工确认验证。

---

## 七、第四部分：Dynamic Planner

实现动态计划生成和重新规划。

建议设计为：

GeneratePlan(ctx, goalID) → PlanProposal

Replan(ctx, goalID, evaluationID) → PlanProposal

PlanProposal 至少包含：

- Proposal ID
- Goal ID
- Base Plan Version
- New Plan Version
- Reason
- Trigger
- Added Tasks
- Modified Tasks
- Retained Tasks
- Removed Tasks
- Dependency Changes
- Estimated Budget
- Required Permissions
- Success Criteria Mapping
- Created At

### 7.1 Plan Version

每次计划变更必须产生新的 Plan Version。

禁止直接覆盖历史计划。

需要保存：

- 原始计划。
- 每次重新规划的提案。
- 提案被接受或拒绝的记录。
- 计划版本之间的差异。
- 计划变更原因。
- 变更关联的 Evaluation ID。

### 7.2 Task 保留规则

这是本阶段最重要的约束之一。

重新规划时：

**已完成 Task：**

- 默认保留。
- 不得因为重新规划自动重新执行。
- 不得删除原有 Execution 和 Evidence。
- 如果相关 Evidence 已失效，可以新增验证 Task，而不是篡改历史记录。

**运行中的 Task：**

- 不得直接删除。
- 必须检查当前 Execution 状态。
- 必要时通过现有取消机制处理。
- 对存在外部副作用可能性的 Task，必须先处理 unknown 状态。

**失败 Task：**

- 根据失败原因决定重试、替换或增加补偿 Task。
- 不能无限重试。
- 必须遵守已有重试预算。

**等待审批的 Task：**

- 保留审批状态。
- 如果动作参数发生实质变化，必须重新审批。
- 不得利用新 Plan 复用旧审批授权不同动作。

### 7.3 DAG 校验

新 Plan 必须通过现有 DAG 校验逻辑。

必须验证：

- 不存在循环依赖。
- 依赖节点存在。
- 不存在非法依赖。
- Task 状态迁移合法。
- 不会破坏已经完成 Task 的历史关系。
- 新增 Task 的权限和预算符合要求。

### 7.4 Plan Proposal 的提交

Planner 只生成提案。

由 Go Core 执行：

1. 校验 Base Plan Version。
2. 校验 Goal 当前状态。
3. 校验 DAG。
4. 校验预算。
5. 校验 Policy。
6. 校验 Task 状态变化。
7. 保存新 Plan Version。
8. 写入 Audit 和 Outbox。
9. 使新计划生效。

如果 Base Plan Version 已发生变化，应拒绝旧提案或重新生成。

避免并发 Planner 覆盖彼此结果。

---

## 八、第五部分：Supervisor Controller

实现统一决策循环。

建议逻辑：

```text
Load Goal
    |
Load Current Plan
    |
Evaluate Goal
    |
    +-- Continue
    |      |
    |   Select Ready Tasks
    |      |
    |   Submit to Execution Manager
    |
    +-- Replan
    |      |
    |   Generate Plan Proposal
    |      |
    |   Validate & Commit Plan
    |
    +-- Wait Approval
    |      |
    |   Wait Existing Approval Flow
    |
    +-- Wait Condition
    |      |
    |   Persist Waiting State
    |
    +-- Verify Completion
    |      |
    |   Run Result Verifier
    |      |
    |   +-- Verified → Complete Goal
    |   +-- Insufficient → Replan / Continue
    |   +-- Rejected → Replan / Escalate
    |
    +-- Stop / Escalate
```

### 8.1 决策持久化

每一轮 Supervisor Decision 必须持久化。

至少记录：

- Decision ID
- Goal ID
- Goal Version
- Plan Version
- Evaluation ID
- Decision Type
- Decision Reason
- Related Proposal ID
- Related Verification ID
- Created At

要求：

- 可查询。
- 可审计。
- 可恢复。
- 支持幂等。
- 并发决策不能重复提交相同计划或重复启动执行。

### 8.2 执行预算

复用已有 Goal 级 Tool Usage。

本阶段增加或完善：

- 最大决策轮数。
- 最大重新规划次数。
- 最大 Task 数量。
- 最大模型调用次数。
- 最大 Tool Call 次数。
- 最大 Token 使用量。
- 最大执行时间。
- 最大失败重试次数。

预算必须由 Core 强制执行，不能依赖模型自觉遵守。

达到预算上限时：

- 停止自动推进。
- 持久化原因。
- 保留当前状态。
- 允许用户调整预算后恢复。

### 8.3 停止条件

必须有明确停止条件：

- Goal 已验证完成。
- Goal 被用户取消。
- 预算耗尽。
- 达到最大重规划次数。
- 不存在可执行 Task 且没有可等待事件。
- 需要人工处理。
- 检测到不可恢复错误。
- 触发 Policy 安全阻断。

不得让 Supervisor 无限循环调用模型。

---

## 九、数据库与 API

首先检查当前数据库 Schema 和现有 API。

只在确实缺少能力时增加 Migration。

不要重复创建已经存在的实体。

可能需要新增：

- goal_success_criteria
- goal_evaluations
- task_verifications
- goal_verifications
- plan_versions
- plan_proposals
- supervisor_decisions

以上是逻辑实体建议，不要求机械地一项对应一张表。应根据现有模型和数据库风格决定。

要求：

- 外键完整。
- 唯一约束合理。
- 状态约束明确。
- 并发提交安全。
- 事务边界正确。
- Migration 可重复验证。
- Migration 失败可以回滚。
- 新旧数据兼容。

API 只暴露必要的管理接口。

建议评估是否需要：

- 查询 Goal Evaluation。
- 查询 Verification。
- 查询 Plan Version。
- 查询 Plan Proposal。
- 接受或拒绝 Plan Proposal。
- 查询 Supervisor Decision。
- 手动触发重新评估。

所有 API 必须遵守现有认证和权限体系。

不要为了增加 API 数量而重复暴露内部方法。

同步更新 OpenAPI。

---

## 十、端到端集成测试

必须使用真实 PostgreSQL。

不要只用 Mock Repository 测试整个功能。

建立一个完整 Goal 场景：

Goal：

收集一个指定技术主题的资料，比较至少三个技术方案，生成有来源引用的技术报告。

Success Criteria：

1. 至少收集规定数量的有效来源。
2. 至少比较三个方案。
3. 比较维度完整。
4. 结论与 Evidence 对应。
5. 报告实际存在且可以读取。
6. 所有必要 Criterion 均通过验证。

故障注入流程：

1. 创建 Goal。
2. Planner 创建初始 Plan。
3. Supervisor 启动执行。
4. 第一个 Tool Call 成功。
5. 第一个 Task 完成并保存 Evidence。
6. 第二个 Tool Call 失败。
7. Evaluator 识别失败和当前阻塞。
8. Planner 生成新 Plan。
9. 新 Plan 保留已完成 Task，不重复执行已成功副作用。
10. 新 Task 需要 A2 或 A3 审批。
11. 验证旧审批不能授权参数发生实质变化的新动作。
12. Worker 在执行期间崩溃。
13. Core 重启。
14. 恢复 Execution 和 Supervisor 决策。
15. 正确处理 Gateway 中的 unknown Invocation。
16. Agent 提交最终报告。
17. Agent 声称 Goal 已完成。
18. Verifier 检查 Evidence 和 Success Criteria。
19. 首次验证因为缺少 Evidence 返回 insufficient_evidence。
20. Planner 根据验证结果增加补充 Task。
21. 补充 Task 执行完成。
22. 再次验证。
23. 所有必要 Criterion 通过后，Go Core 才允许 Goal 完成。

必须断言：

- 已完成 Task 不被重复执行。
- Execution 不丢失。
- Tool Invocation 不重复产生外部副作用。
- 审批不能跨参数复用。
- Plan Version 单调递增。
- 历史计划和提案保留。
- Verifier 不接受不存在的 Evidence。
- 模型最终答复不能直接完成 Goal。
- Evidence 不足时 Goal 保持未完成。
- 达到预算时 Supervisor 停止。
- Core 重启后可以恢复。
- Audit / Outbox 记录完整。

另外覆盖：

- 并发评估。
- 并发重新规划。
- 旧 Plan Proposal 提交。
- DAG 循环。
- 依赖 Task 缺失。
- 矛盾 Evidence。
- Evidence 过期。
- 外部工具 unknown。
- Approval 等待。
- Goal 取消。
- 最大重规划次数。
- 最大预算。

---

## 十一、非功能要求

### 安全

- 模型不得直接修改 Goal / Task 状态。
- 模型不得直接操作数据库。
- Planner 不得直接提交执行。
- Verifier 不得自行生成 Evidence。
- 所有工具继续通过 Tool Gateway。
- Policy Engine 独立于模型。
- A3 操作必须人工审批。
- 所有决策和状态变化可审计。

### 可靠性

- 所有关键状态持久化。
- 决策支持幂等。
- 事务边界明确。
- 并发修改需要版本校验。
- 重启后可以恢复。
- 不承诺无法保证的 Exactly-once 外部执行。

### 可观测性

增加结构化日志和必要的 OpenTelemetry 指标：

- Goal Evaluation Count。
- Evaluation Duration。
- Replanning Count。
- Verification Success / Failure。
- Supervisor Decision Count。
- Goal Completion Duration。
- Budget Exhaustion Count。
- Blocked Goal Count。

日志中不得输出密钥、访问令牌或敏感凭据。

---

## 十二、实施顺序

不要一次性大规模重构。

建议拆成以下子阶段：

### Phase 12.2.1 — Goal Success Criteria & Evaluator

- 检查现有 Goal 模型。
- 明确 Success Criteria。
- 实现 Goal Evaluator。
- 实现 Evaluation 持久化。
- 增加单元和 PostgreSQL 集成测试。

### Phase 12.2.2 — Result Verifier

- 实现 Task Verifier。
- 实现 Goal Verifier。
- Evidence 关联与有效性检查。
- 确定性验证优先。
- 模型辅助验证作为可选能力。
- 增加伪造、缺失、过期和矛盾 Evidence 测试。

### Phase 12.2.3 — Dynamic Planner

- Plan Version。
- Plan Proposal。
- 增量计划变更。
- 已完成 Task 保留。
- DAG 校验。
- 预算和权限校验。
- 并发版本冲突处理。

### Phase 12.2.4 — Supervisor Controller

- 统一决策状态机。
- 决策持久化。
- Evaluation / Planner / Verifier 编排。
- 执行预算。
- 停止条件。
- 崩溃恢复。

### Phase 12.2.5 — End-to-End Acceptance

- 完整 Goal 故障注入。
- 真实 PostgreSQL。
- Worker 崩溃。
- Core 重启。
- 审批恢复。
- 动态重新规划。
- Evidence 验证。
- Goal 完成判定。

每个子阶段完成后都要先测试，再进入下一个子阶段。

---

## 十三、技术约束

- 继续使用 Go Core。
- 保持当前 Go 1.25.0 最低版本。
- 不随意升级 Go 版本。
- 复用当前 PostgreSQL 和 pgvector 基础设施。
- 继续使用现有 Repository、Domain Service、事务和 Migration 模式。
- 继续使用当前 OpenAI-compatible 和 Ollama Provider。
- 不引入新的 Agent Framework，除非证明现有实现无法满足需求并获得明确理由。
- 不为了本阶段引入 Temporal。
- 不提前实现 Multi-Agent。
- 不重写 MCP Manager 或 Tool Gateway。
- 不把 Agent 框架类型泄漏到 Core Domain。

---

## 十四、交付要求

完成后更新：

- IMPLEMENTATION_STATUS.md
- PHASE12_PROGRESS.md
- TEST_REPORT.md
- ARCHITECTURE.md
- OpenAPI 文档
- 必要的数据库设计文档

报告必须明确区分：

- 已实现。
- 单元测试通过。
- PostgreSQL 集成测试通过。
- 故障注入通过。
- CI 已配置。
- CI 云端实际通过。
- 尚未实现。
- 已知限制。

禁止只写“测试通过”而不说明测试覆盖范围。

最终必须运行：

```bash
make fmt
make test
make build
make vet
go test -race fovvdk.
make build-arm64
make test-integration
```

如果 CI 已配置 Go 1.25.0，还应确保该版本工作流覆盖本阶段新增代码。

Compose / YAML 校验也必须通过。

---

## 十五、最终验收标准

Phase 12.2 只有同时满足以下条件才能标记完成：

1. Goal 有明确、可持久化的成功标准。
2. Evaluator 能够根据实际状态评估 Goal。
3. Verifier 能够独立验证 Task 和 Goal。
4. Agent 最终答复不能直接完成 Goal。
5. Planner 可以根据失败和新 Evidence 动态生成新计划。
6. Plan Version 和 Proposal 有完整历史。
7. 重新规划不会破坏已完成 Task。
8. 新计划必须经过 DAG、Policy、预算和状态校验。
9. Supervisor 能够持久化并恢复决策循环。
10. Supervisor 有明确预算和停止条件。
11. Approval、Gateway、Invocation、Audit、Outbox 继续保持原有安全边界。
12. 真实 PostgreSQL 端到端故障注入测试通过。
13. Worker 和 Core 重启恢复测试通过。
14. 全部现有测试和新增测试通过。

**本阶段最终交付目标：**

一个 Supervisor 可以围绕 Goal 自主评估进度、发现问题、重新规划、调用已有执行系统，并通过独立 Evidence 验证决定是否完成 Goal 的完整闭环。

注意：本阶段不实现 Multi-Agent Coordination。完成后再单独进入 Phase 12.3 或后续 Agent Runtime / Multi-Agent 阶段，具体顺序根据现有实现状态决定。

开始前先检查当前代码、领域模型、数据库 Migration、现有 Supervisor 和 ModelAgent 实现，形成简短的差距分析，然后按上述子阶段逐步实施。不得假设上述能力完全不存在，也不得重复创建已有实现。