# DayOrder Agent Runtime、Skill 与多 Agent 架构设计

- 日期：2026-09-03
- 状态：已批准
- 当前项目基线：DayOrder `7d4613b`
- 参考项目基线：m-agent `30eaf98`
- 一期实施及风险修复已完成，复核基线：`36d2bd9`
- 当前下一步：按已批准的 [Phase 2A 只读集成设计](2026-09-05-agent-phase2a-readonly-integration-design.md) 和 [独立实施计划](../plans/2026-09-05-agent-phase2a-readonly-integration.md) 进入开发；设计与计划不代表二期实现已完成

## 1. 背景

DayOrder 已经具备一套偏领域安全和可靠执行的 Agent 骨架：`AgentRun`、`AgentStep`、`AgentChange`、`AgentSourceRef`、数据 Scope、实体版本校验、JSON Patch 字段白名单、逐项确认、审计、撤销、PostgreSQL、Outbox 和幂等机制。当前生产入口有意关闭：Web 默认不展示 Agent，API 返回 `AGENT_NOT_AVAILABLE`，Worker 也没有注册 Agent Run 事件处理。

现有 Agent Provider 是一次性 `AgentSnapshot -> AgentPlan` 分析接口，不具备模型与工具反复交互的 ReAct 循环。提交 `988e702` 移除了旧 Agent 的生产接线，因此本设计不会直接恢复旧链路，而是在现有领域安全骨架外建立新的 Runtime、Skill、Tool 和 Provider 边界。

m-agent 提供了可以借鉴的通用能力：

- 完整的模型调用、Tool Call、Tool Result ReAct 循环；
- Skill Registry、`skill_list`、`skill_load` 和渐进式指令加载；
- Provider 流式事件、中止、重试和用量统计；
- 子 Agent、任务队列、进度、Mailbox 和结果契约；
- Tool Schema、权限策略、审批、会话恢复和上下文压缩。

m-agent 是本地编码 Agent。DayOrder 不复制它的 Shell、任意文件系统访问、Yolo 权限或本地工程目录 Scope，而是保留其可复用的协议和编排思想，替换为账户数据、HTTP API、Go Application Service 与未来 App Native Bridge。

本设计基于以下 m-agent 实现：

- `packages/server/src/skill-registry.ts`：Skill 发现、解析、合并和 Registry View；
- `packages/tools/src/skill.ts`：`skill_list`、`skill_load` 和渐进式加载；
- `packages/core/src/tool-schema.ts`：Tool 到 JSON Schema 的转换；
- `packages/core/src/agent.ts`：ReAct 循环与 Runtime 事件；
- `packages/core/src/task-manager.ts`、`packages/core/src/mailbox.ts`：子任务生命周期和消息传递；
- `docs/subagent-execution-spec.md`：主 Agent/Worker、能力冻结和结果回流约束。

## 2. 已确认的架构决策

1. 前台 Foreground Runtime 使用 TypeScript，运行在 Web 以及未来 App 的前端宿主中。
2. 后台 Background Runtime 使用 Go，运行在 DayOrder 服务端 Worker 中。
3. 首期不使用 Go/Rust WASM；前后端共享协议和测试语义，不强求共享 Runtime 源码。
4. Provider 的厂商适配、API Key、模型请求、重试和标准化全部位于服务端。
5. 前后端共享 `ToolSpec`，但使用不同 `ToolBinding`：前端走正常 HTTP，服务端直接调用 Go Application Service，设备能力使用 Native Bridge。
6. Skill 参考 m-agent 的 `SKILL.md`、Registry 和渐进式加载模型，并增加 DayOrder 的执行位置、后台可用性、风险和账户权限约束。
7. 多 Agent 采用 supervisor/worker 模型。主 Agent 负责规划和能力选择；子 Agent 是能力冻结的有限执行者。
8. 一期只完成架构基础、组件测试和一致性测试；真实 Provider、业务 Tool、整体集成测试与功能开放放到二期。
9. 一期继续保持生产 Agent 关闭，直到二期至少完成一个端到端只读 Skill。

## 3. 目标与非目标

### 3.1 目标

- 建立语言无关、可版本化的 Agent Protocol。
- 在 TypeScript 和 Go 中实现行为一致、可重放验证的 ReAct Runtime。
- 建立 Skill 与 Tool 分离的能力模型。
- 支持 m-agent 风格的 Skill 渐进式发现和加载。
- 明确 HTTP、Application Service 和 Native Bridge 三类 Tool Binding。
- 将所有模型厂商访问收敛到服务端 Provider Gateway。
- 为前台即时任务和后台计划任务建立清晰、互不冒充能力的执行边界。
- 为子 Agent 定义能力冻结、预算、结果契约和父子关系。
- 保留现有 AgentChange、权限、版本、审计和 Outbox 安全机制。
- 用一套语言无关的 Conformance Suite 约束前后端 Runtime 行为。

