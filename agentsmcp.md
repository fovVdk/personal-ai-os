# Personal AI OS — Phase 11.3：MCP Integration & Final Acceptance

## 一、项目现状

Personal AI OS 已完成 Phase 0–10。

Phase 11 已完成：

- Versioned Tool Registry
- Tool Execution Gateway
- Policy / Approval Integration
- Persistent Tool Invocation
- One-time Approval Context
- Execution Continuation
- Lease Expiration / Unknown Recovery
- Tool Idempotency
- Native Read-only Tools
- Tool Metadata API

Phase 11 尚未完成的部分：

- MCP Client Adapter
- MCP Server Lifecycle Management
- MCP Tool Discovery
- MCP Tool Invocation
- Real MCP Server Integration Tests

相关报告：

- docs/PHASE11_DESIGN.md
- docs/PHASE11_IMPLEMENTATION_REPORT.md
- docs/IMPLEMENTATION_STATUS.md
- docs/TEST_REPORT.md

## 二、本阶段目标

完成 MCP Client，使 Personal AI OS 能够安全接入符合 MCP 协议的外部工具服务。

所有 MCP 工具必须经过现有 Tool Registry 和 Tool Execution Gateway。

禁止 MCP Client 绕过现有 Policy、Approval 和 Tool Invocation 机制。

## 三、Go 版本决策

当前项目最低版本为 Go 1.22。

根据已经完成的 SDK 兼容性检查：

- MCP SDK v1.0.0 要求 Go 1.23。
- MCP SDK v1.8.0 要求 Go 1.25。
- 当前开发环境为 Go 1.27.3。

采用 Go 1.25 作为新的项目最低版本，使用 MCP SDK v1.8.0。

执行前：

1. 检查当前 go.mod。
2. 检查所有依赖的最低 Go 版本。
3. 检查当前 SDK v1.8.0 的 API 和实际兼容性。
4. 检查项目现有代码是否兼容 Go 1.25。
5. 不要自动升级其他无关依赖。
6. 不要修改现有领域模型来适配 SDK。

如果发现 SDK v1.8.0 存在实际兼容问题，暂停并报告，不要静默更换版本。

## 四、MCP Client Adapter

建立独立 MCP Adapter。

至少实现：

1. MCP Client 初始化。
2. MCP Server 连接。
3. MCP Server 断开。
4. Server 健康状态。
5. 工具发现。
6. 工具 Schema 获取。
7. 工具注册与更新。
8. 工具调用。
9. 调用超时。
10. Context Cancellation。
11. 连接异常处理。
12. 重连机制。
13. Server 禁用。
14. Server 注销。

所有 MCP SDK 类型限制在 Adapter 内部。

核心领域模型不能直接依赖 MCP SDK 类型。

## 五、传输层

先检查 SDK 支持的传输方式。

至少评估：

- stdio
- Streamable HTTP

根据项目现有部署结构，设计可扩展 Transport Adapter。

安全要求：

stdio：

- 只能启动明确配置和授权的命令。
- 禁止 Agent 或模型任意提供 command。
- 不允许任意 shell 拼接。
- 参数必须作为独立参数传递。
- 明确工作目录和环境变量白名单。

Streamable HTTP：

- 限制允许访问的 Server 地址。
- 验证 TLS。
- 控制重定向。
- 配置连接和请求超时。
- 防止 SSRF。
- 凭据不能写入日志。

不要为了支持所有传输方式而过度设计。

## 六、MCP Tool Discovery

MCP Server 提供的工具应转换成当前 Tool Registry 的标准定义。

必须处理：

- Tool ID
- Provider ID
- Version
- Description
- Input Schema
- Output Schema（如果可用）
- Server Identity
- Risk Classification
- Timeout
- Idempotency Capability
- Enabled Status

注意：

MCP Server 提供的工具描述、Schema 和 Annotation 均视为不可信输入。

