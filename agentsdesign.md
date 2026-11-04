# Personal AI OS — Phase 11：Tool Fabric & Secure Execution

## 项目背景

Personal AI OS 已完成 Phase 0–10。

当前系统已经具备：

- Go Core
- Goal / Task / Execution
- Supervisor / Planner
- Policy / Approval
- PostgreSQL 持久化
- Durable Execution Runtime
- Worker Pool
- Lease / Heartbeat
- Fencing Token
- Checkpoint
- Run / Step 幂等记录
- Recovery Scanner
- Memory / Knowledge / Node
- Model Gateway

Phase 9 和 Phase 10 均已通过真实 PostgreSQL 集成验收。

本阶段目标：

建立统一、安全、可扩展的 Tool Fabric，让未来所有 Agent、模型、MCP Server、Home Assistant、Xiaozhi 和 ROS 2 节点都通过统一的工具执行体系与外部世界交互。

必须先阅读：

- AGENTS.md
- docs/IMPLEMENTATION_PLAN.md
- docs/IMPLEMENTATION_STATUS.md
- docs/PHASE10_DESIGN.md
- docs/PHASE10_IMPLEMENTATION_REPORT.md
- 当前 Tool、Policy、Approval、Execution、Agent 相关代码

不要重新实现已有能力，不要破坏 Phase 10 的执行架构。

---

# 一、架构原则

必须遵守：

1. Go Core 是唯一控制权中心。
2. Agent 不能直接调用外部工具。
3. 所有工具调用必须经过 Tool Execution Gateway。
4. Policy Engine 是唯一权限决策来源。
5. Approval Manager 管理人工审批。
6. MCP Server 不拥有系统权限。
7. 工具的描述和返回内容都是不可信数据。
8. 工具执行必须关联 Goal、Task、Execution 和 Agent Identity。
9. 工具执行记录必须持久化。
10. 高风险操作默认关闭。

Tool Registry 只负责发现和管理工具，不负责授予权限。

Tool Runtime 负责实际执行，但不能自行决定权限。

---

# 二、Phase 11.1：Tool Registry

审查当前 Tool Registry，补齐以下能力：

1. 工具唯一标识。
2. 工具名称。
3. 工具描述。
4. 工具版本。
5. 输入参数 Schema。
6. 输出结果 Schema。
7. 工具来源。
8. 工具类型。
9. 风险等级。
10. 幂等能力声明。
11. 超时配置。
12. 执行环境。
13. 工具启用/禁用状态。
14. 工具健康状态。

支持以下工具来源：

- Native Go Tool
- MCP Tool
- Remote Tool Adapter

工具身份不能只使用工具名称。

应考虑稳定的 Tool ID、Provider ID 和版本信息，避免不同来源工具名称冲突。

工具定义发生变化时，不能静默改变已有审批所绑定的工具语义。

# 三、Phase 11.2：Tool Execution Gateway

建立统一工具调用入口。

建议接口：

ToolExecutionGateway.Execute(ctx, request)

请求必须包含：

- Goal ID
- Task ID
- Execution ID
- Step ID
- Agent ID
- Tool ID
- Tool Version
- Arguments
- Idempotency Key
- Trace Context

实际字段根据当前项目规范确定。

执行过程：

1. 验证调用者身份。
2. 验证 Goal、Task、Execution 状态。
3. 验证工具是否存在且启用。
4. 验证工具版本。
5. 验证参数 Schema。
6. 规范化参数并计算 Hash。
7. 查询持久化幂等记录。
8. 调用 Policy Engine。
9. 如果需要审批，进入 waiting_approval。
10. 审批通过后重新检查权限和参数 Hash。
11. 检查当前 Execution 的有效 Lease 和 Fencing Token。
12. 创建持久化 Tool Invocation 记录。
13. 执行工具。
14. 验证结果 Schema。
15. 保存结果、Audit 和必要的 Evidence。
16. 更新 Invocation 状态。
17. 返回结构化执行结果。

任何步骤失败都必须返回明确错误。

不能吞掉错误。

不能允许 Agent 通过其他路径直接调用工具。

# 四、Policy 与 Approval

必须复用现有 Policy Engine 和 Approval Manager。

权限等级继续使用：

- A0：只读、查询、分析。
- A1：创建笔记、创建任务等低风险操作。
- A2：普通修改、发送消息、设备控制。
- A3：付款、重要删除、门锁、安防和机器人高风险动作。

要求：

1. 工具自身声明风险等级不能代替 Policy Engine 判断。
2. 工具注册不能自动获得调用权限。
3. Agent 不能提高自身权限。
4. 模型不能绕过 Policy Engine。
5. 审批绑定工具 ID、版本、规范化参数 Hash 和必要的调用上下文。
6. 工具版本或参数发生实质变化，必须重新审批。
7. 审批通过后执行前重新验证。
8. 审批超时、拒绝、撤销时不能产生工具副作用。
9. Execution 失效或取消后，审批不能继续授权执行。
10. 所有决策都必须记录 Audit。

# 五、幂等和外部副作用安全

检查 Phase 10 已有 Run / Step 幂等机制。

不要重复建立一套冲突的幂等系统。

建立 Tool Invocation 层面的幂等控制。

必须支持：

