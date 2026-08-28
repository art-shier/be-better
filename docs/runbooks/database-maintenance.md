# PostgreSQL 数据库维护手册

生产 PostgreSQL 17 运行在内部 Docker 网络，不发布宿主机端口。所有维护先确认备份，再通过容器内本地 socket 执行；API、Worker、migrator、backup 和 monitor 角色不得混用。

## 日常检查

```bash
dayorder_compose=(docker compose --env-file deploy/.env.production -f deploy/compose.yaml)
"${dayorder_compose[@]}" ps postgres api worker
"${dayorder_compose[@]}" run --rm migrate -check
pwsh -File deploy/scripts/backup-check.ps1
"${dayorder_compose[@]}" logs --since 24h --tail 500 postgres
```

每天检查数据库健康、migration 是否干净、最新备份年龄、WAL 归档、磁盘余量、连接数、锁等待和超过 1 秒的语句。Prometheus 仅绑定 `127.0.0.1`，通过 SSH 隧道访问，不能开放到公网。

可用只读诊断模板：

```bash
"${dayorder_compose[@]}" exec -T postgres sh -ceu '
  exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "
    SELECT now() AT TIME ZONE '\''UTC'\'' AS checked_at_utc,
           pg_database_size(current_database()) AS database_bytes;
    SELECT state, count(*) FROM pg_stat_activity GROUP BY state ORDER BY state;
    SELECT count(*) AS waiting_locks FROM pg_stat_activity WHERE wait_event_type = '\''Lock'\'';
    SELECT relname, n_live_tup, n_dead_tup, last_autovacuum, last_autoanalyze
    FROM pg_stat_user_tables ORDER BY n_dead_tup DESC LIMIT 20;
  "
'
```

## VACUUM、ANALYZE 与索引

Autovacuum 默认开启。先根据 `pg_stat_user_tables`、事务年龄和查询计划确认问题，再调整单表参数或执行维护：

```bash
"${dayorder_compose[@]}" exec -T postgres sh -ceu '
  exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
    -c "VACUUM (ANALYZE, VERBOSE) dayorder.tasks;"
'
```

常规维护不使用 `VACUUM FULL`，因为它需要重锁并重写表。确需 `REINDEX ... CONCURRENTLY`、大规模回填或表重写时，应建立变更单、完成恢复点、评估额外磁盘空间并安排维护窗口。禁止在生产直接运行未经测试的 ad-hoc DDL。

## 容量与连接

- API 初始最大连接 20，Worker 初始最大连接 5；PostgreSQL `max_connections` 为 100。先修复长事务、连接泄漏和慢查询，再考虑调大池。
- 数据盘在预计耗尽前扩容；不得删除 WAL、`postgres_data` 或 pgBackRest 仓库来临时腾空间。
- 检查 PostgreSQL、pgBackRest、Prometheus、Caddy 数据和 Docker 日志共同占用，至少保留一次维护所需的峰值空间。
- 所有服务与数据库使用 UTC；不要通过修改服务器时区修复业务时间问题。

## 数据保留

产品策略要求定期清理过期 Session/token、软删除实体、同步变更、幂等记录、成功/失败 Outbox 和审计元数据。当前版本尚未实现自动保留任务；在有经过测试、可观测且可重试的清理 Worker 前，只监控增长，不直接用临时 SQL 批量删除业务表。新增清理任务时必须保留同步 tombstone 窗口和用户删除清单语义，并先在恢复副本验证。

## 升级 PostgreSQL

小版本升级前运行完整备份和恢复演练，锁定目标镜像 digest，阅读官方发行说明并在预发布副本运行 API/RLS/同步/Worker 验收。升级时停止应用写入，完成备份，替换镜像并验证；主版本升级使用新 Volume 的 `pg_upgrade` 或逻辑迁移方案，不原地跨主版本启动旧数据目录。

## 周期任务

- 每 15 分钟：pgBackRest check 与备份年龄指标。
- 每小时：增量备份并同步异地仓库。
- 每天：完整备份、容量/锁/长事务检查。
- 工作日：按既定窗口执行差异备份。
- 每月：隔离恢复演练，记录真实 RPO/RTO 并重放用户删除清单。
- 每季度：容量预测、连接池评估、依赖/镜像升级和事故手册演练。

任何维护后都要验证 `/health/ready`、两用户 RLS 隔离、核心资源读写、增量同步、Worker/Outbox 和新备份；失败时按 [事故手册](incident.md)处置。
