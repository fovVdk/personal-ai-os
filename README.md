# Personal AI OS

Personal AI OS 是一个以 Goal 为最高管理对象的本地优先 Go 模块化单体。当前仓库实现的是可编译、可测试的 V1 核心垂直切片，不代表设计清单中的所有能力均已交付。本文说明**实际运行的设计与边界**；未实现项请勿视为可用功能。

Personal AI OS is a local-first Go modular monolith with Goals as the top-level management object. This repository contains an executable, tested V1 slice—not every capability in the broader design. This README documents the **current behavior and boundaries**, not future promises.

## 设计目标与不变量 | Goals and invariants

- **Goal-first / 目标优先：** Goal、Task 与执行记录各有独立 UUID 和持久化生命周期；聊天会话不是任务身份。
- **Go Core 控制 / Core owns control:** Go 服务控制领域状态、策略、审批和执行账本。模型及 Agent 只能提供候选计划/结果，不能直接改数据库或授予权限。
- **Evidence-gated completion / 证据门控完成：** Goal 完成请求必须携带可追溯 Evidence。Mock Agent 的结果标记为未验证，不能伪装成任务完成。
- **Policy before side effects / 副作用前先鉴权：** A0–A3 Policy 与参数哈希审批门在工具调用前判断。当前没有启用的副作用工具执行器，所以审批 API 管理授权记录，但尚未连接真实工具执行流程。
- **Persisted state / 状态持久化：** PostgreSQL 保存 Goal、Task、审批、执行结果、Evidence、Outbox、审计、Memory、Knowledge、Node 和模型用量记录。
- **Modular monolith / 模块化单体：** 当前是一个 Go HTTP 程序；领域层依赖本地接口，数据库和 Provider 由适配器实现，避免为 V1 过早拆分服务。

## 架构与依赖方向 | Architecture

```text
HTTP / Chi (internal/api)
				│ calls domain contracts
				▼
Goal · Task · Planner · Supervisor · Policy · Approval · Agent Runtime · Model Gateway
				│ injected interfaces
				▼
PostgreSQL adapters · Model Providers · structured logger
```

Goal 的规划/执行关系为 `Goal → structured Plan → Task DAG → Agent/Model proposal → Execution record → Evidence`。目前 Subgoal/Milestone 仅由 Goal 的父子关系和计划结构预留表达，不存在独立完整的 Subgoal/Milestone 管理 API；Tool 和 MCP 也尚无可调用实现。

### 模块职责与当前状态 | Modules and status

| Package | 职责 | 当前状态 |
| --- | --- | --- |
| `internal/api` | Chi 路由、中间件、JSON 输入输出 | 已实现当前列出的 HTTP 路由 |
| `internal/goal`, `internal/task` | 领域模型、校验、状态转换、Repository 契约 | 已实现核心状态规则及 PostgreSQL 持久化 |
| `internal/planner` | 结构化计划、JSON 校验、依赖 DAG 校验 | Mock 与 Model Planner 已接入 |
| `internal/supervisor` | 预算内规划/任务调度、执行账本及恢复协调 | 已实现有界 turn；没有后台恢复 worker |
| `internal/policy`, `internal/approval`, `internal/tool/policy` | 自主等级决策与参数绑定审批门 | 策略与审批门已实现并测试；没有生产工具执行器 |
| `internal/agent/runtime`, `registry` | 框架无关 Agent 接口和注册信息 | 已实现 Mock General/Research/Planning Agent；无 Eino Adapter |
| `internal/model/gateway`, `provider` | 统一模型契约、预算/Schema 校验及 Provider | Mock、OpenAI-compatible、Ollama；工具调用仅解析为提议 |
| `internal/memory` | 带来源/置信度的记忆模型与 API | PostgreSQL CRUD、软删除和全文搜索 |
| `internal/knowledge` | 文档、内容哈希、分块与引用 | 文本导入及 PostgreSQL 全文检索；未生成 Embedding |
| `internal/node` | 节点身份、能力、状态 | 注册/查询 API 与存储；无独立心跳 HTTP 路由/过期 worker |
| `internal/app/store` | PostgreSQL Repository、事务及持久化适配器 | 已实现当前切片的适配器 |
| `internal/event`, `internal/audit` | 事件与审计领域边界 | Goal/Task/审批写入事务性 Outbox 和审计记录；无发布 worker/查询 API |
| `internal/identity`, `internal/tool/mcp`, `internal/observability` | 扩展边界 | 多用户/RBAC、MCP、OpenTelemetry 尚未完整实现；认证当前为共享 API Key |

