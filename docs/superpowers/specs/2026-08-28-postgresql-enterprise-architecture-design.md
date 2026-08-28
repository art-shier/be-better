# DayOrder PostgreSQL 企业级公开多用户架构设计

- 日期：2026-08-28
- 状态：已批准
- 适用范围：公开注册、多个人账号、账号之间完全隔离

## 1. 背景

DayOrder 当前是本地优先的个人计划应用。游客数据保存在浏览器 `localStorage`；登录账户由 Go 服务把整份 `AppData` 序列化后保存到 SQLite 的单行状态中。前端在数据变化停止约 500 ms 后，通过统一的 `/state` 接口上传完整快照。

该模型适合原型和单机个人使用，但不适合作为公开多用户生产服务的长期数据模型：

- SQLite 存储被限制为单连接，读取和写入会串行化。
- 修改一个任务也会上传并覆盖全部目标、任务、日程、笔记、Agent 和审计数据。
- 服务端无法对业务实体进行分页、搜索、索引、细粒度校验和权限控制。
- 多设备冲突发生在整个账户快照，而不是单个实体。
- 笔记、Agent 运行和审计持续增长后，每次同步成本都会增长。
- 单文件数据库不适合未来的多 API 实例、托管高可用和标准化灾难恢复。

项目没有需要保留的 SQLite 生产数据，也不要求兼容旧数据库或旧 `/state` 协议。因此本次采用全新的 PostgreSQL schema，不设计 SQLite 导入、双写或兼容层。

## 2. 目标与非目标

### 2.1 目标

- 使用 PostgreSQL 作为唯一的开发、测试、预发布和生产数据库。
- 将目标、任务、日程、记录、笔记、复盘等核心实体关系化。
- 以 `user_id` 作为个人账号数据所有权边界。
- 使用资源级 API、实体版本和增量同步替代整包 `/state`。
- 保留本地优先体验、离线缓存和离线操作队列。
- 提供数据库级跨用户隔离、幂等写入、审计和可靠异步任务。
- 支持单台云服务器上的规范化 Docker Compose 生产部署。
- 提供异地备份、时间点恢复、健康检查、指标、日志和安全基线。
- 为以后增加 API 副本、托管 PostgreSQL 和消息队列保留演进空间。

### 2.2 非目标

- 首版不支持组织、团队、成员角色或共享目标与任务。
- 首版不实现事件溯源架构。
- 首版不引入 Redis 或独立消息队列。
- 首版不实现跨云或跨可用区高可用。
- 不迁移任何历史 SQLite 数据。
- 不保留旧 `/state` API 或旧状态快照协议。

## 3. 核心架构决策

### 3.1 数据库选择

选择 PostgreSQL，而不是 MySQL 或继续使用 SQLite，原因如下：

- 并发事务、约束、索引、窗口查询和全文检索能力适合公开多用户服务。
- JSONB 适合用户设置、审计快照和 Agent 扩展元数据，但不会承载整份业务状态。
- Row Level Security 可以作为应用层鉴权之外的第二道隔离防线。
- `FOR UPDATE SKIP LOCKED`、事务 Outbox 和成熟备份工具适合可靠后台任务。
- PostgreSQL 具有成熟的 Go 驱动、迁移、监控和托管服务生态。

### 3.2 建表原则

不会把每个 TypeScript interface 机械映射为数据库表。建模规则是：

- 能独立创建、修改、删除、分页或查询的业务实体使用独立表。
- 具有多条记录和独立生命周期的父级组成项使用子表。
- 需要查询和约束的多对多关系使用关联表。
- 设置、历史快照和不参与关键查询的扩展元数据使用受控 JSONB。
- 只用于 API 传输、撤销命令或页面状态的类型不建独立表。

### 3.3 选定方案

采用“关系表 + 实体级乐观锁 + 增量同步”方案。

未选方案：

- JSONB 快照加索引表：需要维护双份数据一致性，仍保留写放大和大范围冲突。
- 事件溯源：审计能力强，但投影、回放和版本治理复杂度超出首版需求。

## 4. 系统拓扑

首期在单台云服务器上运行 Docker Compose：

