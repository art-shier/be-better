# 日序 DayOrder

日序是面向个人用户的本地优先计划服务。React/Vite 前端提供游客空间、IndexedDB 离线缓存和乐观交互；Go API、Worker 与 PostgreSQL 负责多用户身份、资源持久化、增量同步、审计和可靠后台任务。首版不提供组织、团队或共享目标协作。

架构与实施依据：

- [PostgreSQL 企业级架构设计](docs/superpowers/specs/2026-08-28-postgresql-enterprise-architecture-design.md)
- [PostgreSQL 企业级实施计划](docs/superpowers/plans/2026-08-28-postgresql-enterprise-implementation.md)

## 目录结构

```text
apps/web/        React 19 + TypeScript + Vite
apps/api/        Go API、Worker、Migration 与 PostgreSQL repository
deploy/          生产 Compose、Caddy、PostgreSQL、pgBackRest 与 Prometheus
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

游客数据仍只写浏览器 localStorage。注册直接建立 Session 后，用户可以把游客资源按依赖顺序转换成离线 Mutation；只有全部提交完成才清理游客副本。

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
- 存活检查：<http://127.0.0.1:8080/health/live>
- 就绪检查：<http://127.0.0.1:8080/health/ready>
- Worker：另一个终端运行 `npm run dev:worker`

开发默认 `DAYORDER_MAIL_SINK=log`，不会投递邮件。当前邮箱验证与忘记密码接口返回未接入错误；提醒邮件等 Worker 邮件任务走真实流程时配置 SMTP，生产环境强制 SMTP 与 TLS。

## 认证与离线账户

- 注册只校验邮箱格式，直接创建 `active` 账户并建立正式 Session；修改邮箱同样直接生效且不生成验证邮件。
- 邮箱验证/重发与忘记/重置密码接口当前返回 `503` 未接入错误。
- 密码使用 Argon2id；30 天不透明 Session 只通过 `HttpOnly`、`SameSite=Lax` Cookie 传递，数据库只保存令牌 SHA-256 哈希。
- 登录已有账户不会合并游客数据。
- API 暂时不可达时，已缓存账户仍可从 IndexedDB 打开和编辑；网络恢复、页面聚焦或周期定时器会继续同步。
- 正常退出先撤销服务端 Session，再只清理当前账户的 IndexedDB 缓存，不影响游客空间和其他账户缓存。
- Agent 功能当前暂未接入：Web 隐藏入口，相关 API 返回 `503 AGENT_NOT_AVAILABLE`，Worker 不调用任何 Agent Provider。现有领域模型和数据表保留，等待后续重构。

## 测试与构建

```powershell
npm run typecheck
npm test
go vet ./apps/api/...
npm run build
npm run test:architecture
npm run test:security
npm run test:runtime
```

真实 PostgreSQL 集成测试和运行验收需要 Docker。Docker 或 daemon 不可用时会明确输出 `SKIPPED`；测试不会用 SQLite 冒充 PostgreSQL。

`test:runtime` 使用隔离的 Compose project 和临时 volume，验证空库 migration、认证、两用户隔离、关系资源 CRUD、两设备增量同步、幂等与版本冲突、API/Worker 重启、Outbox 和并发负载；随后执行部署安全检查。结束后只删除它创建的隔离资源。CI 还会启动完整生产 Compose，验证 Caddy TLS、SPA 深链接、API 代理和容器安全属性。

## 常用命令

| 命令 | 作用 |
| --- | --- |
| `npm run dev` | 同时启动 Vite 与正式 PostgreSQL API |
| `npm run dev:worker` | 启动 Outbox Worker |
| `npm run db:up` / `db:down` | 启停本地 PostgreSQL |
| `npm run db:migrate` / `db:check` | 执行或检查 schema migration |
| `npm run db:generate` | 使用独立 `tools.mod` 中的 sqlc 重新生成数据库访问代码 |
| `npm run build` | 构建 Web、API 和 Worker |
| `npm start` | 启动正式 PostgreSQL API；生产静态资源将由 Caddy 托管 |

## Docker Compose 单机部署（保留）

生产仅支持全新 PostgreSQL 数据库，不提供其他数据库导入、双写或旧快照协议。先按 [密钥手册](docs/runbooks/secrets.md) 创建 `deploy/.env.production` 和 `deploy/secrets/*`，再执行：

```powershell
docker compose --env-file deploy/.env.production -f deploy/compose.yaml config
docker compose --env-file deploy/.env.production -f deploy/compose.yaml build --pull
docker compose --env-file deploy/.env.production -f deploy/compose.yaml up -d
Invoke-WebRequest https://你的域名/health/ready
```

PostgreSQL 不发布主机端口；公网只开放 Caddy 的 80/443。上线、回滚、事故、用户删除和数据库维护分别见 [运行手册目录](docs/runbooks/)。备份与恢复目标为 RPO ≤ 5 分钟、RTO ≤ 60 分钟。

## ConfigHub PostgreSQL 配置

ConfigHub CLI 在当前接入中是只读客户端：配置值需要在 ConfigHub Web 中维护，CLI 只负责读取并注入当前进程。数据库配置固定存放在项目/环境 `shier/prod`，需要以下七个键：`db_address`、`db_port`、`db_username`、`db_password`、`db_migrator_password`、`db_api_password`、`db_worker_password`。认证 HMAC 和 Worker 的 SMTP 密码分别使用同一环境中的 `dayorder_auth_hmac_key`、`dayorder_smtp_password`。`db_username` 不得使用三个固定运行时角色名，四个数据库密码必须彼此不同。本地 `.confighub.yaml` 包含 Machine Token，已被 Git 忽略，不得复制到其他项目文件。

首次初始化或角色密码轮换时执行：

```powershell
npm run config:db:preflight
npm run config:db:bootstrap
npm run config:db:check
```

`preflight` 只读检查 PostgreSQL TLS、管理员建库/建角色能力和既有对象所有权；必须先通过它再执行 Bootstrap。Bootstrap 只处理固定的 `dayorder-test`、`dayorder` 两个数据库以及 `dayorder_migrator`、`dayorder_api`、`dayorder_worker` 三个角色，可重复执行，绝不会删除数据库、Schema 或角色。默认开发环境和本地业务验证只连接 `dayorder-test`：

```powershell
npm run config:dev:api
npm run config:dev:worker
```

生产运行或生产 schema 检查必须显式设置 `DAYORDER_ENV=production`；建议在独立 PowerShell 作用域中设置，避免污染后续本地命令：

```powershell
& {
  $env:DAYORDER_ENV = 'production'
  confighub run --project shier --env prod -- go run ./apps/api/cmd/migrate -check
}
```

轮换任一数据库角色密码时，顺序固定为：先在 ConfigHub 发布包含新密码的 Revision，再运行 Bootstrap 更新 PostgreSQL 角色，最后重启受影响的 API、Worker 或 Migrator。不要把 `confighub export` 的原始输出、密码、Token 或完整数据库 URL 粘贴到日志、Issue、聊天记录或提交中。

## 核心配置

| 变量 | 作用 |
| --- | --- |
| `DATABASE_URL` | API PostgreSQL URL 显式覆盖；未设置时开发/生产环境回退到 ConfigHub |
| `MIGRATION_DATABASE_URL` | Migration PostgreSQL URL 显式覆盖；未设置时开发/生产环境回退到 ConfigHub |
| `WORKER_DATABASE_URL` | Worker PostgreSQL URL 显式覆盖；未设置时开发/生产环境回退到 ConfigHub |
| `DAYORDER_ENV` | `development`、`test` 或 `production` |
| `DAYORDER_ADDR` | API 监听地址 |
| `DAYORDER_PUBLIC_URL` | 邮件链接和公开服务根地址；生产必须 HTTPS |
| `DAYORDER_ALLOWED_ORIGINS` | 允许携带凭据的 Web Origin |
| `DAYORDER_AUTH_HMAC_KEY` | 认证/游标签名密钥的旧版显式覆盖；Release 默认使用 ConfigHub 的 `dayorder_auth_hmac_key` |
| `DAYORDER_MAIL_SINK` | `log` 或 `smtp`；生产必须 `smtp` |
| `VITE_API_BASE_URL` | 前端 API 根地址；开发默认 `/api/v1`，生产默认 `https://better-api.shier.art/api/v1`，仅当显式设置值去除首尾空白后为非空值时覆盖默认值 |
| `VITE_API_PROXY_TARGET` | Vite `/api` 代理目标 |

完整示例见 [.env.example](.env.example)。

## GitHub Release Linux 部署

生产部署的首选入口是公开 GitHub Release。部署机需要 Linux、Bash、`curl`、`tar`、`sha256sum`、`flock` 和常见 GNU 工具；部署 Server 或 Worker 的机器还需要 ConfigHub CLI，Web-only 部署不需要。Web、Server 和 Worker 可以在不同机器上分别运行。首次在部署根目录执行：

```bash
mkdir -p ~/a
cd ~/a
curl -fsSLO https://github.com/art-shier/be-better/releases/latest/download/dayorder-deploy.sh
chmod 0755 dayorder-deploy.sh
./dayorder-deploy.sh all
```

后续可用 `./dayorder-deploy.sh upgrade all` 升级应用到 GitHub 最新 Release；该命令不接受 `--version`，也不会自更新 `dayorder-deploy.sh`。需要在版本未变化时重新生成 unit、执行 Server migration 检查并重启 API/Worker，可运行 `./dayorder-deploy.sh redeploy all`，或用 `--version vX.Y.Z` 重部署指定版本。服务启停和状态检查可使用 `start`、`stop`、`restart`、`status`，例如 `./dayorder-deploy.sh restart all`；这些命令只管理 API 与 Worker，Web 没有独立的 systemd 服务，只报告当前 `current-web`。旧的 `web|server|worker|all` 部署命令保持兼容。

首次运行 Server 或 Worker 时，脚本会创建 `~/a/dayorder-config/{api.env,migrate.env,worker.env,secrets/}` 并停止。把包含 ConfigHub Server 和 Machine Token 的 `.confighub.yaml` 放到 `~/a/dayorder-config/`；启动脚本会在该目录执行 `confighub run --project shier --env prod`，不再使用数据库 URL 密钥文件。ConfigHub CLI 自行处理配置文件、连接和权限错误，任一失败都会原样输出并停止部署。

认证 HMAC 和 SMTP 密码分别从 ConfigHub 的 `dayorder_auth_hmac_key`、`dayorder_smtp_password` 注入，Release 不再要求本地 `secrets/auth_hmac_key`、`secrets/smtp_password` 或 `secrets/agent_http_key`。Agent 当前暂未接入，Web 入口和 Worker Provider 均已屏蔽。填写环境文件后限制权限，再重新运行 `./dayorder-deploy.sh all`：

```bash
chmod 0700 ~/a/dayorder-config ~/a/dayorder-config/secrets
chmod 0600 ~/a/dayorder-config/api.env ~/a/dayorder-config/migrate.env ~/a/dayorder-config/worker.env
```

从旧版本升级时，部署器不会覆盖已有的 `migrate.env`；必须确认其中包含 `DAYORDER_ENV=production`，否则会在实际 migration 以及 Migrator 的 ConfigHub 调用之前安全失败。旧版环境文件引用的数据库 URL、`auth_hmac_key`、`smtp_password` 和 `agent_http_key` 密钥文件应保留到相邻 Release 的回滚窗口结束。新包装器会清除数据库 URL 覆盖；存在的旧 HMAC/SMTP 文件仍作为兼容回退读取，但 ConfigHub 小写键优先；Agent 密钥不再使用。回滚后的旧脚本仍需要这些文件。

如果脚本要求启用用户级 systemd linger，只需执行一次 `sudo loginctl enable-linger "$USER"`，然后再次运行部署命令。

Web 仅更新 `~/a/current-web` 的静态资源链接；请让 Nginx/Caddy 的站点根目录指向该链接。API 和 Worker 会作为两个独立的 `systemd --user` 服务运行；部署器会把预检通过的 ConfigHub CLI 绝对路径写入 unit，避免依赖 systemd 是否继承交互 shell 的 PATH。完整的首次安装、更新、日志、指定版本和回退限制见[前后端分离部署手册](docs/runbooks/separate-deployment.md)。

Schema 检查拒绝 dirty schema 和低于二进制内嵌 migration floor 的版本；它只在 expand/contract 约束下接受 clean schema at or above the embedded migration floor。这不是无限向前兼容承诺：升级和应用回退均为 adjacent-release only，不得跨过多个 Release。恢复旧 Server 链接后，部署器会重启旧 API 并再次检查 `/health/ready`；若仍不健康，会输出 `restored API failed readiness; manual intervention required` 并以失败退出。

### 本地构建/离线传输

项目提供不依赖 Docker 的 Linux 构建与运行脚本。构建机需要 Node.js 22.22+（或 24.15+）、npm、Go 1.25+ 和 Bash；后端运行服务器不需要安装 Node.js 或 Go。

#### 1. 构建并部署前端

开发和测试默认请求 `/api/v1`，Vite 会将 `/api` 代理到 `http://127.0.0.1:8080`。

未显式设置 `VITE_API_BASE_URL` 时，普通 `npm run build:release:web` 是生产构建，默认写入 `https://better-api.shier.art/api/v1`：

```bash
npm run build:release:web
```

预发布或其他部署可以在构建时覆盖默认地址：

```bash
VITE_API_BASE_URL=https://staging-api.example.com/api/v1 npm run build:release:web
```

静态产物位于 `release/web/`，将该目录内容上传到 Nginx、Caddy、对象存储或 CDN 的站点根目录即可，例如：

```bash
rsync -av release/web/ deploy@web.example.com:/var/www/dayorder/
```

前端没有需要启动的 Node.js 服务。静态服务器需要把未知 SPA 路由回退到 `index.html`。`VITE_API_BASE_URL` 会写入静态 JS，API 地址变化后必须重新构建并重新部署前端。

#### 2. 构建并部署后端

构建当前机器架构的 Linux API、Worker 和 Migrator：

```bash
npm run build:release:backend
```

需要交叉构建时可以显式指定架构：

```bash
GOARCH=amd64 npm run build:release:backend
# 或者
GOARCH=arm64 npm run build:release:backend
```

上传完整后端发布目录：

```bash
ssh deploy@api.example.com 'sudo install -d -o deploy -g deploy /opt/dayorder/releases/0.3.0'
rsync -av release/backend/ deploy@api.example.com:/opt/dayorder/releases/0.3.0/
ssh deploy@api.example.com 'sudo chown -R dayorder:dayorder /opt/dayorder/releases/0.3.0'
```

`release/backend/` 包含：

```text
bin/dayorder-api
bin/dayorder-worker
bin/dayorder-migrate
scripts/start-api.sh
scripts/start-worker.sh
scripts/migrate.sh
config/api.env.example
config/worker.env.example
config/migrate.env.example
```

#### 3. 准备后端配置

在后端服务器分别创建 API、Worker 和 Migrator 配置：

```bash
sudo install -d -m 0750 -o root -g dayorder /etc/dayorder /etc/dayorder/secrets
sudo install -m 0640 -o root -g dayorder /opt/dayorder/releases/0.3.0/config/api.env.example /etc/dayorder/api.env
sudo install -m 0640 -o root -g dayorder /opt/dayorder/releases/0.3.0/config/worker.env.example /etc/dayorder/worker.env
sudo install -m 0640 -o root -g dayorder /opt/dayorder/releases/0.3.0/config/migrate.env.example /etc/dayorder/migrate.env
```

把 `.confighub.yaml` 放在三个环境文件所在的 `/etc/dayorder/`。启动脚本通过 `confighub run --project shier --env prod` 为 API、Worker、Migrator 注入各自角色所需的数据库配置，并为 API/Worker 注入 `dayorder_auth_hmac_key`、为 Worker 注入 `dayorder_smtp_password`；无需创建应用密钥文件。Agent 当前暂未接入。

前端部署在 `https://app.example.com` 时，API 配置至少需要调整：

```bash
DAYORDER_PUBLIC_URL=https://app.example.com
DAYORDER_ALLOWED_ORIGINS=https://app.example.com
```

#### 4. 执行数据库迁移

每次启动新版本前先执行 migration，再检查 schema 版本：

```bash
cd /opt/dayorder/releases/0.3.0
sudo -u dayorder ./scripts/migrate.sh up /etc/dayorder/migrate.env
sudo -u dayorder ./scripts/migrate.sh check /etc/dayorder/migrate.env
```

任一命令失败都应停止发布，不要继续重启 API 或 Worker。

#### 5. 分别启动 API 和 Worker

API 和 Worker 是两个独立的前台服务：

```bash
cd /opt/dayorder/releases/0.3.0
sudo -u dayorder ./scripts/start-api.sh /etc/dayorder/api.env
```

另一个终端或独立的 systemd/Supervisor 服务启动 Worker：

```bash
cd /opt/dayorder/releases/0.3.0
sudo -u dayorder ./scripts/start-worker.sh /etc/dayorder/worker.env
```

启动脚本使用 `exec` 保持前台运行，不自行放入后台。生产环境应让进程管理器负责开机启动、日志、重启和停止超时。

#### 6. 发布后检查

```bash
curl --fail --silent --show-error https://api.example.com/health/live
curl --fail --silent --show-error https://api.example.com/health/ready
curl --fail --silent --show-error http://127.0.0.1:9091/metrics >/dev/null
```

完整的服务器依赖、跨域配置、上传、迁移、启动和健康检查命令见 [前后端分离部署手册](docs/runbooks/separate-deployment.md)。原有 Docker Compose 部署路径继续保留，但不是该流程的依赖。

## API 概览

所有业务接口位于 `/api/v1`：

- 认证：注册、登录、退出和 Session；邮箱验证/重发及忘记/重置密码当前返回 `503` 未接入错误。
- 账户：资料、邮箱、密码、设置和设备管理。
- 资源：Goals/Milestones、Tasks、Calendar Events/Reminders、Records、Notes、Daily Reviews、Tags。
- 同步：`GET /sync/bootstrap`、`GET /sync/changes`、`POST /sync/mutations`。
- Agent/审计：Agent 路径暂时返回 `503 AGENT_NOT_AVAILABLE`；Audit Events 和服务端撤销保持可用。
- 运维：`GET /health/live`、`GET /health/ready`；指标使用独立内部监听端口。

资源写入使用 `Idempotency-Key`、`X-Device-ID` 和 `If-Match`；错误采用统一 envelope，并区分认证、验证、冲突、限流和暂时不可用。