不得直接相信 MCP Server 声明的风险等级和权限。

工具版本或 Schema 发生变化时，应重新评估工具定义，不能静默改变已有审批语义。

## 七、MCP Tool Execution

执行过程必须经过：

Agent
→ Tool Execution Gateway
→ Tool Registry
→ Policy Engine
→ Approval Manager
→ Tool Runtime
→ MCP Client
→ MCP Server

MCP Client 不得直接接受 Agent 的调用请求。

执行时必须关联：

- Goal
- Task
- Execution
- Step
- Agent Identity
- Tool ID
- Tool Version
- Provider ID
- Invocation ID
- Idempotency Key

执行结果必须经过校验并保存 Tool Invocation 记录。

对于外部结果不确定的调用，进入 unknown，不允许盲目重试。

## 八、MCP Server 管理

建立 MCP Server Registry 或复用现有 Provider Registry。

支持：

- Server ID
- Server Name
- Transport
- Endpoint 或受控启动配置
- Server Version
- Connection Status
- Last Connected At
- Last Error
- Enabled Status
- Tool Discovery Timestamp

凭据必须通过配置或安全凭据管理机制提供，不允许写入普通日志和文档。

## 九、真实 MCP Server 集成测试

建立测试专用 MCP Server。

使用真实 MCP SDK 和真实 Client Adapter。

至少测试：

1. Server 成功连接。
2. Server 工具发现。
3. 工具 Schema 转换。
4. 正常工具调用。
5. 参数校验失败。
6. Server 返回错误。
7. Server 断开。
8. 请求超时。
9. Context Cancellation。
10. Server 重启和重连。
11. Server 返回不符合预期的内容。
12. Server 工具定义发生变化。
13. 工具被禁用后拒绝调用。
14. 需要审批的工具不能绕过审批。
15. 重复幂等请求不能产生重复副作用。
16. Gateway Audit 完整。
17. Server 无法改变 Go Core Policy。
18. Agent 无法绕过 Gateway 直接执行 MCP 工具。

测试不能只使用 Mock MCP Client。

## 十、与现有架构的兼容性

必须保证：

- 不破坏 Phase 9 数据库迁移。
- 不破坏 Phase 10 Durable Execution。
- 不破坏 Phase 11 Gateway。
- 不改变现有 Native Tool 行为。
- 不让 MCP SDK 类型泄漏到核心领域层。
- 不允许 MCP Server 获得系统级控制权限。

如果需要数据库 Migration，必须新增版本，不得修改已经执行过的历史 Migration。

## 十一、测试与交付

执行：

- make fmt
- make test
- make build
- make vet
- go test -race ./...
- make test-integration
- 真实 MCP Server 集成测试
- ARM64 构建验证
- Docker Compose 配置检查

更新：

- go.mod
- go.sum
- AGENTS.md（仅在确实需要时）
- README.md
- docs/IMPLEMENTATION_STATUS.md
- docs/TEST_REPORT.md

创建：

- docs/PHASE11_MCP_DESIGN.md
- docs/PHASE11_MCP_IMPLEMENTATION_REPORT.md

报告必须明确区分：

- SDK 编译验证
- 真实 Server 连接验证
- 真实工具调用验证
- 权限与审批验证
- 故障恢复验证
- 尚未完成的内容

## 十二、完成标准

只有在以下条件全部满足后，才可以标记 Phase 11 完成：

1. MCP Client 可以连接真实 MCP Server。
2. 可以发现并注册 MCP 工具。
3. 可以通过 Tool Gateway 执行 MCP 工具。
4. Policy 和 Approval 真实生效。
5. 持久化 Invocation 正常。
6. 超时、取消、断线和重连测试通过。
7. MCP 工具无法绕过 Go Core 的安全机制。
8. 全部测试实际执行通过。

完成后暂停，不要开始 Eino、Home Assistant、Xiaozhi 或 ROS 2 开发。