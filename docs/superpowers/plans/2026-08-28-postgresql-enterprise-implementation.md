# DayOrder PostgreSQL 企业级改造实施计划

- 日期：2026-08-28
- 依据：`docs/superpowers/specs/2026-08-28-postgresql-enterprise-architecture-design.md`
- 目标环境：单台云服务器、Docker Compose、PostgreSQL
- 数据策略：全新数据库，不迁移、不双写、不兼容历史 SQLite 数据或 `/state` 协议

## 1. 执行原则

本改造跨越数据库、认证、资源 API、离线同步、Agent、后台任务和生产部署，按可验证的垂直阶段实施。每个阶段必须满足：

1. 先写失败测试，再写最小实现。
2. 数据库约束和 RLS 必须由真实 PostgreSQL 集成测试验证，不能只用 mock。
3. 所有用户业务写入必须通过统一事务协调器，同时写业务实体、同步事件、审计、幂等结果和必要的 Outbox。
4. 新旧代码可以在开发分支短暂共存，但生产配置不提供 SQLite、双写或旧 `/state` 开关。
5. 新资源链路完成验收之前不删除当前可运行基线；验收通过后一次性删除旧链路。
6. 每个阶段运行本阶段测试和全量回归；失败不得带入下一阶段。
7. 生产密钥、真实邮箱、备份凭据和数据库密码不得进入仓库。

阶段 1–4 使用临时的 `apps/api/cmd/server-next` 组合 PostgreSQL 新后端，只用于开发和集成测试；当前 `cmd/server` 和前端保持可运行基线。阶段 5 完成资源同步后，把前端和正式 `cmd/server` 一次切换到新链路并删除 `server-next`，随后在阶段 8 删除残留 SQLite 和旧 `/state` 代码。该策略不双写、不导入旧数据，也不会把兼容开关带入生产镜像。

当前工作区不是 Git 仓库。正式实施前必须先初始化或接入版本控制；启用 Git 后，每个任务按文中“检查点”形成独立提交。未经版本控制不得开始生产部署。

## 2. 技术选型

- PostgreSQL 驱动：`github.com/jackc/pgx/v5` 与 `pgxpool`。
- SQL 类型生成：`sqlc`，通过 Go `tool` 依赖锁定版本；生成代码提交仓库，CI 校验生成结果无漂移。
- Migration：`golang-migrate/migrate/v4`，由独立 `cmd/migrate` 执行；生产只向前迁移。
- PostgreSQL 集成测试：`testcontainers-go`，测试使用真实 PostgreSQL 容器。
- Web 离线数据库：IndexedDB，使用轻量 `idb` 封装。
- HTTP 边缘：Caddy。
- 后台可靠任务：PostgreSQL Transactional Outbox，不引入 Redis。
- 备份：pgBackRest + S3 兼容对象存储 + WAL 连续归档。
- 指标：Prometheus client、postgres exporter、node exporter；外部 HTTPS 可用性探测独立部署。
- CI 默认实现：GitHub Actions；若仓库最终托管到其他平台，只替换工作流适配层，不改变测试脚本和发布门禁。

## 3. 目标目录边界

后端从当前 `store/sqlite.go + httpapi/server.go` 集中结构拆分为：

```text
apps/api/
  cmd/
    server/
    worker/
    migrate/
  migrations/
  internal/
    auth/             密码、令牌、认证领域逻辑
    config/           环境配置和校验
    database/         pgxpool、事务、RLS 上下文
    db/               sqlc 查询与生成代码
    model/            共享领域模型
    postgres/         repository 实现
    service/          业务用例与事务编排
    httpapi/          路由、中间件、handler、错误协议
    mail/             邮件发送接口和 SMTP 实现
    worker/           Outbox 领取与处理器
    testdb/           PostgreSQL 集成测试设施
```

前端从当前单体 `AppStore.tsx` 拆分为：

```text
apps/web/src/
  api/
    http.ts
    auth.ts
    resources.ts
    sync.ts
  offline/
    db.ts
    cache.ts
    mutations.ts
  sync/
    engine.ts
    pull.ts
    push.ts
    conflicts.ts
  store/
    AppStore.tsx
    reducer.ts
    commands.ts
    selectors.ts
```

`AppStore` 继续向现有页面提供统一上下文，减少 UI 重写；网络、持久化、Mutation 和冲突处理移到独立模块。

## 4. 阶段 0：建立基线与版本控制

### 任务 0.1：记录当前基线

