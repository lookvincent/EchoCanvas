# EchoCanvas Git-Native 多 Agent 交接工作流手册

## 设计原则

本工作流旨在解决**跨电脑切换、跨模型/跨工具接力（Antigravity / Claude Code / Codex / Cursor）时的上下文丢失与流程失控**问题：

1. **Git 自包含单一事实源**：所有阶段状态、任务依赖图、关键决策和交接快照全部保存在 `.workflow/` 下随代码库一起版本控制，不依赖任何云端账号或本地聊天记录。
2. **上下文防火墙**：每个任务显式声明最小必要 `context_files`，新会话只需读取最新交接文档 + 决策账本 + 当前任务指定文档章节即可开工，防止长上下文污染。
3. **可执行硬门禁**：里程碑准出不再依赖模型口头承诺，必须通过 `workflow.py verify-gate` 脚本校验文件、DDL 与测试退出码。

---

## 目录结构

```text
.workflow/
├── README.md                  # 本手册
├── state.json                 # 全局阶段、里程碑、各 Gate 状态与最新交接指针
├── tasks.jsonl                # 原子任务与前置依赖图 (depends_on)
├── decisions.md               # 架构决策 (ADR) 与新增踩坑账本
├── handoffs/
│   ├── TEMPLATE.md            # 标准交接模板
│   └── YYYY-MM-DD_NNN_*.md    # 每次会话的交接快照
└── scripts/
    └── workflow.py            # 零第三方依赖 Python 3 工作流 CLI
```

---

## 常用 CLI 命令速查

以下命令均在仓库根目录执行（纯 Python 标准库，无需激活虚拟环境）：

### 1. 查看全局状态（每次开新会话第一步）
```bash
python .workflow/scripts/workflow.py status
```

### 2. 查看当前无阻塞可执行任务
```bash
python .workflow/scripts/workflow.py ready
```

### 3. 认领单个原子任务
```bash
python .workflow/scripts/workflow.py claim T-M1-01 --agent "codex/gpt-5"
```

### 4. 完成任务并记录真实证据
```bash
python .workflow/scripts/workflow.py complete T-M1-01 --evidence "docker compose up -d && psql verified 8 tables"
```

### 5. 提交前执行密钥与隐私零泄露审计（必跑）
```bash
python .workflow/scripts/workflow.py scan-secrets
```

### 6. 生成交接快照（换电脑或切换 Agent 前必执行）
```bash
python .workflow/scripts/workflow.py handoff \
  --agent "antigravity" \
  --model "gemini-pro" \
  --summary "完成 PG16 + Redis Docker Compose 与 8 张业务表初始化" \
  --next "下一步认领 T-M1-02 初始化 Go 控制面项目" \
  --pitfalls "Windows 下 psql 注意使用 UTF-8" \
  --files "deploy/docker-compose.yml,deploy/sql/001_init.sql"
```

### 7. 执行里程碑硬门禁检查（自动包含 `scan-secrets`）
```bash
python .workflow/scripts/workflow.py verify-gate S2
python .workflow/scripts/workflow.py verify-gate S3
python .workflow/scripts/workflow.py verify-gate M1
```