```text
公网浏览器
  -> Caddy：TLS、静态 Web、安全响应头、反向代理
     -> Go API：认证、资源 API、同步、事务编排
        -> PostgreSQL
     -> Go Worker：提醒、Agent、Outbox 任务
        -> PostgreSQL

PostgreSQL
  -> pgBackRest / WAL 归档
     -> 异地 S3 兼容对象存储

API / Worker / PostgreSQL
  -> 指标、结构化日志、外部可用性监控
```

Compose 服务包括：

- `caddy`：唯一公网入口，负责 HTTPS、静态 Web 和 `/api/v1` 代理。
- `api`：无本地持久状态的 Go HTTP 服务。
- `worker`：与 API 使用同一镜像、不同启动命令。
- `postgres`：仅内部 Docker 网络可访问。
- `migrate`：一次性数据库迁移任务。
- `backup`：备份、WAL 归档和恢复操作。
- `monitoring`：Prometheus 和必要的 exporter；Grafana 可以作为部署配置的一部分。

该拓扑是生产级单机架构，不是高可用架构。宿主机或所在可用区故障会导致服务中断；恢复依赖异地备份。未来高可用版本需要多台服务器、负载均衡和托管或复制型 PostgreSQL。

## 5. 用户隔离与数据库权限

### 5.1 所有权边界

- `users.id` 是个人账号标识。
- 所有用户业务表包含非空 `user_id`。
- 普通客户端不能在请求体、查询参数或 Header 中指定数据所有者。
- API 只能从已验证 Session 推导 `user_id`。
- 不属于当前用户的资源统一按不存在处理，返回 `404`，避免资源枚举。

### 5.2 组合外键

关键业务关联使用包含用户标识的组合外键。例如：

```text
tasks(user_id, goal_id)
  -> goals(user_id, id)
```

即使应用代码发生错误，数据库也不能把用户 A 的任务关联到用户 B 的目标。被组合引用的业务表具有 `UNIQUE(user_id, id)`。

### 5.3 Row Level Security

用户业务表启用 RLS。每次已认证请求在事务内设置当前用户：

```sql
SELECT set_config('app.current_user_id', $1, true);
```

RLS 策略只允许读取和修改 `user_id` 等于当前事务用户的行。`$1` 是已经通过 Session 验证的 UUID 参数；该设置只能由服务端事务代码建立，不能拼接 SQL，也不能来自客户端字段。

认证前的邮箱登录查询和 Session 令牌解析不能依赖当前用户 RLS。运行角色不直接读取完整身份表，而是调用固定 SQL 的最小权限数据库函数完成身份定位；函数不使用动态 SQL，只返回认证所需字段。登录接口仍需邮箱与 IP 双维度限流，防止账号枚举和批量哈希计算。

### 5.4 数据库角色

- `dayorder_owner`：对象所有者，不用于应用运行。
- `dayorder_migrator`：只在发布迁移任务中使用。
- `dayorder_app`：API 的受限运行角色。
- `dayorder_worker`：Worker 角色；只能执行固定的 Outbox 领取函数，并在取得事件后切换到对应用户的 RLS 事务上下文。
- `dayorder_backup`：备份与恢复所需权限。
- `dayorder_monitor`：只读监控权限。

API 和 Worker 运行角色不拥有表、不能执行 DDL，也不能绕过 RLS。Worker 不能任意扫描全部业务表；跨用户领取 Outbox 由无动态 SQL、返回字段受限的数据库函数完成。管理操作使用单独的受审计入口，不伪造普通用户 Session。

## 6. 通用数据约定

用户可编辑、需要独立同步的业务资源表统一包含：

```text
id          UUID PRIMARY KEY
user_id     UUID NOT NULL
version     BIGINT NOT NULL DEFAULT 1
created_at  TIMESTAMPTZ NOT NULL
updated_at  TIMESTAMPTZ NOT NULL
deleted_at  TIMESTAMPTZ NULL
```

- 业务 ID 由客户端生成随机 UUID，以支持离线创建；服务端验证格式、唯一性和所有权。
- 时间以 UTC 保存，API 使用 RFC 3339 表示。
- 状态和类型使用 `VARCHAR + CHECK`，不使用 PostgreSQL ENUM，降低未来增加状态的迁移成本。
- 更新使用 `WHERE id = $1 AND user_id = $2 AND version = $3`，成功后 `version + 1`。
- 软删除同样增加版本并写入同步事件。
- 金额和指标数值使用满足精度要求的 `NUMERIC`，不使用浮点数表示需要精确比较的值。