领域包位于 `internal/`，不能被外部 Go 模块直接导入。`pkg/contracts/` 预留稳定跨进程契约，不应将内部领域结构直接用作 wire schema。Framework/Provider 特定类型应留在 Adapter 边界，不能渗透到 Goal、Task、Policy 等领域模型。

## 关键请求与执行流程 | Request and execution flow

1. 进程读取环境变量、建立 pgx pool，应用迁移，然后组装 Repository、Model Gateway、Planner、Agent Registry、Supervisor 与 Chi Server。
2. HTTP 请求经 Request ID、请求日志、恢复、超时和 API Key 中间件处理。`/health` 检查进程存活；`/ready` 检查数据库连接。
3. Planner 生成结构化计划。模型输出使用严格 JSON 解码并验证字段、Goal 归属、预算和依赖 DAG；计划中的任务通过一个数据库事务创建。
4. Supervisor 对单次 turn 施加步骤数、模型调用、Token、工具调用、成本、重试和运行时间预算，并优先执行依赖已满足的任务。模型或 Mock Agent 不能绕过 Go Core 状态管理。
5. 执行结果、Evidence、Task 状态、审计和 Outbox 由 Go Core/存储适配器持久化。未验证结果不会冒充完成。恢复逻辑在下一次相关 Supervisor turn 中检查部分 `running`/`retrying` 状态及 Evidence 已保存但 Task 状态尚未更新的窗口；当前没有常驻恢复进程或外部副作用前的持久幂等 claim。
6. 对需要审批的工具动作，授权应绑定 Tool、Action、具体参数哈希和有效期。当前 API 可创建/批准/拒绝请求，策略门可验证匹配审批，但尚未存在实际的 side-effect tool runner。

## PostgreSQL 数据设计 | Persistence

迁移文件按 `0001`–`0007` 顺序嵌入二进制，应用时使用 PostgreSQL advisory lock、事务及迁移内容校验和。迁移只向前演进，不应手工删除版本记录或在生产库上试验。

| Migration | 主要对象 |
| --- | --- |
| `0001_core.sql` | `users`（含本地 owner）、`goals`、`tasks`、`task_dependencies` |
| `0002_execution_approval_memory.sql` | `approvals`、`execution_runs`、`execution_evidence`、`event_outbox`、`audit_records`、`memories` |
| `0003_goal_evidence.sql` | `goal_evidence` |
| `0004_knowledge_vectors.sql` | `vector` 扩展、`knowledge_documents`、`knowledge_chunks.embedding`（接口/列已预留，尚未接入 embedding 检索） |
| `0005_task_attempts.sql` | Task 持久化尝试计数 |
| `0006_nodes.sql` | `nodes` |
| `0007_model_usage.sql` | 按 Goal 累计模型调用和 Token 的 `model_usage` 账本 |

主键使用 UUID，业务时间使用 `timestamptz`，可扩展字段使用 JSONB。Goal/Task 创建、修改和转换以及审批决策通过事务写入相关审计/Outbox 记录。Knowledge 和 Memory 当前使用 PostgreSQL 全文检索，不是向量检索。

## HTTP API

完整机器可读描述见 [docs/openapi.yaml](docs/openapi.yaml)。除公开的 Health 路由外，当前 `/api/v1` 路由均受共享 API Key 保护：`X-API-Key` 或 `Authorization: Bearer ...`。没有设置 `PAIOS_API_KEY` 时认证关闭，但服务仅允许绑定 loopback；production 必须配置至少 32 字符的 Key。请求体拒绝未知 JSON 字段，大小限制为 1 MiB；错误采用 `{ "error": { "code", "message", "request_id" } }`。