### 3.2 非目标

一期不实现：

- OpenAI、DeepSeek、豆包或其他真实 Provider Adapter；
- 日历、任务、笔记等真实业务 Tool；
- Web/App 到服务端再到模型和数据库的整体集成测试；
- App Native Bridge 或设备系统日历访问；
- 真实后台子 Agent 调度和并发执行；
- 前后台运行中的 Run 迁移；
- 第三方 Skill 安装、签名市场或动态代码执行；
- Skill 自带脚本、Shell、任意文件系统访问或 Yolo 模式；
- 生产 Agent 功能开放或恢复已经移除的旧接线；
- Go/Rust WASM Agent Kernel。

未来若双 Runtime 的维护成本成为实际问题，可以把纯状态机、协议校验和 Skill 解析提取为 Go 或 Rust Kernel，再由前端通过 WASM 接入。本设计保留这一替换边界，但不提前承担 WASM 的包体、异步桥接和第二语言成本。

## 4. 总体架构

```text
Web / Future App
  UI
   -> TypeScript Foreground Runtime
      -> Skill Registry
      -> Client Tool Registry
         -> HttpToolBinding -> DayOrder HTTP API
         -> NativeToolBinding -> App Native Bridge（未来）
      -> HTTP/SSE Provider Gateway Client

DayOrder Server
  API
   -> Provider Gateway HTTP/SSE Endpoint（未来二期开放）
   -> Application Services

  Worker
   -> Go Background Runtime
      -> Skill Registry
      -> Server Tool Registry
         -> ServiceToolBinding -> Application Services
      -> Server-side Provider Interface

  Existing Safety Domain
   -> AgentRun / AgentStep / AgentChange / SourceRef
   -> Scope / Version Check / Audit / Outbox / Idempotency

Shared Source of Truth
  contracts/agent/v1/*.schema.json
  contracts/agent/conformance/*.json
  built-in SKILL.md bundles
```

Provider Gateway 是逻辑上的统一服务端边界，不要求所有服务端调用都经过 HTTP。前端只能通过认证后的 HTTP/SSE 访问；API 和 Worker 可以使用同一套 Go Provider 接口与厂商适配代码。真实 Provider Adapter 在二期实现。

## 5. Agent Protocol

### 5.1 单一协议源

根目录维护版本化 JSON Schema，作为 Go 和 TypeScript 的共同契约。至少覆盖：

- Run、Turn、Step、Message 与 Content Block；
- Runtime Input、Runtime Event 与 Effect；
- ToolSpec、ToolCall、ToolResult 与 ToolError；
- SkillDescriptor、SkillActivation 与 CapabilitySnapshot；
- ApprovalRequest、ApprovalResponse；
- WorkerSpawnSpec、WorkerProgress 与 WorkerResult；
- Provider 请求、流事件、停止原因和 Usage；
- 标准错误结构。

一期从 Schema 生成 Go 和 TypeScript 的线协议 DTO；Runtime 内部可以使用符合各自语言习惯的领域类型，但所有序列化边界都必须经过生成 DTO 和 Schema 校验。JSON Schema 是线协议的唯一真相，内部类型不能反向改变协议。

### 5.2 版本规则

- 所有请求、事件、快照和测试向量携带 `protocolVersion`。
- 一期从 `1.0` 开始，只保证同一主版本向后兼容。
- 未知可选字段必须忽略；未知事件类型、Effect 类型或必填字段必须返回 `protocol_incompatible`，不得猜测执行。
- Skill 保存 `min-runtime-version`；不满足要求时不可激活。
- 每个 Run 的协议状态包含 `skillId + version + digest` 和 Capability Snapshot，确保恢复与审计使用相同能力集合；一期在 Fixture 和内存状态中验证，二期再接入数据库持久化。

### 5.3 Runtime 状态机

ReAct 核心遵循纯转换模型：

```text
Current State + Input Event -> New State + Ordered Effects[]
```

状态机不直接访问时间、随机数、网络、数据库、浏览器 API 或 Native API。需要这些值时，由 Host 生成带 ID、时间戳或结果的输入事件。这样同一个测试向量在 Go 和 TypeScript 中可以产生可比较的规范化输出。

