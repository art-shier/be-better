# GitHub Release 发布与一键部署设计

## 背景与目标

项目已经能够在本地生成 Web 静态资源，以及 Linux API、Worker、Migrator 和运行脚本，但当前 GitHub Actions 只有持续集成与安全检查，没有面向裸机分离部署的发布流水线。此次工作将以公开 GitHub 仓库的 Release 作为长期分发入口，让运维人员通过一个部署脚本下载、校验、安装和更新 Web、Server 与 Worker，无需 Docker，也无需手工整理各个二进制和脚本。

目标包括：

- 推送标准 `v<major>.<minor>.<patch>` 标签时自动创建 GitHub Release。
- 发布架构无关的 Web 静态资源，以及 Linux `amd64`、`arm64` 的 Server 和 Worker 产物。
- 将 Migrator、迁移脚本和迁移配置模板放入 Server 产物。
- 提供一个公共、稳定下载地址的一键部署脚本。
- 支持分组件部署和 `all` 部署，默认使用最新 Release，也允许指定版本。
- 默认以执行部署脚本时的当前目录为部署根目录，持久配置位于该目录的 `dayorder-config`。
- Server 和 Worker 由 `systemd --user` 管理；Web 只安装静态资源，不安装或启动 Nginx/Caddy。

## 非目标

- 不构建、发布或部署 Docker 镜像。
- 不自动安装 PostgreSQL、Nginx、Caddy、systemd 或操作系统依赖。
- 不在 Release 中包含真实环境配置、数据库连接或密钥。
- 不让 API 启动时自动执行 migration。
- 不自动执行数据库降级 migration。
- 不自动删除历史版本或用户配置。
- 首版不发布预发布标签；只有 `v数字.数字.数字` 形式的稳定版本标签有效。

## 方案选择

采用“独立组件压缩包 + 通用部署脚本”。与每个架构一个完整平台包相比，该方案允许 Web、Server 和 Worker 部署在不同机器并独立更新；与发布裸二进制相比，压缩包能够原子携带二进制、运行脚本和安全配置模板。

## Release 产物契约

每个 GitHub Release 使用固定资产名。版本由 Release 标签和 Manifest 表达，不重复写入文件名，因此可以通过 GitHub 的 `releases/latest/download/...` 地址下载最新版本：

```text
dayorder-web.tar.gz
dayorder-server-linux-amd64.tar.gz
dayorder-server-linux-arm64.tar.gz
dayorder-worker-linux-amd64.tar.gz
dayorder-worker-linux-arm64.tar.gz
dayorder-deploy.sh
release-manifest.json
SHA256SUMS
```

### Web 产物

`dayorder-web.tar.gz` 包含 Vite 生产构建生成的 `index.html` 与 `assets/`。生产构建未显式设置 `VITE_API_BASE_URL` 时使用现有生产默认地址 `https://better-api.shier.art/api/v1`。Web 包不含 Node.js 服务、Nginx 或 Caddy。

### Server 产物

每个 Server 架构包只包含 Server 部署所需内容：

```text
bin/dayorder-api
bin/dayorder-migrate
scripts/runtime-env.sh
scripts/start-api.sh
scripts/migrate.sh
config/api.env.example
config/migrate.env.example
```

API 运行时仍只使用受限 API 数据库账号；Migrator 通过独立的 migration 配置和数据库账号运行。

### Worker 产物

每个 Worker 架构包只包含 Worker 部署所需内容：

```text
bin/dayorder-worker
scripts/runtime-env.sh
scripts/start-worker.sh
config/worker.env.example
```

### Manifest 与校验和

`release-manifest.json` 使用稳定 schema，顶层字段固定为 `schemaVersion`、`version`、`revision`、`deployScriptVersion` 和 `assets`。首版两个 schema 版本均为整数 `1`；`assets` 记录 Web 资产名，以及按 `amd64`、`arm64` 索引的 Server/Worker 资产名。部署脚本必须拒绝无法识别的 schema 或脚本兼容版本。`SHA256SUMS` 覆盖五个组件压缩包、部署脚本和 Manifest，但不包含自身。

## GitHub Actions 发布流水线

新增 `.github/workflows/release.yml`，仅在推送匹配 `v*` 的标签时启动。第一阶段再次用严格正则验证标签为 `v数字.数字.数字`，并通过完整 Git 历史确认标签提交属于 `origin/main`；不符合要求时立即失败且不创建 Release。

流水线阶段如下：

