# 日序账户、认证与用户数据设计

日期：2026-08-27  
状态：已确认

## 1. 背景与目标

日序当前允许所有浏览器直接访问完整应用，并把一份全局 AppData 同步到 SQLite 的单行 `app_state`。该模型没有身份边界，无法支持多人账户、可靠注销或用户级数据隔离。

本次改造增加登录、注册和用户模块，同时保留现有本地优先体验：

- 游客无需登录即可使用今天、目标、任务、日程、记录和笔记。
- Agent 必须在 Session 已验证且服务在线时使用。
- 注册时可以把当前设备的游客数据迁移到新账户。
- 登录用户的数据在 SQLite 和浏览器缓存中均按用户隔离。
- 登录用户断网后仍可使用核心功能，恢复连接后继续按 revision 同步。
- 更新产品 PRD、静态交互原型和真实前后端，使三者描述一致。

## 2. 首版范围

### 2.1 包含

- 邮箱与密码注册、登录和退出。
- 用户称呼、邮箱、密码和当前会话信息。
- 修改称呼；修改邮箱和密码时验证当前密码。
- 30 天服务端 Session。
- 注册时原子迁移游客 AppData。
- 每用户独立 AppData、revision 和更新时间。
- 游客、本地离线账户、已验证账户三种运行状态。
- Agent 登录门禁。
- 登录限流、来源校验、安全 Cookie 和通用错误响应。

### 2.2 不包含

- 邮箱验证、邮件验证码和忘记密码。
- 第三方 OAuth、SSO 或外部身份供应商。
- 头像上传、公开用户主页、关注、分享或团队协作。
- 管理员后台和角色权限系统。
- 自动合并已有账户与本机游客数据。
- 删除账户。该高风险能力在后续版本单独设计数据保留与恢复策略。

## 3. 产品规则

### 3.1 游客能力

游客数据只写入浏览器 localStorage，不访问服务端状态接口。游客可使用全部确定性核心功能，但不能启动 Agent、查看账户同步状态或修改账户资料。

游客点击 Agent 导航或快捷入口时，保留当前应用上下文并打开账户弹窗。界面必须说明：Agent 需要登录；目标、任务、日程、记录和笔记仍可作为游客使用。

### 3.2 注册与迁移

注册弹窗使用“登录 / 注册”分段切换。注册字段包括称呼、邮箱和密码，并默认勾选“迁移这台设备的数据”。迁移区展示本机目标、任务和笔记数量，明确说明失败时不会删除本机数据。

注册请求可携带完整游客 AppData。未携带 `initialData` 时，服务端使用与前端 schema 一致的规范空白 AppData v1；无论是否迁移，账户创建后都存在可同步状态。服务端在同一个 SQLite 事务中完成：

1. 创建用户。
2. 创建初始 `user_app_state`，revision 为 1。
3. 创建 Session。

只有服务端成功提交并返回用户、Session 和状态后，前端才把数据写入用户分区缓存并清空游客空间。注册或迁移失败时不得产生半成品，也不得修改游客数据。

### 3.3 登录已有账户

登录已有账户时不自动把当前游客数据混入该账户，避免把同一设备上其他人的内容写入错误账户。游客数据保持不变，退出后仍可访问。登录完成后切换到该用户的分区缓存，并拉取用户服务端状态。

### 3.4 退出与 Session 失效

正常退出需要联网。服务端撤销 Session 后，前端清除当前用户的浏览器数据缓存和同步元数据，再返回游客空间。离线时退出按钮保持可见但不可执行，并明确提示需要联网。

Session 在线校验为失效时，前端停止远端同步并显示重新登录弹窗，但保留该账户的本地缓存。重新登录同一账户后继续同步；不得自动把账户缓存切换为游客数据。

## 4. 架构

### 4.1 服务端模块

Go 服务拆分为以下边界：

- `auth`：密码哈希、Session 令牌、Cookie、当前用户解析和登录限流。
- `users`：用户创建、查询、称呼、邮箱和密码更新。
- `state`：只接收认证上下文中的 `user_id`，负责该用户的 AppData 和 revision。
- `httpapi`：请求解析、验证、错误映射、安全头、CORS 与 Origin 校验。
- `store`：SQLite schema、事务和上述模块的持久化接口。

请求不能通过 URL、查询参数或 JSON 指定 `user_id`。状态处理器只能使用 Session 中解析出的用户 ID，从接口设计上消除跨用户寻址能力。

### 4.2 前端模块

- `AuthProvider`：启动时校验 Session，暴露游客、离线账户、已认证账户和加载状态。
- `auth/client`：注册、登录、退出、当前用户和资料安全接口；所有请求使用 `credentials: "include"`。
- `AuthDialog`：上下文内登录和注册弹窗，支持 Agent 门禁来源文案。
- `AccountMenu`：头像入口，提供账户资料、数据与同步、应用设置和退出。
- `AccountDialog`：个人资料、账户安全和当前会话。
- `AppStoreProvider`：按游客或用户 ID 选择 localStorage 分区；仅已认证用户执行远端同步。
- `AgentGate`：同时要求已验证 Session 与在线服务，不以本地账户提示代替认证。

