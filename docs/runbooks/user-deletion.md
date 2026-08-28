# 用户删除运行手册

当前版本已具备数据库删除状态和级联约束，但尚未开放自助删除账户 API，也没有自动到期清理任务。删除请求必须由受控运维流程执行；不得声称用户点击后会自动完成。

在线用户数据都以 `users.id` 为根。身份、计划、内容、Agent、审计、同步、幂等和 Outbox 表通过 `ON DELETE CASCADE` 删除。任务到目标、任务到来源记录、日程到目标的三个业务外键使用 `RESTRICT`；硬删除用户前必须先解除这三类关联，不能假设根级联会自动绕过它们。物理备份不逐行改写，恢复旧备份后必须重放独立删除清单。

## 接收与核验

1. 通过已认证渠道核验账户所有权，并创建不含敏感正文的删除工单。
2. 记录用户 UUID、请求 UTC 时间、计划清理时间和工单标识。删除 SQL 与删除清单必须双人复核。
3. 不在普通日志、聊天或工单中记录密码、Session、token、笔记正文或数据库 URL。
4. 若请求涉及法定保留或安全调查，由数据负责人先决定保留范围；运维人员不能自行扩大保留。

## 立即冻结账户

以 PostgreSQL 管理员本地连接执行下列模板。先把占位值替换为经过复核的 UUID、64 位十六进制请求哈希和 UTC 时间；恢复期默认 30 天，可按公开政策调整。

```sql
\set ON_ERROR_STOP on
\set user_id '00000000-0000-0000-0000-000000000000'
\set request_hash_hex '0000000000000000000000000000000000000000000000000000000000000000'

BEGIN;
SELECT id, status, created_at, deleted_at
FROM dayorder.users
WHERE id = :'user_id'::uuid
FOR UPDATE;

UPDATE dayorder.users
SET status = 'deletion_pending', updated_at = statement_timestamp()
WHERE id = :'user_id'::uuid AND deleted_at IS NULL;

UPDATE dayorder.sessions
SET revoked_at = coalesce(revoked_at, statement_timestamp())
WHERE user_id = :'user_id'::uuid;

UPDATE dayorder.account_tokens
SET consumed_at = coalesce(consumed_at, statement_timestamp())
WHERE user_id = :'user_id'::uuid;

INSERT INTO dayorder.account_deletions (
    user_id, requested_at, scheduled_for, request_hash
)
VALUES (
    :'user_id'::uuid,
    statement_timestamp(),
    statement_timestamp() + interval '30 days',
    decode(:'request_hash_hex', 'hex')
)
ON CONFLICT (user_id) DO UPDATE
SET requested_at = excluded.requested_at,
    scheduled_for = excluded.scheduled_for,
    completed_at = NULL,
    request_hash = excluded.request_hash;
COMMIT;
```

事务提交后，既有 Session 因撤销而失效，新登录也因账户不是 `active` 而被拒绝。验证账户状态和活动 Session 数均符合预期，但不要在验证输出中包含邮箱或内容字段。

## 恢复期内撤销请求

只有在公开政策允许、所有权重新核验、尚未执行物理清理且没有强制保留冲突时才能撤销。双人复核后把账户状态恢复为 `active`、删除 `account_deletions` 排程；旧 Session 和 token 不恢复，用户必须重新登录。把撤销原因写入外部工单，不能伪造应用审计事件。

## 到期物理清理

先生成并离线保存只含用户 UUID、删除时间和可重放 SQL 的清单。清单不得包含邮箱、正文或密码哈希，并应使用独立加密存储、不可变审计和与物理备份相同或更长的保留期。

可重放清单必须先解除三个 `RESTRICT` 关系，再删除用户根：

```sql
BEGIN;
UPDATE dayorder.tasks
SET goal_id = NULL, source_record_id = NULL
WHERE user_id = '00000000-0000-0000-0000-000000000000'::uuid;

UPDATE dayorder.calendar_events
SET goal_id = NULL
WHERE user_id = '00000000-0000-0000-0000-000000000000'::uuid;

DELETE FROM dayorder.users
WHERE id = '00000000-0000-0000-0000-000000000000'::uuid;
COMMIT;
```

生产执行前在同一事务中锁定 `account_deletions` 行，并确认 `scheduled_for <= statement_timestamp()` 且 `completed_at IS NULL`；若条件不成立必须回滚。随后执行清单中的解除关联和根删除。根删除会级联其余在线用户数据，包括账户排程本身；“完成”证据保存在数据库外的删除清单和运维审计中。

执行后至少验证：`users` 中该 UUID 为 0 行；所有带 `user_id` 的 DayOrder 表均为 0 行；API 登录失败；搜索、指标和日志中没有新增该用户数据。只检查计数，不导出正文。

## 备份与恢复约束

备份中的数据按备份保留策略自然过期，不手工修改 pgBackRest 备份。每次从早于删除时间的备份恢复时，必须在恢复库对外开放前重放全部适用删除清单，并再次执行零行验证。示例：

```powershell
pwsh -File deploy/scripts/restore-drill.ps1 -Force -DeletionManifest C:\secure\deletions.sql
```

删除清单缺失、哈希不匹配、级联失败或恢复演练未重放清单时，恢复流程必须失败并保持隔离。