规范状态至少包括：

- `idle`
- `model_pending`
- `model_streaming`
- `tool_pending`
- `approval_pending`
- `completed`
- `failed`
- `cancelled`

这些是 Runtime 内部状态，不替换现有产品层 `AgentRun.status`。一期定义显式映射器，二期再根据持久化需要决定是否扩展数据库字段，避免把 Runtime 的细粒度瞬态直接暴露为产品状态。

### 5.4 标准事件

核心事件至少包括：

- `run_started`
- `turn_started`
- `text_delta`
- `tool_call_requested`
- `tool_call_completed`
- `approval_requested`
- `approval_resolved`
- `usage_reported`
- `worker_spawned`
- `worker_progressed`
- `worker_completed`
- `run_completed`
- `run_failed`
- `run_cancelled`

每个事件包含稳定的 `runId`、单调递增 `sequence`，并按需要携带 `turnId`、`stepId`、`toolCallId`、`workerId`。测试比较事件语义和顺序，不比较时间戳、随机 ID 或模型自然语言是否逐字相同。

## 6. 双 Runtime 的一致性保障

前后端 Runtime 是同一逻辑 Runtime 的两种 Host 实现，不是两个可以自由演化的 Agent 产品。一致性由以下机制保证：

1. 共享版本化 JSON Schema。
2. 共享状态转换规则和错误分类。
3. 共享语言无关的 Golden Fixtures。
4. 相同的 Mock Provider 输出和 Mock ToolResult 输入。
5. Go、TypeScript 在 CI 中分别运行完整 Conformance Suite。
6. 使用 Trace Replay 对同一标准事件流进行重放并比较最终状态和 Effect 顺序。
7. Provider 厂商格式只在服务端标准化，前端不重复实现 Provider 转换。
8. Tool 能力通过 Capability Snapshot 明确协商，不以空实现伪装一致。

一致性的定义是：相同的规范状态与输入事件产生相同的状态、Effect、错误类别和终止原因。它不要求浏览器、Worker 和手机拥有相同 Tool，也不比较具有随机性的模型文案。

## 7. Skill 模型

### 7.1 Skill 与 Tool 分离

- Skill 描述 Agent 如何完成某类任务，包括指令、约束、资源、所需 Tool 和输出契约。
- Tool 是宿主真正执行读取、计算、提案或外部操作的能力。
- Skill 只能声明允许申请的 Tool，不能实现 Tool，也不能授予自身权限。

有效能力始终是以下交集：

```text
Skill allowed-tools
∩ Runtime 已注册 Tool
∩ 用户/Run 的数据授权
∩ 系统安全策略
```

### 7.2 Skill Bundle

参考 m-agent，以 `SKILL.md` 的 YAML Frontmatter 和 Markdown 正文作为唯一入口，不再单独维护 `skill.yaml`：

```text
skills/
  calendar-management/
    SKILL.md
    references/
    assets/
    schemas/
```

示例：

```yaml
---
name: calendar-management
description: 读取、创建和调整用户日程
version: 1.0.0
allowed-tools:
  - dayorder.calendar.read
  - dayorder.calendar.propose-change
  - device.calendar.read
execution-target: either
background-allowed: true
user-invocable: true
disable-model-invocation: false
min-runtime-version: 1.0.0
risk-level: medium
---
```

一期解析器必须使用完整 YAML 语义和 Schema 校验，不复制 m-agent 当前宽松的逐行 Frontmatter 解析方式。名称使用稳定、命名空间安全的格式；版本使用 SemVer；正文、辅助文件数量、单文件大小和总大小均设置上限。

一期允许 `references/`、`assets/` 和 `schemas/` 静态文件，不允许 `scripts/` 或任何动态可执行内容。辅助文件只能通过规范化相对路径读取，拒绝绝对路径、`..` 穿越和符号链接逃逸。

### 7.3 Scope 与执行位置

Skill 来源 Scope 为：

- `system`：产品内置 Skill；
- `user`：用户安装并可跨设备同步的 Skill；
- `device`：由当前 App 或设备提供的 Skill。

一期只加载 `system` Skill，`user` 和 `device` 只保留协议值。Scope 由 Registry 来源决定，不信任 Skill 自报。

执行位置使用独立字段：

- `client`
- `server`
- `either`

来源 Scope 和执行位置不得混用。例如系统内置 Skill 仍可以要求只在客户端运行。

### 7.4 渐进式加载

沿用 m-agent 模型：

