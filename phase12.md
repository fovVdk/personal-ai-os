Phase 12 — Agent Intelligence & Goal-driven Autonomy
建议进入

目标：让系统从能够执行预先安排的 Task，升级为能够围绕长期 Goal 自主规划、调用工具、评估结果和动态调整计划的系统。

核心工作：
1. Supervisor 自主决策循环
2. Planner 动态规划与重新规划
3. Agent Runtime
4. 模型结构化 Tool Call
5. Tool Gateway 自动调用编排
6. Goal 进度评估与 Evidence 验证
7. 预算、重试、停止条件与人工审批
8. 多 Agent 协作基础机制


这里有一个重要的架构要求：不要让模型直接控制 Task 状态，也不要让模型直接执行工具。

Goal
  ↓
Supervisor
  ↓
Load Context & Evaluate Progress
  ↓
Planner
  ↓
Generate / Update Plan
  ↓
Task Manager
  ↓
Agent Runtime
  ↓
Structured Tool Call
  ↓
Tool Gateway
  ↓
Execution
  ↓
Collect Result & Evidence
  ↓
Evaluate Goal Progress
  ↓
Continue / Replan / Wait Approval / Stop

每一轮决策都应该是可持久化、可恢复、可审计的。模型输出只是候选决策，真正的状态变更必须由 Go Core 完成。