身份切换时通过 identity key 重新挂载数据 Store，避免上一个用户的 React 状态短暂泄漏给下一个用户。

## 5. SQLite 数据模型

```sql
CREATE TABLE users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL COLLATE NOCASE UNIQUE,
  display_name TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash BLOB NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT ''
);

CREATE INDEX sessions_user_id_idx ON sessions(user_id);
CREATE INDEX sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE user_app_state (
  user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL CHECK (revision > 0),
  payload BLOB NOT NULL,
  updated_at TEXT NOT NULL
);
```

现有 `app_state` 表保留为只读旧数据备份，不自动分配给第一个注册用户。客户端现有 `dayorder.app.v1` 才是升级时游客数据迁移的来源。

## 6. 浏览器存储分区

- 旧版入口：`dayorder.app.v1`
- 游客状态：`dayorder.guest.app.v1`
- 用户状态：`dayorder.user.<userId>.app.v1`
- 用户同步元数据：`dayorder.user.<userId>.sync.v1`
- 用户冲突备份：`dayorder.user.<userId>.conflict.v1`
- 最近账户提示：`dayorder.last-account.v1`，只保存用户 ID、称呼和邮箱，不保存令牌。

首次升级时若游客 key 不存在，则把旧版 `dayorder.app.v1` 复制到游客 key。升级完成后删除旧 key 和无用户归属的旧同步元数据，避免把原全局 revision 带入新账户。

正常退出删除当前用户的状态、同步元数据、冲突备份和最近账户提示。其他用户分区不应存在；若检测到残留也不能被当前身份读取。

## 7. HTTP API

所有接口位于 `/api/v1`，接受和返回 JSON。身份与状态响应设置 `Cache-Control: no-store`。

### 7.1 身份接口

```text
POST /auth/register
  body: { displayName, email, password, initialData? }
  201: { user, state? } + Set-Cookie

POST /auth/login
  body: { email, password }
  200: { user, expiresAt } + Set-Cookie

POST /auth/logout
  204 + expired Set-Cookie

GET /auth/session
  200: { user, expiresAt }
  401: AUTH_REQUIRED
```

`user` 只包含 `id`、`email`、`displayName`、`createdAt` 和 `updatedAt`。

### 7.2 用户接口

```text
PATCH /users/me
  body: { displayName }
  200: { user }

PUT /users/me/email
  body: { currentPassword, email }
  200: { user }

PUT /users/me/password
  body: { currentPassword, password }
  204 + rotated current Session
```

密码修改成功后撤销其他 Session，并轮换当前 Session 的令牌。

### 7.3 状态接口

```text
GET /state
  200: { revision, data, updatedAt }
  401: AUTH_REQUIRED
  404: STATE_NOT_FOUND

PUT /state
  body: { expectedRevision, data }
  200: { revision, data, updatedAt }
  401: AUTH_REQUIRED
  409: REVISION_CONFLICT
```

注册创建初始状态后，正常用户不会出现 `STATE_NOT_FOUND`；保留 404 便于数据修复和兼容空账户。

## 8. 验证与安全

- 邮箱去除首尾空白并转为小写后写入；必须满足常规邮箱格式，最大 254 字符。
- 称呼去除首尾空白，长度为 1–40 个 Unicode 字符。
- 密码长度为 10–128 个字符，不强制混合字符类别。
- 密码使用 Argon2id。参数封装在可升级哈希格式中，登录成功时允许透明提升旧参数。
- Session 令牌使用密码学安全随机源生成至少 32 字节，只把 SHA-256 哈希写入数据库。
- Cookie 名为 `dayorder_session`，设置 `HttpOnly`、`SameSite=Lax`、`Path=/`、30 天 Max-Age；HTTPS 环境设置 `Secure`。
- 登录失败统一返回 `INVALID_CREDENTIALS`，不区分邮箱不存在与密码错误。
- 每个 IP 与标准化邮箱组合在 15 分钟内允许 5 次失败；成功登录后清零。首版限流状态保存在进程内存，不承诺跨实例共享。
- 状态请求继续限制为 16 MB，并从结构层验证完整 AppData v1。
- 写请求校验 Origin；允许列表外的跨域请求拒绝。允许的跨域开发来源必须启用 credentials。
- 日志不得记录密码、原始 Session 令牌、完整 AppData 或 Cookie。

## 9. 离线与同步状态

### 9.1 游客

始终显示“仅保存在本机”。服务端离线不影响游客核心功能，也不重复尝试匿名 `/state` 请求。

### 9.2 已验证账户

按现有 500ms debounce 和 revision 乐观锁同步。成功状态显示最近同步时间；网络失败继续本地写入并等待 `online` 事件重试。

### 9.3 离线账户

启动时服务不可达且存在最近账户提示及对应缓存时，进入离线账户状态。核心功能读取该用户缓存；Agent、资料修改、密码修改和退出登录不可用。该状态不能被视为服务端已验证身份。