1. 系统提示只包含 Skill 名称、简短描述和必要触发信息。
2. 主 Agent 通过 `skill_list` 查看当前 Registry 中可见的 Skill。
3. 主 Agent 通过 `skill_load` 加载完整指令和辅助文件清单。
4. Runtime 校验 Skill 版本、执行位置、后台许可和有效 Tool 交集。
5. 激活结果只对当前 Run 有效，不写入长期会话权限。

与 m-agent 不同，`skill_load` 不返回可以绕过用户授权的 `permissionGrant`。它只能产生 Tool 激活候选，Runtime 仍需执行 Scope、账户权限、系统权限和审批判断。

### 7.5 Registry 合并规则

未来存在多来源时，同名 Skill 不进行静默覆盖。Registry 使用 `(name, version, digest, scope)` 唯一标识，并按显式优先级选择可见版本；版本或 Digest 冲突必须产生可观察 Warning 或失败。一期只有系统来源，也要实现重复名称和 Digest 校验，为未来扩展保留确定行为。

## 8. Tool 模型

### 8.1 三层结构

```text
ToolSpec：共享名称、语义、Schema、风险和权限要求
  -> ToolBinding：某个 Runtime 中的具体实现
     -> Transport/Capability：HTTP、Go Service 或 Native Bridge
```

同一业务语义使用相同 Tool ID，不因传输方式不同而改名：

| Tool ID | 前端 Binding | 服务端 Binding |
| --- | --- | --- |
| `dayorder.calendar.read` | DayOrder HTTP API | Go Calendar Application Service |
| `dayorder.notes.search` | DayOrder HTTP API | Go Note Application Service |
| `dayorder.task.propose-change` | HTTP 创建 AgentChange | 直接调用 AgentChange Service |
| `device.calendar.read` | App Native Bridge | 不注册 |
| `device.notification.schedule` | App Native Bridge | 不注册 |

设备本地能力使用独立 `device.*` 命名空间，因为其语义、权限和可用性与 DayOrder 服务端资源不同。普通浏览器不能读取系统日历；Web 无 Native Binding 时必须报告 `capability_unavailable`。

### 8.2 ToolSpec

共享 ToolSpec 至少定义：

- 稳定、带命名空间的 Tool ID；
- 模型可见描述；
- 输入和输出 JSON Schema；
- `read`、`reversible_write`、`irreversible_write`、`external_communication` 副作用等级；
- 所需数据域、用户权限和系统权限；
- 是否幂等及幂等键要求；
- 默认超时和结果大小上限；
- 支持的执行位置；
- 审批策略。

ToolResult 使用结构化数据和标准错误，不像 m-agent 那样只依赖 `display` 字符串。可选 UI 摘要与提供给模型的数据分离；所有结果在送回模型前必须通过输出 Schema 和大小限制。

### 8.3 ToolBinding

前端数据流：

```text
Foreground Runtime
  -> HttpToolBinding
  -> authenticated DayOrder HTTP API
  -> Application Service
  -> Repository
```

前端不得直接访问服务端数据库，也不复制业务授权和写入规则。Binding 可以复用现有 Typed API Client，但必须负责 Tool 输入到 HTTP 请求、HTTP 错误到标准 ToolError、HTTP 响应到 ToolResult 的明确映射。

后台数据流：

```text
Background Runtime
  -> ServiceToolBinding
  -> Go Application Service
  -> Repository
```

服务端 Binding 不调用自己的 HTTP Handler，也不绕过 Application Service 直接访问 Repository。它使用明确的用户 Actor/Scope 上下文，复用事务、RLS、版本、同步和审计规则。

未来设备数据流：

```text
Foreground Runtime
  -> NativeToolBinding
  -> App Native Bridge
  -> OS permission-protected capability
```

WASM 即使未来引入，也不能直接获得设备权限；Capability Broker 和 Native Bridge 始终属于宿主层。

### 8.4 写操作

无论 Tool 位于前端还是服务端，涉及 DayOrder 核心数据的 Agent 写操作均生成 `AgentChange`，不得直接调用普通 CRUD 完成最终写入：

```text
Tool Call
  -> AgentChange Proposal
  -> 字段白名单、Scope、引用与 Base Version 校验
  -> 用户确认
  -> Application Service 应用
  -> Sync Change + Audit Event
```

前端通过 HTTP 创建提案；服务端通过 AgentChange Application Service 创建提案。两条路径返回相同 ToolResult。最终应用仍使用现有逐项确认、事务、版本冲突和撤销机制。

## 9. Provider Gateway

### 9.1 职责

Provider Gateway 负责：

