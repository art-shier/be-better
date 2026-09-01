# Linux 前后端分离部署手册

本手册说明通过 GitHub Release 在 Linux 裸机上分别安装和更新 DayOrder Web、Server 与 Worker。该流程不构建或运行 Docker，也不安装 PostgreSQL、Nginx、Caddy 或 systemd。部署机需要 Linux、Bash、`curl`、`tar`、`sha256sum`、`flock` 和常见 GNU 工具；Server 或 Worker 部署还需要 ConfigHub CLI，Web-only 部署不需要。

## 首次安装

在希望成为部署根目录的位置下载稳定的部署脚本。未指定 `--version` 时，脚本解析最新公开 Release：

```bash
mkdir -p ~/a
cd ~/a
curl -fsSLO https://github.com/art-shier/be-better/releases/latest/download/dayorder-deploy.sh
chmod 0755 dayorder-deploy.sh
./dayorder-deploy.sh all
```

`--root` 未指定时使用命令启动时的 `$PWD`，不是脚本文件所在目录；例如可将 Server 安装到另一个绝对路径：

```bash
./dayorder-deploy.sh server --root /srv/dayorder
```

以在 `~/a` 中运行脚本为例，目录布局固定为：

```text
~/a/
├── dayorder-deploy.sh
├── releases/
│   ├── v0.3.0/
│   │   ├── web/
│   │   ├── server/
│   │   └── worker/
│   └── v0.3.1/
├── current-web -> releases/v0.3.1/web
├── current-server -> releases/v0.3.1/server
├── current-worker -> releases/v0.3.1/worker
└── dayorder-config/
    ├── .confighub.yaml
    ├── api.env
    ├── migrate.env
    ├── worker.env
    └── secrets/
        ├── auth_hmac_key
        ├── smtp_password
        └── agent_http_key
```

首次 Server 部署缺少 `api.env` 或 `migrate.env`、或首次 Worker 部署缺少 `worker.env` 时，脚本会创建权限受限的 `dayorder-config` 和 `secrets/`，从已校验产物复制缺失模板，然后在 migration、链接切换和服务启动之前停止。已有配置永不覆盖，历史版本不会自动删除。

把包含 ConfigHub Server 和 Machine Token 的 `.confighub.yaml` 放到 `dayorder-config/`。API、Worker 和 Migrator 启动时都会先切换到环境文件所在目录，再执行 `confighub run --project shier --env prod`。部署器不解析或校验 `.confighub.yaml`；CLI 缺失、配置不可读、Server 不可达、Token 无效或无权限时，ConfigHub 的错误会直接显示在控制台并在 migration 和服务切换前停止部署。

部署器会解析预检通过的 ConfigHub CLI 绝对路径并写入 API、Worker 的 systemd unit，因此服务启动不依赖 systemd 是否继承交互 shell 的 PATH。

三个非数据库密钥的完整路径是 `secrets/auth_hmac_key`、`secrets/smtp_password` 和 `secrets/agent_http_key`。每个文件必须包含 exactly one non-empty single-line value；空文件、多行文件、符号链接、非部署用户拥有的文件或宽松权限都会在 migration 前被拒绝。首次填写后执行：

```bash
touch dayorder-config/secrets/auth_hmac_key \
  dayorder-config/secrets/smtp_password \
  dayorder-config/secrets/agent_http_key
chmod 0700 dayorder-config dayorder-config/secrets
chmod 0600 dayorder-config/api.env dayorder-config/migrate.env dayorder-config/worker.env \
  dayorder-config/secrets/auth_hmac_key \
  dayorder-config/secrets/smtp_password dayorder-config/secrets/agent_http_key
```

从旧版本升级时，已有环境文件不会自动覆盖。确认 `dayorder-config/migrate.env` 包含 `DAYORDER_ENV=production`；缺失或设置为其他值会在实际 migration 以及 Migrator 的 ConfigHub 调用之前失败。旧版环境文件引用的 `api_database_url`、`worker_database_url` 和 `migration_database_url` 密钥文件应保留到相邻 Release 的回滚窗口结束，新脚本虽然不读取它们，但回滚后的旧脚本仍然需要。

Server 与 Worker 需要可用的用户级 systemd 管理器。若脚本要求启用 linger，请执行一次：