**读取和验证**

- `package.json`
- `apps/web/package.json`
- `apps/api/go.mod`
- `README.md`
- 当前全部测试和构建脚本

**步骤**

1. 运行并记录：

   ```powershell
   npm run typecheck
   npm test
   go vet ./apps/api/...
   npm run build
   node scripts/validate-prototype.mjs
   npm run test:runtime
   ```

2. 将任何基线失败与本次改造分开处理，不掩盖既有失败。
3. 检查工作区中的用户文件和未跟踪文件，禁止覆盖无关内容。

**验收**

- 形成一份可重复的基线结果。
- 当前应用仍可注册、登录、同步和退出。

### 任务 0.2：接入 Git 和提交门禁

**修改**

- `.gitignore`
- 新增 `.gitattributes`
- 新增 `.editorconfig`

**步骤**

1. 若用户未把工作区接入现有仓库，则在根目录初始化 Git。
2. 确认 `data/`、`.env*`、PostgreSQL 数据目录、备份、测试临时目录和本地监控数据全部忽略。
3. 保留 `.env.example` 可提交，但禁止提交真实 `.env`。
4. 建立主分支保护和 PR 必须通过 CI 的规则。

**检查点**

- `chore: establish repository and enterprise build baseline`

## 5. 阶段 1：PostgreSQL 基础设施与迁移框架

### 任务 1.1：增加配置模型和本地 PostgreSQL

**新增**

- `compose.dev.yaml`
- `.env.example`
- `apps/api/internal/config/config.go`
- `apps/api/internal/config/config_test.go`
- `apps/api/internal/database/pool.go`
- `apps/api/internal/database/pool_test.go`
- `apps/api/internal/testdb/postgres.go`

**修改**

- `apps/api/go.mod`
- `apps/api/go.sum`
- `package.json`
- `README.md`

**测试先行**

1. 配置测试覆盖：缺少 `DATABASE_URL`、无效连接池数字、生产环境弱密钥、非法 Origin 和默认超时。
2. Testcontainers 测试启动 PostgreSQL，验证连接、UTC、查询超时和连接池关闭。
3. 验证 PostgreSQL 不可用时 readiness 失败，但 liveness 不依赖数据库。

**实现**

- 用 `DATABASE_URL` 替代 `DAYORDER_DB_PATH`。
- 配置 API 最大连接数 20、Worker 最大连接数 5，并允许环境变量覆盖。
- `compose.dev.yaml` 只将 PostgreSQL 绑定到 `127.0.0.1`，使用独立开发卷和 healthcheck。
- 增加 `npm run db:up`、`db:down`、`db:migrate`、`test:integration`。

**验证**

```powershell
docker compose -f compose.dev.yaml up -d postgres
go test ./apps/api/internal/config ./apps/api/internal/database ./apps/api/internal/testdb
```

### 任务 1.2：建立版本化 migration

**新增**

- `apps/api/cmd/migrate/main.go`
- `apps/api/internal/migrations/migrations.go`
- `apps/api/internal/migrations/migrations_test.go`
- `apps/api/internal/testdb/roles.go`
- `apps/api/migrations/000001_identity.up.sql`
- `apps/api/migrations/000002_domain.up.sql`
- `apps/api/migrations/000003_agent_sync_audit.up.sql`
- `apps/api/migrations/000004_security_functions_rls.up.sql`
- `deploy/scripts/bootstrap-roles.sql`

**测试先行**

1. 空数据库执行全部 migration 成功。
2. 重复执行无变化。
3. schema 版本落后时 readiness 失败并报告通用错误，不泄露 SQL。
4. 从上一 migration 版本升级到最新成功。
5. 所有表、索引、CHECK、唯一约束和组合外键与设计文档一致。

**实现**

- Migration 由单独二进制运行，API 启动不自动建表。
- 数据库角色属于环境预配置，不由 schema migration 动态创建。幂等 `bootstrap-roles.sql` 由数据库管理员执行，并通过 psql 变量或 Secret 注入登录凭据；Testcontainers fixture 创建同名临时角色。
- 生产不提供自动 Down；回退应用依赖向前兼容 schema 和恢复点。
- `000004` 创建认证定位函数、Outbox 领取函数、RLS policy 和最小权限 grants。

### 任务 1.3：引入 sqlc 与数据库角色测试

**新增**