- 保存和读取服务端 Provider 凭据；
- 将统一 ModelTurn 请求转换为厂商请求；
- 将 SSE/流式响应标准化为 Agent Event；
- Tool Call 参数拼装与 Schema 校验；
- 超时、中止、`Retry-After`、有限指数退避；
- 模型 Profile、配额、Token Usage 与成本记录；
- 上下文长度检查和未来的 Compaction 接口；
- 日志脱敏和厂商错误标准化。

前端不持有厂商 API Key，不选择任意 Provider URL，也不解析 OpenAI、DeepSeek 或豆包的私有事件格式。前端提交规范化 Run/Turn 上下文和 ToolSpec 引用，接收标准事件。

### 9.2 一期边界

一期只定义服务端 Provider Interface、规范请求/事件、Foreground HTTP Client Interface 和 Mock Provider。不会开放真实 Gateway Endpoint，也不会接入任何厂商。

现有 `AgentProvider.Analyze(snapshot) -> plan` 和 `agentprovider.HTTPProvider` 是旧的一次性分析模型，不作为新 ReAct Provider Contract。生产接线继续关闭；二期在真实只读链路通过后再决定删除或迁移旧实现。

## 10. 前台与后台运行模型

### 10.1 Foreground Runtime

前台适用于即时通讯式任务：

```text
User/UI
  -> TypeScript Runtime
  -> HTTP/SSE Provider Gateway
  -> Standard Tool Call
  -> Client Tool Registry
  -> HttpToolBinding 或未来 NativeToolBinding
  -> ToolResult
  -> 下一次 Model Turn
```

它负责流式 UI、当前设备权限、交互式审批和短生命周期子 Agent。页面或 App 关闭后不承诺继续执行。二期需要恢复时，恢复依据是服务端已经确认持久化的 Run/Step，而不是仅存在于页面内存的未提交副作用。

### 10.2 Background Runtime

后台适用于计划和长任务：

```text
Scheduler/Outbox
  -> Worker
  -> Go Runtime
  -> Server-side Provider Interface
  -> Server Tool Registry
  -> ServiceToolBinding
  -> Application Service
```

它负责持久化、Checkpoint、重试、租约、计划任务和后台子 Agent，不依赖浏览器在线。所有业务数据访问仍在任务所属用户的 RLS/Actor 上下文中完成。

### 10.3 执行模式

Run 创建时明确指定：

- `foreground`
- `background`

子 Agent 继承父 Agent 的执行模式。一期和二期首个版本不实现运行中的前后台迁移，也不实现隐式 Handoff。需要后台持续运行的请求必须创建 Background Run，而不是把正在执行的 Foreground Run 转交给 Worker。

后台 Run 在创建前根据 Capability Snapshot 拒绝 `device.*` 依赖；如果运行时仍遇到缺失能力，则以 `capability_unavailable` 终止，不能等待一个尚未实现的跨端 Handoff，也不能伪造执行结果。跨端 Handoff 只有在出现真实产品需求后才另行设计。

## 11. 多 Agent 模型

采用 m-agent 的 supervisor/worker 原则：

- 主 Agent 是唯一规划者、Skill 选择者、任务拆分者和用户决策责任人。
- 子 Agent 是执行 Worker，不是第二个可自由扩权的编排器。
- Skill 选择是 orchestration concern；Skill 消费是 execution concern。
- 子 Agent 完成结果先返回主 Agent，主 Agent 审阅后才能继续或向用户输出。

WorkerSpawnSpec 至少冻结：

- 父 Run/Worker 标识；
- Goal、Success Criteria、Scope、Constraints 和 Hints；
- Skill ID、版本与 Digest；
- ToolSpec 白名单；
- 数据权限和审批策略；
- Token、步骤、时间和子任务预算；
- Output Contract；
- 执行模式和协议版本。

子 Agent 不得访问 `skill_list`、`skill_load` 或完整 Skill Registry，不得自行附加 Skill、扩大 Tool 集合、修改目标或再成为无边界的主 Agent。一期只实现 SpawnSpec、能力冻结、状态和结果契约及其测试；真实任务队列、Mailbox、进度持久化和并发调度放到二期。首个版本的最大嵌套深度为一层，后续只有在存在明确需求和预算模型时再扩展。

WorkerResult 是结构化、可校验的父 Agent 输入，至少区分 Summary、Findings、Risks 和 Next Steps；它不是默认直接展示给用户的最终答复。

## 12. 权限与安全

### 12.1 权限来源

Runtime 的有效权限来自四个独立来源：

