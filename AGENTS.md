# EchoCanvas Agent Engineering Contract (`AGENTS.md`)

> 本文件是 **EchoCanvas** 仓库的跨 Agent 工具通用行为契约（适用于 Antigravity、Codex、Claude Code、Cursor、Gemini CLI、OpenCode 等任意模型与终端 Harness）。
> 无论你在哪台电脑、切换到哪个模型或工具，**开工前必须完整遵循本契约**。

---

## 1. 项目定位与技术栈总览

* **项目名称**：EchoCanvas（浮生绘卷）—— 多模态视觉叙事与 AIGC 工程编排平台
* **核心架构**：
  * **控制面与 AIGC 网关**：`services/go-gateway`（Go 1.23+ / Gin / pgx / Asynq / slog + OpenTelemetry）
  * **AI 编排与多模态 Sidecar**：`services/py-agent`（Python 3.11+ / FastAPI / LangGraph / `langgraph-checkpoint-postgres` / Pydantic v2 / LiteLLM / Langfuse）
  * **存储与消息层**：PostgreSQL 16（`pgvector`）+ Redis 7 + 对象存储（MinIO / OSS）
  * **前端交互层**：`apps/web`（Next.js + TailwindCSS）
* **权威事实源（SSoT）**：
  * 需求边界见 `docs/02-PRD.md`（需求 ID `FR-A` ~ `FR-H`，人机卡点 `Gate 1/2/3`）
  * 架构、DDL、API 契约与 ADR 见 `docs/03-TDD.md`
  * 当前进度、任务依赖图与交接快照见 `.workflow/`

---

## 2. 绝对工程红线（Guardrails）

1. **【P0 最高红线】密钥与个人隐私零泄露（面向公开开源）**：
   * 本项目将在研发完成后面向 GitHub 公开发布。**严禁**在任何代码、配置文件、文档、测试夹具、交接快照（`.workflow/handoffs/`）或 Git Commit Message 中写入任何真实 API Key、Access Token、数据库密码、私钥证书或未脱敏的 `.env` 文件。
   * **严禁**写入个人真实姓名、就职公司名称、工作邮箱、公司内网域名/IP、业务敏感信息或本机绝对路径（例如包含操作系统用户名的本地目录路径；文档与代码内统一只使用仓库相对路径）。
   * 每次提交前**必须**执行 `python .workflow/scripts/workflow.py scan-secrets` 且确认返回 0 告警（`[SCAN PASS]`）。
2. **严守 ADR-01 ~ ADR-04，禁止过度设计**：
   * 禁止在未获批准的情况下引入 gRPC/Protobuf、Milvus、Neo4j、Kafka 或 K8s。
   * Go 与 Python 间统一采用 **内部 HTTP/JSON + 共享 PostgreSQL / Redis**。
3. **AIGC 出口唯一性**：
   * Python Sidecar 禁止直连外部商业 LLM / 生图 API；所有模型请求必须经过 Go 网关代理并记录到 `usage_ledger`，确保限流、熔断与预算熔断唯一真实。
4. **禁止编造虚标数据与跳过测试**：
   * 禁止编造任何未实测的准确率、耗时或可用性百分比；禁止用假断言或删除测试来凑通过。
5. **Git 安全操作**：
   * 禁止擅自 `git push --force`、硬重置他人分支或提交未经脱敏检查的文件。

---

## 3. 强制接力启动协议（Session Boot Protocol）

每次开启新会话（或换电脑 `git pull`、换模型后），**必须按顺序执行以下 3 步**再写第一行代码：

1. **读取全局状态**：
   ```bash
   python .workflow/scripts/workflow.py status
   ```
   确认当前所处阶段（`current_stage` / `current_milestone`）、是否存在未完成的 `in_progress` 任务以及最新交接文件。
2. **上下文防火墙（严禁全仓盲读）**：
   * 只读取最新一份 `.workflow/handoffs/*.md`、`.workflow/decisions.md`，以及目标任务在 `tasks.jsonl` 中声明的 `context_files` 相关章节。
   * 禁止为了“了解全貌”把全部历史交接或无关文档一次性灌入上下文。
3. **检查并认领单个原子任务**：
   ```bash
   python .workflow/scripts/workflow.py ready
   python .workflow/scripts/workflow.py claim <TASK_ID> --agent "<tool>/<model>"
   ```

---

## 4. 强制交接封箱协议（Session Handoff Protocol）

每次会话结束、准备切换模型/工具或暂停工作前，**必须完成以下 5 步**：

1. **真实验证**：运行当前任务对应的 `verify_cmd`（或对应模块的构建、单元测试、Lint 命令），确保取得真实的通过输出。
2. **更新任务账本**：
   ```bash
   python .workflow/scripts/workflow.py complete <TASK_ID> --evidence "<实际运行的命令及关键通过输出>"
   ```
   *(若任务未完成即交接，请勿调用 `complete`，而是在下面的 `handoff` 中写清未完成断点。)*
3. **沉淀决策与踩坑**：若本轮修改了约定、遇到环境坑或新增技术权衡，追加记录到 `.workflow/decisions.md`（注意同样遵守 P0 隐私零泄露红线）。
4. **执行密钥与隐私扫描**：
   ```bash
   python .workflow/scripts/workflow.py scan-secrets
   ```
   必须返回 `[SCAN PASS]` 后方可进入下一步提交。
5. **生成交接快照并提交 Git**：
   ```bash
   python .workflow/scripts/workflow.py handoff \
     --agent "<tool>" \
     --model "<model>" \
     --summary "<本轮完成事项与验证证据>" \
     --next "<下一任Agent建议先做的任务与切入点>" \
     --pitfalls "<已知阻塞或注意事项，可选>"
   ```
   随后执行 `git status` 检查变更范围，用规范提交信息（`feat/fix/docs/chore(scope): ...`）提交到本地仓库（远端推送需由用户确认）。

---

## 5. 里程碑硬门禁（Stage-Gate）

每当一个阶段（`S2` / `S3` / `M1` / `M2` / `M3` / `M4`）收尾时，**必须运行可执行门禁脚本**（已自动内置 `scan-secrets` 审计）：

```bash
python .workflow/scripts/workflow.py verify-gate <STAGE>
```

**脚本返回非 0 退出码时，严禁宣布该里程碑完成或推进下一阶段。**