- `apps/api/sqlc.yaml`
- `apps/api/internal/db/query/identity.sql`
- `apps/api/internal/db/query/domain.sql`
- `apps/api/internal/db/query/sync.sql`
- `apps/api/internal/db/query/agent.sql`
- `apps/api/internal/db/gen/`（生成代码）
- `apps/api/internal/database/roles_test.go`
- `apps/api/internal/database/rls_test.go`

**测试先行**

- API 角色不能执行 DDL。
- 用户 A 的事务不能读取、更新或关联用户 B 的实体。
- 未设置 RLS 用户上下文时，用户业务查询返回零行或被拒绝。
- Worker 只能通过受限函数领取 Outbox，不能任意扫描其他用户业务表。
- 认证定位函数只返回认证所需字段。

**验证**

```powershell
go tool sqlc generate -f apps/api/sqlc.yaml
git diff --exit-code -- apps/api/internal/db/gen
go test ./apps/api/internal/database -run 'Role|RLS'
```

**检查点**

- `feat(api): establish PostgreSQL schema migrations and RLS foundation`

## 6. 阶段 2：账户、认证和 Session PostgreSQL 化

### 任务 2.1：身份 repository 与应用服务

**新增**

- `apps/api/internal/model/account.go`
- `apps/api/internal/postgres/account_repository.go`
- `apps/api/internal/postgres/account_repository_test.go`
- `apps/api/internal/service/account.go`
- `apps/api/internal/service/account_test.go`
- `apps/api/internal/service/session.go`
- `apps/api/internal/service/session_test.go`

**测试先行**

- 标准化邮箱唯一且大小写不敏感。
- 注册创建 `pending_verification` 用户和一次性验证令牌。
- 未验证账户不能创建远端业务资源。
- 验证令牌只可消费一次、过期后失效。
- 登录统一返回无枚举信息的凭据错误。
- Session 只存 token hash，支持过期、撤销和密码轮换。
- `last_seen_at` 节流更新，不在每次请求写数据库。
- 修改密码撤销其他 Session 并轮换当前 Session。
- `login_throttles` 按规范化 IP 和标准化邮箱的 HMAC 持久限流。

### 任务 2.2：邮件 Outbox 与 SMTP 适配

**新增**

- `apps/api/internal/mail/sender.go`
- `apps/api/internal/mail/smtp.go`
- `apps/api/internal/mail/smtp_test.go`
- `apps/api/cmd/worker/main.go`
- `apps/api/internal/postgres/outbox_repository.go`
- `apps/api/internal/worker/runner.go`
- `apps/api/internal/worker/email.go`
- `apps/api/internal/worker/runner_test.go`
- `apps/api/internal/worker/email_test.go`

**实现**

- 注册事务写 `email.verification.requested` Outbox。
- 阶段 2 即提供可运行的最小 Worker，只处理验证邮件；阶段 6 在同一 Runner 上增加提醒和 Agent handler。
- Worker 渲染固定模板并调用 SMTP；开发环境使用捕获邮箱或显式日志 sink，生产禁止把令牌写日志。
- 邮件发送失败由 Outbox 指数退避重试，不回滚已经创建的待验证账号。

### 任务 2.3：拆分认证 HTTP 层

**新增**

- `apps/api/internal/httpapi/router.go`
- `apps/api/internal/httpapi/auth_handlers.go`
- `apps/api/internal/httpapi/user_handlers.go`
- `apps/api/internal/httpapi/middleware.go`
- `apps/api/internal/httpapi/errors.go`
- `apps/api/internal/httpapi/request.go`
- 对应 `*_test.go`

**修改**

- `apps/api/cmd/server-next/main.go`（临时集成入口）

当前 `apps/api/cmd/server/main.go` 和旧 `server.go` 在此阶段保持不变；新 Router 使用独立构造函数，由 `server-next` 调用。

**端点**

- 注册、验证邮箱、重发验证邮件。
- 登录、退出、当前 Session。
- 请求密码重置、完成密码重置。
- 修改显示名、邮箱和密码。

**测试先行**

- Cookie 在 HTTPS 下强制 `Secure`、始终 `HttpOnly` 和 `SameSite=Lax`。
- 写请求 Origin 校验、请求体上限和统一错误结构。
- 请求 ID 被响应、日志和审计贯穿。
- 未验证、禁用、删除中、过期 Session 等状态行为明确。
- IP 与邮箱双维度限流返回 `429` 和 `Retry-After`。

**验证**

```powershell
go test ./apps/api/internal/auth ./apps/api/internal/postgres ./apps/api/internal/service ./apps/api/internal/httpapi
```

