# 项目总指令：自主开发 Personal AI OS Go Core

你现在是一名资深 Go 系统架构师、AI Agent 工程师、分布式系统工程师和全栈开发工程师。

你的任务是直接在当前打开的代码仓库中，开发一个完整的 Personal AI OS（个人自主智能体操作系统）核心系统。

这不是一个普通的 AI Chat 项目，也不是一个简单的 Agent Demo。

它是一个以 Goal（目标）为最高管理对象，能够长期保存个人记忆、管理目标、主动规划任务、调用工具执行操作、跟踪执行结果、在必要时请求用户审批，并最终连接家庭设备、视觉系统和机器人的个人自主智能体系统。

你必须直接操作当前项目文件，创建和修改代码，执行命令，运行测试，修复问题，直到完成本指令规定的 V1 基础系统。

不要只输出代码示例或开发建议。必须实际创建代码文件。

---

# 一、项目背景与硬件环境

## 1.1 项目定位

项目名称：Personal AI OS

核心定位：

一个本地优先、支持云端模型、具有长期记忆和自主任务能力的个人智能体操作系统。

核心能力：

1. Goal Management：长期目标管理。
2. Autonomous Agent：自主规划和执行。
3. Task Management：任务生命周期管理。
4. Execution Engine：可恢复的任务执行。
5. Personal Memory：长期个人记忆。
6. Knowledge Intelligence：个人知识管理。
7. Model Gateway：统一模型接口。
8. MCP Tool System：工具注册和调用。
9. Permission System：独立权限管理。
10. Approval System：重要操作人工审批。
11. Event System：事件驱动。
12. Audit System：操作审计。
13. Node Management：为未来多节点预留能力。

## 1.2 当前运行环境

主要开发和部署环境：

- Mac mini M4
- Apple Silicon ARM64
- macOS
- 16GB 统一内存
- 256GB 内置 SSD
- 可使用 Docker Compose
- 可连接云端大语言模型
- 可使用 Ollama 运行本地模型

未来可能增加：

- NVIDIA GPU 推理节点。
- Vision Node。
- Home Assistant。
- Xiaozhi 语音设备。
- ROS 2 Robot Node。

当前不要实现这些未来设备的具体业务功能，但必须通过接口和 Adapter 为未来集成做好准备。

## 1.3 技术原则

- 后端核心使用 Go。
- Python 仅用于确实有必要的 AI 专项服务。
- 数据库使用 PostgreSQL。
- 向量检索使用 pgvector。
- 数据访问使用 pgx 和 sqlc，或采用合理的 pgx 封装。
- HTTP API 使用 Chi。
- MCP 使用官方 Go SDK。
- 模型调用采用统一 Model Gateway。
- Agent 框架采用适配器模式，优先评估 Eino。
- 本地模型支持 Ollama。
- 使用 Docker Compose 管理基础设施。
- 使用 OpenTelemetry 预留可观测能力。

开发前必须检查当前环境中的 Go 版本、Docker 版本和依赖兼容性。

不要随意升级 Go 工具链，也不要盲目采用尚未验证兼容的最新依赖版本。

---

# 二、最高级架构原则

以下原则是强制要求，不允许违反。

## 2.1 Goal 优先

系统的最高级管理对象是 Goal。

层级：

Goal
  → Subgoal
    → Milestone
      → Plan
        → Task
          → Task Step
            → Agent / Tool
              → Execution
                → Evidence

Goal 不属于某个 Agent。

Task 不属于某次对话。

Agent 不拥有最终授权权。

模型不决定系统是否有权执行操作。

用户关闭聊天窗口，不代表 Goal 和 Task 结束。

系统重启，不代表任务丢失。

## 2.2 核心控制权

系统控制权归 Go Core。

Go Core 负责：

- Goal 生命周期。
- Task 生命周期。
- Agent 注册。
- Tool 注册。
- Node 注册。
- Permission。
- Approval。
- Execution。
- Event。
- Audit。
- Memory 和 Knowledge 的业务管理。

Agent 框架只负责 Agent 内部推理、工具编排和工作流。

模型只负责智能推理和生成。

工具只负责被授权的具体操作。

## 2.3 权限独立

必须实现独立 Policy Engine。

任何有副作用的工具调用，都必须先经过 Policy Engine。

模型不能修改自己的权限。

Agent 不能自行提高权限。

MCP Server 不能自行授予权限。

审批必须绑定具体动作和参数。

重要参数发生变化时，必须重新审批。

