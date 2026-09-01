# DayOrder ConfigHub PostgreSQL 接入与初始化设计

## 背景与目标

DayOrder 当前从环境变量读取 PostgreSQL 连接信息，并为 API、Worker 和 Migrator 使用三种不同的数据库角色。当前目录已经配置 ConfigHub CLI，本地 `.confighub.yaml` 可以访问 `https://config.shier.art` 的 `shier/prod`。ConfigHub Revision 2 已提供以下非空字段：

- `db_address`
- `db_port`
- `db_username`
- `db_password`

在开始 Bootstrap 前，新 Revision 还必须增加三个互不相同的运行角色密码：

- `db_migrator_password`
- `db_api_password`
- `db_worker_password`

目标是在不把密码写入仓库或 `.env` 文件的前提下，让 DayOrder 从这些字段构造连接信息，创建并初始化两个数据库：

- `dayorder`：生产数据库；
- `dayorder-test`：本地功能验收数据库。

Redis 当前没有业务用途，本次不增加 Redis 客户端或配置。

## 方案选择

### 采用：应用侧安全适配 ConfigHub 字段

通过 `confighub run --project shier --env prod -- <command>` 将配置字段注入进程。DayOrder 新增一个聚焦于 PostgreSQL 连接构造的内部组件，用于验证字段、构造管理员和运行角色 DSN，并在读取后清理原始敏感环境变量。

这一方案保留现有 `DATABASE_URL`、`WORKER_DATABASE_URL` 和 `MIGRATION_DATABASE_URL` 接口。显式提供原生 URL 时继续优先使用原生 URL，现有开发、测试、Compose 和发布流程不会被强制切换到 ConfigHub。

### 不采用：把完整 DSN 重新录入 ConfigHub

完整 DSN 可以让 DayOrder 零适配，但会重复地址、端口和密码，轮换时需要同时修改多个值，也不符合本次固定数据库名的要求。显式的三个角色密码仍由 ConfigHub 管理，但地址、端口、角色名和数据库名由 DayOrder 组合为 DSN。

### 不采用：导出 `.env` 或缓存文件

该方式实现简单，但会让数据库密码落盘，产生过期、误提交和日志泄漏风险。ConfigHub CLI 只用于向子进程注入配置，不生成持久化配置快照。

## 配置契约

ConfigHub 项目和环境固定为 `shier/prod`。远端字段含义如下：

- `db_address`：PostgreSQL 主机名或 IP，不接受 URL、路径或内嵌凭据；
- `db_port`：1–65535 的十进制端口；
- `db_username`：具有创建数据库和角色权限的初始化管理员，仅 Bootstrap 使用；
- `db_password`：初始化管理员密码，仅 Bootstrap 使用；
- `db_migrator_password`：`dayorder_migrator` 的独立密码；
- `db_api_password`：`dayorder_api` 的独立密码；
- `db_worker_password`：`dayorder_worker` 的独立密码。

数据库名由 DayOrder 环境确定，不读取远端 `db_name`：

- `development` 和本地验收固定使用 `dayorder-test`；
- `production` 固定使用 `dayorder`；
- 自动化单元测试继续使用测试自身显式提供的 URL，不访问 ConfigHub。

从 ConfigHub 字段构造的连接一律启用 `sslmode=require`。如果数据库服务不支持 TLS，初始化会失败并明确报告，不自动降级到明文连接。

## 数据库身份与密码管理

运行时继续使用现有三个固定角色：

- `dayorder_migrator`
- `dayorder_api`
- `dayorder_worker`

三个角色使用 ConfigHub 中显式保存的独立密码，不直接复用管理员密码，也不采用自定义密码派生规则。Bootstrap 将对应密码设置到 PostgreSQL 角色；PostgreSQL 在系统目录中保存 SCRAM verifier，而不是保存可读取的明文密码。

ConfigHub Server 会在自身 SQLite 和备份中保存这些配置值，这是当前产品的既有限制。DayOrder 不再把它们写入 `.env`、`deploy/secrets/*`、临时文件或日志。API、Worker 和 Migrator 完成配置解析后会从进程环境移除管理员字段和其他角色的密码，只保留自身所需的受限角色 DSN。

轮换单个角色密码时，在 ConfigHub 发布包含新密码的 Revision 后立即重新执行 Bootstrap，再重启对应进程。Bootstrap 必须只更新目标角色密码和权限，不修改其他角色凭据。

## Bootstrap 流程

新增独立 Bootstrap 命令，顺序执行以下步骤：

