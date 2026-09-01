# 单机生产部署手册

本手册适用于一台 Linux 云服务器上的 DayOrder Docker Compose 部署。生产只支持全新 PostgreSQL 数据库；不导入 SQLite、不运行双写，也不恢复旧 `/state` 快照。

## 发布前提

- DNS 已指向服务器，云防火墙只开放受限 SSH、TCP 80/443 和 UDP 443。
- Docker Engine、Docker Compose v2、Git 与 PowerShell 7（`pwsh`）已安装。
- `deploy/.env.production` 只包含公开配置，`deploy/secrets/*` 已按 [密钥手册](secrets.md) 创建，归当前部署用户和 GID `10001` 所有并设为 `0640`。
- 要发布的 Git commit 已通过 CI；发布版本、commit SHA 和构建时间已经写入环境文件。
- pgBackRest 同机仓库可用，并有异地加密副本。

以下命令均在仓库根目录执行：

```bash
dayorder_compose=(docker compose --env-file deploy/.env.production -f deploy/compose.yaml)
dayorder_domain="dayorder.example.com"
```

## 发布前检查

```bash
git status --short
git rev-parse HEAD
"${dayorder_compose[@]}" config --quiet
node scripts/validate-deploy.mjs
pwsh -File deploy/scripts/backup-check.ps1 -RunBackup -BackupType incr
```

继续发布前必须满足：工作树没有未解释的改动、commit 与发布单一致、Compose 能完整渲染、静态安全检查通过、最新备份检查成功。不得把 `docker compose config` 的完整输出贴入公开工单，因为其中包含 secret 文件路径和生产拓扑。

## 首次上线

1. 构建带不可变版本标签的所有镜像：

   ```bash
   "${dayorder_compose[@]}" build --pull caddy api worker migrate postgres
   ```

2. 启动 PostgreSQL。首次初始化会创建最小权限角色和加密 pgBackRest 仓库：

   ```bash
   "${dayorder_compose[@]}" up -d --wait postgres
   ```

3. 用独立 migrator 角色在空库上执行 forward-only migration：

   ```bash
   "${dayorder_compose[@]}" run --rm migrate
   "${dayorder_compose[@]}" run --rm migrate -check
   ```

4. 启动应用、入口与监控：

   ```bash
   "${dayorder_compose[@]}" up -d --wait api worker caddy prometheus node-exporter
   "${dayorder_compose[@]}" ps
   ```

5. 初始化完整备份并检查 WAL 归档：

   ```bash
   pwsh -File deploy/scripts/backup-check.ps1 -RunBackup -BackupType full
   ```

6. 安装 [备份手册](backup-restore.md)中的宿主机计划任务，并配置服务器外的 HTTPS 可用性检测和告警通道。

## 常规发布

数据库迁移只允许向前执行。新版本若修改 schema，必须遵循 expand/contract，保证上一应用版本仍可在新 schema 上短期运行。

```bash
"${dayorder_compose[@]}" build --pull caddy api worker migrate postgres
"${dayorder_compose[@]}" up -d --wait postgres
"${dayorder_compose[@]}" run --rm migrate
"${dayorder_compose[@]}" run --rm migrate -check
"${dayorder_compose[@]}" up -d --wait api worker caddy prometheus node-exporter
```

不要使用 `docker compose down --volumes`。常规发布不得删除 `postgres_data`、`pgbackrest_repository`、Caddy 或 Prometheus Volume。

## 发布后验收

```bash
curl --fail --silent --show-error "https://${dayorder_domain}/health/live"
curl --fail --silent --show-error "https://${dayorder_domain}/health/ready"
pwsh -File scripts/security-acceptance.ps1 \
  -BaseUrl "https://${dayorder_domain}" \
  -EnvironmentFile deploy/.env.production \
  -ProjectName dayorder
pwsh -File deploy/scripts/backup-check.ps1
"${dayorder_compose[@]}" logs --since 15m --tail 300 api worker caddy postgres
```

验收还应人工完成一次注册、邮箱验证、登录、资源写入与另一设备同步。观察至少 15 分钟，确认 API 5xx、连接池等待、Outbox 堆积、同步冲突和备份告警均无异常，再关闭发布窗口。

任一必要检查失败时停止继续发布，保留日志和镜像，按 [回滚手册](rollback.md)处理。不要在失败发布中重复执行不可解释的手工 SQL。
