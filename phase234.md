# Phase 12.3.4 — Autonomous Goal E2E Closure

继续开发 Personal AI OS Go Core。

当前状态：

- Phase 12.3.1 Persistent Goal Job Runtime：已实现并通过验收。
- Phase 12.3.2 Event-driven Wakeup：已实现并通过验收。
- Phase 12.3.3 Source Snapshot & Reference Verification：已实现并通过验收。
- Phase 12.3.4 Autonomous Goal E2E：尚未完成。

本阶段只处理 12.3.4，不要重新实现 12.3.1–12.3.3，不要进行无关重构。

## 一、核心目标

建立一个可重复执行、可故障注入、可跨进程重启恢复的完整自主研究 Goal 场景。

最终证明：

Goal 创建 → 自动规划 → Task 执行 → 来源采集 → 引用关联 → 报告生成 → Artifact 验证 → Success Criteria 验证 → Goal 完成

整个生命周期由后台 Goal Job 和 Supervisor 驱动，不依赖用户持续调用 HTTP API 推进。

## 二、先做实现审查

开始编码之前：

1. 阅读现有 Goal Job、Supervisor、Outbox Dispatcher、Source Snapshot、Reference Verifier、Artifact Verifier 和 Evaluator。
2. 检查现有 Migration 0014、0015、0016。
3. 检查现有 API 和 OpenAPI。
4. 检查当前 Worker、Agent、Tool Gateway 的调用链。
5. 明确目前缺失的报告生成流程。
6. 输出 12.3.4 实施设计和文件变更计划。

不要假设前序阶段的报告描述完全等同于代码实际行为。

## 三、建立最小 Research Pipeline

实现一个最小但完整的 Research Pipeline。

建议的处理流程：

1. 接收研究 Goal。
2. 读取结构化 Success Criteria。
3. 由 Supervisor 生成研究计划。
4. 将研究任务拆分为可执行 Task。
5. 通过 Tool Gateway 获取研究资料。
6. 通过 source.capture 获取真实来源快照。
7. 将来源快照与研究材料关联。
8. 生成结构化研究结果。
9. 生成 Markdown 报告。
10. 为报告建立引用索引。
11. 调用 Artifact Verifier。
12. 调用 Reference Verifier。
13. 交由 Evaluator 判断 Success Criteria。
14. 由 Supervisor 决定是否完成或重新规划。

要求：

- 报告生成不能绕过 Task 和 Execution。
- 所有外部工具调用必须经过 Tool Gateway。
- 来源快照必须使用现有不可变存储机制。
- 报告引用必须能够关联 Source Snapshot ID。
- Artifact 必须通过现有 Artifact Verifier。
- 不允许模型直接创建可信 Evidence。
- 不允许模型自行将 Goal 标记为完成。

如果需要新增 Research Domain Service，应保持它与现有 Goal、Task、Execution、Evidence 领域模型的边界清晰。

不要为了完成研究流程引入新的 Agent 框架。

## 四、真实来源采集验收

本阶段必须增加真实外部来源采集验收。

分为两类测试：

### A. 确定性集成测试

使用可控 HTTP 测试服务，覆盖：

- 正常来源采集。
- 重定向。
- 超时。
- 非法 MIME。
- 超大响应。
- URL 不在白名单。
- DNS/IP 安全检查。
- 快照 Hash。
- 快照不可变性。
- 引用与快照关联。
- 来源过期。
- 内容变化后的重新验证。

这类测试保证可重复运行。

### B. 真实网络验收

增加显式启用的真实网络验收测试：

- 只允许精确配置的公开 HTTPS 主机。
- 不允许访问任意用户输入的 URL。
- 不允许访问私有网络或内部地址。
- 真实采集至少一个公开研究来源。
- 持久化真实响应快照。
- 核对 HTTP 状态、最终 URL、获取时间和内容 Hash。
- 生成引用并验证其对应快照。

真实网络测试应通过独立环境变量或 Makefile target 显式启动。

例如：

make test-source-live

没有配置真实来源白名单、网络不可用或采集失败时，必须标记为 BLOCKED 或 NOT VERIFIED，不得用测试服务器结果冒充真实网络验收。

## 五、完整 Approval 生命周期

在研究 Goal 中加入一个具有实际业务意义的审批节点。

例如：

- 研究和来源读取本身保持 A0。
- 报告生成在授权目录内执行。
- 对外发布、发送报告或其他受策略保护的操作必须经过相应审批。

不要为了测试审批而错误地把只读研究操作设置成高风险操作。

必须验证：

1. Supervisor 发现审批条件。
2. Goal Job 进入 waiting_approval。
3. Approval 被持久化。
4. Job 不进行无意义的高频轮询。
5. 用户批准后产生真实 Approval 事件。
6. Outbox Dispatcher 投递事件。
7. Goal Job 被唤醒。
8. Supervisor 重新读取数据库状态。
9. 后续执行恢复。
10. 审批拒绝时进入正确的终止或重新规划路径。

审批必须绑定原始动作和参数，不能在恢复执行时悄悄更换参数。

## 六、Unknown Invocation 与外部副作用恢复

必须在 E2E 中模拟至少一次真实的 unknown Invocation 场景。

测试场景：

