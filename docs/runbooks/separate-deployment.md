# Linux 前后端分离部署手册

本手册用于将 DayOrder Web、API、Worker 和数据库迁移器分别部署到 Linux。该流程不构建或运行 Docker 容器，也不负责安装 PostgreSQL、反向代理或进程管理器。

## 依赖与服务边界

构建机需要 Node.js 22.22+（或 24.15+）、npm、Go 1.25+、Bash、`realpath` 和常见 GNU 工具。后端运行机只需要 Linux、CA 证书、时区数据、Bash 以及可访问的 PostgreSQL。

- Web 是纯静态资源。
- API 是长期运行的 HTTP 服务。
- Worker 是长期运行的异步任务服务。
- Migrator 是发布期间执行一次的数据库任务。

## 构建 Web

开发和测试默认请求 `/api/v1`，Vite 会将 `/api` 代理到 `http://127.0.0.1:8080`。

未显式设置 `VITE_API_BASE_URL` 时，普通 `npm run build:release:web` 是生产构建，默认写入 `https://better-api.shier.art/api/v1`：

```bash
npm run build:release:web
```

预发布或其他部署可以在构建时覆盖默认地址：

```bash
VITE_API_BASE_URL=https://staging-api.example.com/api/v1 npm run build:release:web
```

产物位于 `release/web/`。把该目录的内容同步到静态服务器站点根目录；SPA 服务必须把未知前端路由回退到 `index.html`。跨域部署还需将 Web Origin 写入 API 的 `DAYORDER_ALLOWED_ORIGINS`，并保持 HTTPS 与凭据请求配置一致。为了让当前 `SameSite=Lax` Session Cookie 稳定工作，优先使用同域反向代理，或使用同一注册域下的 Web/API 子域名。

## 构建 Backend

默认构建当前机器架构的 Linux 二进制：

```bash
npm run build:release:backend
```

也可以显式构建目标架构：

```bash
GOARCH=amd64 npm run build:release:backend
GOARCH=arm64 npm run build:release:backend
```

产物位于 `release/backend/`。上传整个目录，不要只上传 `bin/`，因为运行脚本通过相对路径寻找二进制。

## 安装目录与配置

以下示例把版本产物安装到 `/opt/dayorder/releases/0.2.0`，配置和密钥放到版本目录之外。系统用户只需首次创建：

```bash
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin dayorder
sudo install -d -o dayorder -g dayorder /opt/dayorder/releases/0.2.0
sudo cp -a release/backend/. /opt/dayorder/releases/0.2.0/
sudo chown -R dayorder:dayorder /opt/dayorder/releases/0.2.0
sudo install -d -m 0750 -o root -g dayorder /etc/dayorder /etc/dayorder/secrets
sudo install -m 0640 -o root -g dayorder deploy/bare-metal/config/api.env.example /etc/dayorder/api.env
sudo install -m 0640 -o root -g dayorder deploy/bare-metal/config/worker.env.example /etc/dayorder/worker.env
sudo install -m 0640 -o root -g dayorder deploy/bare-metal/config/migrate.env.example /etc/dayorder/migrate.env
```

如果 `dayorder` 用户已存在，跳过 `useradd`。编辑三个环境文件中的域名、SMTP、Agent 和容量设置。`DAYORDER_PUBLIC_URL` 应指向用户能打开验证与重置页面的 Web Origin。分别创建以下密钥文件并保持 `0640 root:dayorder` 权限：

- `api_database_url`
- `worker_database_url`
- `migration_database_url`
- `auth_hmac_key`
- `smtp_password`
- `agent_http_key`

数据库 URL 应使用三个不同的 PostgreSQL 账号。API 和 Worker 不得使用 migrator 账号。密钥文件只能包含单行值，末尾换行会由启动脚本移除。

## 执行数据库迁移

每次启动新应用版本前执行：

```bash
cd /opt/dayorder/releases/0.2.0
sudo -u dayorder ./scripts/migrate.sh up /etc/dayorder/migrate.env
sudo -u dayorder ./scripts/migrate.sh check /etc/dayorder/migrate.env
```

任一命令失败都应停止发布。迁移只向前执行，不提供自动降级。

## 启动 API 与 Worker

启动脚本通过 `exec` 保持前台并把信号直接交给 Go 进程，应由 systemd、Supervisor 或同类工具管理：

```bash
cd /opt/dayorder/releases/0.2.0
sudo -u dayorder ./scripts/start-api.sh /etc/dayorder/api.env
sudo -u dayorder ./scripts/start-worker.sh /etc/dayorder/worker.env
```

为 API 和 Worker 创建两个独立服务单元，分别设置工作目录、`ExecStart`、非 root 用户、重启策略和停止超时。不要在启动脚本外再套 `nohup` 或 `&`。API 建议预留至少 30 秒停止时间，Worker 至少 60 秒。

## 反向代理与静态站点

公网反向代理只需要把 `/api/*` 和 `/health/*` 转发到 API 的 `DAYORDER_ADDR`。API 和 Worker 指标地址默认绑定回环接口，不应直接暴露公网。Worker 没有业务 HTTP 入口。

如果 Web 与 API 分属不同域名，认证 Cookie 请求依赖浏览器凭据模式和 API CORS 白名单；两端都必须使用 HTTPS。

## 发布后检查

```bash
curl --fail --silent --show-error https://api.example.com/health/live
curl --fail --silent --show-error https://api.example.com/health/ready
curl --fail --silent --show-error http://127.0.0.1:9090/metrics >/dev/null
curl --fail --silent --show-error http://127.0.0.1:9091/metrics >/dev/null
```

随后检查 API 与 Worker 日志，并人工完成注册、邮箱验证、登录、资源写入和同步。API `/health/ready` 会拒绝 schema 版本落后的数据库。

## 升级与回退

新版本先上传到新的版本目录，执行 migration 和检查后再切换两个服务。保留上一版完整后端目录和 Web 产物，便于回退应用二进制。

数据库 migration 不会随应用回退而降级。涉及 schema 的版本必须采用 expand/contract，保证相邻应用版本能在新 schema 上短期运行。回退后重新检查 `/health/ready`、Worker 日志和 Outbox 堆积。
