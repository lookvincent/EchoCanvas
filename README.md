# EchoCanvas（浮生绘卷）

> 基于 **Go 控制面网关 + Python LangGraph Sidecar** 协同架构的多模态视觉叙事与 AIGC 工程编排平台。

## 项目定位

EchoCanvas 致力于解决多模态连续视觉叙事中**“长耗时异步渲染易中断”**、**“跨镜头角色与场景易漂移穿帮”**及**“多模型调用成本与稳定性不可控”**三大真实工程痛点，提供支持 3 个人机协同卡点（资产锁定 / 脚本审阅 / 单帧局部重绘）的生产级工作台。

## 核心架构速览

* **控制面与 AIGC 网关（`services/go-gateway`）**：Go 1.23+ / Gin / pgx / Asynq / OpenTelemetry，负责 REST & SSE 推流、异步调度、令牌桶限流、主备熔断、Pre-flight 预算控制及 `usage_ledger` 成本账单。
* **AI 编排 Sidecar（`services/py-agent`）**：Python 3.11+ / FastAPI / LangGraph / Pydantic v2 / LiteLLM / Langfuse，负责有状态工作流、分层记忆一致性控制、生图适配与 VLM 质检 / Eval。
* **存储与消息层（`deploy/`）**：PostgreSQL 16（含 `pgvector` 与 LangGraph Checkpointer）+ Redis 7 + 对象存储（MinIO / OSS）。
* **前端工作台（`apps/web`）**：Next.js + TailwindCSS。

## 核心文档导航

| 文档 | 说明 |
| :--- | :--- |
| [docs/01-CHARTER.md](./docs/01-CHARTER.md) | 项目立项书、大厂 Stage-Gate 研发流程、30 天排期与风险矩阵 |
| [docs/02-PRD.md](./docs/02-PRD.md) | 完整产品需求、3 个 HITL 流程、`FR-A~FR-H` 需求清单与验收标准 |
| [docs/03-TDD.md](./docs/03-TDD.md) | 总体技术方案、时序图、8 张 PG16 DDL 表、接口契约、Eval 与 ADR-01~04 |
| [.workflow/README.md](./.workflow/README.md) | 自包含多 Agent 交接工作流与 CLI 使用手册 |
| [AGENTS.md](./AGENTS.md) | 跨工具 / 跨模型统一工程契约 |

## 跨电脑 / 跨 Agent 续做指南（3 步接力）

在任意新电脑拉取本仓库或切换任意 Coding Agent（Antigravity / Claude Code / Codex / Cursor）后，只需运行：

```bash
# 1. 查看当前阶段、阻塞关系与最新交接快照路径
python .workflow/scripts/workflow.py status

# 2. 查看当前前置依赖已满足的可执行原子任务
python .workflow/scripts/workflow.py ready

# 3. 认领任务开始研发
python .workflow/scripts/workflow.py claim T-M1-01 --agent "codex/gpt-5"
```

## 当前研发进度

* **已完成**：`S1 立项`、`S2 PRD 评审`、`S3 TDD 评审`
* **当前阶段**：`S4 迭代研发联调` → **M1（Go 控制面与 AIGC 网关底座）待启动**
