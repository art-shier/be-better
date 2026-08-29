# DayOrder 前后端分离部署设计

## 背景与目标

DayOrder 当前以 Docker Compose 作为主要生产部署路径。项目本身已经包含彼此独立的 Vite 前端、Go API、Go Worker 和 Go 数据库迁移入口，但缺少不依赖 Docker 的可交付构建产物、运行脚本和完整操作说明。

本次调整提供一条 Linux 裸机或虚拟机部署路径：

- 前端只构建静态资源，可部署到 Nginx、Caddy、对象存储或 CDN。
- 后端构建为 API、Worker 和 Migrator 三个独立 Linux 二进制。
- API 与 Worker 作为两个长期运行的前台进程，交由 systemd、Supervisor 或其他进程管理器管理。
- Migrator 作为发布期间的一次性任务，独立执行迁移和版本检查。
- 构建和运行流程不依赖 Docker。

现有 Docker 相关文件保留，避免删除仍可能被使用的部署能力；新的分离部署流程不引用它们。

## 部署边界

### 前端

前端构建机需要满足项目声明的 Node.js 和 npm 版本要求。构建结果不包含 Node.js 服务，只包含浏览器静态资源。

前端 API 地址在 Vite 构建期间确定：

- 同域反向代理使用默认值 `/api/v1`。
- 前后端使用不同域名时，通过 `VITE_API_BASE_URL=https://api.example.com/api/v1` 指定绝对地址，同时在 API 配置中允许前端 Origin。

### API

API 由 `apps/api/cmd/server` 构建，提供业务 HTTP 接口、存活检查、就绪检查和独立指标端口。API 使用受限的 `DATABASE_URL` 数据库账号，不持有 schema 变更权限。

### Worker

Worker 由 `apps/api/cmd/worker` 构建，处理邮件、提醒和 Agent Outbox 任务，并提供独立指标端口。Worker 使用受限的 `WORKER_DATABASE_URL` 数据库账号，与 API 独立启动、停止和恢复。

### Migrator

Migrator 由 `apps/api/cmd/migrate` 构建，使用 `MIGRATION_DATABASE_URL` 执行内嵌的 forward-only SQL migration。Migrator 不作为常驻服务运行，其数据库账号不提供给 API 或 Worker。

## 发布产物

默认构建结果写入仓库根目录下的 `release/`：

```text
release/
├── web/
│   ├── index.html
│   └── assets/
└── backend/
    ├── bin/
    │   ├── dayorder-api
    │   ├── dayorder-worker
    │   └── dayorder-migrate
    ├── scripts/
    │   ├── runtime-env.sh
    │   ├── start-api.sh
    │   ├── start-worker.sh
    │   └── migrate.sh
    └── config/
        ├── api.env.example
        ├── worker.env.example
        └── migrate.env.example
```

`release/` 是可再生构建输出，不纳入版本控制。后端运行脚本通过自身位置解析相邻的 `bin/`，因此整个 `release/backend/` 可以复制到任意绝对路径，不依赖源码仓库路径。

## 源码文件组织

裸机部署源文件放在 `deploy/bare-metal/`：

```text
deploy/bare-metal/
├── build-web.sh
├── build-backend.sh
├── runtime/
│   ├── runtime-env.sh
│   ├── start-api.sh
│   ├── start-worker.sh
│   └── migrate.sh
└── config/
    ├── api.env.example
    ├── worker.env.example
    └── migrate.env.example
```

根目录 `package.json` 增加便于发现的 Linux 发布构建命令，但开发期原有的 `build:web`、`build:api` 和 Docker 命令继续保留。

## 构建脚本

### Web 构建

`deploy/bare-metal/build-web.sh` 执行以下工作：

1. 定位仓库根目录，不依赖调用者当前工作目录。
2. 检查 `node` 和 `npm` 可用。
3. 使用根目录锁文件执行 `npm ci`。
4. 调用现有 workspace Web 构建，包含 TypeScript project build 和 Vite build。
5. 将 `apps/web/dist` 的完整内容复制到临时目录，再用干净产物替换默认的 `release/web`。

输出目录可通过脚本的第一个位置参数覆盖。脚本拒绝空路径、文件路径或不安全的根目录目标，避免清理错误位置。构建失败时不发布半成品目录。

### Backend 构建

`deploy/bare-metal/build-backend.sh` 执行以下工作：

1. 定位仓库根目录并检查 `go` 可用。
2. 固定 `CGO_ENABLED=0` 和 `GOOS=linux`。
3. 默认使用 `go env GOARCH`，允许调用者设置 `GOARCH=amd64` 或 `GOARCH=arm64`。
4. 使用 `-buildvcs=false -trimpath -ldflags="-s -w"` 分别构建 API、Worker 和 Migrator。
5. 把运行脚本及配置模板复制到同一个临时发布目录。
6. 所有文件完成后再替换默认的 `release/backend`，避免出现混合版本。

输出目录同样可通过第一个位置参数覆盖。最终二进制和运行脚本必须具有执行权限。

## 运行脚本与配置