| 方法 | 路径 | 功能 |
| --- | --- | --- |
| GET | `/health`, `/ready` | 存活及数据库就绪探针（无需 Key） |
| POST/GET | `/api/v1/goals` | 创建/列出 Goal |
| GET/PATCH | `/api/v1/goals/{id}` | 读取/修改 Goal |
| POST | `/api/v1/goals/{id}/activate`, `/pause`, `/resume`, `/complete` | Goal 状态操作；完成需 Evidence |
| POST/GET | `/api/v1/tasks` | 创建/按 Goal 列出 Task |
| GET | `/api/v1/tasks/{id}` | 读取 Task |
| POST | `/api/v1/tasks/{id}/pause`, `/resume`, `/retry`, `/cancel` | Task 状态操作 |
| POST | `/api/v1/planner/generate` | 生成并持久化结构化计划及任务 |
| POST/GET | `/api/v1/supervisor/turn`, `/api/v1/supervisor/status` | 执行有界 Goal turn / 查看本地协调器状态 |
| POST/GET | `/api/v1/approvals` | 创建审批 / 列出待审批记录 |
| GET | `/api/v1/approvals/{id}` | 查询审批 |
| POST | `/api/v1/approvals/{id}/approve`, `/reject` | 决策审批 |
| GET | `/api/v1/agents`, `/api/v1/agents/{id}` | 列出/查看注册的 Mock Agent |
| POST/GET | `/api/v1/memories` | 创建/列出记忆 |
| GET/PATCH/DELETE | `/api/v1/memories/{id}` | 读取/修改/软删除记忆 |
| POST | `/api/v1/memories/search` | 关键词全文搜索 |
| POST/GET | `/api/v1/knowledge/documents` | 导入文本/列出文档 |
| POST | `/api/v1/knowledge/search` | 全文检索并返回引用 |
| GET | `/api/v1/nodes`, `/api/v1/nodes/{id}` | 列出/读取节点 |
| POST | `/api/v1/nodes/register` | 注册节点 |

当前没有 Task PATCH、工具清单/调用 API、Node 心跳 API、OpenAPI UI 或完整 Subgoal/Milestone CRUD。

## 配置与安全 | Configuration and security

运行时**读取环境变量**；`configs/config.example.yaml` 仅作参考模板，当前不由程序解析。`.env.example` 也只是模板。`DATA_DIR` 和 `FILE_STORAGE_DIR` 当前被读取为配置值，但文本 Knowledge 内容存于数据库，尚无文件存储 Adapter。

| 环境变量 | 默认值/用途 |
| --- | --- |
| `APP_ENV`, `APP_HOST`, `APP_PORT` | `development`, `127.0.0.1`, `8080` |
| `DATABASE_URL` | PostgreSQL 连接串；必须提供 |
| `LOG_LEVEL` | `info`；JSON 结构化日志 |
| `PAIOS_API_KEY` | 空表示关闭认证；生产环境必填且至少 32 字符 |
| `MODEL_PROVIDER` | `mock`；另支持 `openai-compatible`、`ollama` |
| `MODEL_BASE_URL`, `MODEL_API_KEY`, `MODEL_NAME` | OpenAI-compatible Provider 配置；Secret 只从环境变量读取 |
| `OLLAMA_BASE_URL` | `http://localhost:11434` |
| `DATA_DIR`, `FILE_STORAGE_DIR` | 配置占位；目前未被文件存储流程使用 |
| `MAX_AGENT_STEPS`, `MAX_MODEL_CALLS`, `MAX_MODEL_TOKENS`, `MAX_TOOL_CALLS` | Supervisor 单次 Goal turn 预算 |
| `MAX_EXECUTION_SECONDS`, `MAX_EXECUTION_COST`, `MAX_RETRIES` | 执行时间、成本和重试预算 |
| `POSTGRES_PASSWORD` | Compose 数据库密码；请修改默认值 |
| `PAIOS_DATA_DIR`, `OLLAMA_DATA_DIR` | Compose 主机持久化路径 |

不要提交真实 Key、密码、用户数据或 `.env`。Compose 将 PostgreSQL/Ollama 端口绑定到 loopback；数据库默认数据目录为仓库 `data/postgres`，可在 `.env` 中改为外置 SSD 绝对路径。Ollama 需用 Compose 的 `ollama` profile 显式启动；不会自动下载模型。对外暴露 API 前应配置 TLS/反向代理和强 API Key，并限制网络访问。

## 本地运行 | Local development

Requirements: Go 1.22+（`go.mod` 语言目标为 1.22）、Docker Compose、PostgreSQL 16 + pgvector。依赖版本固定在 `go.mod`/`go.sum`。若仓库位于 GOPATH 目录中，Makefile 使用外部临时 GOPATH 确保启用 Go Modules。