**检查点**

- `feat(auth): move accounts and sessions to PostgreSQL`

## 7. 阶段 3：统一事务协调器、幂等、同步和审计基础

### 任务 3.1：事务与 RLS 上下文

**新增**

- `apps/api/internal/database/transaction.go`
- `apps/api/internal/database/transaction_test.go`
- `apps/api/internal/service/command.go`
- `apps/api/internal/service/command_test.go`

**实现**

- 每个认证业务命令开启事务并通过参数化 `set_config` 设置当前用户。
- 事务协调器接收业务变更函数，并统一写入审计、同步、幂等和 Outbox。
- PostgreSQL deadlock 与可重试事务错误执行有限、带抖动重试；业务版本冲突不重试。

### 任务 3.2：幂等服务

**新增**

- `apps/api/internal/postgres/idempotency_repository.go`
- `apps/api/internal/service/idempotency.go`
- 对应测试

**测试先行**

- 相同用户、设备、Mutation ID 和请求哈希只执行一次。
- 相同 ID 不同请求哈希返回 `IDEMPOTENCY_CONFLICT`。
- 并发重复请求只有一个执行者，其他请求得到相同结果。
- 结果保存与业务写入同一事务提交。

### 任务 3.3：同步日志和审计服务

**新增**

- `apps/api/internal/postgres/sync_repository.go`
- `apps/api/internal/postgres/audit_repository.go`
- `apps/api/internal/service/sync.go`
- `apps/api/internal/service/audit.go`
- 对应测试

**测试先行**

- 每次成功变更生成一个用户隔离的递增同步事件。
- 删除生成 tombstone。
- 过期游标返回 `SYNC_RESET_REQUIRED`。
- 审计过滤密码、令牌和受限正文。
- 事务回滚时不留下同步、审计或幂等残留。

**检查点**

- `feat(api): add transactional idempotency audit and sync primitives`

## 8. 阶段 4：核心资源与 API

### 任务 4.1：目标与里程碑

**新增**

- `apps/api/internal/model/goal.go`
- `apps/api/internal/postgres/goal_repository.go`
- `apps/api/internal/service/goal.go`
- `apps/api/internal/httpapi/goal_handlers.go`
- 各层对应测试

**覆盖**

- 创建、读取、游标分页、更新和软删除目标。
- 里程碑创建、排序、完成和删除。
- `If-Match` 版本校验。
- 用户隔离、字段 CHECK、日期和数值校验。

### 任务 4.2：任务

**新增**

- `apps/api/internal/model/task.go`
- `apps/api/internal/postgres/task_repository.go`
- `apps/api/internal/service/task.go`
- `apps/api/internal/httpapi/task_handlers.go`
- 各层对应测试

**覆盖**

- 状态、优先级、截止时间、排期、目标归属和来源记录。
- 删除目标时解除任务关联并为相关任务产生版本、同步和审计变化。
- 完成、恢复、归档和软删除。
- 不同任务并发更新互不冲突，同一旧版本稳定返回 `409`。

### 任务 4.3：日程与提醒

**新增**

- `apps/api/internal/model/calendar.go`
- `apps/api/internal/postgres/calendar_repository.go`
- `apps/api/internal/service/calendar.go`
- `apps/api/internal/httpapi/calendar_handlers.go`
- 各层对应测试

**覆盖**

- IANA 时区校验、开始结束约束、目标关联。
- 提醒偏移、投递渠道、软删除和版本。
- 事件变化重新计算待发送提醒，并通过 Outbox 唤醒 Worker。

### 任务 4.4：记录、笔记、复盘和标签

**新增**

- `apps/api/internal/model/content.go`
- `apps/api/internal/postgres/content_repository.go`
- `apps/api/internal/service/content.go`
- `apps/api/internal/httpapi/content_handlers.go`
- 各层对应测试

**覆盖**

- 记录 CRUD、归档、情绪和精力范围。
- Note Markdown、分类、归档、标签和跨实体链接。
- 每日复盘 `UNIQUE(user_id, review_date)`。
- 标签标准化、用户内唯一、引用解除和无引用清理。
- 笔记全文检索 `tsvector + GIN`，查询始终带用户条件。

### 任务 4.5：统一资源协议

**修改**

- `apps/api/internal/httpapi/router.go`
- `apps/api/internal/httpapi/errors.go`
- `apps/api/internal/httpapi/middleware.go`

