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

## 4. 严谨任务卡点与自主连续推进协议（`[WF-01]`）

每次执行任务时，**无需在每个原子任务完成后向用户请示确认**，严格按以下“**卡点验收 → 自动推进**”闭环连续执行：

1. **前置核查与真实验证**：代码与测试全部完成后，必须运行可执行任务卡点：
   ```bash
   python .workflow/scripts/workflow.py verify-task <TASK_ID>
   ```
   该命令强制依次校验前置依赖、`scan-secrets` 零泄露、必交文件清单（`required_files`）、全部 `acceptance_cmds`（`go vet` / `go test` / `pytest` / `build`）以及防空测试校验。
2. **闭环当前任务**：`complete` 已强制绑定 `verify-task`，只有卡点全部通过才会允许标记 `done` 并自动打印下一批可执行任务：
   ```bash
   python .workflow/scripts/workflow.py complete <TASK_ID> --evidence "<实际运行的命令及关键通过输出>"
   ```
3. **沉淀决策与原子提交**：若本轮遇到环境坑或新增技术权衡，追加记录到 `.workflow/decisions.md`（严守 P0 隐私红线）；随后检查 `git status` 并完成原子提交（`feat/fix/docs/chore(scope): ...`）。
4. **自动连续推进下一个任务**：若 `complete` 输出 `[AUTO-ADVANCE]` 列出了无阻塞任务（或当前里程碑全部完成则先执行 `verify-gate <STAGE> --update-state`），直接认领下一个任务继续推进，直至本里程碑闭环或遇到以下暂停条件：
   * 同一任务卡点连续修复 **2 次**仍失败；
   * 缺失外部真实密钥/凭证或需要调整已冻结的 `ADR-01~04`；
   * 涉及不可逆高风险远端操作。
5. **切换工具/暂停前的交接快照**：当需要切换电脑、切换模型或结束当前里程碑时，生成结构化交接快照并推送：
   ```bash
   python .workflow/scripts/workflow.py handoff \
     --agent "<tool>" \
     --model "<model>" \
     --summary "<本轮完成事项与验证证据>" \
     --next "<下一任Agent建议先做的任务与切入点>" \
     --pitfalls "<已知阻塞或注意事项，可选>"
   ```

---

## 5. 里程碑硬门禁（Stage-Gate）

每当一个阶段（`S2` / `S3` / `M1` / `M2` / `M3` / `M4`）收尾时，**必须运行可执行门禁脚本**（已自动内置 `scan-secrets` 审计与全量测试）：

```bash
python .workflow/scripts/workflow.py verify-gate <STAGE> --update-state
```

**脚本返回非 0 退出码时，严禁宣布该里程碑完成或推进下一阶段。**
