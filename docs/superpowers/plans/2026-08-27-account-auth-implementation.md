# 日序账户、认证与用户数据实施计划

日期：2026-08-27  
依据：`docs/superpowers/specs/2026-08-27-account-auth-design.md`

## 目标

在现有 React + Go monorepo 中增加完整账户模块，同时保留游客本地优先体验。所有账户状态都由 HttpOnly Session Cookie 认证，Agent 仅在在线且 Session 已验证时可用；注册迁移、用户数据隔离、离线缓存和 revision 同步必须可自动化验证。

## 1. 服务端认证基础

- 新增 `internal/auth`：Argon2id 可升级密码哈希、常量时间校验、安全随机 Session token 与 SHA-256 token hash。
- 扩展 SQLite schema：`users`、`sessions`、`user_app_state` 与索引，启用 foreign keys，保留旧 `app_state` 为只读备份。
- 在 store 中建立用户、会话、注册事务、用户状态读写接口；所有状态方法必须显式接收认证得到的 user ID。
- 增加单元测试：密码、Session 过期、注册事务、重复邮箱、两用户状态隔离和 revision 冲突。

## 2. 服务端 HTTP API 与安全边界

- 增加注册、登录、退出、Session 校验、资料、邮箱、密码接口。
- 增加 Session 中间件与当前用户上下文，`/state` 改为只操作当前用户。
- Cookie 使用 `HttpOnly`、`SameSite=Lax`、30 天 Max-Age，并按 HTTPS 设置 `Secure`。
- 写请求校验 Origin；CORS 支持 credentials；所有身份/状态响应 `no-store`。
- 实现进程内 IP + 标准化邮箱登录失败限流与统一 `INVALID_CREDENTIALS`。
- 增加 HTTP 测试覆盖错误码、Cookie、未认证、跨用户隔离、会话轮换、来源校验和请求体上限。

## 3. 前端存储分区与认证状态

- 新增认证 API client、类型与 `AuthProvider`，区分 loading、guest、offline-account、authenticated。
- 把全局 localStorage 迁移为 guest/user/sync/conflict/last-account 分区，并保留旧 key 一次性升级。
- 让 `AppStoreProvider` 按 identity key 重挂载；游客完全不调用 `/state`，认证用户才 hydrate/sync。
- 注册成功后先写用户缓存再清除 guest；失败不改 guest。登录已有账户不合并 guest。
- Session 失效保留账户缓存并进入重新登录状态；正常退出成功后清除当前账户缓存并返回 guest。
- 增加存储迁移、身份切换、游客无网络请求、迁移成功/失败、过期与冲突测试。

## 4. 前端账户界面与 Agent 门禁

- 增加 `AuthDialog`：登录/注册分段、迁移摘要、字段错误、提交状态和 Agent 上下文文案。
- 增加 `AccountMenu` 与 `AccountDialog`：资料、安全、会话、同步状态和在线退出。
- Agent 导航与快捷入口统一经过 `AgentGate`；guest、offline-account、expired 均不能运行。
- 延续现有冷灰蓝视觉 token：10/8/6px 圆角、44px 触控目标、360px 无横向滚动、无渐变。
- 增加关键 UI 流程、键盘交互和响应式样式测试。

## 5. 文档与原型

- 更新 `docs/dayorder-product-spec.md`：范围、用户故事、游客/账户规则、模型、API、安全、离线、验收指标。
- 更新 `docs/dayorder-prototype.html`：Agent 门禁、登录注册、迁移摘要、账户菜单、资料/安全/会话、离线和失效状态。
- 确认 PRD、原型、真实 UI 的文案、视觉 token 与行为一致。

## 6. 验证与交付

- 运行 TypeScript、Vitest、Go test、Go vet、前后端生产构建。
- 经 Vite 代理执行注册迁移、Cookie Session、状态同步、退出、再次登录。
- 直接调用 API 验证未认证、两用户隔离、重复邮箱、密码轮换和 Cookie 属性。
- 重启 Go 服务验证持久化；验证 Go 静态托管下 SPA 深链接和 `/api` 共存。
- 对设计规格完成逐条审计；任何未满足项必须修复或明确报告，不以测试数量代替需求验收。