**同时覆盖用户设置**

- 为 `user_settings` 提供读取和受版本保护的 PATCH。
- 路径使用 `GET/PATCH /api/v1/users/me/settings`。
- 设置变化增加 `version`，并写入同步、审计和幂等记录。
- JSONB 必须通过固定 schema 校验，拒绝未知顶层键和过深嵌套。

**测试先行**

- `POST` 返回 `201`，`PATCH` 支持受控 Merge Patch，`DELETE` 返回 `204`。
- 创建、修改和删除要求 Idempotency Key。
- 修改和删除要求 If-Match。
- 列表使用不透明游标和固定最大 page size。
- 资源不属于当前用户时返回 `404`。
- 错误返回 code、message、fields、retryable、requestId。

**验证**

```powershell
go test ./apps/api/internal/model ./apps/api/internal/postgres ./apps/api/internal/service ./apps/api/internal/httpapi
go vet ./apps/api/...
```

**检查点**

- `feat(api): expose versioned resource APIs for planning data`

## 9. 阶段 5：前端 IndexedDB、乐观 UI 与增量同步

### 任务 5.0：前端认证状态适配

**新增**

- `apps/web/src/api/http.ts`
- `apps/web/src/api/auth.ts`
- `apps/web/src/auth/VerificationNotice.tsx`
- 对应测试文件

**修改**

- `apps/web/src/auth/client.ts`
- `apps/web/src/auth/AuthProvider.tsx`
- `apps/web/src/components/AuthDialog.tsx`
- `apps/web/src/components/AccountControls.tsx`

**实现**

- 注册后进入“等待邮箱验证”，不再期待服务端返回整份 State。
- 增加验证链接处理、重发验证和密码重置 UI；验证成功端点建立正式 Session，使客户端可以立即提交游客迁移 Mutation。
- 账号激活后再把游客资源转换为离线 Mutation；所有 Mutation 成功前不清空游客缓存。
- 统一 API error envelope 和 field error 显示。
- 该任务与后续资源同步任务在同一切换阶段合并，完成前保持现有前端默认入口不变。

### 任务 5.1：领域类型和 UUID

**修改**

- `apps/web/src/domain/types.ts`
- `apps/web/src/domain/ids.ts`
- `apps/web/src/domain/seed.ts`
- 领域测试

**实现**

- 服务端资源增加 `version`、`createdAt`、`updatedAt`、`deletedAt`。
- 新资源使用纯随机 UUID，不再使用 `task_`、`goal_` 等前缀。
- 将里程碑和提醒的传输类型与父实体关系显式化。

### 任务 5.2：IndexedDB 存储

**新增**

- `apps/web/src/offline/db.ts`
- `apps/web/src/offline/cache.ts`
- `apps/web/src/offline/mutations.ts`
- 对应测试

**修改**

- `apps/web/package.json`
- `apps/web/src/store/storage.ts`

**对象存储**

- `entities`：按用户、类型、ID 保存实体。
- `mutations`：按设备和本地顺序保存未提交操作。
- `syncMeta`：保存 cursor、设备 ID 和最后同步时间。
- `accounts`：保存非敏感的最后账户提示。

**测试先行**

- 用户缓存隔离。
- 原子写入实体和 Mutation。
- 浏览器刷新后队列仍存在。
- 配额或 IndexedDB 错误不会静默清空内存状态。
- 登出只清理当前账户缓存，不影响游客空间或其他账户。

### 任务 5.3：资源 API client

**新增**

- `apps/web/src/api/resources.ts`
- `apps/web/src/api/sync.ts`
- 对应测试

**修改**

- `apps/web/src/api/client.ts`
- `apps/web/src/auth/client.ts`

**覆盖**

- 通用错误 envelope。
- Idempotency Key、If-Match 和请求 ID。
- 资源列表游标。
- `/sync/bootstrap` 高水位游标。
- `/sync/changes` 与 `/sync/mutations`。
- 401、409、429、503 的分类处理。

### 任务 5.4：拆分 AppStore

**新增**

- `apps/web/src/store/reducer.ts`
- `apps/web/src/store/commands.ts`
- `apps/web/src/store/selectors.ts`
- 对应测试

**修改**

- `apps/web/src/store/AppStore.tsx`
- `apps/web/src/store/AppStore.test.tsx`

**实现**