1. 检出标签提交，安装受支持版本的 Node.js 与 Go。
2. 运行 Web 测试、TypeScript 检查、Go 测试和裸机部署测试。
3. Web 构建任务生成并检查生产静态资源。
4. Backend matrix 并行构建 Linux `amd64` 和 `arm64`，然后从现有后端发布内容分别组装 Server 与 Worker 压缩包。
5. 构建任务通过短期 GitHub Actions Artifact 把中间文件传给最终发布任务；这些中间文件只用于工作流内部，不是生产下载入口。
6. 最终任务检查五个组件资产齐全且压缩包内容符合契约，生成 Manifest 与 SHA-256 清单。
7. 创建 Draft Release，上传或覆盖同名资产。
8. 再次确认 Release 资产清单完整后，将 Draft 公开并生成 Release Notes。

工作流重跑时复用同一标签的 Draft Release，并覆盖其资产。任何测试、构建、打包、校验或上传失败都会让 Release 保持 Draft，不向部署端暴露不完整版本。

第三方 GitHub Action 必须固定到完整 commit SHA。工作流默认权限为 `contents: read`，只有创建和发布 Release 的最终任务获得 `contents: write`。同一标签的发布使用 concurrency 串行化，发布过程不使用 `cancel-in-progress`，避免中途取消留下状态不明的 Draft。

## 一键部署命令

公开仓库允许首次直接下载最新部署脚本：

```bash
curl -fsSLO https://github.com/art-shier/be-better/releases/latest/download/dayorder-deploy.sh
chmod 0755 dayorder-deploy.sh
```

部署脚本支持：

```bash
./dayorder-deploy.sh web
./dayorder-deploy.sh server
./dayorder-deploy.sh worker
./dayorder-deploy.sh all
./dayorder-deploy.sh all --version v0.3.0
./dayorder-deploy.sh server --root /absolute/deploy/root
```

第一个位置参数必须是 `web`、`server`、`worker` 或 `all`。`--version` 可选，未提供时解析最新公开 Release；显式版本必须符合稳定版本标签格式。`--root` 可选，未提供时使用脚本启动时的 `$PWD`，而不是脚本文件所在目录。所有内部路径转换为绝对路径后再使用。脚本拒绝空路径、文件系统根目录、当前用户 home 目录和符号链接形式的部署根目录，避免误把宽泛目录作为版本管理目标。

部署脚本依赖 Linux、Bash、`curl`、`tar`、`sha256sum`、`flock` 和常见 GNU 工具。每次运行对部署根目录中的锁文件持有独占锁，拒绝同一根目录的并发部署。Server/Worker 还依赖可用的 `systemd --user` 管理器。脚本将 `x86_64` 映射为 `amd64`，将 `aarch64` 或 `arm64` 映射为 `arm64`，拒绝其他架构。

## 部署目录

以在 `~/a` 内运行脚本为例，默认布局为：

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
    ├── api.env
    ├── migrate.env
    ├── worker.env
    └── secrets/
```

`dayorder-config` 独立于版本目录，升级、指定版本部署和链接回退都不会覆盖它。历史版本默认保留，由运维人员确认后自行清理。

## 首次部署与配置

Web 不需要运行配置，可以直接部署。Server 首次部署若缺少 `api.env` 或 `migrate.env`，Worker 首次部署若缺少 `worker.env`，部署脚本会：

1. 创建权限受限的 `dayorder-config` 和 `secrets` 目录。
2. 从已校验的 Release 产物复制缺失的 `.env` 模板，但绝不覆盖已有文件。
3. 输出必须填写的配置、密钥文件和权限说明。
4. 在执行 migration、切换链接或启动服务之前停止。

运维人员填写配置和密钥后，重新执行同一部署命令即可。`all` 会先检查 Server 与 Worker 的全部配置，任何配置缺失时不切换任何组件。

首次部署 Server 或 Worker 还会确认用户级 systemd 管理器可用，并检查 linger。若 linger 未启用，脚本在切换版本前停止并输出一次性命令：

```bash
sudo loginctl enable-linger "$USER"
```

部署脚本本身不提权、不自动执行 `sudo`。

## 组件部署行为

### Web

Web 部署下载并校验 `dayorder-web.tar.gz`，安全解压到 `releases/<version>/web`，确认 `index.html` 存在，然后原子切换 `current-web`。它不安装、配置或重启 Nginx/Caddy。静态服务器的站点根目录应指向部署根目录下的 `current-web`；如果使用 CDN，缓存刷新由运维平台负责。

### Server

Server 部署按本机架构选择资产，下载、校验并解压到 `releases/<version>/server`。切换前在新版本目录中使用持久化 `migrate.env` 执行：

```bash
scripts/migrate.sh up <root>/dayorder-config/migrate.env
scripts/migrate.sh check <root>/dayorder-config/migrate.env
```

两步均成功后，脚本原子切换 `current-server`，创建或更新 `~/.config/systemd/user/dayorder-api.service`，执行 `systemctl --user daemon-reload` 和 `enable --now` 或 restart。服务单元引用绝对的 `current-server`、`api.env` 路径，使用当前部署用户运行，设置自动重启和至少 30 秒停止超时。

API 重启后，脚本轮询健康地址，默认使用 `http://127.0.0.1:8080/health/ready`；运维可通过 `DAYORDER_DEPLOY_HEALTH_URL` 环境变量覆盖。超时或非成功响应视为发布失败。