1. 从 `.env.example` 复制为 `.env`，修改 `POSTGRES_PASSWORD`；如需 API Key，配置至少 32 字符的 `PAIOS_API_KEY`。
2. `make docker-up` 启动 PostgreSQL；Compose 需先拉取 `pgvector/pgvector:pg16` 镜像。
3. `make migrate` 手动应用迁移（服务启动时也会自动应用待执行迁移）。
4. `make run` 启动 API；使用 Mock Model 不需云端凭据。
5. 访问 `http://localhost:8080/health` 和 `http://localhost:8080/ready`。

Make targets：`make build`、`make test`、`make vet`、`make fmt`、`make run`、`make migrate`、`make docker-up`、`make docker-down`。`make run` 与 `make migrate` 需要 PostgreSQL；`make docker-*` 需要 Docker，并要求先创建 `.env`。

## API 快速示例 | API example

创建 Goal（若设置了 Key，请先在 shell 环境中导出 `PAIOS_API_KEY`）：

```sh
curl -sS http://localhost:8080/api/v1/goals \
	-H 'Content-Type: application/json' \
	-H "X-API-Key: ${PAIOS_API_KEY}" \
	-d '{"title":"Plan a home automation system","priority":4,"success_criteria":["A reviewed phased plan exists"]}'
```

把响应中的 Goal UUID 传给 Planner：

```sh
curl -sS http://localhost:8080/api/v1/planner/generate \
	-H 'Content-Type: application/json' \
	-H "X-API-Key: ${PAIOS_API_KEY}" \
	-d '{"goal_id":"<goal-uuid>"}'
```

Mock 配置返回确定性结构化计划；配置真实 Provider 时 Planner 请求模型生成并校验计划。Supervisor 的 Mock Agent 输出未经独立验证，不会谎称任务或 Goal 已完成；这类结果可能以 HTTP 409 表示未能验证完成。

## 测试、验证与已知限制 | Tests and limitations

单元测试覆盖 Goal/Task 状态转换、Evidence 门控、A0–A3 Policy、审批哈希和过期、重试/并发幂等基础能力、工具审批门、配置校验、HTTP Handler、计划 DAG、Provider/Tool-call Schema、Knowledge 分块等。PostgreSQL 集成测试存在于 `tests/integration/`，只有设置专用 `PAIOS_TEST_DATABASE_URL` 时才运行；请只使用可丢弃测试库。当前没有覆盖完整真实模型 E2E、全部领域集成和所有故障注入场景的测试套件。

当前已验证 `make fmt`, `make test`, `make build`, `make vet`, `go test -race ./...`，以及 Compose/YAML 配置解析。最近的工作环境中 PostgreSQL 集成测试和迁移未实际执行：`pgvector/pgvector:pg16` 镜像拉取未能完成。请参阅 [docs/TEST_REPORT.md](docs/TEST_REPORT.md) 和 [docs/IMPLEMENTATION_STATUS.md](docs/IMPLEMENTATION_STATUS.md) 获取环境相关验证及未完成清单。较早的 [docs/IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md) 是 Phase 0 基线记录，不等于当前完成状态。

主要尚未实现或不完整的能力：Eino、可执行内置工具/MCP、工具审批到真实执行器的连接、持久化外部副作用前幂等 claim、后台恢复 worker、逐步 Checkpoint、Memory/Knowledge Embedding 与向量检索、RBAC/API Token、文件存储、Outbox 发布 worker、Audit 查询/保留策略、OpenTelemetry、Node 心跳 API、完整数据库集成和端到端故障恢复测试。

## 依赖边界与变更原则 | Contribution boundaries

- 核心领域保持框架无关；Agent 框架、模型供应商 SDK 和 MCP 类型只能位于适配层。
- 模型 Tool Call 是待授权的提议，不是命令；新增副作用工具必须接入 Policy、参数绑定 Approval、审计和持久化执行保护后才可启用。
- 所有外部操作采用 `context.Context`、明确超时及可取消边界；新增数据库对象通过向前迁移维护。
- 测试贴近所属包；跨模块数据库测试放入 `tests/integration/`，E2E 放入 `tests/e2e/`。
- 每次报告功能状态时区分已实现、仅有接口/迁移预留及未实现，不能把未来设计写成现有能力。
