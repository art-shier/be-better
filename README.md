# 日序 DayOrder

日序是一个本地优先的个人计划应用。仓库采用 monorepo 结构：React/Vite 前端负责交互、离线缓存和游客模式，Go 服务负责账户 API、按用户隔离的 SQLite 持久化，以及生产环境静态资源托管。

## 目录结构

```text
apps/
  web/      React 19 + TypeScript + Vite
  api/      Go HTTP server + SQLite
docs/       PRD、账户设计、实现计划和交互原型
scripts/    prototype 与真实运行验收脚本
```

根目录通过 npm workspaces 管理前端，通过 `go.work` 管理 Go 模块。

## 使用模式

- 游客无需登录即可使用今天、目标、任务、日程、记录和笔记；数据只写入当前浏览器。
- 注册时可选择把当前游客数据原子迁移到新账户；注册失败不会清除游客数据。
- 登录已有账户不会合并当前游客数据。账户缓存按用户 ID 隔离，并在 Go 服务可用时按 revision 同步。
- 服务暂时不可达或 Session 失效时，已缓存的账户数据仍可本地使用，不会自动切换成游客数据。
- Agent 必须同时满足“Session 已验证”和“Go 服务可达”；游客、离线账户和失效 Session 都不能运行 Agent。
- 正常退出会撤销当前 Session、清除当前用户的浏览器缓存，再返回仍然存在的游客空间。

认证使用邮箱和密码。密码以 Argon2id 保存；30 天不透明 Session 只在 `HttpOnly`、`SameSite=Lax` Cookie 中传递，数据库只保存令牌的 SHA-256 哈希。修改密码会撤销其他 Session 并轮换当前 Session。

## 本地开发

环境要求：Node.js 22.22+（或 24.15+）以及 Go 1.25+。

```powershell
npm install
npm run dev
```

如果当前环境设置了 `NODE_ENV=production`，安装时显式包含开发依赖：

```powershell
npm install --include=dev --workspaces --include-workspace-root
```

开发模式会同时启动：

- Web：通常为 <http://127.0.0.1:5173>
- API：<http://127.0.0.1:8080>
- 健康检查：<http://127.0.0.1:8080/api/v1/health>

Vite 会把 `/api` 代理到 Go 服务。默认端口被占用时，可分别运行 `npm run dev:api` 和 `npm run dev:web`，并用下方环境变量调整地址。

## 测试与构建

```powershell
npm run typecheck
npm test
go vet ./apps/api/...
npm run build
node scripts/validate-prototype.mjs
npm run test:runtime
```

`test:runtime` 会在可用端口上启动隔离的 Vite、Go 和临时 SQLite，真实验证代理注册/登录、游客数据迁移、Cookie、双用户隔离、资料与密码、Session 轮换、服务重启、SPA 深链和退出登录；脚本结束后会关闭它启动的进程并清理临时数据。

生产构建后运行：

```powershell
npm start
```

Go 服务会托管 `apps/web/dist`，默认地址为 <http://127.0.0.1:8080>。首次启动会创建 `data/dayorder.db`，未知前端路径会回退到 SPA 的 `index.html`。

## 数据与同步

浏览器每次变更都会立即写入对应身份的 localStorage 分区。只有已验证账户会访问远端状态接口；Go 服务在线时，前端以 500 ms debounce 把完整 AppData 同步到该用户的 SQLite 状态。

- 注册迁移在同一事务中创建 User、revision 为 1 的 UserAppState 和 Session。
- 写入使用 revision 乐观锁，避免静默覆盖并发更新。
- 发生冲突时，本地版本会备份到当前用户专属的 conflict key，随后加载最新服务端版本。
- 旧版 `dayorder.app.v1` 首次升级时只迁移到游客分区，不会归入任意账户。
- Service Worker 明确排除 `/api/*`，不会缓存账户或状态接口。

## 配置

| 变量 | 默认值 | 作用 |
| --- | --- | --- |
| `DAYORDER_ADDR` | `127.0.0.1:8080` | Go 服务监听地址 |
| `DAYORDER_DB_PATH` | `data/dayorder.db` | SQLite 文件路径 |
| `DAYORDER_WEB_DIR` | 空 | 要托管的前端构建目录 |
| `DAYORDER_ALLOWED_ORIGINS` | 本地 Vite 地址 | 允许携带凭据的跨域来源，逗号分隔 |
| `VITE_API_BASE_URL` | `/api/v1` | 前端请求使用的 API 根地址 |
| `VITE_API_PROXY_TARGET` | `http://127.0.0.1:8080` | Vite 开发服务器的 `/api` 代理目标 |

命令行参数 `-addr`、`-db`、`-web-dir` 可覆盖对应的 Go 服务配置。

## API

所有业务接口位于 `/api/v1`：

- `GET /health`：服务与 SQLite 健康状态。
- `POST /auth/register`：注册，可携带 `initialData` 迁移游客数据。
- `POST /auth/login`、`POST /auth/logout`、`GET /auth/session`：登录、退出与当前 Session。
- `PATCH /users/me`：修改称呼。
- `PUT /users/me/email`：验证当前密码后修改邮箱。
- `PUT /users/me/password`：修改密码并轮换 Session。
- `GET /state`：读取当前认证用户的应用状态。
- `PUT /state`：按 `expectedRevision` 写入当前认证用户的应用状态。

状态接口不能指定用户 ID，身份只能来自 Session。状态请求上限为 16 MB；SQLite 使用 WAL 模式。服务同时提供凭据 CORS、写请求 Origin 校验、登录限流、安全响应头和优雅停机。