### Worker

Worker 部署下载对应架构资产，校验并解压到 `releases/<version>/worker`，原子切换 `current-worker`，创建或更新 `~/.config/systemd/user/dayorder-worker.service`，然后 reload、enable 或 restart。Worker 单元引用持久化 `worker.env`，设置自动重启和至少 60 秒停止超时。部署成功条件是 `systemctl --user is-active dayorder-worker.service` 返回 active；Worker 不需要业务 HTTP 健康端点。

### All

`all` 先解析一次版本和架构，下载并校验所需的全部资产，完成全部配置与 systemd 预检，再依次执行：

```text
Server migration 与健康检查 → Worker 启动检查 → Web 链接切换
```

该顺序确保新 Web 不会在 Server 尚未就绪时对外提供。相同组件已经指向目标版本时直接提示并跳过。

## 原子切换与失败恢复

所有下载都通过 HTTPS 写入部署根目录内的临时目录。脚本先验证 HTTP 状态、资产名、SHA-256 和归档内容，再解压到临时版本目录；归档不得包含绝对路径、逃逸目标目录的 `..` 路径、符号链接、硬链接或设备节点。只有完整验证后才把临时目录移动为正式版本目录。

切换前记录每个 `current-*` 的旧目标，通过临时符号链接加原子 rename 更新。失败处理规则如下：

- 下载、校验、解压、配置预检或 migration 失败：不切换任何链接。
- Server 切换后启动或健康检查失败：恢复旧 Server 链接并重启旧 API。
- Worker 切换后启动检查失败：恢复旧 Worker 链接并重启旧 Worker。
- `all` 的后续步骤失败：按逆序恢复本次已经切换的应用链接，并重新启动需要恢复的旧服务。
- Web 在切换前验证静态入口；切换本身只修改符号链接。

数据库 migration 只向前执行，应用链接恢复不会降级 schema。版本必须继续遵守 expand/contract，保证相邻应用版本能够在已经升级的 schema 上运行。若不存在可恢复的旧版本，脚本保留失败诊断并以非零状态退出。

## 更新与指定版本

更新时重新执行同一个脚本即可：

```bash
cd ~/a
./dayorder-deploy.sh all
```

未指定版本时部署最新公开 Release。指定 `--version v0.3.0` 可以部署或尝试回退到该应用版本，但不会回退数据库。脚本不会覆盖 `dayorder-config`。已经部署目标版本的组件默认跳过，避免无意义重启。

## 自动化测试与验收

发布实现必须提供以下验证：

- 静态检查 Release workflow 的标签触发、最小权限、concurrency、Draft 发布顺序和固定 Action SHA。
- 执行现有 Web、Go、架构、部署和裸机运行脚本测试。
- 实际构建 Web 生产包，以及 Linux `amd64`、`arm64` 的 Server/Worker 包。
- 检查每个压缩包只包含契约规定的文件、权限正确且不含真实密钥。
- 检查 Manifest 与 SHA-256 清单覆盖所有资产。
- 对所有 Shell 脚本执行 Bash 语法检查。
- 使用临时目录和伪造的网络/systemd 命令测试架构选择、latest、指定版本、首次配置、重复部署、migration 失败、Server 健康失败、Worker 启动失败和链接恢复。
- 在完成前真实运行发布构建命令，并从生成资产执行一次本地无特权部署演练；systemd 交互在自动测试中通过受控替身验证。

## 文档更新

README 与 `docs/runbooks/separate-deployment.md` 将补充：

- 创建并推送版本标签的发布命令。
- GitHub Release 资产说明与首次下载部署脚本命令。
- 当前目录默认规则、`dayorder-config`、首次配置和密钥权限。
- `systemd --user`、linger、日志与服务状态检查。
- `web`、`server`、`worker`、`all` 的首次部署和更新命令。
- Nginx/Caddy 指向 `current-web`，Web 部署不启动静态服务器。
- 默认 latest、显式版本、应用链接恢复和数据库不降级限制。