1. Tool Gateway 创建 Invocation。
2. 外部测试服务实际接收到请求并执行操作。
3. Worker 在收到响应前异常退出。
4. Core 无法确定外部操作是否成功。
5. Invocation 进入 unknown。
6. Core 重启。
7. 恢复流程识别 unknown Invocation。
8. 根据现有幂等键、下游去重或对账策略恢复。

必须证明：

- 不会盲目重复执行非幂等操作。
- 不会把 unknown 自动当成 success。
- 不会把 unknown 自动当成 failure。
- 无法确定时保持 unknown，等待对账或人工处理。
- 最终执行结果有可追溯记录。

不能用数据库中人为设置一个 unknown 状态代替真实的 Worker 崩溃和外部效果不确定场景。

## 七、Worker 崩溃和 Core 重启

必须在同一个 E2E 流程中完成：

1. 创建 Goal。
2. 后台 Worker 开始执行。
3. 完成至少一个 Task。
4. 保存 Checkpoint。
5. 模拟 Worker 强制退出。
6. 等待 Lease 过期。
7. 第二个 Worker 接管。
8. 验证 Fencing Token 递增。
9. 验证旧 Worker 无法覆盖新状态。
10. 重启 Go Core。
11. 恢复 Goal Job。
12. 恢复 Outbox 消费状态。
13. 恢复 Execution 和 Checkpoint。
14. 继续剩余任务。
15. 最终生成报告并验证。

不要把 Worker 进程内存状态作为恢复依据。

## 八、最终 Goal 完成验证

最终必须验证以下条件：

- 所有必需 Task 均有合法完成状态。
- 所有必需 Evidence 均由 Core Verifier 产生。
- Evidence 与 Goal、Task、Criterion ID 和版本正确关联。
- Artifact Hash 与当前文件一致。
- Source Snapshot 仍然有效。
- 报告引用可以追溯到真实快照。
- 必需 Success Criteria 全部满足。
- Evaluation 是当前版本的有效 Evaluation。
- Supervisor 产生合法的完成 Decision。
- Goal 状态最终为 completed。

还必须测试反例：

1. 删除报告文件，Goal 不得完成。
2. 修改报告内容，旧 Artifact 验证不得通过。
3. 修改来源快照对应的内容或使其过期，旧引用验证不得继续通过。
4. 缺少必需 Evidence，Goal 不得完成。
5. Criterion 版本发生变化，旧 Evidence 不得自动满足新版本。
6. 只完成 Task、没有有效 Evaluation，Goal 不得完成。
7. Agent 自报完成，Goal 不得完成。

## 九、完整 E2E 测试要求

建立独立的 E2E 测试入口。

建议：

make test-autonomous-e2e

至少使用：

- 真实 PostgreSQL 16 + pgvector。
- 真实 Go Core。
- 真实持久化 Goal Job。
- 真实后台 Worker。
- 真实 Outbox Dispatcher。
- 真实 Tool Gateway。
- 真实 Source Snapshot。
- 真实 Artifact Verifier。
- 真实 Reference Verifier。
- 真实 Evaluator。
- 真实 Supervisor。

允许使用确定性测试 Agent 或可控模型 Provider 保证测试可重复，但不能因此跳过真实 Tool Gateway、数据库、审批、事件和恢复流程。

必须区分：

- 确定性集成 E2E。
- 真实网络采集 E2E。
- 真实模型 E2E。

不要把其中一种测试的结果描述成全部通过。

## 十、交付报告

新增：

- docs/AUTONOMOUS_GOAL_E2E_REPORT.md

报告必须包含：

1. 完整流程架构。
2. 关键代码文件。
3. 新增或修改的 Migration。
4. Research Pipeline 实现。
5. Approval 生命周期测试。
6. Unknown Invocation 测试。
7. Worker 崩溃恢复测试。
8. Core 重启恢复测试。
9. Artifact 验证测试。
10. Reference 验证测试。
11. Goal 最终完成测试。
12. 失败场景测试。
13. 真实网络采集结果。
14. 未完成事项。

同时更新：

- IMPLEMENTATION_STATUS.md
- 架构文档
- OpenAPI
- README

## 十一、最终验收

运行：

make fmt
make test
make build
make vet
go test -race ./...
make build-arm64
make test-integration
make test-autonomous-e2e

如果真实网络验收条件满足，再运行：

make test-source-live

Hosted Go 1.25 CI 必须单独确认实际执行结果。

最终报告严格区分：

- IMPLEMENTED
- UNIT TESTED
- POSTGRESQL INTEGRATION TESTED
- FAILURE INJECTION TESTED
- AUTONOMOUS E2E VERIFIED
- LIVE SOURCE CAPTURE VERIFIED
- HOSTED CI VERIFIED
- BLOCKED
- NOT VERIFIED

所有结论必须有对应测试记录或可复现步骤。

本阶段不增加 Multi-Agent，不增加新的模型框架，不接入机器人硬件，不进行无关重构。

最终目标：

让 Personal AI OS 能够真正创建一个长期 Goal，在后台自主规划、执行、采集资料、生成报告、等待审批、从未知外部效果中安全恢复、跨 Worker 和 Core 重启继续执行，并最终通过独立验证完成 Goal。