- 相同 Key、相同参数：返回已有结果或当前状态。
- 相同 Key、不同参数：拒绝。
- 正在执行的重复请求：不能并发执行同一副作用。
- 已成功的重复请求：不能再次产生副作用。
- 已失败请求：根据错误分类决定是否允许重试。
- 外部执行结果不确定：进入 unknown，禁止盲目重试。

注意：

不能承诺任意外部 API 都具有 Exactly-once。

对不支持幂等的外部工具，必须明确声明，并提供结果核实或人工介入机制。

Worker Lease 失效后，旧 Worker 不能继续提交内部状态；对于外部系统无法验证 Fencing Token 的调用，必须采取额外的调用隔离或结果对账策略。

# 六、Tool Runtime

建立统一执行接口，支持：

- Native Go Tool
- MCP Tool
- Remote Adapter

执行要求：

1. Context 超时。
2. Context Cancellation。
3. 输入输出限制。
4. 错误分类。
5. 重试策略。
6. 执行日志。
7. 结果 Schema 验证。
8. 调用预算。
9. 并发限制。
10. 工具健康检查。

对于执行时间较长的工具，必须与 Durable Execution Runtime 协作，不允许简单阻塞 HTTP 请求。

对本地文件系统、Shell 和浏览器等高风险能力，必须采用明确的沙箱和资源限制策略。

不要默认开放任意 Shell 执行。

# 七、Phase 11.3：MCP Client

在 Tool Gateway 和 Runtime 稳定后，实现 MCP Client Adapter。

要求：

1. 支持 MCP Server 注册。
2. 支持 MCP Server 连接和断开。
3. 支持工具发现。
4. 支持工具 Schema 导入。
5. 支持工具调用。
6. 支持连接超时。
7. 支持调用超时。
8. 支持断线检测。
9. 支持重新连接。
10. 支持工具启用和禁用。
11. 支持 Server 身份管理。
12. 支持调用审计。

MCP Server 必须被视为不可信的外部执行环境。

必须对工具 Schema、工具名称、描述、返回值和错误信息进行校验。

不得允许 MCP Server 自行改变 Go Core 的权限策略。

优先采用成熟的官方 MCP Go SDK，先验证 SDK 与当前 Go 版本和目标平台兼容性。

不要为了 MCP 重写现有 Tool Registry。

# 八、第一批真实工具

优先实现少量可控工具用于验收：

1. time.now：获取当前时间。
2. filesystem.read：读取授权目录中的文件。
3. filesystem.list：列出授权目录内容。
4. knowledge.search：查询已有知识库。
5. memory.search：查询已有记忆。
6. http.fetch：访问明确允许的 HTTP/HTTPS 地址。

这些工具优先只读。

filesystem.read/list 必须防止路径穿越和符号链接逃逸。

http.fetch 必须防止 SSRF，限制目标地址、重定向、协议和响应大小。

不要在本阶段开放：

- 任意 Shell。
- 任意文件删除。
- 任意文件写入。
- 付款。
- 门锁控制。
- 安防控制。
- 机器人运动控制。

# 九、Supervisor 和 Agent 集成

Agent 只能提交结构化 Tool Call Request。

Supervisor 不直接执行工具。

Tool Gateway 执行后返回结构化结果。

Agent 可以根据结果决定下一步，但不能修改执行权限或审批状态。

工具结果必须作为不可信数据处理，防止 Prompt Injection。

# 十、测试要求

必须使用真实 PostgreSQL 完成集成测试。

至少覆盖：

1. 正常工具注册和发现。
2. 工具禁用后拒绝调用。
3. 参数 Schema 不匹配。
4. 未授权调用被拒绝。
5. A3 工具没有审批时无法执行。
6. 审批参数发生变化后拒绝执行。
7. 审批撤销后拒绝执行。
8. 相同幂等 Key 不产生重复副作用。
9. 相同 Key、不同参数被拒绝。
10. 两个 Worker 同时请求同一副作用。
11. Worker Lease 过期后的调用处理。
12. 工具超时。
13. 工具取消。
14. 工具返回无效 Schema。
15. MCP Server 断线。
16. MCP Server 返回恶意或不符合 Schema 的内容。
17. Agent 尝试绕过 Gateway。
18. Audit 完整性。
19. Tool Invocation 重启恢复。
20. 不确定结果不会被盲目重试。

所有测试必须真实执行。

不能用 Mock 测试冒充真实 MCP 集成测试。

# 十一、文档和交付

创建：

docs/PHASE11_DESIGN.md
docs/PHASE11_IMPLEMENTATION_REPORT.md

更新：

docs/IMPLEMENTATION_STATUS.md
docs/TEST_REPORT.md
README.md
OpenAPI 文档。

报告中明确区分：

- 已实现。
- 单元测试通过。
- 真实 PostgreSQL 集成测试通过。
- 真实 MCP Server 验证通过。
- 尚未实现。

完成后暂停，不要继续开发 Eino、Home Assistant、Xiaozhi 或 ROS 2 集成。

---

# 十二、开发过程

分三个子阶段执行：

Phase 11.1：Tool Registry + Gateway 基础。

Phase 11.2：Policy / Approval / Idempotency / Secure Runtime。

Phase 11.3：MCP Client + 第一批只读工具。

每个子阶段结束都要：

1. 执行测试。
2. 检查架构边界。
3. 更新实施状态。
4. 记录未解决问题。

不要一次性生成大量代码后才测试。

优先保证安全执行链路完整，再增加工具数量。