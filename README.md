# 日序 DayOrder

日序是面向个人用户的本地优先计划服务。React/Vite 前端提供游客空间、IndexedDB 离线缓存和乐观交互；Go API、Worker 与 PostgreSQL 负责多用户身份、资源持久化、增量同步、审计和可靠后台任务。首版不提供组织、团队或共享目标协作。

架构与实施依据：

- [PostgreSQL 企业级架构设计](docs/superpowers/specs/2026-08-28-postgresql-enterprise-architecture-design.md)
- [PostgreSQL 企业级实施计划](docs/superpowers/plans/2026-08-28-postgresql-enterprise-implementation.md)

## 目录结构

```text
apps/web/        React 19 + TypeScript + Vite
apps/api/        Go API、Worker、Migration 与 PostgreSQL repository
deploy/          数据库角色初始化；生产 Compose 文件在后续部署阶段补齐
docs/            产品、架构、实施计划和运行手册
scripts/         构建与真实运行验收脚本
```

根目录通过 npm workspaces 管理前端，通过 `go.work` 管理 Go 模块。

## 数据模型与同步

- 目标、里程碑、任务、日程、提醒、记录、笔记、复盘和标签使用关系表；设置、Agent scope/patch 等结构灵活且不参与核心查询的字段才使用 JSONB。
- 所有用户资源都带 `user_id`，关键外键同时包含 `user_id`；PostgreSQL RLS 作为应用过滤之外的第二层租户隔离。
- 登录账户的浏览器缓存保存在 IndexedDB：`entities`、`mutations`、`syncMeta` 和 `accounts` 四个对象存储按账户隔离。
- UI 变更立即更新内存，并在同一个 IndexedDB 事务中写入乐观实体和 Mutation。同一实体的连续离线更新会合并，避免客户端自己制造旧版本冲突。
- 同步使用实体版本、设备顺序、幂等 Mutation、opaque cursor 和增量 change feed；首次同步使用 bootstrap 高水位、分页快照和追赶拉取，不再每 500 ms 上传整份账户 JSON。
- 笔记标签使用关联表；笔记跨实体弱关联使用 `entity_links`，服务层在同一用户事务中校验目标存在。

游客数据仍只写浏览器 localStorage。邮箱验证成功后，用户可以把游客资源按依赖顺序转换成离线 Mutation；只有全部提交完成才清理游客副本。

## 本地开发

环境要求：Node.js 22.22+（或 24.15+）、Go 1.25+、Docker 与 Docker Compose。

```powershell
npm install
Copy-Item .env.example .env
Get-Content .env | Where-Object { $_ -match '^[^#].*=' } | ForEach-Object {
  $name, $value = $_ -split '=', 2
  Set-Item -Path "Env:$name" -Value $value
}
npm run db:up
npm run db:migrate
npm run db:check
npm run dev
```

新建开发卷会自动创建相互隔离的 `dayorder_migrator`、`dayorder_api` 和 `dayorder_worker` 角色。旧开发卷或轮换密码后可运行 `npm run db:bootstrap`。API 角色不拥有 DDL 权限，Worker 使用独立受限连接。

开发进程：

- Web：通常为 <http://127.0.0.1:5173>
- PostgreSQL API：<http://127.0.0.1:8080>
- 健康检查：<http://127.0.0.1:8080/api/v1/health>
- Worker：另一个终端运行 `npm run dev:worker`

开发默认 `DAYORDER_MAIL_SINK=log`，不会投递邮件，也不会把验证或重置令牌写入日志。走真实邮件流程时配置 SMTP；生产环境强制 SMTP 与 TLS。

## 认证与离线账户

- 注册先创建 `pending_verification` 账户；验证邮箱成功后才建立正式 Session。
- 密码使用 Argon2id；30 天不透明 Session 只通过 `HttpOnly`、`SameSite=Lax` Cookie 传递，数据库只保存令牌 SHA-256 哈希。
- 登录已有账户不会合并游客数据。
- API 暂时不可达时，已缓存账户仍可从 IndexedDB 打开和编辑；网络恢复、页面聚焦或周期定时器会继续同步。
- 正常退出先撤销服务端 Session，再只清理当前账户的 IndexedDB 缓存，不影响游客空间和其他账户缓存。
- Agent 需要已验证且在线的账户。当前 Agent UI 仍待持久化阶段切换为服务端 Run/Change 模型。

## 测试与构建

```powershell
npm run typecheck
npm run test:web
go test ./apps/api/...
go vet ./apps/api/...
npm run build
npm run test:runtime:postgres
```

真实 PostgreSQL 集成测试和运行验收需要 Docker。Docker 或 daemon 不可用时会明确输出 `SKIPPED`；测试不会用 SQLite 冒充 PostgreSQL。

`test:runtime:postgres` 使用隔离的 Compose project 和临时 volume，验证注册、邮箱验证、核心资源、两设备增量同步以及 API 重启后的 Session/资源持久化，结束后只删除它创建的隔离资源。

## 常用命令

| 命令 | 作用 |
| --- | --- |
| `npm run dev` | 同时启动 Vite 与正式 PostgreSQL API |
| `npm run dev:worker` | 启动 Outbox Worker |
| `npm run db:up` / `db:down` | 启停本地 PostgreSQL |
| `npm run db:migrate` / `db:check` | 执行或检查 schema migration |
| `npm run db:generate` | 使用 sqlc 重新生成数据库访问代码 |
| `npm run build` | 构建 Web、API 和 Worker |
| `npm start` | 启动正式 PostgreSQL API；生产静态资源将由 Caddy 托管 |

旧 SQLite store 和旧 `/state` 源码当前只作为未引用的回归基线保留，正式 `cmd/server`、前端和 npm 运行入口均已不再使用；完整生产回归后会在清理阶段删除。

## 核心配置

| 变量 | 作用 |
| --- | --- |
| `DATABASE_URL` | API 受限角色 PostgreSQL URL，必填 |
| `MIGRATION_DATABASE_URL` | Migration 角色 PostgreSQL URL |
| `WORKER_DATABASE_URL` | Worker 受限角色 PostgreSQL URL |
| `DAYORDER_ENV` | `development`、`test` 或 `production` |
| `DAYORDER_ADDR` | API 监听地址 |
| `DAYORDER_PUBLIC_URL` | 邮件链接和公开服务根地址；生产必须 HTTPS |
| `DAYORDER_ALLOWED_ORIGINS` | 允许携带凭据的 Web Origin |
| `DAYORDER_AUTH_HMAC_KEY` | 至少 32 字节的认证/游标签名密钥 |
| `DAYORDER_MAIL_SINK` | `log` 或 `smtp`；生产必须 `smtp` |
| `VITE_API_BASE_URL` | 前端 API 根路径，默认 `/api/v1` |
| `VITE_API_PROXY_TARGET` | Vite `/api` 代理目标 |

完整示例见 [.env.example](.env.example)。

## API 概览

所有业务接口位于 `/api/v1`：

- 认证：注册、邮箱验证/重发、登录、退出、Session、忘记/重置密码。
- 账户：资料、邮箱、密码、设置和设备管理。
- 资源：Goals/Milestones、Tasks、Calendar Events/Reminders、Records、Notes、Daily Reviews、Tags。
- 同步：`GET /sync/bootstrap`、`GET /sync/changes`、`POST /sync/mutations`。
- 运维：`GET /health` 和 `GET /ready`。

资源写入使用 `Idempotency-Key`、`X-Device-ID` 和 `If-Match`；错误采用统一 envelope，并区分认证、验证、冲突、限流和暂时不可用。