## 7. 关系模型

### 7.1 账户与认证

#### `users`

```text
id, email, normalized_email, display_name
password_hash, status, email_verified_at
created_at, updated_at, deleted_at
```

- 活跃账号的 `normalized_email` 唯一。
- `status` 包括 `pending_verification`、`active`、`disabled` 和 `deletion_pending`。
- 密码继续使用 Argon2id。

#### `sessions`

```text
id, user_id, token_hash, user_agent
created_at, last_seen_at, expires_at, revoked_at
```

- 只保存不透明 Session 令牌的密码学哈希。
- `token_hash` 唯一。
- `last_seen_at` 节流更新，不在每个认证请求上写数据库。

#### `account_tokens`

```text
id, user_id, purpose, token_hash
expires_at, consumed_at, created_at
```

统一承载邮箱验证和密码重置等一次性令牌，只保存哈希，并在消费时原子标记。

注册先创建 `pending_verification` 账号并发送验证邮件。该状态只能访问验证、重发验证邮件和注销接口；验证成功后才变为 `active` 并允许创建远端业务数据。游客数据迁移在账号激活后通过正常资源 Mutation 完成，不在未验证注册事务中写入整份状态。

#### `login_throttles`

```text
key_hash, dimension, window_started_at
failures, blocked_until, updated_at
```

在不引入 Redis 的前提下，PostgreSQL 同时保存 IP 和邮箱维度的认证失败窗口，使多个 API 副本共享一致限流状态。`key_hash` 使用服务端密钥 HMAC 处理标准化邮箱或规范化 IP，不在限流表中保存明文账号标识。成功登录会按策略清理或衰减计数；Caddy 只承担 TLS、请求体和连接边界，不依赖第三方限流插件。

#### `user_settings`

```text
user_id, schema_version, version, settings JSONB, updated_at
```

JSONB 保存 AI 开关、提醒开关、关注领域和权限配置。服务端按 schema 进行验证，不接受任意无限嵌套数据。

### 7.2 目标与任务

#### `goals`

```text
id, user_id, title, why, area
metric_type, target_value, current_value, unit
start_date, due_date, status, health
version, created_at, updated_at, deleted_at
```

#### `goal_milestones`

```text
id, user_id, goal_id, title
due_at, completed_at, sort_order
version, created_at, updated_at, deleted_at
```

里程碑具有独立完成、排序和修改行为，因此使用子表，而不是目标 JSONB 数组。

#### `tasks`

```text
id, user_id, title, status, priority
estimate_minutes, due_at, scheduled_start, scheduled_end
goal_id, source_record_id, completed_at
version, created_at, updated_at, deleted_at
```

约束包括：

- `estimate_minutes >= 0`。
- `scheduled_end >= scheduled_start`。
- `(user_id, goal_id)` 引用 `goals(user_id, id)`。
- `(user_id, source_record_id)` 引用 `records(user_id, id)`。

删除目标不会自动删除任务；任务的 `goal_id` 在同一事务中置空并生成同步事件。

### 7.3 日程与提醒

#### `calendar_events`

```text
id, user_id, title, start_at, end_at, timezone
location, kind, source_calendar, goal_id
version, created_at, updated_at, deleted_at
```

- `end_at >= start_at`。
- `timezone` 保存 IANA 时区名称，以便正确处理夏令时和重复日历展示。

#### `calendar_event_reminders`

```text
id, user_id, event_id, offset_minutes, channel
scheduled_at, status, delivered_at, attempts
version, created_at, updated_at, deleted_at
```

提醒独立建表，使 Worker 可以查询即将触发的提醒、重试失败投递并记录结果。

### 7.4 记录、笔记与复盘

#### `records`

```text
id, user_id, raw_text, kind, occurred_at
mood, energy, archived_at
version, created_at, updated_at, deleted_at
```

#### `notes`

```text
id, user_id, title, body_markdown, category, archived_at
version, created_at, updated_at, deleted_at
```

笔记正文保存 Markdown。后续全文检索使用 PostgreSQL `tsvector` 和 GIN 索引，不把搜索结果作为权威数据保存。

#### `daily_reviews`

```text
id, user_id, review_date, wins, blockers
mood, energy, tomorrow_focus, ai_summary
version, created_at, updated_at, deleted_at
```