```bash
sudo loginctl enable-linger "$USER"
```

脚本不会提权或自动执行 `sudo`。

## 组件操作与更新

按需部署单一组件，或使用 `all`：

```bash
./dayorder-deploy.sh web
./dayorder-deploy.sh server
./dayorder-deploy.sh worker
./dayorder-deploy.sh all
./dayorder-deploy.sh all --version v0.3.0
```

`all` 会先完成 Server migration 与就绪检查，再激活 Worker，最后切换 Web 链接。Server 或 Worker 激活失败时，应用链接会恢复到本次部署之前的版本并重新启动旧服务；`all` 后续步骤失败也会按逆序恢复本次已切换的应用链接。恢复旧 Server 后还会重新轮询 API 就绪端点；若旧 API 仍不健康，脚本会输出 `restored API failed readiness; manual intervention required` 并失败退出，运维必须检查链接、服务状态和日志。

Web 只下载、校验并切换 `current-web`；Web 部署不安装、配置或启动 Nginx/Caddy，也不启动静态服务器。Nginx/Caddy 必须将站点根目录指向 `<root>/current-web`。Server 与 Worker 是两个独立的 `systemd --user` 服务，分别使用持久化的 `api.env`、`migrate.env` 与 `worker.env`，并从同目录的 `.confighub.yaml` 读取数据库配置。

指定旧版本仅切换应用版本；数据库 migration 不会回退。Migrator 与 API 拒绝 dirty schema 和低于二进制内嵌 migration floor 的版本，只在 expand/contract 约束下接受 clean schema at or above the embedded migration floor。这不是任意跨度的向前兼容：升级与应用回退均为 adjacent-release only，一次只能跨一个相邻 Release，不能跳过多个版本。

## 服务状态与日志

部署完成后检查两个服务：

```bash
systemctl --user status dayorder-api.service dayorder-worker.service
journalctl --user -u dayorder-api.service -f
journalctl --user -u dayorder-worker.service -f
```

API 默认就绪检查地址为 `http://127.0.0.1:8080/health/ready`；可用 `DAYORDER_DEPLOY_HEALTH_URL` 覆盖。Worker 成功条件是 `systemctl --user is-active dayorder-worker.service` 返回 `active`，不提供业务 HTTP 健康端点。

## Release 内容与安全边界

每个 Release 提供 Web、Linux `amd64`/`arm64` Server 与 Worker 压缩包，以及 `dayorder-deploy.sh`、`release-manifest.json` 和 `SHA256SUMS`。脚本在解压和激活前校验 HTTPS 下载、资产名、Manifest、SHA-256 和归档内容。

Migration 只会在切换 Server 前向前执行并检查 schema；migration、下载、校验、解压或配置预检失败时不切换链接。干净但高于当前二进制 floor 的 schema 仅因相邻 Release 的 expand/contract 保证而被接受，不能据此把旧二进制用于任意未来 schema。请在确认不再需要的版本后自行清理 `releases/`，不要删除 `dayorder-config`。

## 本地构建/离线传输

无法访问 GitHub Release 时，可在具备 Node.js 22.22+（或 24.15+）、npm、Go 1.25+ 与 Bash 的构建机生成静态资源和 Linux 后端目录，再用受控传输工具复制：

```bash
npm run build:release:web
npm run build:release:backend
```

Web 位于 `release/web/`；后端目录位于 `release/backend/`，其中包含 API、Worker、Migrator、启动脚本和环境模板。离线传输后仍须安装 ConfigHub CLI，在三个环境文件所在目录配置 `.confighub.yaml`，先执行 migration 检查，并由两个独立服务管理 API 与 Worker。

`VITE_API_BASE_URL` 会在 Web 构建时写入静态资源；离线环境要改用其他 API 地址时，必须在构建时显式设置它。传输后的后端仍按以下顺序执行并由进程管理器托管：

```bash
VITE_API_BASE_URL=https://api.example.com/api/v1 npm run build:release:web
cd release/backend
./scripts/migrate.sh up /path/to/migrate.env
./scripts/migrate.sh check /path/to/migrate.env
./scripts/start-api.sh /path/to/api.env
./scripts/start-worker.sh /path/to/worker.env
```
