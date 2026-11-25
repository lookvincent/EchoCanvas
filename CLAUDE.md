# Claude Code 入口指引

本仓库以根目录下的 [AGENTS.md](./AGENTS.md) 作为跨工具唯一的工程规范与工作流契约。

进入本仓库后，请立即按以下顺序行动：
1. 阅读 `AGENTS.md` 中的**绝对工程红线**与**启停协议**；
2. 在终端运行 `python .workflow/scripts/workflow.py status` 与 `python .workflow/scripts/workflow.py ready`；
3. 仅按目标任务的 `context_files` 与最新 `.workflow/handoffs/*.md` 加载最小必要上下文后认领任务开工；
4. 严禁泄露任何 API Key、个人隐私或本机绝对路径；提交与交接前务必执行验证命令、`python .workflow/scripts/workflow.py scan-secrets`、`complete`（如完成）及 `handoff`。