部分唯一索引 `UNIQUE(user_id, review_date) WHERE deleted_at IS NULL` 保证每个用户每天一份未删除的正式复盘，同时允许删除后重新创建。

### 7.5 标签与跨实体关系

#### `tags`

```text
id, user_id, name, normalized_name
version, created_at, updated_at, deleted_at
```

同一用户未删除标签的 `normalized_name` 通过部分唯一索引保持唯一。

#### `record_tags` 与 `note_tags`

使用包含 `user_id` 的组合主键和组合外键表达多对多关系。标签不保存为 JSONB，以支持搜索、去重和统计。

#### `entity_links`

```text
id, user_id
source_type, source_id
target_type, target_id
relation_type, created_at
```

该表只承载笔记关联、Agent 来源引用等跨类型弱关系。由于目标类型是多态的，数据库不能用一个普通外键指向多张表；服务层在同一事务内校验源、目标存在且属于同一用户。任务到目标等关键关系仍使用真实组合外键。

## 8. Agent 模型

### 8.1 `agent_runs`

```text
id, user_id, intent, status, action_mode
scope JSONB, provider, model
started_at, finished_at, summary
error_code, error_message
version, created_at, updated_at
```

### 8.2 `agent_steps`

```text
id, user_id, run_id, sequence_no
title, detail, status, metadata JSONB
started_at, finished_at
version, created_at, updated_at
```

`UNIQUE(run_id, sequence_no)` 保持稳定步骤顺序。

### 8.3 `agent_changes`

```text
id, user_id, run_id, change_type
target_type, target_id, base_version
patch JSONB, preview_before JSONB, preview_after JSONB
reason, status, accepted_at, applied_at
version, created_at, updated_at
```

- `patch` 是受控 JSON Patch，不是 SQL。
- 服务端按实体类型和动作维护可修改字段白名单。
- 应用前必须验证所有权、`base_version` 和用户确认状态。
- 版本冲突时不强制覆盖，要求重新分析或再次确认。

### 8.4 `agent_source_refs`

```text
id, user_id, run_id
entity_type, entity_id, entity_version
label_snapshot, created_at
```

历史来源允许在原实体软删除后继续显示，因此保存读取时版本和标签快照。

## 9. 审计

### 9.1 表结构

#### `audit_events`

```text
id, user_id, actor_type, actor_id
action, request_id
before_data JSONB, after_data JSONB, metadata JSONB
created_at
```

#### `audit_event_entities`

```text
audit_event_id, user_id, entity_type, entity_id
```

### 9.2 规则

- 审计只追加，普通业务代码不能更新已有记录。
- 一次 HTTP 请求中的多项变化共享 `request_id`。
- 密码、Session 令牌、一次性令牌和外部 API 密钥不进入审计快照。
- 日志和审计不会无条件保存 Note 正文；需要撤销时保存经过大小限制和字段过滤的业务前值。
- 普通用户只能读取自己的审计数据。
- 默认保留审计元数据 365 天；账号最终删除时按隐私策略删除或匿名化。

## 10. 增量同步与幂等

### 10.1 `sync_changes`

```text
sequence BIGINT GENERATED ALWAYS AS IDENTITY
user_id, entity_type, entity_id
operation, entity_version, changed_at
```

- `operation` 为 `create`、`update` 或 `delete`。
- `(user_id, sequence)` 建索引。
- API 对客户端返回不透明编码游标，不暴露数据库实现细节。
- 默认保留 90 天。游标过期时返回 `SYNC_RESET_REQUIRED` 并要求全量重建缓存。

### 10.2 初次同步

1. 客户端取得当前同步起点游标。
2. 按资源类型和游标分页下载现有实体。
3. 下载完成后拉取起点之后的增量变化。
4. 按实体版本应用变化并保存最新游标。

分页期间发生的新建、更新和删除都会出现在起点之后的增量中，因此不会遗漏。

### 10.3 `user_devices`

```text
id, user_id, device_name, platform
last_seen_at, last_sync_cursor
created_at, revoked_at
```

用户可以撤销设备；被撤销设备必须重新认证。

### 10.4 `client_mutations`

```text
id, user_id, device_id, mutation_id
request_hash, response_status, response_body JSONB
created_at, expires_at
```

`UNIQUE(user_id, device_id, mutation_id)` 保证离线重试幂等。相同 Mutation ID 对应不同请求哈希时返回幂等冲突。成功记录默认保留 30 天。

