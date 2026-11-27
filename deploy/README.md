# EchoCanvas 本地基础设施部署指南

本目录提供 EchoCanvas 所需的全套本地基础设施编排：
* **PostgreSQL 16（`pgvector/pgvector:pg16`）**：业务状态表、向量索引与后续 LangGraph Checkpointer 持久化（首次启动自动执行 `deploy/sql/001_init.sql` 初始化 8 张业务表）。
* **Redis 7（`redis:7-alpine`）**：Asynq 异步任务队列、限流计数与 SSE Pub/Sub 通道。
* **RustFS（`rustfs/rustfs:latest`，Apache-2.0 S3 兼容对象存储）**：本地 S3 兼容存储与控制台（`9000` API / `9001` Console），由 `s3-init`（`amazon/aws-cli`）自动创建默认 Bucket（`S3_BUCKET=echocanvas-media`）。

---

## 1. 准备环境变量

在仓库根目录复制示例配置（`.env` 已被 `.gitignore` 忽略，不会误提交）：

```bash
cp .env.example .env
```

> **端口冲突提示**：如果本机默认端口（例如 `6379`、`5432` 或 `9000`）已被本地服务占用，可在 `.env` 中覆盖宿主机映射端口，例如：
> `REDIS_PORT=16379` 并同步更新 `REDIS_ADDR=localhost:16379`。

---

## 2. 启动基础设施

```bash
docker compose -f deploy/docker-compose.yml --env-file .env up -d --wait
```

---

## 3. 验证建库与 8 张物理表

```bash
# 查看容器健康状态（postgres/redis/rustfs 均为 healthy，s3-init 为 Exited 0）
docker compose -f deploy/docker-compose.yml ps -a

# 验证 uuid-ossp 与 vector 扩展
docker compose -f deploy/docker-compose.yml exec -T postgres psql -U echocanvas -d echocanvas -c "\dx"

# 验证 8 张核心业务表是否全部初始化成功
docker compose -f deploy/docker-compose.yml exec -T postgres psql -U echocanvas -d echocanvas -c "\dt"
```

---

## 4. 常用维护命令

```bash
# 仅停止容器（保留持久化数据卷）
docker compose -f deploy/docker-compose.yml down

# 彻底清理容器与本地数据卷（用于重新验证 001_init.sql 初始化）
docker compose -f deploy/docker-compose.yml down -v
```