### 9.4 冲突

冲突备份必须写入当前用户专属 key，再加载该用户最新远端状态。任何冲突处理不得访问游客 key 或其他用户 key。

## 10. 界面与视觉

采用确认过的“上下文内账户弹窗”方案：

1. 游客点击 Agent 后显示居中的 Agent 门禁，主应用保持可见。
2. 登录与注册在同一弹窗内分段切换。
3. 注册模式展示本机数据数量和默认开启的迁移选项。
4. 登录后头像显示用户首字母，点击打开账户菜单。
5. 账户菜单区分账户资料、数据与同步、应用设置和退出。
6. 账户对话框包含个人资料、账户安全和当前会话三个页签。

视觉沿用现有真实应用而不是旧 PRD 中的早期方案：

- 画布 `#f4f6f8`、表面 `#ffffff`、炭蓝文字 `#18232e`。
- 雾蓝主色 `#52758a`，低饱和锈色 `#9b6954` 仅用于迁移提示等次级强调。
- 结构圆角 10px、面板 8px、控件 6px。
- 登录注册不使用渐变、大圆角、社交登录图标或营销插画。
- 360px 宽度无横向滚动；移动端账户弹窗转为接近全屏的底层对话框，触控目标不小于 44px。
- 支持键盘焦点、表单 label、错误关联、提交中禁用和 `prefers-reduced-motion`。

## 11. 错误体验

- 字段错误显示在对应字段下方，提交后聚焦首个错误。
- 重复邮箱提示“该邮箱已注册，请直接登录”。这是注册接口允许暴露的信息；登录接口仍使用通用错误。
- 迁移失败提示“账户未创建，本机数据没有变化”，并提供重试。
- 登录失效提示“登录已过期，本机更改仍在此设备”，提供重新登录。
- 离线点击 Agent 提示“Agent 需要联网验证账户”，不打开不可提交的表单。
- revision 冲突提示本地冲突备份已保留，并显示恢复来源。
- 修改邮箱或密码失败时不退出当前会话，也不清空表单中的非密码字段。

## 12. PRD 与原型更新

产品规格需要新增：

- 账户与游客原则。
- Agent 登录门槛。
- 注册迁移、已有账户登录和退出规则。
- User、Session、UserAppState 数据模型。
- 身份 API、安全边界和验收指标。
- M1 账户能力范围。
- 与真实应用一致的冷灰蓝配色和 10 / 8 / 6px 圆角。

静态原型需要新增或更新：

- Agent 游客门禁。
- 登录和注册弹窗。
- 注册数据迁移摘要。
- 登录后的账户菜单。
- 账户资料、安全与会话页面。
- 游客、已同步、离线账户和登录过期状态。
- 全局视觉 token 与真实前端一致。

## 13. 测试策略

### 13.1 Go

- 密码哈希与校验、令牌哈希和 Session 过期。
- 注册、重复邮箱、登录、统一错误、退出和限流。
- 修改称呼、邮箱和密码；密码修改撤销其他会话。
- 注册事务失败时不留下用户、Session 或状态。
- 两个用户相同 revision 互不影响，不能读取或覆盖对方状态。
- 未认证状态接口返回 401；旧 `app_state` 不会被新用户访问。
- Cookie 属性、Origin、CORS credentials、请求上限与安全响应头。

### 13.2 前端

- 旧 localStorage 到游客 key 的一次性升级。
- 游客核心 CRUD 不触发网络状态请求。
- 游客 Agent 门禁和登录注册弹窗。
- 注册迁移成功后切换用户分区并清空游客空间。
- 注册失败保留游客数据。
- 登录已有账户不合并游客数据。
- 用户切换不显示上一个用户状态。
- Session 过期、离线账户、重新登录和正常退出。
- 用户专属同步、冲突备份和远端 hydration 不重复 PUT。
- 360px 响应式、44px 触控目标和键盘操作。

### 13.3 运行验收

- 经 Vite 代理完成注册、Cookie 登录、迁移、同步、退出和再次登录。
- 直接调用 API 验证未认证、跨用户隔离、错误码和 Cookie。
- 重启 Go 服务后 Session 与用户状态仍存在。
- 停止 Go 服务后游客及离线账户核心功能仍可用，Agent 不可运行。
- Go 静态托管模式下 SPA 深链接、Cookie 和 `/api` 路由不冲突。

## 14. 完成标准

- 游客无需登录即可完成核心本地操作。
- Agent 在游客、离线账户和失效 Session 下均不能运行。
- 注册能够安全、原子地迁移本机数据，失败时零数据损失。
- 每个认证用户只能读写自己的状态；自动化测试证明至少两个用户相互隔离。
- 登录、退出、资料、邮箱和密码功能前后端完整连通。
- PRD、静态原型、真实界面和 API 对上述行为描述一致。
- 全量 TypeScript、Vitest、Go test、Go vet、生产构建和运行验收通过。