## 2.4 模块化单体

第一版采用 Modular Monolith。

一个 Go 主程序。

内部模块按照领域划分。

不要一开始拆成大量微服务。

但必须使用接口隔离模块之间的依赖，保证未来可以拆分。

---

# 三、现有项目目录

项目目录已经按照既定设计创建。

首先检查当前仓库，不要删除已有目录，不要擅自重建项目。

预期结构：

personal-ai-os/
├── cmd/
│   └── paios/
│       └── main.go
├── internal/
│   ├── app/
│   │   ├── bootstrap/
│   │   ├── lifecycle/
│   │   └── config/
│   ├── identity/
│   ├── goal/
│   ├── supervisor/
│   ├── planner/
│   ├── task/
│   ├── execution/
│   ├── policy/
│   ├── approval/
│   ├── agent/
│   │   ├── registry/
│   │   ├── runtime/
│   │   └── adapter/
│   ├── tool/
│   │   ├── registry/
│   │   ├── policy/
│   │   └── mcp/
│   ├── node/
│   ├── memory/
│   ├── knowledge/
│   ├── model/
│   │   ├── gateway/
│   │   ├── provider/
│   │   └── routing/
│   ├── event/
│   ├── audit/
│   ├── api/
│   └── observability/
├── pkg/
│   ├── contracts/
│   ├── errors/
│   └── types/
├── migrations/
├── configs/
├── deployments/
│   ├── docker/
│   └── compose/
├── docs/
├── scripts/
├── tests/
│   ├── integration/
│   ├── e2e/
│   └── fixtures/
├── go.mod
└── README.md

允许根据实际需要增加文件和子目录。

如果发现已有文件或实现，先检查和复用，不要覆盖有价值的代码。

---

# 四、开发任务总览

你需要完成以下模块。

## 4.1 系统基础模块

实现：

- 配置加载。
- 应用初始化。
- 应用生命周期管理。
- 日志。
- 错误处理。
- 数据库连接。
- 数据库迁移。
- 健康检查。
- HTTP Server。
- 优雅关闭。
- OpenTelemetry 接口预留。

## 4.2 Identity 模块

实现：

- User。
- Role。
- Permission。
- 用户身份验证。
- API Token 或 API Key 管理。
- 基础认证中间件。

第一版只需单用户本地部署，但数据结构不能写死为只有一个用户。

API Key 必须安全保存，不能明文写入日志。

## 4.3 Goal 模块

实现完整 Goal 生命周期：

- draft
- active
- monitoring
- blocked
- paused
- completed
- cancelled
- archived

支持：

- 创建目标。
- 查询目标。
- 修改目标。
- 设置优先级。
- 设置成功标准。
- 创建 Subgoal。
- 管理 Milestone。
- 暂停目标。
- 恢复目标。
- 完成目标。
- 归档目标。
- 更新进度。

每个 Goal 至少包含：

- ID
- Title
- Description
- OwnerID
- ParentGoalID
- Priority
- SuccessCriteria
- Constraints
- Status
- Progress
- AutonomyPolicy
- ReviewPolicy
- CreatedAt
- UpdatedAt

目标完成必须有 Evidence 支持。

不能仅凭模型生成一句“已经完成”就改变 Goal 状态。

## 4.4 Task 模块

实现：

- Task 创建。
- Task 更新。
- Task 查询。
- Task 取消。
- Task 暂停。
- Task 恢复。
- Task 依赖关系。
- Task 优先级。
- Task 与 Goal 关联。
- Task Step 管理。

Task 状态至少包括：

- created
- ready
- running
- waiting_approval
- waiting_external
- retrying
- blocked
- completed
- failed
- cancelled

Task 必须拥有独立 ID，不能使用 Chat Session ID 作为任务 ID。

## 4.5 Execution 模块

实现：

- Execution Run。
- Execution Step。
- Checkpoint。
- Retry。
- Timeout。
- Idempotency Key。
- Execution Result。
- Evidence。
- Failure Recovery。

必须保证：

服务重启后，可以恢复未完成任务。

对于可能重复执行的非幂等操作，必须进行幂等保护。

必须区分：

- 操作已经执行成功。
- 操作执行失败。
- 操作结果未知。
- 操作执行成功但尚未验证。

## 4.6 Planner 模块

实现结构化计划。

Planner 将 Goal 转换为：

- Subgoal。
- Milestone。
- Plan。
- Task。
- Task Dependency。