1. Skill 的 `allowed-tools` 上限；
2. Runtime 当前实际注册的 ToolBinding；
3. 用户为本次 Run 指定的 AgentScope；
4. 服务端策略、账户权限、设备系统权限和审批结果。

任何一项不满足都不能执行。客户端传入的 user ID、Skill Scope、Tool 成功结果或权限声明都不是服务端信任根；服务端从认证 Session、Run 记录和自身策略重新推导。

### 12.2 Tool 和 Skill 安全

- Tool 输入和输出均执行 Schema、深度、数量和字节上限校验。
- Tool 数据被视为外部内容，不允许其文本改变 Runtime 权限或系统策略。
- Skill 只包含声明、指令和静态资源，不执行下载代码。
- 前端无法通过伪造 ToolResult 绕过服务端写入校验。
- 服务端写入继续执行所有权、RLS、字段白名单、Base Version、确认和审计检查。
- Provider 日志不记录 API Key、Cookie、完整笔记正文或未经筛选的 Tool Payload。
- 每个 Run 绑定明确用户、Scope、模型 Profile 和 Capability Snapshot。

### 12.3 审批

OS 权限、Agent Tool 审批和 AgentChange 确认是三种不同机制：

- OS 权限决定 App 能否调用设备能力；
- Tool 审批决定 Agent 是否可以发起某种副作用；
- AgentChange 确认决定某项 DayOrder 数据变更是否真正应用。

其中任何一层通过都不能替代其他层。

## 13. 错误、重试与运行限制

### 13.1 标准错误

两套 Runtime 使用相同错误类别：

- `validation_failed`
- `protocol_incompatible`
- `capability_unavailable`
- `permission_denied`
- `approval_denied`
- `provider_unavailable`
- `provider_rate_limited`
- `tool_failed`
- `timeout`
- `version_conflict`
- `cancelled`
- `internal_error`

标准错误包含稳定 Code、面向用户的安全 Message、`retryable`、关联 Run/Turn/Tool Call ID 和可选的受控 Details。内部堆栈、SQL、Provider 原始密钥或其他用户信息不能进入客户端错误。

### 13.2 重试

- Provider 临时网络错误、429 和明确的 5xx 可以按照 `Retry-After` 或有限指数退避重试。
- 只读、显式幂等的 Tool 可以有限重试。
- 写入提案必须携带幂等键。
- 未知副作用、不可逆写入和外部通信在结果不确定时不得自动重试。
- `version_conflict` 不自动覆盖；需要重新读取、重新分析或再次确认。
- 前端使用 AbortSignal，Go 使用 Context；取消必须向 Provider 和当前 ToolBinding 传播。

### 13.3 预算与循环保护

每个 Run 和 Worker 都必须限制：

- 最大 ReAct 步数；
- 最大 Token 与费用预算；
- 最大运行时间；
- 单 Tool 超时和结果大小；
- 最大子 Agent 数量和并发数；
- 重复 Tool Call 和无进展循环次数。

达到限制时产生稳定的失败或受控停止事件，不能静默截断为成功。

### 13.4 后台超时、失败与恢复边界

决策状态：2026-09-04 已确认；二期首次接入后台 Agent 时必须遵守，后续基于运行数据复盘。

服务端 Worker 是长期运行并持续消费任务的宿主进程，不是每个 `AgentRun` 独占且可在超时后销毁的 OS 进程。首个生产版本只允许 DayOrder 自有、受信任并遵守 Go `context` 取消协议的进程内 `ToolBinding`。Runtime 不承诺在同一进程内强制终止忽略 `context` 的任意代码；第三方可执行 Tool、非协作依赖以及需要硬终止保证的任务，必须等到独立进程或容器隔离方案另行设计后才能接入。

超时按层级处理：

- 单次 Tool 超时生成 `ok=false`、错误码为 `timeout` 的 `ToolResult` 并返回 ReAct 循环。Agent 可以选择替代 Tool、调整方案或安全地结束说明，因此单次 Tool 超时不直接把整个 Run 标记为失败，也不自动重放同一次调用。
- 整体 Run 达到时间预算后进入 Runtime `failed`；投影到现有产品模型时记录为 `AgentRun.status=failed`，并保存 `errorCode=timeout`。Worker 释放该 Run 的并发槽后继续消费其他任务。
- 周期计划的一次 Run 超时只终止并标记当前执行实例，不自动停用计划定义；后续计划实例仍按原计划触发。一次性计划在安全重试耗尽后保持失败。
- 已经持久化的 Runtime 终态属于任务结果，Worker 应确认消费，不能把它作为通用处理器错误从头重跑整个 Run。只有数据库断连、事务提交失败或队列确认失败等基础设施错误，才能通过 Worker/Outbox 的投递机制重试。
- 只读或明确幂等的操作可以在预算内有限重试。写操作必须携带稳定幂等键；创建日程、外部通信等操作超时且结果不确定时，必须先查询或对账，不能盲目重试。
- 用户取消、Worker 停机和任务租约失效与超时分别记录。它们都必须向 Provider 和当前 Tool 传播取消信号，但不能统一伪装为 `timeout`。