### 10.5 冲突规则

- 不同实体的变化互不冲突。
- 同一实体的旧版本写入返回 `409 ENTITY_VERSION_CONFLICT`。
- 响应包含当前版本和经过权限过滤的当前实体。
- Note 正文冲突保存本地副本，不自动覆盖。
- 删除与修改冲突返回 `ENTITY_DELETED`。
- 不使用“最后写入者自动覆盖”。

## 11. 可靠异步任务

### 11.1 `outbox_events`

```text
id, user_id, event_type
aggregate_type, aggregate_id
payload JSONB, status, available_at
attempts, locked_at, last_error
created_at, processed_at
```

业务事务需要触发提醒、邮件或 Agent 后台工作时，只在同一事务写入 Outbox。Worker 通过受限数据库函数使用 `FOR UPDATE SKIP LOCKED` 领取最小任务描述，然后在该任务所属用户的 RLS 上下文中处理业务数据；失败后指数退避重试。

首期不引入 Redis：单机部署下 PostgreSQL Outbox 可以提供所需的持久性和领取并发控制，也减少一个有状态组件。成功 Outbox 默认保留 7 天，最终失败记录保留 30 天。

## 12. 事务边界

一次业务修改必须在同一个 PostgreSQL 事务中完成：

```text
更新或创建业务实体
写入 sync_changes
写入 audit_events 和关联实体
写入 client_mutations 的幂等结果
按需写入 outbox_events
COMMIT
```

任何一步失败，整个事务回滚。不会出现业务数据已变化但其他设备收不到、没有审计或异步任务丢失的半成功状态。

数据库死锁或可重试序列化错误由服务层执行有限次数、带抖动的重试；业务版本冲突不自动重试或覆盖。

## 13. 资源 API

### 13.1 路径

保留 `/api/v1` 版本前缀，移除统一 `/state`。主要资源包括：

```text
/goals
/goals/{goalId}
/goals/{goalId}/milestones
/milestones/{milestoneId}
/tasks
/tasks/{taskId}
/calendar-events
/calendar-events/{eventId}
/calendar-events/{eventId}/reminders
/records
/records/{recordId}
/notes
/notes/{noteId}
/daily-reviews
/daily-reviews/{reviewId}
/tags
/tags/{tagId}
/users/me/settings
/agent-runs
/agent-runs/{runId}
/agent-changes/{changeId}/accept
/agent-changes/{changeId}/reject
/audit-events
/sync/bootstrap
/sync/changes
/sync/mutations
```

### 13.2 HTTP 行为

- `POST` 创建并返回 `201`。
- `GET` 获取单条或游标分页列表。
- `PATCH` 使用 `application/merge-patch+json`，只允许资源白名单字段。
- `DELETE` 软删除并返回 `204`。
- 创建、修改、删除需要 `Idempotency-Key`。
- 修改和删除需要 `If-Match` 实体版本。
- 列表使用不透明游标，不使用页码分页。

### 13.3 离线批量变更

`POST /api/v1/sync/mutations` 每批最多 100 项，并限制总请求体大小。服务端按设备提交顺序处理，每项使用独立事务，返回 `applied`、`conflict`、`rejected` 或 `duplicate`。

批量同步和普通资源接口调用同一应用服务和校验规则，不能各自实现一套业务逻辑。

### 13.4 增量拉取

`GET /api/v1/sync/bootstrap` 返回开始全量分页前的高水位游标。`GET /api/v1/sync/changes` 返回变化实体的最新表示或删除 tombstone，避免客户端为每个变化再发起单条查询。响应包含 `nextCursor` 和 `hasMore`。

## 14. 错误协议

统一错误结构：

```json
{
  "error": {
    "code": "VALIDATION_FAILED",
    "message": "请求数据不符合要求",
    "fields": { "title": "标题不能为空" },
    "retryable": false,
    "requestId": "req_uuid"
  }
}
```

状态码约定：

- `400`：JSON 或协议格式错误。
- `401`：未登录或 Session 失效。
- `404`：资源不存在或不属于当前用户。
- `409`：实体版本、唯一约束或幂等冲突。
- `422`：业务字段校验失败。
- `429`：请求频率过高，并返回 `Retry-After`。
- `500`：未预期内部错误。
- `503`：数据库等必要依赖暂时不可用。

