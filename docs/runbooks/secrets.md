# 生产密钥管理

DayOrder 的生产 Compose 只保存密钥文件路径。数据库 URL、密码、HMAC、SMTP、Agent Key 和备份加密口令均通过 `/run/secrets` 只读挂载，不能写入 `.env.production`、Compose、镜像层或 Git。

## 首次生成

在服务器的 `deploy` 目录执行，先确保当前用户是唯一可读取这些文件的运维账号：

```bash
umask 077
mkdir -p secrets
for name in postgres_admin_password migrator_db_password api_db_password worker_db_password backup_db_password monitor_db_password auth_hmac_key pgbackrest_cipher_pass; do
  openssl rand -hex 32 > "secrets/$name"
done
openssl rand -hex 32 > secrets/smtp_password
openssl rand -hex 32 > secrets/agent_http_key

printf 'postgres://dayorder_migrator:%s@postgres:5432/dayorder?sslmode=disable&search_path=dayorder' "$(cat secrets/migrator_db_password)" > secrets/migration_database_url
printf 'postgres://dayorder_api:%s@postgres:5432/dayorder?sslmode=disable' "$(cat secrets/api_db_password)" > secrets/api_database_url
printf 'postgres://dayorder_worker:%s@postgres:5432/dayorder?sslmode=disable' "$(cat secrets/worker_db_password)" > secrets/worker_database_url
chmod 600 secrets/*
```

把 `smtp_password` 和 `agent_http_key` 替换为供应商实际签发的值。以上数据库密码使用十六进制字符，因此放入 URL 时不需要额外转义。内部数据库流量只走 Docker 的 `data` 网络，PostgreSQL 没有主机端口；公网 TLS 在 Caddy 终止。

`pgbackrest_cipher_pass` 一旦投入使用必须单独离线保管。丢失它会使全部加密备份不可恢复。建议将密钥副本存入云厂商 KMS/Secret Manager 和独立的应急保管位置，而不是只留在同一台服务器。

## 环境文件

复制 `deploy/env.production.example` 为 `deploy/.env.production`，只填写域名、SMTP 地址、Agent URL、镜像版本等非密钥配置。文件仍应设为 `0600`，因为它描述生产拓扑：

```bash
cp deploy/env.production.example deploy/.env.production
chmod 600 deploy/.env.production
```

启动前检查：

```bash
git status --ignored --short deploy/secrets deploy/.env.production
docker compose --env-file deploy/.env.production -f deploy/compose.yaml config
```

渲染后的 Compose 可以出现 `/run/secrets/...`，但不得出现任何密钥正文。不要把 `docker compose config` 的完整输出粘贴到公开工单。

## 轮换

数据库密码采用“数据库先接受新密码，再更新文件并重启消费者”的顺序：

1. 使用管理员连接执行 `ALTER ROLE ... PASSWORD ...`。
2. 原子替换对应密码文件和数据库 URL 文件，权限保持 `0600`。
3. 只重建受影响的 `api`、`worker` 或 `migrate` 容器。
4. 验证 `/health/ready`、Worker 指标和登录流程。
5. 观察 15 分钟日志与告警后关闭变更窗口。

`DAYORDER_AUTH_HMAC_KEY` 轮换会同时使现有 Session、资源游标和同步游标失效，应安排维护窗口并提前通知用户重新登录。pgBackRest 加密口令不能直接覆盖：必须建立新仓库并完成一次全量备份与恢复演练后，才能退役旧仓库。

SMTP 与 Agent Key 先在供应商侧创建第二把 Key，更新 secret、重建 Worker、确认邮件与 Agent Run 成功，再撤销旧 Key。

## 泄露处置

立即停止受影响入口，吊销或轮换密钥，保留审计日志，检查从最早可能泄露时间起的 Session、登录限流、Agent 调用和数据库连接。不得把泄露值写进事件报告；用密钥名称和指纹后 6 位标识即可。
