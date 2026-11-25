# 工程决策与避坑账本（Decisions & Pitfalls Log）

> 所有跨会话不可推翻的技术决策与开发过程中踩到的坑必须记录于此。每次切换新 Agent 或新模型时，先读本文件，避免重复踩坑或擅自推翻既定架构。

---

## 一、 已冻结核心架构决策（来自 `docs/03-TDD.md` 第八章）

* **[ADR-01] 双语言协同边界**：采用 **Go 控制面与 AIGC 网关（`services/go-gateway`）+ Python AI Sidecar（`services/py-agent`）**，不改为纯 Python 单体，也不在 Go 侧手写 LangGraph/Prompt 逻辑。
* **[ADR-02] 跨进程通信协议**：Go ↔ Python 采用 **内部 HTTP/JSON（OpenAPI/JSON Schema 约束）+ 共享 PostgreSQL 16 / Redis 7**；30 天 MVP 阶段禁止引入 gRPC/Protobuf 增加联调复杂度。
* **[ADR-03] 存储单库收敛**：采用 **PostgreSQL 16（含 `pgvector`）+ Redis 7 + MinIO/OSS** 一套体系搞定业务状态、LangGraph Checkpointer、向量检索与实体状态快照，禁止额外引入 Milvus 或 Neo4j。
* **[ADR-04] 外部模型出口唯一收口**：Python Sidecar 不直接持有商业 LLM / 生图 API Key，所有外部模型调用**必须统一经由 Go 网关代理端点**，确保限流、主备熔断、Pre-flight 预算检查与 `usage_ledger` 记账为唯一真实来源。

---

## 二、 安全、业务与工作流强制约束

* **[SEC-01] 公开仓密钥与隐私零泄露红线**：本项目将在完成后开源公开。严禁在代码、文档、配置、测试夹具、`.workflow/handoffs/` 或 Git Commit 中写入真实 API Key/Token/密码、个人姓名/工作邮箱/公司名或本机绝对路径；所有 `.workflow/scripts/workflow.py verify-gate` 门禁及每次提交前强制通过 `scan-secrets` 审计。
* **[BIZ-01] 人机协同三卡点不可跳过**：流程必须保留 `Gate 1`（资产锁定 `user_ground_truth`）、`Gate 2`（分镜脚本审阅）与 `Gate 3`（单帧局部重绘），避免未确认直接触发批量生图扣费。
* **[BIZ-02] 自动质检重试硬上限**：`vlm_quality_check` 不合格自动定向重试严格限制 `<= 2` 次；被上游安全过滤拦截（`SAFETY_BLOCKED`）立即终止重试并返回 `422xx`。
* **[BIZ-03] 数据指标真实性**：所有一致性提升比例、Token 降幅与 P95 耗时只能从 `eval/` 跑完后写入，严禁在代码、文档或 README 中虚构数字。

---

## 三、 踩坑与经验追加区（按时间倒序追加）

### [2026-11-25] 初始化约定
* **背景**：Windows PowerShell 默认控制台编码可能影响中文输出，`.workflow/scripts/workflow.py` 已在入口显式配置 UTF-8 重定向与显式文件编码。
* **规范**：所有 Markdown 中的 Mermaid 节点标签避免使用 `<br/>` 等 HTML 标签，含特殊字符标签统一用双引号包裹。