二期至少记录 Tool 超时率、Run 超时率、取消完成时延、重试次数、结果不确定次数、Worker 并发槽占用和 goroutine 趋势。出现以下任一情况时复盘本决策：需要运行第三方或用户提供的可执行 Tool；依赖无法可靠响应 `context`；存在硬截止时间或强制终止要求；超时后资源持续增长；后台并发和任务积压达到进程内隔离的安全上限。复盘时优先评估每 Tool 或每 Run 的子进程/容器隔离，而不是继续在线程或 goroutine 层模拟强制终止。

## 14. 与现有 DayOrder 领域模型的关系

- `AgentRun` 继续作为用户可见、可审计的顶层运行记录。
- `AgentStep` 承载经过筛选的用户可见步骤；高频 Token Delta 不逐条写入关系表。
- `AgentChange` 继续作为所有 DayOrder Agent 写入的唯一提案和确认入口。
- `AgentSourceRef` 保存依据实体及读取时版本。
- `AuditEvent` 记录 Run、审批、提案应用、取消和失败等关键动作。
- Outbox 继续作为后台计划任务的可靠触发基础。

一期不修改数据库 Schema、不注册 Worker Agent 事件、不开放新 API。Runtime Event 与现有模型之间定义纯映射和接口边界，并在组件测试中验证。二期根据真实持久化需求增加 Migration，禁止一期为了未来字段提前扩表。

## 15. 一期：Agent Foundation

一期实施计划只覆盖以下内容：

1. `contracts/agent/v1` 下的协议 Schema、版本规则和标准错误。
2. `contracts/agent/conformance` 下的语言无关测试向量。
3. TypeScript Runtime 的纯状态机、Effect Driver 接口和 Mock Host。
4. Go Runtime 的纯状态机、Effect Driver 接口和 Mock Host。
5. ToolSpec、ToolBinding、Tool Registry 和 Capability Snapshot 接口。
6. m-agent 风格的 `SKILL.md` 解析、系统 Skill Registry、`skill_list` 与 `skill_load` 基础能力。
7. Skill 激活与 Tool/Scope/Policy 交集计算。
8. Provider Interface、统一流事件和 Mock Provider。
9. WorkerSpawnSpec、冻结能力快照、Worker 状态和结果契约。
10. Conformance、Schema、Parser、Registry、权限和 Mock ReAct 组件测试。
11. 架构校验，防止 Web 引入 Provider Key、厂商 SDK 或直接数据库能力。

一期允许提供仅用于测试的内置 Skill 和 Mock ToolSpec，但不提供可访问真实用户数据的 ToolBinding。

一期完成后仍保持：

- `<App />` 默认 `agentAvailable=false`；
- 生产 Agent API 返回 `AGENT_NOT_AVAILABLE`；
- Worker 不注册 Agent Run 处理器；
- 现有旧 Provider 不重新接线；
- 无真实模型流量、无新增 Agent 数据写入。

## 16. 二期：真实能力接入

2026-09-05 更新：已批准 Phase 2A 只读最小闭环，具体范围见 [Phase 2A 设计](2026-09-05-agent-phase2a-readonly-integration-design.md)。下列条目是二期整体路线，不全部属于 Phase 2A；写入、计划调度、真实子 Agent 和 Native Tool 仍需另行对齐。

二期另行制定实施计划，按风险递增顺序交付：

1. 服务端只读 ToolSpec 与 ServiceToolBinding；
2. 前端对应的 HttpToolBinding；
3. 第一批内置 DayOrder Skill；
4. OpenAI、DeepSeek、豆包等服务端 Provider Adapter 与流式 Gateway；
5. 一个只读 Skill 的前台和后台端到端闭环；
6. AgentChange 提案类 Tool；
7. 审批、应用、冲突、撤销与审计闭环；
8. 后台计划任务和真实子 Agent；
9. 未来 App 的 Native Calendar、Notification 等设备 Tool。

只有至少一个端到端只读 Skill 通过整体集成测试、安全测试和可观察性验收后，才允许开启受控 Agent Feature Flag。写能力必须在只读能力稳定后单独开放。

