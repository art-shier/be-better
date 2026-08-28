# 发布回滚手册

DayOrder 使用 forward-only PostgreSQL migration，不提供 down migration。回滚分为“仅回退应用镜像”和“从备份恢复数据库”两类；先判断 schema 是否仍与上一版本兼容。

## 立即处置

1. 记录故障开始时间、失败版本、上一稳定版本、migration 版本和请求 ID 样本。
2. 停止新发布和计划中的数据库维护；数据完整性不确定时先停止公网写入与 Worker：

   ```bash
   dayorder_compose=(docker compose --env-file deploy/.env.production -f deploy/compose.yaml)
   "${dayorder_compose[@]}" stop caddy worker api
   ```

3. 保留当前容器日志和 PostgreSQL Volume，不运行 `down --volumes`，不在原卷上试错。

## 仅回退应用镜像

满足以下全部条件时使用：数据库健康；新 migration 是 expand/contract 且上一版本兼容；没有需要撤销的数据变换；故障来自 API、Worker 或 Web 镜像。

1. 把 `DAYORDER_VERSION`、`DAYORDER_IMAGE_REVISION` 和 `DAYORDER_IMAGE_CREATED` 改回上一稳定构建的值。确认对应镜像仍在本机或可信镜像仓库中。
2. 不重新构建镜像，以旧标签重建服务：

   ```bash
   dayorder_compose=(docker compose --env-file deploy/.env.production -f deploy/compose.yaml)
   "${dayorder_compose[@]}" up -d --no-build --wait api worker caddy
   ```

3. 验证 readiness、登录、两用户隔离、资源读取和 Outbox 消费。观察错误率与连接池至少 15 分钟。

不得为了回退应用而手工删除列、表、索引或 migration 记录。若旧镜像不兼容当前 schema，保持入口关闭并进入数据库恢复。

## 数据库恢复

以下情况需要恢复到新 Volume：破坏性 migration、不可逆错误写入、数据损坏、WAL/文件系统故障，或应用回退无法兼容当前 schema。

1. 冻结 API 与 Worker 写入，记录允许的数据恢复时间点。
2. 保留故障卷；先按 [备份与恢复手册](backup-restore.md)在隔离 Volume 完成恢复演练。
3. 选择目标备份/WAL 时间点，恢复到新 Volume，重放仍在保留期内的用户删除清单。
4. 在隔离库验证 migration 为干净版本、RLS 已启用、两用户相互不可见、核心资源可读写、Outbox 状态一致。
5. 由两人复核后把应用连接切换到新库。旧卷保留到业务验收和新完整备份均完成。

出现备份链错误、加密密钥不匹配、WAL 缺口、删除清单失败或恢复结果超过允许 RPO 时，停止恢复并按 P0 数据事件升级，不能把未验证数据库开放给用户。

## 回滚完成标准

- `/health/ready` 连续成功，Caddy TLS 与安全头验收通过。
- 注册/登录或既有测试账户登录成功，Session 轮换正常。
- 受限数据库角色的 RLS 验证通过，跨用户 API 返回 404。
- 资源写入、增量同步、Worker 和 Outbox 正常。
- pgBackRest 完整备份已完成并复制到异地。
- 事件记录包含原因、影响时间、丢失数据上限、最终版本和后续修复负责人。