所有错误包含 `requestId`。对外错误不返回 SQL、驱动、堆栈、内部路径或其他用户信息。

## 15. PostgreSQL 运行配置

- 使用仍受支持的稳定 PostgreSQL 主版本，生产镜像锁定明确版本和镜像 digest。
- PostgreSQL 不映射宿主机公网端口。
- 数据目录使用独立持久卷。
- 数据库、应用和容器统一使用 UTC。
- API 初始最大连接数 20，Worker 初始最大连接数 5；最终值由压测和服务器资源决定。
- 设置查询超时、锁等待超时和空闲事务超时。
- 使用 SCRAM 密码认证。
- 单机首期不使用 PgBouncer；扩展到多个 API 副本后重新评估。

Go 服务需要分别配置最大连接数、空闲连接数、连接最大寿命和连接空闲寿命，并暴露连接池等待指标。

## 16. 数据库迁移

- 使用版本化 SQL migration 文件。
- 迁移由独立 `migrate` 任务执行，不在 API 启动时隐式建表。
- 生产迁移采用 forward-only 发布策略；回退应用时依赖向前兼容 schema。
- 字段重命名和删除采用 expand/contract：先增加、双读写过渡、完成回填，再在后续版本删除旧字段。
- 每次生产迁移前创建数据库恢复点。
- CI 在空数据库和上一发布版本数据库上验证迁移。

项目不提供 SQLite 到 PostgreSQL 的数据迁移，也不保留 SQLite store。

## 17. 备份与灾难恢复

- 使用 pgBackRest 或等价工具进行物理备份和 WAL 连续归档。
- 每周全量、每日差异备份。
- 备份加密后上传到服务器之外的 S3 兼容对象存储。
- 至少保留最近 4 个每周完整备份集及其完成时间点恢复所需的 WAL；每日差异备份保留不少于 14 天。
- 每月在隔离环境执行一次真实恢复演练并记录结果。
- 发布破坏性迁移前额外创建恢复点。

首期恢复目标：

- RPO 不超过 5 分钟。
- RTO 不超过 60 分钟。

恢复手册必须覆盖新服务器初始化、镜像版本、密钥恢复、PostgreSQL 恢复、WAL 重放、账号删除清单重放、应用启动和验收检查。

## 18. 健康检查与可观测性

### 18.1 健康检查

- `/health/live`：只表示进程事件循环和关键 goroutine 存活，不访问数据库。
- `/health/ready`：验证数据库连接、所需 schema 版本和关键依赖状态。
- Worker 提供心跳与最近成功处理时间指标。

### 18.2 指标

至少采集：

- HTTP 请求量、P50/P95/P99 延迟和 4xx/5xx。
- PostgreSQL 连接池使用、等待时间、慢查询、锁等待和磁盘空间。
- Outbox 待处理数量、最老任务年龄、重试和最终失败数。
- 同步冲突率、游标重置次数和批量变更失败率。
- 登录失败、密码哈希耗时和限流次数。
- 最近成功备份时间和最近恢复演练时间。

### 18.3 日志

- 使用 JSON 结构化日志输出到标准输出。
- 字段包含时间、级别、服务、版本、请求 ID、路由、状态码和耗时。
- 不记录密码、Cookie、Session、一次性令牌、完整请求体或笔记正文。
- 用户标识在运维日志中使用受控 ID 或不可逆标识，避免记录邮箱。

由于本机监控会与服务器一起故障，还需要服务器之外的 HTTPS 可用性检测和告警渠道。

## 19. 安全基线

- Caddy 负责自动 TLS、HSTS、CSP 和其他安全响应头。
- Session Cookie 使用 `Secure`、`HttpOnly` 和 `SameSite=Lax`。
- 所有写请求校验 `Origin`；API 设置按端点区分的请求体上限。
- PostgreSQL `login_throttles` 对经过 HMAC 的标准化邮箱和规范化 IP 做共享持久限流，Caddy 不依赖第三方限流插件。
- 密码使用 Argon2id；令牌使用密码学随机数并只保存哈希。
- 容器以非 root 用户运行，根文件系统只读，并删除不必要 Linux capabilities。
- 生产密钥不进入镜像、Git、Compose 文件或普通日志。
- 服务器防火墙只开放 80/443 和受限 SSH；PostgreSQL 不开放公网。
- CI 生成 SBOM，并扫描应用依赖和容器镜像。
- 依赖和基础镜像按固定升级窗口维护。