- Reducer 保持纯函数，只负责乐观内存状态。
- Command 层把现有 Action 转换为一个或多个实体 Mutation。
- UI dispatch 同时更新内存并原子持久化实体和 Mutation。
- 审计不再由前端伪造；成功同步后由服务端审计接口提供。
- 页面组件尽量不改变调用接口，减少 UI 回归。

### 任务 5.5：同步引擎

**新增**

- `apps/web/src/sync/engine.ts`
- `apps/web/src/sync/push.ts`
- `apps/web/src/sync/pull.ts`
- `apps/web/src/sync/conflicts.ts`
- 对应测试

**实现顺序**

1. 登录后从 IndexedDB 立即恢复界面。
2. 没有 cursor 时调用 `/sync/bootstrap` 取得高水位，再分页下载全部资源。
3. 全量下载完成后拉取高水位之后的变化，消除分页期间的竞态。
4. 有 cursor 时先按依赖和本地顺序推送离线 Mutation；版本冲突保留本地操作。
5. 拉取 cursor 之后的服务端变化，按实体版本合并并写入 IndexedDB。
6. 原子保存实体、Mutation 处理结果和新 cursor。
7. 网络恢复、页面重新聚焦和定时器触发下一轮同步。

**冲突测试**

- 不同实体修改不冲突。
- 相同任务旧版本保留本地待处理变更并显示冲突。
- Note 正文冲突保存独立本地副本。
- 删除 tombstone 移除缓存实体。
- `SYNC_RESET_REQUIRED` 执行分页全量重建，不清空未提交 Mutation。
- 重复服务端变化和重复 Mutation 结果均幂等。

### 任务 5.6：游客数据迁移

**修改**

- `apps/web/src/auth/AuthProvider.tsx`
- `apps/web/src/components/AuthDialog.tsx`
- `apps/web/src/store/storage.ts`
- 对应测试

**行为**

- 邮箱验证完成后把游客 AppData 转换为资源创建 Mutation。
- 按依赖排序：目标、里程碑和记录先于任务、日程、笔记关联。
- 全部 Mutation 成功后才删除游客数据。
- 部分失败时保留游客数据和可重试队列，不能产生静默丢失。

### 任务 5.7：切换正式开发运行链路

**新增**

- `scripts/runtime-postgres-acceptance.ps1`

**修改**

- `apps/api/cmd/server/main.go`
- `package.json`
- `README.md`

**删除**

- `apps/api/cmd/server-next/main.go`

**行为**

- 正式 `cmd/server` 改为 PostgreSQL Router，`npm run dev:api` 和 `npm start` 不再启动 SQLite runtime。
- 前端默认使用资源 API、IndexedDB 和增量同步。
- 旧 SQLite store 与 `/state` 源码暂时保留但不被运行入口引用，阶段 8 在完整回归后删除。
- PostgreSQL 运行验收覆盖注册验证、核心资源、两设备同步和重启持久化。

**验证**

```powershell
npm run typecheck
npm run test:web
npm run build:web
npm run test:runtime:postgres
```

**检查点**

- `feat(web): replace snapshot sync with IndexedDB resource synchronization`

## 10. 阶段 6：Agent、审计、Outbox 与 Worker

### 任务 6.1：Agent repository 与服务

**新增**

- `apps/api/internal/model/agent.go`
- `apps/api/internal/postgres/agent_repository.go`
- `apps/api/internal/service/agent.go`
- `apps/api/internal/httpapi/agent_handlers.go`
- 对应测试

**实现**

- Agent Run、Step、Change 和 Source Ref 按设计持久化。
- `patch` 只接受受控 JSON Patch；实体类型和字段使用白名单。
- Accept 时验证用户、目标实体、base version 和 pending 状态。
- 应用变更与实体写、审计、同步和 Outbox 同事务。
- Provider 通过接口注入；测试和本地开发使用确定性 provider，生产 provider 必须通过显式配置启用。

### 任务 6.2：审计读取和撤销

**新增**

- `apps/api/internal/httpapi/audit_handlers.go`
- `apps/api/internal/service/undo.go`
- 对应测试

**行为**

- 审计只读、游标分页、仅当前用户。
- 撤销是新的受版本保护业务命令，不修改历史审计行。
- 目标实体已经变化时撤销返回冲突。
- 敏感字段和超大正文执行过滤与大小限制。

### 任务 6.3：扩展 Worker 与 Outbox

**修改**

- `apps/api/cmd/worker/main.go`
- `apps/api/internal/postgres/outbox_repository.go`
- `apps/api/internal/worker/runner.go`

**新增**