Plan 必须是结构化数据，不允许只有一段自然语言。

支持：

- 任务分解。
- 依赖关系。
- 执行顺序。
- 成功标准。
- 资源估算。
- 风险描述。

模型输出必须经过 Schema 校验。

## 4.7 Supervisor 模块

Supervisor 是系统的智能协调中心。

实现：

1. 接收用户请求。
2. 获取当前 Goal 和 Task 状态。
3. 分析请求。
4. 判断是否需要创建 Goal。
5. 判断是否需要创建 Task。
6. 调用 Planner。
7. 选择 Agent。
8. 启动执行。
9. 检查执行结果。
10. 更新系统状态。
11. 决定下一步行动。

Supervisor 必须使用有限状态和明确的执行预算。

不能无限调用模型。

必须支持：

- 最大执行步骤。
- 最大模型调用次数。
- 最大工具调用次数。
- 最大运行时间。
- 最大成本预算。
- 最大重试次数。
- 停止条件。

## 4.8 Policy 模块

实现 A0-A3 自主等级：

A0：读取、搜索、分析、查询。

A1：创建笔记、整理知识、创建任务。

A2：修改文件、发送消息、普通设备控制。

A3：付款、重要删除、门锁、安防、机器人高风险动作。

默认规则：

- A0：允许自动执行。
- A1：允许自动执行并记录。
- A2：根据具体工具授权策略决定是否审批。
- A3：必须人工审批。

权限判断必须发生在工具执行之前。

不得仅通过 Prompt 实现权限控制。

## 4.9 Approval 模块

实现：

- 创建审批请求。
- 查看待审批列表。
- 批准。
- 拒绝。
- 过期。
- 撤销。
- 审批记录查询。

审批记录绑定：

- ApprovalID
- UserID
- TaskID
- ToolID
- Action
- ParametersHash
- RiskLevel
- Status
- ExpiresAt
- ApprovedAt

审批参数发生实质变化，原审批失效。

第一版可以通过 Web API 完成审批，不需要先开发完整前端。

## 4.10 Agent 模块

实现：

- Agent Registry。
- Agent Profile。
- Agent Capability。
- Agent Version。
- Agent Runtime Interface。
- Agent Adapter。

定义统一 Agent 接口。

例如：

Agent.Execute(ctx, request)

Agent 不直接访问核心数据库。

Agent 不直接修改 Goal 和 Task 状态。

Agent 的任务执行结果必须返回 Go Core，由 Execution Manager 统一处理。

先实现：

- General Agent。
- Research Agent。
- Planning Agent。

可以使用 Mock Agent 保证整个系统在没有云端模型时仍可运行。

Eino 作为可选适配器接入。

不要让 Eino 类型泄漏到 Goal、Task、Policy 等核心领域。

如果当前 Eino API 与 Go 工具链不兼容，先完成 Agent Adapter 和 Mock Runtime，不允许因此阻塞核心系统。

## 4.11 Model 模块

实现统一 Model Gateway。

定义：

- ModelRequest。
- ModelResponse。
- ModelProvider。
- ModelCapabilities。
- ModelUsage。
- ModelError。

至少实现：

1. OpenAI-compatible Provider。
2. Ollama Provider。
3. Mock Provider。

支持：

- 普通文本请求。
- 流式请求接口。
- Tool Calling。
- 超时。
- 错误处理。
- Token Usage。
- Model Routing。
- Provider Fallback 接口。

API Key 从环境变量或安全配置加载。

不能将模型供应商 API Key 写入源代码。

不要在第一版强制部署 LiteLLM。

## 4.12 Tool 模块

实现：

- Tool Registry。
- Tool Schema。
- Tool Capability。
- Tool Permission。
- Tool Execution。
- Tool Result。

支持 MCP Client Adapter。

第一版至少提供以下内部工具：

- 获取系统时间。
- 查询 Goal。
- 查询 Task。
- 创建 Note。
- 读取允许访问的文件。

文件操作必须限制在配置的工作目录内。

禁止提供不受限制的任意 Shell 执行工具。

## 4.13 Memory 模块

实现基础个人记忆能力：

- Memory 创建。
- Memory 查询。
- Memory 更新。
- Memory 删除。
- Memory 分类。
- Memory 来源。
- Memory 置信度。
- Memory 时间信息。
- Memory 向量检索接口。

初始分类：

- fact
- preference
- experience
- relationship
- event
- instruction

记忆必须保留来源信息。

不能把所有聊天内容不加区分地存成长期记忆。

