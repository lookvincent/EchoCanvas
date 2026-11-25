# 会话交接快照模板（Handoff Template）

* **交接编号**：`YYYY-MM-DD_NNN`
* **交接时间**：`YYYY-MM-DDTHH:MM:SS+08:00`
* **移交 Agent / 模型**：`<tool>/<model>`
* **当前阶段与里程碑**：`S4 / M1`

---

## 1. 本轮已完成事项与真实产出
- [ ] 任务 ID 与改动文件路径

## 2. 已运行的验证命令与实测结果
```bash
# 贴入实际运行的命令和关键通过输出，禁止口头声称已验证
python .workflow/scripts/workflow.py scan-secrets
```

## 3. 未完成断点与已知踩坑（Pitfalls）
- 如有中断点、临时 Mock 或环境问题，在此如实写清

## 4. 下一任 Agent 必读 Top-3 文件（上下文防火墙）
1. `<最新交接文件>`
2. `.workflow/decisions.md`
3. `<对应任务的 context_files>`

## 5. 下一步推荐执行命令
```bash
python .workflow/scripts/workflow.py ready
python .workflow/scripts/workflow.py claim <NEXT_TASK_ID> --agent "<your_tool>/<your_model>"
```