- `apps/api/internal/worker/reminder.go`
- `apps/api/internal/worker/agent.go`
- 对应测试

**测试先行**

- 两个 Worker 不会同时领取同一任务。
- Worker 崩溃后锁超时任务可以重新领取。
- 指数退避、最大重试和最终失败状态正确。
- 提醒和邮件处理幂等。
- Worker 只能在目标用户 RLS 上下文读取业务实体。

### 任务 6.4：前端 Agent 和审计适配

**修改**

- `apps/web/src/pages/AgentPage.tsx`
- `apps/web/src/components/AssistantDrawer.tsx`
- `apps/web/src/store/reducer.ts`
- `apps/web/src/store/commands.ts`
- 新增 Agent 与审计 API client 和测试

**实现**

- 移除前端伪造 Agent 状态推进和直接改业务数组的逻辑。
- UI 展示服务端 Run、Step、Change 状态。
- Accept/Reject 使用版本和幂等键。
- 审计和撤销从服务端资源读取。

**检查点**

- `feat(agent): persist agent changes audit and reliable background work`

## 11. 阶段 7：生产 Docker Compose、备份与可观测性

### 任务 7.1：不可变容器镜像

**新增**

- `Dockerfile`
- `.dockerignore`
- `deploy/compose.yaml`
- `deploy/Caddyfile`
- `deploy/env.production.example`

**实现**

- 多阶段构建 Web、API、Worker 和 Migrate 二进制。
- Caddy 单独托管 `apps/web/dist`，Go 不再承担生产静态文件服务。
- 运行容器使用非 root、只读根文件系统、临时目录 tmpfs 和最小 capabilities。
- 镜像锁定版本和 digest；构建生成版本、提交 SHA 和构建时间标签。

### 任务 7.2：PostgreSQL 生产配置与密钥

**新增**

- `deploy/postgres/postgresql.conf`
- `deploy/postgres/pg_hba.conf`
- `docs/runbooks/secrets.md`

**修改**

- `deploy/scripts/bootstrap-roles.sql`

**验证**

- PostgreSQL 不映射公网端口。
- 使用 SCRAM。
- API、Worker、Migration、Backup 和 Monitor 角色权限符合测试。
- Compose 文件不包含真实密码。
- 服务器防火墙只开放 80/443 和受限 SSH。

### 任务 7.3：pgBackRest 与恢复演练

**新增**

- `deploy/pgbackrest/pgbackrest.conf.example`
- `deploy/scripts/backup-check.ps1`
- `deploy/scripts/restore-drill.ps1`
- `docs/runbooks/backup-restore.md`

**验证**

1. 执行完整备份和 WAL 归档。
2. 删除隔离测试数据库。
3. 在新 Volume 恢复完整备份并重放 WAL。
4. 重放账号删除清单。
5. 运行跨用户隔离和资源验收。
6. 记录恢复耗时，验证 RPO 不超过 5 分钟、RTO 不超过 60 分钟。

### 任务 7.4：指标、日志和告警

**新增**

- `apps/api/internal/observability/metrics.go`
- `apps/api/internal/observability/logging.go`
- `deploy/prometheus/prometheus.yml`
- `deploy/prometheus/alerts.yml`
- `docs/runbooks/alerts.md`
- 对应测试

**指标**

- HTTP 延迟和错误。
- pgxpool 使用和等待。
- 同步冲突、游标重置和批处理失败。
- Outbox backlog、最老年龄和最终失败。
- 登录限流和密码哈希耗时。
- 备份年龄和恢复演练时间。

**日志检查**

- JSON 格式包含 request ID、route、status 和 duration。
- 自动化测试断言密码、Cookie、Session、令牌和笔记正文不进入日志。

### 任务 7.5：GitHub Actions 门禁

**新增**

- `.github/workflows/ci.yml`
- `.github/workflows/security.yml`
- `.github/dependabot.yml`

**门禁**

- Web typecheck、Vitest 和生产构建。
- Go test、race、vet 和构建。
- PostgreSQL migration 与 RLS 集成测试。
- Docker Compose 运行验收。
- sqlc 生成漂移检查。
- SBOM、依赖和镜像漏洞扫描。
- 禁止含高危漏洞或失败 migration 的镜像进入发布。

**检查点**

- `chore(deploy): add hardened compose backup observability and CI`

## 12. 阶段 8：删除 SQLite 和旧快照链路

### 任务 8.1：删除后端 SQLite

**删除**