第一版实现基础 CRUD、关键词检索和向量检索接口。

Mem0 只作为未来可替换的算法 Adapter，不作为核心数据库。

## 4.14 Knowledge 模块

实现基础知识管理：

- Document。
- Document Source。
- Document Chunk。
- Metadata。
- Embedding。
- Retrieval。
- Citation。

支持：

- 文档登记。
- 文本导入。
- 文本分块。
- 向量写入。
- 关键词搜索。
- 向量搜索。
- 混合检索接口。

原始文件保存在文件系统或对象存储中。

数据库保存元数据和文件引用。

## 4.15 Event 模块

实现领域事件机制。

至少包括：

- GoalCreated
- GoalUpdated
- GoalCompleted
- TaskCreated
- TaskStarted
- TaskCompleted
- TaskFailed
- ApprovalRequested
- ApprovalGranted
- ApprovalRejected
- ToolExecuted
- ExecutionRecovered
- NodeRegistered

重要事件使用 PostgreSQL Transactional Outbox。

事件发布必须考虑重复消费和幂等。

第一版不需要 Kafka、RabbitMQ 或 NATS。

## 4.16 Audit 模块

记录关键操作：

- 用户请求。
- Goal 创建与修改。
- Task 创建与执行。
- 工具调用。
- 权限判断。
- 审批。
- 模型调用。
- 执行结果。
- 失败和恢复。

Audit 记录应具有：

- EventID
- Actor
- Action
- Resource
- ResourceID
- Result
- Timestamp
- CorrelationID
- Metadata

审计记录不能由普通 Agent 随意修改。

## 4.17 Node 模块

为未来分布式部署预留：

- Node Registry。
- Node Identity。
- Node Type。
- Node Capability。
- Node Health。
- Node Heartbeat。
- Node Status。

第一版不实现复杂节点通信，只实现节点注册、查询和状态管理接口。

---

# 五、数据库与数据持久化

本阶段不要求完成最终数据架构设计文档，但为了实现可运行系统，必须设计与当前 V1 功能匹配的最小数据库结构。

使用 PostgreSQL + pgvector。

要求：

1. 所有核心数据必须持久化。
2. 合理使用 UUID。
3. 合理设置外键和索引。
4. 使用 UTC 时间。
5. 重要更新使用事务。
6. 使用数据库迁移文件管理 Schema。
7. 数据库结构必须可以持续升级。
8. 不能把关键状态只保存在 Go 内存中。
9. 必须有必要的 JSONB 字段支持可扩展配置。
10. 预留多用户和多节点支持。

请为实际实现的模块创建迁移文件。

不要为了尚未实现的未来功能提前创建大量无用表。

---

# 六、HTTP API

使用 Chi。

统一 API 前缀：

/api/v1

至少实现：

## Health

GET /health
GET /ready

## Goal

POST /goals
GET /goals
GET /goals/{id}
PATCH /goals/{id}
POST /goals/{id}/pause
POST /goals/{id}/resume
POST /goals/{id}/complete

## Task

POST /tasks
GET /tasks
GET /tasks/{id}
POST /tasks/{id}/pause
POST /tasks/{id}/resume
POST /tasks/{id}/cancel

## Planner

POST /planner/generate

## Supervisor

POST /supervisor/turn
GET /supervisor/status

## Approval

GET /approvals
GET /approvals/{id}
POST /approvals/{id}/approve
POST /approvals/{id}/reject

## Agent

GET /agents
GET /agents/{id}

## Tool

GET /tools
GET /tools/{id}

## Memory

POST /memories
GET /memories
GET /memories/{id}
PATCH /memories/{id}
DELETE /memories/{id}
POST /memories/search

## Knowledge

POST /knowledge/documents
GET /knowledge/documents
POST /knowledge/search

## Node

GET /nodes
POST /nodes/register

所有 API 必须：

- 统一 JSON 格式。
- 统一错误格式。
- 参数校验。
- 请求 ID。
- 超时。
- 日志。
- 认证中间件。
- OpenAPI 文档。

---

# 七、配置管理

提供：

configs/config.example.yaml

以及：

.env.example

至少包含：

- APP_ENV
- APP_PORT
- DATABASE_URL
- LOG_LEVEL
- MODEL_PROVIDER
- MODEL_BASE_URL
- MODEL_API_KEY
- MODEL_NAME
- OLLAMA_BASE_URL
- DATA_DIR
- FILE_STORAGE_DIR
- MAX_AGENT_STEPS
- MAX_MODEL_CALLS
- MAX_TOOL_CALLS
- MAX_EXECUTION_SECONDS
- MAX_RETRIES