## 20. 删除与保留策略

- 目标、里程碑、任务、日程、记录、笔记和复盘使用软删除，以便同步 tombstone。
- 目标删除时不级联删除任务；解除关系并更新相关任务版本。
- Session 使用撤销标记，过期后物理清理。
- 无引用标签可以物理清理。
- 已软删除业务实体默认在 120 天后物理清理；游标超过 90 天的设备会先执行全量重建，因此不会依赖更早 tombstone。
- `sync_changes` 默认保留 90 天。
- `client_mutations` 默认保留 30 天。
- 成功 Outbox 默认保留 7 天，最终失败记录保留 30 天。
- 审计元数据默认保留 365 天；敏感快照使用更短、可配置的字段级策略。
- 用户请求删除时立即冻结账户、撤销全部 Session，经过恢复期后异步清理在线业务数据并删除或匿名化在线审计数据。物理备份不逐行改写；它们按备份保留周期自然过期。恢复旧备份后必须先重放独立保存的账号删除清单，完成清理后才能恢复对外服务。

## 21. 测试策略

SQLite 从后端实现和测试中移除。开发、CI、预发布和生产均使用 PostgreSQL。

测试层级：

- 领域与应用服务单元测试。
- 使用真实 PostgreSQL 的 repository 集成测试。
- RLS、组合外键和跨用户访问隔离测试。
- 资源 API、字段校验、乐观锁和统一错误协议测试。
- 幂等重试、离线批量提交、同步游标和游标过期测试。
- Agent 确认、版本冲突、字段白名单和审计测试。
- Outbox 领取、并发 Worker、重试和最终失败测试。
- migration 空库测试和上一版本升级测试。
- Docker Compose 运行验收测试。
- 并发压测、慢查询分析和连接池压力测试。
- 备份恢复和新服务器灾难恢复演练。

## 22. 发布流程

```text
静态检查与测试
-> 构建不可变镜像
-> 生成 SBOM 并执行漏洞扫描
-> 创建数据库恢复点
-> 执行 migration 任务
-> 启动新 API 和 Worker
-> readiness 检查通过
-> Caddy 切换流量
-> 运行注册、登录、CRUD、同步和 Worker 验收
-> 观察错误率、延迟、Outbox 和数据库指标
```

API 设计为无状态，以支持同机多个副本或未来多机部署。单机 Compose 不能消除宿主机故障，也不承诺跨可用区连续服务。

## 23. 实施分解

该改造跨越数据库、API、前端同步、后台任务和生产运维，不应作为一个不可分割的大提交实施。后续实施计划按以下工作流拆分，并保持每个阶段可测试：

1. PostgreSQL 基础设施、迁移框架、数据库角色和 RLS。
2. 账户认证与 Session PostgreSQL 化。
3. 核心关系表、repository、应用服务和资源 API。
4. 前端资源 store、离线操作队列和增量同步。
5. Agent、审计、幂等、Outbox 和 Worker。
6. Docker Compose、Caddy、备份、监控、安全加固和运行手册。
7. 全链路验收、并发压测和恢复演练。

不建立 SQLite 双写或旧 API 适配阶段。旧实现只在新链路完成验收后从代码中删除，避免在改造过程中失去可运行基线。

## 24. 验收标准

- 新环境可以从空 PostgreSQL 数据库通过 migration 完成初始化。
- 两个注册用户无法通过 API、猜测 UUID、同步游标或数据库 RLS 访问彼此数据。
- 目标、任务、日程、记录、笔记和复盘使用资源 API 独立 CRUD。
- 单实体变更只写相关实体，不上传整个账户状态。
- 两台设备修改不同实体不会产生冲突；修改同一旧版本实体会稳定返回 `409`。
- 离线 Mutation 重试不会重复创建或重复应用。
- 每次成功业务修改都同时产生同步变化和审计记录。
- Outbox 任务在 Worker 重启和临时错误后仍可重试且不会静默丢失。
- PostgreSQL 不暴露公网端口，API 使用受限角色并启用 RLS。
- 备份能够在隔离的新 PostgreSQL 实例恢复，并达到约定的 RPO/RTO。
- 关键指标、结构化日志和外部可用性告警可用。
- 项目测试和运行环境不再依赖 SQLite。