- `apps/api/internal/store/sqlite.go`
- `apps/api/internal/store/sqlite_test.go`

**修改**

- `apps/api/go.mod`
- `apps/api/go.sum`
- `apps/api/cmd/server/main.go`
- `apps/api/internal/httpapi/server.go`
- `README.md`

**要求**

- 移除 `modernc.org/sqlite`。
- 移除 `DAYORDER_DB_PATH`、`-db` 和 `data/dayorder.db` 文档。
- 健康响应不再硬编码 `storage: sqlite`。
- 移除 `/state` handler、AppData 服务端校验和 State repository 接口。

### 任务 8.2：删除前端整包同步

**删除或替换**

- `apps/web/src/api/client.ts` 中的 `getRemoteState`、`putRemoteState`。
- `AppStore.tsx` 中的 500 ms 整包上传、revision、fingerprint 和全账户 conflict key。
- `storage.ts` 中的旧 State revision 保存逻辑。

**要求**

- 游客仍可本地使用。
- 登录账户只通过资源 API 和同步引擎访问远端。
- Service Worker 继续排除 `/api/*`。

### 任务 8.3：重写运行验收

**修改**

- `scripts/runtime-acceptance.ps1`
- `package.json`

**新增**

- `scripts/load-smoke.js`
- `scripts/security-acceptance.ps1`

**运行验收覆盖**

- PostgreSQL 空库 migration。
- 注册、邮箱验证、登录、密码重置和 Session 轮换。
- 两用户 RLS 和 API 隔离。
- 每类核心资源 CRUD。
- 两设备不同实体同步、同实体版本冲突、游标过期全量重建。
- 幂等重试、Outbox、Worker 重启和提醒重试。
- Caddy TLS 代理、SPA 深链接和 API 路由。
- API 和 Worker 重启后数据、Session、队列和游标持续存在。
- PostgreSQL 不暴露公网。

**验证**

```powershell
npm run typecheck
npm test
go test -race ./apps/api/...
go vet ./apps/api/...
npm run build
npm run test:runtime
docker compose -f deploy/compose.yaml config
```

### 任务 8.4：文档和运行手册

**修改**

- `README.md`
- `docs/dayorder-product-spec.md`

**新增**

- `docs/runbooks/deploy.md`
- `docs/runbooks/rollback.md`
- `docs/runbooks/incident.md`
- `docs/runbooks/user-deletion.md`
- `docs/runbooks/database-maintenance.md`

**检查点**

- `refactor: remove SQLite snapshot storage and finalize PostgreSQL runtime`

## 13. 最终验收矩阵

| 领域 | 必须通过的证据 |
|---|---|
| 数据库 | 空库迁移、升级迁移、约束、索引、RLS、受限角色测试 |
| 隔离 | API、repository 和原始 SQL 三层两用户越权测试 |
| 认证 | 验证邮件、密码重置、Session 轮换、限流和安全 Cookie |
| 资源 API | 所有核心资源 CRUD、分页、If-Match 和字段校验 |
| 离线同步 | Mutation 幂等、不同实体并发、同实体冲突、游标重建 |
| Agent | 字段白名单、确认门禁、base version 和审计 |
| Worker | 并发领取、崩溃恢复、退避、最终失败和幂等处理 |
| 备份 | 新 Volume 恢复、WAL 重放、删除清单重放、RPO/RTO 记录 |
| 安全 | 非 root、只读 FS、无公网 PostgreSQL、日志脱敏、镜像扫描 |
| 可观测性 | readiness、指标、告警、request ID 和外部可用性探测 |
| 前端 | 游客、本地缓存、登录账号、离线恢复、冲突 UI 和响应式回归 |

## 14. 完成定义

仅当以下条件全部满足时，改造才算完成：

- 生产和测试不再依赖 SQLite 或 `/state`。
- PostgreSQL schema、RLS、资源 API、同步、幂等、审计和 Outbox 全部自动化验证。
- 前端只同步发生变化的实体，离线数据和未提交 Mutation 在刷新、断网和重启后仍安全。
- 两个用户不能通过任何已知接口或关联路径访问彼此数据。
- Docker Compose 可以从空服务器启动，并通过 readiness 和运行验收。
- 异地备份能够在新 Volume 恢复，并有实际 RPO/RTO 记录。
- CI、漏洞扫描、日志、指标、告警和运行手册可用。
- 设计文档中的验收标准全部有测试、脚本或恢复报告作为证据。