不要在 Git 中提交真实 API Key。

---

# 八、Docker Compose

创建：

deployments/compose/docker-compose.yml

第一版至少支持：

- PostgreSQL。
- pgvector。
- 可选 Ollama。

Go Core 可以先原生运行，也可以提供 Dockerfile。

要求：

- 数据持久化。
- 健康检查。
- 自动重启策略。
- 环境变量配置。
- 外置 SSD 数据目录挂载。
- 合理资源限制。

不要默认启动大量 AI 服务。

不要默认下载大型模型。

---

# 九、测试要求

测试不是可选项。

必须实现：

## 9.1 单元测试

至少覆盖：

- Goal 状态转换。
- Task 状态转换。
- Policy 判断。
- Approval 有效性。
- 参数 Hash 校验。
- Execution 重试。
- 幂等控制。
- Memory 基础操作。

## 9.2 集成测试

至少覆盖：

- 数据库迁移。
- Goal 创建和查询。
- Task 创建和执行。
- Approval 流程。
- Event Outbox。
- Audit 记录。
- Memory CRUD。
- Model Mock Provider。
- Tool 调用。

## 9.3 端到端测试

构造完整场景：

用户请求：

“帮我制定一个家庭自动化系统建设计划。”

系统应：

1. 接收请求。
2. 创建 Goal。
3. 调用 Planner。
4. 生成结构化 Plan。
5. 创建多个 Task。
6. 选择 Planning Agent。
7. 使用 Mock Model 或真实模型生成计划。
8. 保存执行结果。
9. 保存 Evidence。
10. 更新 Goal 进度。
11. 向用户返回结果。

再构造一个需要审批的场景：

Agent 请求执行一项 A3 操作。

系统必须创建审批请求，在审批完成前不能调用实际工具。

## 9.4 故障恢复测试

模拟：

- Go Core 重启。
- 数据库短暂不可用。
- 模型请求超时。
- 工具调用失败。
- 审批等待期间服务重启。

验证任务状态没有丢失。

---

# 十、工程质量要求

强制要求：

1. 使用 context.Context。
2. 正确处理 goroutine 生命周期。
3. 避免无限循环。
4. 避免全局可变状态。
5. 使用结构化日志。
6. 统一错误处理。
7. 重要操作使用事务。
8. 不忽略错误。
9. 不在核心业务中直接调用外部模型 API。
10. 不让 Agent 直接操作数据库。
11. 不允许未经授权的副作用工具调用。
12. 不允许明文保存密钥。
13. 不允许将模型输出直接当作可信数据。
14. 对模型输出进行 Schema 校验。
15. 所有执行步骤具有超时和取消机制。
16. 所有关键任务有持久化状态。
17. 所有数据库操作使用参数化查询。
18. 不允许通过拼接 SQL 实现动态查询。
19. 避免不必要的依赖。
20. 所有外部依赖使用明确版本。

代码必须通过 gofmt。

尽可能通过 go vet 和 go test。

---

# 十一、开发执行顺序

不要一次性生成所有代码。

必须按照下面的顺序持续执行。

## Phase 0：项目检查

首先：

1. 检查项目目录。
2. 检查 go.mod。
3. 检查 Go 版本。
4. 检查已有代码。
5. 检查 Docker 环境。
6. 检查项目是否可以编译。

生成 docs/IMPLEMENTATION_PLAN.md。

列出准备实现的模块、依赖关系和预计文件。

然后继续开发，不要只停留在计划。

## Phase 1：系统基础

完成：

- Config。
- Bootstrap。
- Logger。
- Database。
- Migration。
- HTTP Server。
- Health API。
- Graceful Shutdown。

要求：

go build ./...
go test ./...

必须通过。

## Phase 2：核心领域

完成：

- Identity。
- Goal。
- Task。
- Event。
- Audit。

要求：

核心状态转换测试全部通过。

## Phase 3：自主执行控制

完成：

- Planner。
- Supervisor。
- Execution。
- Policy。
- Approval。

要求：

能够实现 Goal → Plan → Task → Execution 的完整流程。

审批必须真正阻止未经批准的工具调用。

## Phase 4：Agent 和模型

完成：