所有 Bash 脚本使用严格模式，任何未处理错误都会产生非零退出码。运行脚本保持前台，不创建 PID 文件、不调用 `nohup`，也不自行重启服务。

调用形式为：

```bash
./scripts/start-api.sh /etc/dayorder/api.env
./scripts/start-worker.sh /etc/dayorder/worker.env
./scripts/migrate.sh up /etc/dayorder/migrate.env
./scripts/migrate.sh check /etc/dayorder/migrate.env
```

环境文件参数必须显式提供且可读。脚本加载环境文件后只导出其中的环境变量，不把文件内容写入日志。环境文件是 Bash 兼容的键值文件；包含空格或 shell 特殊字符的值必须使用单引号或双引号包裹。

共享的 `runtime-env.sh` 负责：

- 验证环境文件和目标二进制。
- 检查不能同时设置 `VARIABLE` 与 `VARIABLE_FILE`。
- 为 `DATABASE_URL`、`WORKER_DATABASE_URL`、`MIGRATION_DATABASE_URL`、`DAYORDER_AUTH_HMAC_KEY`、`DAYORDER_SMTP_PASSWORD` 和 `DAYORDER_AGENT_HTTP_KEY` 加载 `_FILE` 密钥。
- 拒绝不可读或内容为空的密钥文件。
- 去掉密钥文件末尾的 CR/LF，但不输出密钥值。

API 和 Worker 的完整语义校验继续由现有 Go 配置加载器执行，避免在 Bash 与 Go 中维护两套易分叉的业务规则。Shell 只负责文件、二进制、密钥加载和命令分派等运行前检查。

`migrate.sh` 只接受 `up` 和 `check`：

- `up` 执行 `dayorder-migrate`。
- `check` 执行 `dayorder-migrate -check`。
- 其他动作输出用法并返回非零退出码。

## 配置隔离

三个示例环境文件按最小权限原则拆分：

- `api.env.example` 只包含 API 所需的公开设置、API 数据库连接和认证密钥引用。
- `worker.env.example` 只包含 Worker 数据库连接、邮件、Agent、认证密钥和 Worker 调优设置。
- `migrate.env.example` 只包含 migration 数据库连接。

生产配置建议放在 `/etc/dayorder/`，权限为部署用户可读且其他用户不可读。真实环境文件和密钥不会被复制进发布产物或提交到 Git。

## 发布流程

首次或常规发布遵循相同的服务边界：

1. 在构建机运行 Web 和 Backend 构建脚本。
2. 将 `release/web/` 同步到静态资源服务。
3. 将完整 `release/backend/` 上传到后端服务器的版本目录。
4. 创建或更新 API、Worker 和 Migrator 的独立环境文件。
5. 运行 `migrate.sh up`。
6. 运行 `migrate.sh check`，确认数据库 schema 与发布版本一致。
7. 通过进程管理器分别启动或重启 API 与 Worker。
8. 检查 API `/health/live`、`/health/ready`、API 指标、Worker 指标和两个服务的日志。

数据库迁移保持 forward-only。应用二进制可以切回上一版本，数据库 schema 不自动降级；包含 schema 变化的发布必须采用 expand/contract，使相邻版本在迁移后短期兼容。

## 错误处理

- 缺少构建工具时，构建脚本说明缺少的命令并立即失败。
- Node、TypeScript、Vite 或 Go 构建失败时，不替换已有的完整发布产物。
- 环境文件缺失、不可读或语法错误时，服务不会启动。
- 二进制缺失或不可执行时，运行脚本给出目标路径并失败。
- 密钥直接值和 `_FILE` 同时存在时拒绝启动，消除配置优先级歧义。
- Migrator 出错或 schema 检查失败时，发布流程停止，不继续重启应用服务。

## 测试与验收

新增裸机部署校验，覆盖以下行为：

- 必需脚本、配置模板和发布命令存在。
- Bash 脚本通过 `bash -n` 语法检查并启用严格模式。
- 使用临时假二进制验证 API、Worker 与 migration 命令分派保持前台 `exec` 语义。
- 缺少环境文件、二进制或密钥文件时返回非零退出码和可理解的错误。
- `_FILE` 密钥被正确导出，且标准输出和标准错误不包含密钥值。
- `migrate.sh up` 与 `migrate.sh check` 传递正确参数，无效动作失败。
- Web 构建脚本真实生成 `index.html` 和静态 assets。
- Backend 构建脚本真实生成三个 Linux 可执行文件和完整运行资源。

完成前运行现有 Web 与 API 测试、裸机部署校验、Shell 语法检查，以及两个新的真实构建脚本。README 和独立的前后端分离部署运行手册必须给出依赖、构建、上传、配置、迁移、启动、健康检查和升级顺序。

## 非目标

- 不移除现有 Docker、Compose、PostgreSQL 备份或监控配置。
- 不在脚本中安装或管理 PostgreSQL。
- 不让启动脚本承担 systemd、Supervisor、日志轮转或自动重启职责。
- 不生成容器镜像或 Node.js 前端运行服务。
- 不增加数据库降级 migration。