1. 从 ConfigHub 环境字段构造指向维护数据库 `postgres` 的管理员 DSN。
2. 验证服务器可达、TLS 生效、管理员能够读取数据库和角色目录，并具备 `CREATEDB`、`CREATEROLE` 所需能力。
3. 使用三个显式角色密码创建或校正 `dayorder_migrator`、`dayorder_api`、`dayorder_worker`，设置最小权限属性、连接数限制、UTC 时区和超时。
4. 分别检查 `dayorder-test` 和 `dayorder`。数据库不存在时创建；存在时不删除、不重建，只验证后继续幂等初始化。
5. 在每个数据库中撤销 `PUBLIC` 的默认连接和 Schema 创建权限，创建由 Migrator 拥有的 `dayorder` Schema，并向三个角色授予现有架构要求的权限。
6. 使用 Migrator 的独立密码构造 DSN，对两个数据库执行现有 1–7 号 forward-only migration。
7. 对两个数据库执行 schema version check，并分别使用 API、Worker 角色完成最小权限连接验证。

Bootstrap 不创建 `dayorder_backup` 和 `dayorder_monitor`。这两个角色服务于生产备份与监控部署，不是 DayOrder 本地功能验收的前置条件，也可能要求托管 PostgreSQL 不授予的额外权限。后续接入生产备份和监控时再单独设计。

## DayOrder 命令与数据流

仓库增加清晰命名的 ConfigHub 命令，不替换现有本地 Compose 命令：

```text
ConfigHub shier/prod
        │
        ├── bootstrap ──> 创建/校正角色与两个数据库 ──> migration 1..7
        ├── API ────────> dayorder-test（本地）或 dayorder（生产）
        ├── Worker ─────> dayorder-test（本地）或 dayorder（生产）
        └── Migrator ───> dayorder-test（本地）或 dayorder（生产）
```

推荐新增命令：

- `npm run config:db:bootstrap`：创建并初始化两个数据库；
- `npm run config:db:check`：只读检查本地验收数据库版本；
- `npm run config:dev:api`：使用 `dayorder-test` 启动本地 API；
- `npm run config:dev:worker`：使用 `dayorder-test` 启动本地 Worker。

生产进程仍需显式设置 `DAYORDER_ENV=production`，不会因为使用 `shier/prod` Token 就自动切换到生产数据库。这样可以避免开发者在本机误连 `dayorder`。

## 安全边界

- 立即把 `.confighub.yaml` 加入 `.gitignore`；该文件当前未被忽略且包含 Machine Token。
- 所有检查日志只显示字段名、数据库名、角色名、版本和成功/失败，不显示 DSN、Token 或密码。
- 不调用 `confighub config get token`，不把 `confighub export` 的原始 JSON 输出到终端。
- 不把 ConfigHub 值写入 `.env`、临时脚本、测试快照或错误消息。
- Bootstrap 不提供 Drop Database、清空 Schema 或降级 Migration 功能。
- 本地功能验收只在 `dayorder-test` 写入测试数据，不向 `dayorder` 写入业务测试数据。
- ConfigHub 本身会以明文在 SQLite 和备份中保存配置值；该风险由现有 ConfigHub 部署的访问控制和备份策略承担。

## 错误处理与可恢复性

- 缺少或格式错误的 ConfigHub 字段在建立数据库连接前失败。
- 目标主机、端口、数据库名和角色名不从命令行自由传入，避免误操作其他数据库。
- 数据库已存在时绝不自动删除；如果所有者或权限与预期冲突，Bootstrap 停止并报告差异。
- 角色创建和数据库创建是幂等的；部分步骤失败后可以修复权限或网络问题并重新运行。
- Migration dirty、版本高于当前程序或权限不足时立即停止，不继续启动 API/Worker。
- `dayorder-test` 初始化失败不会触碰 `dayorder` 后续步骤；生产数据库初始化前再次完成目标和权限预检。

## 测试与验收

实现采用测试驱动方式，至少覆盖：

- ConfigHub 字段缺失、非法地址、非法端口和空用户名/密码；
- 开发与生产数据库名选择；
- 三个角色密码与对应 DSN 的映射、隔离性和不泄漏；
- 原生 URL 优先级，确保现有部署兼容；
- Bootstrap SQL 标识符处理、幂等行为、禁止 Drop 和权限冲突失败；
- API、Worker、Migrator 各自只获得对应 DSN；
- `.confighub.yaml` 被 Git 忽略。

真实数据库验收顺序：

1. 只读预检 ConfigHub 字段和 PostgreSQL 管理员能力；
2. 创建并初始化 `dayorder-test`；
3. 运行全部 Go 单元测试、架构检查和数据库版本检查；
4. 本地启动 API 与 Worker，验证 live/ready、注册登录和一组关系资源 CRUD；
5. 清理 `dayorder-test` 中由验收创建的业务数据，但保留数据库和 Schema；
6. 创建并初始化 `dayorder`，只执行 migration/version/角色权限检查，不写入业务验收数据；
7. 重跑不修改状态的版本检查，确认 Bootstrap 可重复执行。

## 非目标

- 不引入 Redis；
- 不删除或重建已存在的数据库；
- 不把生产业务流量切换到新数据库；
- 不部署 ConfigHub Server/Web；
- 不修改 ConfigHub 的存储加密模型；
- 不实现 PostgreSQL 备份、监控或灾难恢复；
- 不运行数据库降级 migration。