- Agent Registry。
- Agent Runtime Interface。
- Mock Agent。
- Model Gateway。
- Mock Provider。
- OpenAI-compatible Provider。
- Ollama Provider。
- 可选 Eino Adapter。

要求：

没有真实模型 API Key，也能运行完整 Mock 流程。

## Phase 5：工具系统

完成：

- Tool Registry。
- Tool Adapter。
- MCP Client。
- 内置安全工具。

要求：

所有工具执行经过 Policy Engine。

## Phase 6：Memory 和 Knowledge

完成：

- Memory CRUD。
- Memory Search。
- Embedding Interface。
- pgvector。
- Knowledge Document。
- Chunking。
- Retrieval。
- Citation。

## Phase 7：Node 和可观测性

完成：

- Node Registry。
- Node Health。
- Node Capability。
- Observability。
- OpenTelemetry 接口。

## Phase 8：集成和交付

完成：

- 全部单元测试。
- 集成测试。
- E2E 测试。
- Docker Compose。
- Dockerfile（如适用）。
- Makefile。
- README。
- OpenAPI 文档。
- 部署文档。
- 故障排查文档。

---

# 十二、开发工作方式

你必须像一个自主软件工程 Agent 一样工作。

执行规则：

1. 先检查现有项目。
2. 根据现有代码确定实现方案。
3. 不要要求我逐个创建文件。
4. 不要每写完一个文件就向我询问是否继续。
5. 不要只提供代码片段。
6. 直接创建文件和修改文件。
7. 自行执行必要的命令。
8. 遇到编译错误自行修复。
9. 遇到测试失败自行分析和修复。
10. 如果某个第三方库不兼容，选择合理替代方案并记录原因。
11. 不要为了使用某个框架牺牲核心架构。
12. 不要擅自删除已有文件。
13. 不要覆盖用户数据。
14. 不要执行不可逆的数据库删除操作。
15. 不要将真实密钥、令牌或个人数据提交到代码仓库。
16. 不要把未完成的功能标记为完成。
17. 不要用空函数或大量 TODO 冒充实现。
18. 不要在没有测试的情况下声称功能已经可用。

如果某个功能依赖外部账号、API Key 或硬件：

- 先实现接口。
- 实现 Mock。
- 实现配置。
- 提供集成说明。
- 不要伪造真实执行结果。

如果遇到确实需要用户决定的高风险事项，例如删除现有数据、覆盖已有配置、执行外部真实操作，先停止该项操作并请求确认；其他非破坏性开发工作继续推进。

如果一次执行受到上下文或时间限制：

- 保存当前进度。
- 更新 docs/IMPLEMENTATION_STATUS.md。
- 记录已完成模块。
- 记录未完成模块。
- 记录编译和测试结果。
- 记录下一步具体工作。
- 下一次继续时先读取该文件，然后继续开发。

不要因为一次会话结束而认为整个项目完成。

---

# 十三、最终交付标准

最终交付必须包括：

1. 完整 Go 源代码。
2. 数据库迁移文件。
3. Docker Compose。
4. 配置模板。
5. Makefile。
6. OpenAPI 文档。
7. README。
8. 系统架构说明。
9. 模块设计说明。
10. 部署说明。
11. 测试代码。
12. 测试报告。
13. IMPLEMENTATION_STATUS.md。

必须提供以下可执行命令：

make build
make test
make run
make migrate
make docker-up
make docker-down

如果某项命令不适合当前部署方式，必须明确说明。

最终验收：

- Go 项目可以成功编译。
- 数据库可以成功启动。
- 数据库迁移可以执行。
- HTTP API 可以正常启动。
- 可以创建和查询 Goal。
- 可以创建和查询 Task。
- 可以生成结构化 Plan。
- 可以通过 Mock Agent 执行任务。
- 可以使用 Mock Model 完成流程。
- 可以请求人工审批。
- 未审批的高风险工具无法执行。
- 执行状态可以持久化。
- 服务重启后任务可以恢复。
- Memory 可以保存和查询。
- Knowledge 可以导入和检索文本。
- Event 和 Audit 可以记录关键操作。
- 所有测试通过。

完成后，提供：

1. 已实现功能清单。
2. 项目目录变更清单。
3. 实际使用的依赖及版本。
4. 编译结果。
5. 测试结果。
6. 数据库启动方式。
7. 系统启动方式。
8. API 调用示例。
9. 尚未实现的功能。
10. 下一阶段建议。

现在开始执行 Phase 0。

先检查当前仓库，然后直接开始实施。不要只给我一份开发计划。