## 17. 测试策略

### 17.1 一期测试

一期不建设整体集成测试，只要求：

- JSON Schema 正例、反例和协议版本测试；
- SKILL.md Frontmatter、正文、辅助文件、路径与大小限制测试；
- Skill 重名、版本、Digest 和 Registry 可见性测试；
- ToolSpec/Binding 注册、重复 ID 和缺失能力测试；
- Skill、Runtime、用户 Scope 和策略交集测试；
- Go、TypeScript 共享同一 Golden Fixture 的状态机测试；
- Mock Provider 发出文本、Tool Call、完成、错误和 Usage 的组件测试；
- Mock Tool 成功、拒绝、失败、超时和取消测试；
- 重复 Tool Call、最大步数、预算和死循环保护测试；
- WorkerSpawnSpec 能力冻结、未知 Skill/Tool 和结果契约测试；
- Trace Replay 最终状态和 Effect 顺序测试。

Mock ReAct 测试覆盖至少一条完整的：

```text
user input
-> model tool call
-> mock tool result
-> second model turn
-> completed
```

它属于 Runtime 组件/一致性测试，不连接真实 HTTP、数据库、Worker 或模型厂商。

### 17.2 二期测试

二期增加：

- Web Runtime -> Provider Gateway -> Fake/Provider Sandbox 的集成测试；
- HttpToolBinding -> DayOrder HTTP API -> PostgreSQL 的集成测试；
- Background Runtime -> ServiceToolBinding -> PostgreSQL/Outbox 的集成测试；
- 同一 ToolSpec 的前端 HTTP Binding 与服务端 Service Binding Contract Test；
- 前后台相同规范场景的 Trace 对比；
- Provider 流中止、重连、限流、超时和故障注入；
- AgentChange 权限、版本冲突、幂等、确认、撤销和审计；
- 真实子 Agent 队列、并发、取消、结果回流与恢复；
- 端到端只读 Skill 和后续写入 Skill 验收。

## 18. 可观察性

一期协议预留并在 Mock 中验证：

- `runId`、`turnId`、`stepId`、`toolCallId`、`workerId`；
- Runtime、Protocol、Skill 和 Tool 版本；
- Provider、模型 Profile 与 Usage；
- Tool/Provider 耗时和标准错误码；
- 审批、取消、预算停止和能力缺失原因。

二期实际指标至少覆盖 Run 成功率、P95 时长、Provider 错误与重试、Tool 错误、审批等待、Token/成本、后台队列年龄、子 Agent 数量和循环保护触发次数。日志只记录受控元数据，不默认记录完整 Prompt、笔记正文或 Tool Payload。

## 19. 验收标准

一期完成必须同时满足：

- Go 和 TypeScript Runtime 通过完全相同版本的 Conformance Fixtures。
- 同一输入事件在规范化后得到相同状态、Effect 顺序、错误类别和停止原因。
- 系统 Skill 可以按 m-agent 风格完成列表、按需加载和辅助文件安全读取。
- Skill 无法通过 `allowed-tools` 自行获取用户权限。
- 子 Agent 能力在 Spawn 时冻结，不能发现新 Skill 或扩大 Tool 集合。
- 前端和服务端可以为同一 ToolSpec 注册不同 Binding；缺失 Binding 明确报告能力不可用。
- Mock Provider 与 Mock Tool 可以完成至少一个双轮 ReAct 场景。
- Provider Key、厂商 SDK 和厂商事件解析没有进入 Web Bundle。
- 没有真实业务 Tool、真实 Provider、数据库 Migration 或生产 Agent 接线。
- 当前生产 Agent 关闭行为保持不变。
- 二期整体集成测试和真实能力没有被伪装为一期已完成内容。

## 20. 后续实施约束

一期 Implementation Plan 只针对 Agent Foundation，不能把二期 Tool、Provider、数据库和端到端接线混入同一计划。该计划及风险修复已完成；下一份计划应在 Phase 2A 具体设计审核后独立制定。如果真实集成需要修正一期协议，必须先更新版本化契约和双端 Mock/Fixture，再接入真实数据，不能仅修改其中一个 Runtime。

本设计不承诺永久维持双 Runtime 实现。如果未来 Conformance Suite 仍无法控制漂移，或状态机逻辑显著膨胀，再基于测量结果评估共享 Go/Rust Kernel；网络、数据库、HTTP、Native Bridge、权限弹窗和 UI 始终保留为 Host Binding，不进入共享 Kernel。
