# 告警响应手册

Prometheus 默认只绑定宿主机 `127.0.0.1:9090`。通过 SSH 隧道查看，不要把 9090 暴露到公网。`deploy/prometheus/alerts.yml` 是门限真源；生产可把 Prometheus 接到云告警服务或 Alertmanager。

所有响应先记录开始时间、告警表达式、最近发布版本和请求 ID样本。不要在工单中粘贴 Cookie、Token、密码、笔记正文或 Agent 输入。

## DayOrderTargetDown

检查 `docker compose ps`、目标容器最近 200 行 JSON 日志、API readiness 和 PostgreSQL 健康。若 Caddy/API 不可用超过两分钟，停止发布并按回滚手册恢复上一镜像。Worker 不可用时先恢复消费，避免重复手工处理 Outbox。

## DayOrderAPIHighErrorRate

按 `route` 和 `status` 分组确认范围，用 `requestId` 关联日志。检查 migration 版本、数据库连接池、Caddy 上游错误和外部依赖。不要用原始 URL path 聚合，路径可能含实体 ID。

## DayOrderAPIHighLatency

比较 HTTP p95、`dayorder_pgxpool_wait_duration_seconds_total`、连接池占用和 PostgreSQL 慢语句。若只影响密码端点，同时检查 `dayorder_password_operation_duration_seconds`；不得通过降低 Argon2id 参数临时缓解。

## DayOrderDatabasePoolSaturated

确认是 API 还是 Worker 池，检查长事务、`idle in transaction`、慢查询和当前并发。先修复阻塞与查询，再评估连接数；单机 PostgreSQL 不应通过无限增大池解决排队。

## DayOrderDatabasePoolWaiting

检查连接泄漏、事务持续时间和数据库 CPU/IO。必要时暂时降低入口请求或 Worker 批量，而不是重启数据库清空症状。

## DayOrderOutboxBacklog

检查 Worker 是否健康、事件类型分布、SMTP 可用性和最近重试。保持单个事件的幂等语义，不得直接把 pending 批量改成 processed。Agent 当前暂未接入，不检查 Provider。

## DayOrderOutboxStalled

五分钟以上的最老事件已影响异步体验。查看 Worker 日志和事件 `attempts`，修复依赖后让正常退避继续；仅在确认处理器幂等且有审计记录时人工重试。

## DayOrderOutboxTerminalFailure

定位 `status = 'dead'` 的事件和最后错误，按事件类型修复。Agent 最终失败还需确认 Run 已转入失败状态。处理完成前保持事件证据，不得删除 dead 行来消警。

## DayOrderOutboxMetricsUnavailable

验证 Worker 数据库连接及 `dayorder.outbox_metrics()` EXECUTE 权限。该告警不等于 Outbox 本身为空；先恢复观测能力。

## DayOrderSyncConflictSpike

检查是否刚发布了字段/版本变更、客户端是否重复使用旧 base version，以及单账号多设备是否异常频繁写同一实体。冲突是受控结果，不能关闭版本保护。

## DayOrderSyncCursorResetSpike

确认服务器时间、HMAC 是否轮换、同步保留期和客户端版本。HMAC 轮换会触发预期的重新登录与全量重建，应与变更窗口对应。

## DayOrderLoginThrottleSpike

按时间和来源网络趋势判断撞库或误配置，但日志不得记录邮箱/IP 原文之外的凭据。持续攻击时在 Caddy/云防火墙加限速，同时保留应用层双维度限流。

## DayOrderBackupMetricMissing / DayOrderBackupStale

检查宿主机计划任务、`backup-check.ps1` 退出状态、pgBackRest 仓库和 WAL 归档。超过 RPO 时冻结高风险发布；不得在未完成一次可验证备份时继续数据库变更。

## DayOrderRestoreDrillOverdue / DayOrderRestoreRTOExceeded

立即安排隔离恢复演练，区分下载、解密、WAL 重放和验收各段耗时。若 RTO 超过 60 分钟，应调整备份频率、仓库位置或服务器资源，并再次演练验证。
