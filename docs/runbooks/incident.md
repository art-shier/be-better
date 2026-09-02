# 生产事故响应手册

本手册覆盖单机 DayOrder 的可用性、安全和数据事故。先恢复安全边界与数据完整性，再恢复完整功能。

## 分级与职责

- P0：疑似跨用户数据泄露、凭据泄露、数据损坏或无法满足 RPO。立即关闭公网入口或受影响功能，冻结写入并升级负责人。
- P1：全站不可用、登录普遍失败、PostgreSQL 不健康、持续高 5xx、Worker/Outbox 严重堆积。
- P2：局部功能降级、延迟升高、少量异步任务失败但核心数据仍安全。

当班响应人记录时间线并负责指挥；另一人负责操作复核和证据保存。P0/P1 中执行数据库删除、恢复、密钥轮换和流量恢复必须双人确认。

## 前十分钟

```bash
dayorder_compose=(docker compose --env-file deploy/.env.production -f deploy/compose.yaml)
date -u
git rev-parse HEAD
"${dayorder_compose[@]}" ps
"${dayorder_compose[@]}" logs --since 30m --tail 500 caddy api worker postgres
curl --fail --silent --show-error https://YOUR_DOMAIN/health/live
curl --fail --silent --show-error https://YOUR_DOMAIN/health/ready
pwsh -File deploy/scripts/backup-check.ps1
```

保存告警表达式、首次发生时间、发布版本、容器状态、请求 ID 样本和最近变更。不要把 Cookie、Session、密码、一次性令牌、完整请求体、笔记正文或 secret 正文写入事故工单。

若存在跨租户访问、数据库损坏或密钥泄露可能，立即停止 Caddy、API 和 Worker，保留 Volume 与日志：

```bash
"${dayorder_compose[@]}" stop caddy worker api
```

## 按症状处理

### API 或入口不可用

比较 `/health/live` 与 `/health/ready`，检查 Caddy 上游、证书续签、API panic、migration 版本和数据库连接池。只在状态明确且数据安全时重建单个服务：

```bash
"${dayorder_compose[@]}" up -d --no-deps --force-recreate api
"${dayorder_compose[@]}" up -d --no-deps --force-recreate caddy
```

如果故障紧随发布，按 [回滚手册](rollback.md)回到上一不可变镜像。

### PostgreSQL 不健康或磁盘告警

停止发布和高风险写入，检查磁盘、容器 OOM、锁等待、长事务、WAL 归档和最新备份。不要通过删除 WAL、备份仓库或 `postgres_data` 腾空间。怀疑数据损坏时保留原卷并在新 Volume 恢复，参见 [数据库维护](database-maintenance.md)和[备份恢复](backup-restore.md)。

### Worker 或 Outbox 堆积

检查 Worker 日志、`dayorder_outbox_*` 指标和 SMTP 可用性。修复依赖后让 Worker 的幂等重试继续消费；不得批量把 `pending`/`dead` 行直接改成 `processed`，也不得删除失败行来消警。Agent 当前暂未接入，不存在 Provider 依赖。

### 同步冲突激增

检查最近客户端和字段变更、HMAC 是否轮换、服务器时间、游标保留期，以及同一账户多设备是否反复写同一实体。版本冲突是安全结果，不能关闭乐观锁或 RLS 来降低指标。

### 凭据疑似泄露

先限制入口，再按 [密钥手册](secrets.md)轮换受影响密钥。HMAC 轮换会让现有 Session 和同步游标失效；pgBackRest 加密口令不能原地覆盖，必须建立新仓库并验证恢复后再退役旧仓库。

### 疑似跨用户访问

立即作为 P0：关闭公网入口和 Worker；保留请求 ID、用户 UUID、资源 UUID 和时间范围；使用受限 `dayorder_api` 角色重现 RLS，而不是只用管理员连接。不得查询或复制无关用户正文。修复后必须重新运行两用户 API/RLS 验收并进行受影响范围分析，得到负责人批准后才恢复流量。

## 恢复与关闭

恢复流量前依次验证 TLS/安全头、readiness、登录、两用户隔离、核心资源 CRUD、增量同步、Worker/Outbox 和备份。P0/P1 至少观察 30 分钟。

事故报告包含：UTC 时间线、根因、用户影响、数据丢失或泄露范围、检测缺口、处置命令、恢复版本、RPO/RTO 实际值和有负责人/截止时间的改进项。事故记录只使用受控用户 ID，不写邮箱或内容正文。
