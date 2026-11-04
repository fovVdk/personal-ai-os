# Personal AI OS — Phase 9：数据库集成验证与持久化验收

当前项目已经完成 V1 核心切片。

本阶段不增加新的业务功能，重点是完成 PostgreSQL 真实环境验证，确保现有核心系统能够可靠地运行在持久化数据库上。

请首先阅读：

- AGENTS.md
- docs/IMPLEMENTATION_PLAN.md
- docs/IMPLEMENTATION_STATUS.md
- TEST_REPORT.md
- 所有数据库 Migration 文件
- 当前 Docker Compose 配置

遵循现有架构，不要重新设计整个项目。

## 一、建立真实 PostgreSQL 环境

1. 检查当前 Docker 环境。
2. 检查 Docker Compose 配置。
3. 检查 pgvector/pgvector:pg16 镜像拉取失败的原因。
4. 尝试重新拉取镜像。
5. 如果镜像拉取仍然失败，分析网络、镜像源和本地 Docker 配置问题。
6. 不要未经验证就更换数据库镜像或数据库版本。
7. 启动 PostgreSQL。
8. 验证数据库连接和 pgvector 扩展。

如果当前环境无法启动数据库，明确记录具体错误和替代验证方案，不得将未运行的测试标记为通过。

## 二、执行全部数据库 Migration

使用真实 PostgreSQL：

1. 从空数据库执行全部 Migration。
2. 检查 Migration 执行顺序。
3. 检查是否存在重复创建、遗漏或不兼容操作。
4. 检查外键、唯一约束、索引和字段类型。
5. 验证 Migration 是否具备合理的事务保护。
6. 测试 Migration 重复执行时的行为。
7. 验证数据库版本记录是否正确。

## 三、核心业务数据库集成测试

针对以下模块编写或完善真实数据库集成测试：

- Goal
- Task
- Execution
- Evidence
- Approval
- Policy
- Audit
- Event
- Outbox
- Memory
- Knowledge
- Node

重点测试：

1. 数据创建、查询、更新和删除。
2. 外键约束。
3. 事务回滚。
4. 并发更新。
5. 唯一约束。
6. 关联数据一致性。
7. 数据库错误处理。
8. 时间戳与状态转换。

不要用 Mock Repository 替代本阶段要求的数据库集成测试。

## 四、验证事务一致性

重点验证以下操作：

Goal/Task 相关业务变更
→ Execution 记录
→ Audit 记录
→ Outbox 事件。

检查这些记录是否按照设计在同一事务中提交。

如果其中某个关键步骤失败，验证是否发生不完整提交。

特别检查：
- 业务记录已经提交但 Outbox 没有写入。
- Task 状态已经更新但 Execution 记录缺失。
- Approval 状态已经改变但审计记录缺失。

## 五、数据库重启与持久化测试

执行：

1. 创建 Goal。
2. 创建多个 Task。
3. 创建 Execution。
4. 写入 Evidence。
5. 创建 Approval。
6. 写入 Audit 和 Outbox。
7. 停止 PostgreSQL。
8. 重启 PostgreSQL。
9. 重新启动 Go 服务。
10. 查询所有数据并验证状态。

确认系统没有因为服务重启而丢失已有状态。

## 六、完善测试工具

根据当前项目情况增加：

- Makefile 数据库启动命令。
- 数据库迁移命令。
- 数据库集成测试命令。
- 数据库清理命令。
- 测试环境配置。
- 必要的测试辅助代码。

注意：测试清理只能作用于专门的测试数据库，不得误删用户数据。

## 七、执行完整测试

至少执行：

- go test ./...
- go test -race ./...
- go vet ./...
- make build
- make test
- make vet
- 数据库 Migration 测试
- 数据库集成测试
- Docker Compose 配置检查

## 八、输出报告

创建：

docs/DATABASE_INTEGRATION_REPORT.md

记录：

1. 数据库环境。
2. 镜像和版本。
3. Migration 执行结果。
4. 核心模块集成测试结果。
5. 事务一致性测试结果。
6. 重启恢复测试结果。
7. 发现的问题。
8. 修复记录。
9. 尚未验证的内容。

同时更新 docs/IMPLEMENTATION_STATUS.md。

本阶段完成后暂停，不要继续开发 MCP、Eino、Embedding 或其他新功能，等待下一步指令。

所有结论必须有真实测试证据。