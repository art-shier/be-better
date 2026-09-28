# Agent Phase 2A Readonly Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用同一内置日程 Skill、双宿主 Tool Binding 和服务端 Provider，完成前台及后台的真实只读集成验收，生产保持关闭。

**Architecture:** TS 和 Go Runtime 使用 Protocol 2.0 的完整模型轮次语义。Run/operation 记录约束服务端授权、预算与去重；前台通过认证 HTTP/SSE，后台在独立集成 Worker 中直接调用 Application Service。Fake 闭环和真实 DeepSeek 冒烟分别报告，不恢复旧 Analyze 接线。

**Tech Stack:** Go 1.25、TypeScript 5.9、Node 24.15.0、JSON Schema 2020-12、现有 json-schema-to-typescript/go-jsonschema、PostgreSQL 17/sqlc/pgx、Vitest、Testcontainers、Playwright 1.63.0（新增测试依赖）、Prometheus。

**Spec:** [已批准的 Phase 2A 设计](../specs/2026-09-05-agent-phase2a-readonly-integration-design.md)。执行者必须先完整读取 Spec，本文将其拆为实现步骤，不扩大授权。

## Global Constraints

- 工作树：`C:/Users/yeshaopeng/workspace/day/.worktrees/agent-foundation`；分支 `feat/agent-foundation`；规划基线 `23389bd`。先确认当前分支与用户改动，再执行任务。
- Protocol `2.0` / Runtime `2.0.0`；集成宿主只接受精确的 2.0，不降级、不混用版本。
- 新 Schema 放在 `contracts/agent/v2`；旧 v1 Schema 保留历史参照，不要求同时维护两套可运行 Runtime。
- 当前严格 DTO 不承诺忽略未知字段。运行时 DTO 与生成文件只能由 Schema/生成器同步修改。
- 内置 `calendar-overview`，版本 `1.0.0`，来源 `system`，`execution-target: either`，`background-allowed: true`，`risk-level: low`。
- `allowed-tools` 仅为 `dayorder.calendar.read`；不注册写入、设备、spawn 能力。
- `dayorder.calendar.read`：`sideEffect=read`，`requiredDomains=[calendar]`，`executionTargets=[client,server]`，`approvalPolicy=never`，`idempotent=true`，`timeoutMs=10000`，`resultMaxBytes=65536`。
- 日程 limit 缺省 20，整数范围 1–50；cursor 最大 4096 字节；时间窗最长 31×24 小时；使用现有闭区间相交语义。
- 只接受 `scope.domains=[calendar]`、必填 from/to、未指定 entityIds；绝不接受后忽略实体 Scope。
- 每个模型轮次最多一个调用，串行执行；先取得 completed/Usage、检查预算，再执行 Tool。
- Run 默认 120 秒（含排队）；Provider Turn 总上限 45 秒、空闲读超时 15 秒；重试最多再试 1 次，发布规范事件后不再重试。
- `maxSteps=8`、`maxTokens=16000`、`maxRepeatedToolCalls=2`、`maxWorkers=1`、`maxConcurrency=1`；Gateway 最多 9 轮，每轮输出最多 2048 Tokens。
- Service 每 Run 最多 8 个不同业务 Tool Call；每账号最多 1 个活动 Run、每分钟最多创建 10 次；客户端不能提高限制。
- 请求体 256 KiB、消息数 64、JSON 嵌套深度 16、单条 SSE data 64 KiB、单轮累计 SSE data 1 MiB。
- 后台集成 batchSize=1、并发 1；保留生产 batchSize=25 和 staleAfter=5 分钟；收尾 context 最多 5 秒；取消检查 250 毫秒、正常依赖退出验收上限 1 秒。
- Tool timeout 回模型；Run timeout 为 failed；终态落库后确认消费；未知中间结果不从头重跑；周期调度/续期/checkpoint 不在本期。
- 可信 Go Binding 同步执行且响应 context；TS 结果先快照一次再验证；保留一期三项风险修复及回归。
- 生产 App 默认关闭、Agent API 不可用、`cmd/worker` 不注册 Agent。新宿主拒绝 production，只连接自身创建的隔离数据库：Docker 容器或明确启用的 ConfigHub 临时库。
- 本文中的命令从工作树根目录执行；选定数据库后端不可用时专用 integration 门禁失败，不算 skip 通过。ConfigHub 仅提供数据库配置，不因此授权读取模型生产凭据；真实厂商调用仍只使用专用测试凭据和合成数据。

## 文件与责任地图

| 单元 | 新增/修改的位置 | 责任 |
| --- | --- | --- |
| 契约 | `contracts/agent/v2/`、现有生成器及 generated 目录 | v2 Runtime/HTTP/SSE DTO、Skill 和 Tool 内容同步 |
| 状态与驱动 | `apps/api/internal/agentruntime/`、`apps/web/src/agent/runtime/` | 完整 Turn、Usage、取消原因、剩余 deadline；不访问数据库 |
| 内置资产 | `skills/calendar-overview/`、`contracts/agent/tools/`、Go `agentassets`、Web `agent/assets` | 一份内容源、digest、日程 Schema |
| 执行存储 | `agentexecution`、`postgres/agent_execution_repository.go`、新 migration/query | 冻结快照、控制字段、operation 幂等与 RLS |
| Application Services | `service/agent_readonly_*.go`、`service/agent_calendar_read.go` | Run 生命周期、预算、Scope、可信引用、终态提交 |
| Provider | `agentprovider/{adapter,deepseek,deepseek_stream,history,fake}.go`、`agentgateway/` | 厂商协议与服务端 Gateway；不依赖 Runtime Driver |
| HTTP | `httpapi/agent_integration_*.go` | 复用认证/Origin/设备校验；新 Router 构造入口 |
| Web 宿主 | `agent/{api,provider,tool,host}/` | API、标准 SSE、HTTP Tool 和 foreground 生命周期 |
| 后台宿主 | `agenthost/`、`worker/agent_readonly.go` | 单 Run 执行、取消、执行权、防重复运行 |
| 验收宿主 | `agentintegration/`、`cmd/agent-integration/`、`apps/web/tests/agent-integration/` | 独占 DB、真实会话、最小浏览器 harness、Fake/real 装配 |
| 观测与 CI | `observability/agent.go`、`scripts/agent-integration.mjs`、CI | 受控指标/日志、跨端验收、退出清理 |

上表未写完整前缀的 Go 包均位于 `apps/api/internal/`；Web agent 目录位于 `apps/web/src/agent/`。

## 执行与评审节奏

按 Task 1–16 顺序执行。Task 1–3 是协议/组件检查点；Task 4–7 是数据与权限检查点；Task 8–13 是宿主接入检查点；Task 14–16 是整体验收。每个任务都完成红测试、实现、绿测试、差异审查后提交；任务内涉及 Go/TS 同一契约时必须一起完成。

代码块是需要实现/验证的关键逻辑与接口，不授权删除现有其他分支。测试中使用标准库、现有工具或本计划定义的 helper；每个新增 public 接口必须出现在所属任务中。提交只暂存该任务文件，不使用 `git add .`。

### 实施进度（截至2026-09-12）

- 2026-09-12：Task15 整体只读审查返回 1 Important、2 Minor。阻断项是实际 Playwright 仅凭 exit0 可误收动态 skip；已用离线编排 RED 复现，补 metadata-only 执行 reporter，精确校验六个真实 file/title 身份各执行一次并通过，拒绝 skip/retry/flaky/重复/缺失/全局错误，输出采集上限16KiB、保持原取消和资源回收边界。CI Web job 接入编排、架构 guard 与 contracts 三文件 Node 回归，主控固定 Node24.15/Go1.25 离线68项通过；原审查者增量复审 Spec PASS / Quality APPROVED，关闭上述Important及CI Minor，无新发现。未改 Go/前台Runtime；Trace在后台终态落库与ProcessWithTrace返回之间短暂missing而非pending的非阻断Minor明确保留，可推进本地提交。第六次真实6/6结果仍有效，但不冒充新reporter完整集成证据；旧34项快照仅预期三项脚本/CI变化，31项未变。CI/Docker未跑；feature分支需推送并建PR才触发当前workflow，外部写入待授权。无第七次ConfigHub/真实模型/生产开启，Task15及Phase2A未宣告完成。

- 2026-09-11：用户明确批准并执行第六次 ConfigHub 完整验收一次，exit0；固定 Node24.15.0/Go1.25.0，Host 实际 PG16.14。Service专项6.32s、外部认证HTTP专项1.19s、浏览器6/6通过（1.1m）。新增断言实际验证前台/后台落库均为 failed/timeout，关闭第五次超时错误码一致性的验收缺口；四个 Provider/Calendar×前后台取消子场景保留并通过1秒依赖退出要求，完整日志六次取消POST均200。两个严格串行临时库 dayorder_agent_it_f32765294edf45458876ca65fd6f85d2 / dayorder_agent_it_83b0da9418904f72ba92191504440da1 均有原始创建和身份核验回收记录；第一库回收后才创建第二库，无额外数据库查询/手动恢复/自动重跑。六段原始结果完整落盘，34项源码/配置hash未变，第四次历史产物和第五次结果保留副本hash未变，当前浏览器结果passed/零失败，验收进程已退出。此次仅执行门禁并更新记录，不改业务源码、不提交/push/merge；Task15完整审查和CI门禁仍待完成，Task16未启动、生产关闭。旧第四次取消500未复现，不宣称其根因或修复；本次授权已用完，无第七次或真实模型调用授权。下方2026-09-10及以前条目是历史检查点，不代表当前状态。

- 2026-09-10：用户批准第五次超时终态差异的有界离线修复。真实前台 Host/Driver 配受控传输与存储边界的确定性 RED 证明：本地 deadline 先于服务端 deadline 时，Provider transport 先断开，使 Run 成为 internal_error，而实际 Trace 是 timeout。最小修改仅在 foreground Host：Runtime 立即停止，Provider transport 等待单次最多 5 秒的 GET/finish 收尾，在 finally 释放；普通断流、Tool timeout 与显式取消语义不变，服务端终态优先。新增浏览器断言要求前台持久化 timeout 且与后台错误码一致；未重跑真实门禁。主控独立 19 文件 / 305 项 Agent+harness 单测、Web 与严格浏览器测试 TypeScript、架构检查及冻结/历史产物核验通过；独立增量审查 spec PASS / quality APPROVED，无 Critical/Important/Minor。此证据不等于复现第五次精确 DB/SSE 时序，不关闭旧取消 500，不代替 Task15 整体审查，也不完成 Task15/Phase2A。没有第六次授权，Task16 未启动，未提交。

- 2026-09-10第五次完整ConfigHub门禁已按授权执行一次并exit0：Service专项5.85s、外部HTTP专项1.55s、浏览器6/6通过（1.0m）。实际19个合成Run（Service direct1+HTTP1；Host external1+browser16），四个Provider/Calendar×前台/后台取消子场景均通过原1秒依赖退出断言；取消接口6次均200，第四次500本次未复现，不能据此宣布其根因或修复。两个严格串行临时库 `dayorder_agent_it_79ef82563f434ad0a04f73eed6b6acfe`、`dayorder_agent_it_32759f2de22e4916bc095d27eeb05e2d` 均有原始创建及guarded-cleanup证据；Host PG16.14，无Docker/真实模型/额外维护查询/自动重试。三段原始输出完整落盘，独立解析确认无截断；第四次失败产物已保全，当前新结果仅passed的.last-run.json。
  **未关闭的验收缺口：**主控日志审计发现run_timeout前台Run持久化后的终态观测为`failed/internal_error`，后台为`failed/timeout`。现有测试只验证前台`failed`及Runtime Trace一致，没有核验前台持久化错误码，故“命令全绿”不代表终态错误一致性已通过。原实现者仅继续只读链路分析和报告更新，本轮不改代码/测试、不提交、不运行第六次。另记录通用认证日志在取消轮询时输出两条raw context-canceled错误，未见凭据/Prompt泄漏，不与第四次取消500混同。Task15仍未完成/正式整体审查，Task16未启动。

- 2026-09-10：已完成获批的有界离线取消诊断，不连接任何数据库、不运行第五次live。仅在既有 `AgentReadonlyService.Cancel` 使用 Logger 输出固定错误类别/context状态、最后观测阶段与 Run/Request UUID；不推断 SQL/RLS/锁/commit 的具体失败原因。真实 Service/Command/Idempotency 单元路径使用既有内存存储/审计边界替身，TDD 验证错误传播、失败不误报终态和敏感信息不泄漏。独立补充审查发现缺省 RequestID 与 Command 自动生成 ID 不一致，已修正为 Cancel 一次补齐、命令和日志共用，显式 ID 不变；新增真实 Command→审计边界关联 RED/GREEN。修正后主控独立7个服务端顶层测试/7个子项、同selector Go1.25 race、vet/架构/diff/gofmt检查通过；增量复审spec/quality通过，无遗留阻断项。不扩公共接口/Schema/HTTP响应/fault/预算，保留1秒依赖退出和持久化优先。两份第四次失败产物保留且hash未变。这仅证明诊断就绪，不证明真实500根因或修复；Task15仍未整体通过/提交/完整独立审查，Task16未启动，后续真实采样需另行授权。

- 2026-09-09第四次真实门禁已获批并执行一次，整体exit1：同库Service专项8.00s、外部HTTP专项1.56s通过；浏览器实际6项执行，5通过/1失败（55.5s）。新增真实通过证据包括Tool/Run超时双端对照、Worker剩余故障矩阵及429真实重试/用量/落库、基线/页面关闭后后台继续、分页/64KiB、安全边界。仅取消用例首个run_timeout/foreground子场景失败：cancel接口500，一秒内依赖仍活动，随后Run落为timeout；其余三个取消子场景未执行，不算通过。实际16个合成Run（Service B2、Host A7/B7），两个严格串行库均保留完整原始创建与核验回收记录：`dayorder_agent_it_e69159765ca34e7ab1574f16a5c02b36`、`dayorder_agent_it_c72e1942723e41959730a84a4ce8e34e`。Host PG16.14，六段输出完整保存，无回收错误或额外维护查询。第四次额度已消耗，无第五次授权；当前仅离线根因分析，Task15仍未整体通过/提交/独立审查，Task16未启动。

- 2026-09-08第三次门禁后的离线修复已冻结：仅测试端用Node文件读取同一generated Schema并复用产品校验器，修复Playwright原生Node加载失败；编排在任何Docker/临时目录/数据库资源前先发现并核对精确6项Fake测试。主控独立验证Node24.15编排21项通过、真实JSON测试发现成功且被实际编排检查器接受；冻结后浏览器专项严格类型、整体架构及diff检查通过。测试发现不执行浏览器，不把list模式skipped=6当作运行结果。验收文档已同步第三次失败及回收证据；第四次真实执行未授权，本轮没有新建库/Run/模型调用。Task15仍未提交、未独立审查、未整体通过，Task16未启动。以下保留较早时间点的历史状态，不代表当前授权。

- 2026-09-08第三次真实门禁已执行、exit1：同库Service/HTTP两页50实体及完整引用对照通过（8.74s），外部HTTP专项通过（3.26s）；Playwright加载测试时因JSON Schema导入缺少type:json而退出，浏览器执行0项，不能算整体通过。本次实际3个Run、2个串行临时库，均保留原始创建及身份核验回收记录：`dayorder_agent_it_7618f064fc6c4dae8590a6028437bd60`、`dayorder_agent_it_fd4e400bd15c49469e5158128bfa01e3`。Host实际PG16.14，完整工具输出已保存无截断；无回收错误、无手工恢复/额外查询。本次额度已使用，仅继续离线修复测试加载及建库前发现门禁；第四次live未授权，Task15未完成、Task16未启动。

- 2026-09-08：用户已批准在剩余离线检查通过后再执行一次 ConfigHub 完整门禁，最多2个严格串行专用临时库、19个合成Run，失败不自动重试，不调用真实模型/不使用Docker/不改既有库。启动前发现新增同库HTTP调用未继承测试库90秒/停止context，正补test-only取消回归与helper修复；本次额度尚未使用。主控已重新通过33项前台/harness回归和浏览器专项严格类型检查，Task15仍未完成，Task16未启动。

- Task15 预算回归已由主控独立离线验证：429首次未知预留7749保留，重试成功已知15，下一轮需8314而剩8236，因此失败；真实Driver的最终分类是provider_unavailable，Gateway预算准入根因为validation_failed。不提高16000预算、不释放未知用量、不缩短Fake流程。错误归因精度留作明确Minor复盘，真实重试/落库矩阵仍待验证。另保留一次合成回归输出完整request/Trace的纪律偏差：已删除完整日志/失败dump并独立复跑受控输出，但不冒充历史输出已脱敏。无真实账号/凭据/数据库数据进入该离线用例；第三次live未执行，Task15尚未完成。

- Task15 后续离线风险修复：前台显式取消现在先启动服务端取消收尾，Runtime 立即停止，但 Provider/Calendar transport 与 iterator.return 等待该收尾后释放，避免先断流导致 internal_error 抢占终态；取消 CAS 最多两次且共享原5秒时限，保留权威终态，异常/提前完成时释放监听器并中止尚未结束的收尾。后台 harness 取消先核对 Worker 更新后的版本。主控独立验证前台/后台33项、编排/架构32项、浏览器专项严格类型与整体架构检查全部通过；HTTP错误信封的类型问题已修复，生产构造器guard覆盖真实Router和新旧Worker/后台Host符号。私有Fixture回收日志成功/失败两分支亦通过离线测试。这些是未提交代码的离线证据，不等于真实1秒退出或全矩阵通过；第三次live尚未执行，Worker故障原因、分页完整同源对照、CI/最终审查仍待完成。

- Task15 真实门禁仍未通过：第一次在 Windows 短名/长名临时路径校验处失败，创建库/Run 均为0；修复后主控19项脚本回归通过。第二次两个精确 Go 专项通过，浏览器2/6通过、4项失败，整体 exit1，继续离线定位。第二轮实际创建两个串行专用库，其中 Service 库 `dayorder_agent_it_9836352eef8f4696bf1c961773dc9912` 保留原始核验回收行；Host 当时没有成功生命周期日志，精确库名未知，不能把未报告 cleanup error 等同原始回收证据。输出另有截断，实际 Run 数未知，不用计划18替代。不得按名称/前缀追补恢复；第一轮可能遗留的本地临时目录因原始身份未保存也未认领/删除。新增私有生命周期记录及离线回归后，第三次真实执行仍须另行窄门禁裁定。Task15 未提交/独立审查，Task16 未启动。

- Task15 首次真实门禁前的历史检查点：已补后台真实 Trace 返回与有界私有快照、固定分页/结果超限 Seed、精确合成会话过期和实际依赖活动诊断。编排安全检查发现并修正取值前未排除旧版 Provider 配置、启动输出无界、退出/临时目录身份/清理失败报告不足；主控用纯合成 getter 复现旧版密钥读取后，独立复验 15 项 Node24.15 离线回归通过。浏览器测试源码被纳入启动前严格类型检查；已锁定的 Playwright1.63 自动失败页面快照另行关闭，防止 DOM 内完整 Trace 落盘。该历史检查点仅代表当时的离线检查；后续两次真实门禁及当前缺项见上一条，不能据此宣称自动化闭环已通过。Task16 真实厂商尚未执行，生产未开放。

- Task 14 已实现并通过一轮独立复审：原实现 `280d1be`、修复 `3838157`。真实 Session/后台正常闭环、活动 HTTP/Worker 关闭及 Tool 超时跨账号隔离专项已通过；A 的 Tool 超时回模型并以“未能完成查询”完成，B 的真实前台读取约 0.55 秒且仅含 B 数据。复审关闭每次初始化重试/在途轮询无界等待、Outbox 故障丢失、创建事务回滚消耗故障四项 Important；故障预留跟随真实角色事务，未知提交隔离原账号/Run，不猜测恢复。修复后主控完整 API/vet/架构、9 项 harness 行为和直接源码类型检查通过，实现者 Go1.25 定向 race、构建通过；fix1 无新真实库。Task14 四次具名临时库创建中，两次保留原始身份核验回收日志，另两次缺原始回收行，主控随后分别通过 TLS/只读维护连接按精确库名确认不存在，不冒充四条原始回收记录。其中一次诊断权限失败、一次异步 session ID 丢失导致尾部/退出码未知；未知运行不计通过。早期 harness 红/绿全文无法恢复，新增 F1/F2 行为红/绿与 F3/F4 缺接口编译红/行为绿分别如实保留。fix1 曾误生成根目录测试二进制，已按精确路径删除并独立确认不存在；专用临时构建产物亦已清理。此前 Web 404 项通过属原实现证据，不冒充 fix1 全套重跑。Task15 浏览器/全故障矩阵与 Task16 真实模型尚未执行，生产未开放。

- Task 1 已实现并通过独立审查：Protocol 2.0 / Runtime 2.0.0、完整 Turn 与 Usage、11 个 Golden 场景；提交 `b891260`。
- Task 2 已实现并通过独立审查：剩余绝对 deadline、明确取消来源、保留协作取消与快照回归；提交 `731f37d`、`eb93826`。
- Task 3 已实现并通过独立复审：内置只读 Skill、共享 ToolSpec、六个 HTTP DTO、双端资产与严格校验；提交 `2137814`，复审修复 `297bb13` 统一 Node/Web 校验配置。
- Task 4A 已实现并通过独立复审：ConfigHub 专用临时库、私有身份核验回收及异常恢复诊断；提交 `245039f`、`1bf4eb5`。主控真实生命周期复验通过，临时库已回收且独立连接确认不存在；实际 PostgreSQL 为 16.14，尚无本地 PG17 验证。
- Task 4 已实现并通过独立复审：migration 000009、执行/operation 存储、双角色 RLS/权限、执行权 token 轮换与版本 CAS、000008 数据升级；提交 `f123339`、修复 `31cc1f6`。主控用 Go1.25.0 在真实 PG16.14 上复跑 8 项专项及 race，全部通过（24.765s）；8 个本次临时库均记录回收。完整 Windows API 测试、SQL 生成漂移与静态检查通过。
- Task 5 已实现并通过独立复审：只读 Run 生命周期、可信 Actor、幂等与账户限额、超时/终态优先、Sync/Audit/Outbox 同事务；提交 `ba50ce5`、修复 `6a0fc5c`。补齐锁等待后重新判定 deadline、完成时保留服务端权威用量和有效预算测试。主控在修复后用 Go1.25.0 对真实 PG16.14 复跑三项数据库专项及 race，全部通过（14.836s），三个临时库均记录回收；完整 Windows API 回归与静态检查通过。本任务实现者 25 个、主控 5 个具名临时库均报告回收。
- Task 6 已实现并通过独立复审：operation 去重、原子预算预留、固定请求重试和可信结算；提交 `db429c0`、修复 `6ab301f`。补齐重试与其他 Provider 的并发互斥、零预留时保留旧尝试的不完整用量；共享 canonicaljson 提取不改变 Runtime 规则。主控最终真实 PG16.14/Go1.25.0 Readonly race 六项数据库测试通过（41.655s），完整 API、共享规范化及 Runtime race 通过。实现者 39 个、主控 12 个具名临时库均报告回收。一次早期数据库/context 超时后单独、矩阵及完整重跑均通过，根因未证实，作为验收异常保留；不归因为已证明的外部故障。
- Task 7 已实现并通过独立复审：受 Run Scope 约束的日程应用服务、真实 API/Worker 角色读取、最小结果投影/分页/引用及 Service Binding；提交 `d44077b`、修复 `fe04fd6`。补齐取消发生于结算前/中时的五秒元数据收尾，保留原始取消/deadline 与脱敏结算错误，发布成功前仍复核当前权限和 context。主控最终完整 API/vet 与真实 PG16.14/Go1.25.0 联合 race 通过（九项数据库测试，service 53.837s、Binding 1.059s）；主控 18 个、实现者 12 个具名临时库均正常经身份校验回收。另有下述一次手动恢复偏差，不能并入正常回收证据。Task 9–16 尚未完成。
- Task 8 已实现并通过独立复审：DeepSeek 文本/单 Tool 流式 Adapter、严格历史关联、原始 Schema 参数校验及输入依赖 Fake；提交 `7b007da`、修复 `2d2eac9`。修复畸形 Usage 零值误认、读流错误分类、拼接参数深度上限及 Retry-After 溢出。新增仅服务端的 KnownUsage 错误元数据和共享 ValidateHistory，不改协议/HTTP DTO。主控最终完整 API/vet/diff 与 Go1.25.0 Adapter race 通过（1.367s），架构检查通过；仅合成 SSE/本地 HTTP，没有数据库或真实模型调用。保留一项 idle 测试调度 Minor。Task 9–16 尚未完成。
- Task 9 已实现并通过两轮独立复审：服务端 Gateway 授权、冻结请求、保守预留、全 Turn 单槽及有界重试；提交 `f5325ce`、修复 `c2ab8dc` / `351b032`。关闭取消抢先零结算丢用量、收尾耗尽导致终态未保存、结算中途取消原因被旧错误掩盖三项风险。主控最终完整 API/vet/diff/架构检查与真实 PG16.14/Go1.25.0 Gateway race 通过（56.406s）；本任务主控 11 个、实现者 33 个具名临时库均正常核验身份回收，无手动恢复。补齐完全不消费 Events、已结算重试间隙及同一原始请求结算后重提的覆盖。Task 10–16 尚未完成，未调用真实模型或开放生产。
- Task 10 已实现并通过独立复审：开发/测试专用 Router、六类认证 HTTP/SSE 路径、设备归属/Origin/JSON 边界、写超时与受控错误；提交 `f6b4713`、修复 `f6e9966`。补齐 FlushError 委托、日程请求最早 deadline、Gateway 已结束后的 HTTP 断流收敛（含未知用量后重试成功）及累计 SSE 上限的终止帧预留。主控最终完整 API/vet/diff/架构检查与真实 PG16.14/Go1.25.0 HTTP race 通过（8.546s）；本任务实现者 10 个、主控 2 个具名临时库均正常核验身份回收，无手动恢复。两次启动命令多写 `/u/` 导致变量未传入 WSL，均在库名生成前失败；已确认是命令抄写问题而非 ConfigHub 缺配置。真实 Session/浏览器/双宿主整体及远程模型验收仍在 Task 14–16，Task 11–16 尚未完成，生产未开放。
- Task 11 已实现并通过两轮独立复审：严格 HTTP Client、标准 fetch/SSE、HTTP 日程 Binding 和真实 foreground Runtime Host；提交 `65dd053`、修复 `ee32c55` / `1e88652`。关闭最终 GET/finish 期间取消漏处理、SSE framing 误占 data 额度、UUID 常量回退，以及修复中引入的未结束多行帧缓冲无上限和 UUID 逻辑重复。主控最终完整 Web 42 文件/397 项测试、typecheck、架构检查、Web/API 构建和 diff 检查通过；保留既有 Vite 大分块提示。未调用数据库、浏览器或真实模型。Task 12–16 尚未完成，生产保持关闭。
- Task 12 已实现并通过首次独立审查（spec PASS、quality APPROVED，零轮修复）：后台 Host、独立 Outbox Handler、可选 batchSize、取消源传播/监测器退出、有界原结果收尾与重投不重跑；产品提交 `8820d71`，工作报告跟踪修正 `8c280c1`。修正真实接线发现的三项完整冻结 Tool grant 和 v2 完成摘要投影。主控完整 API/vet/架构/diff 与真实 Go1.25/PG16.14 专项 race 通过（worker 1.011s、agenthost 20.301s、service 70.446s），14 个本次专用库创建/原始身份核验回收记录一一对应；实现者另报告 36 个唯一库均核验回收，原始输出缺口见下文。审查尚未证明“一秒内 Provider/Tool 实际退出”，明确由 Task 13 的取消时延测量和后续集成验收补齐，不将十秒 whole-Process 测试当作该证据。Task 13–16 尚未完成，生产未开放。
- Task 13 已实现并通过一轮独立复审：受控 Observer/私有 Prometheus 指标、Run/Provider/Tool/后台槽生命周期日志和真实取消时延；提交 `e481647`、修复 `ae6fd6c`、测试计时修正 `37cc37a`。关闭已知但不完整 token 漏计、Tool 结算失败缺观测、前台 Run 缺 Skill digest 三项问题，保留原始错误和收尾预算。主控最终完整 API/vet/架构/diff 与 Go1.25 观测/Service 单元 race 通过（1.032s/1.098s）。实现者真实 Go1.25/PG16.14 四项专项 race 通过（Gateway 5.945s、Host 14.743s），11 个唯一临时库创建/原始身份核验回收记录一一对应；该专项从 Runs.Cancel 入口验证协作 Provider/Calendar 实际退出 ≤1 秒，关闭 Task 12 的该验收缺口。生产指标则明确只测“已存取消时间→依赖退出”，缺失样本不伪造零。主控未重跑 Task 13 数据库门禁；修复轮无新库。另保留主控发现的测试起点错误：五秒收尾 context 创建晚于依赖退出，747ns 差值导致 Go1.25 race 误报；已改按结算入口测剩余预算并独立验证顺序，不改产品策略。Task 14–16 和整体/浏览器/真实厂商验收尚未完成，生产未开放。
- Task 7 历史安全/审计偏差：取消测试因 Migrator 单连接池被持锁事务占满，又从同池查询而死锁。中断后曾手动删除 `dayorder_agent_it_e638f0a9ecce4d4b97cb36325b61011b`，只验证精确名称/模式和当前 owner，未对比已丢失的创建时 OID/owner/身份标记；删除后报告 `remaining=0`。此恢复弱于约定的 Fixture.Close，已告知用户，不能声称完成了原始身份校验；不做追溯性数据库操作来补造证据。测试已改为独立 API 事务观察真实锁等待并保证有界释放。后续中断若缺少原始身份记录必须先报告，不得自行退化为名称/当前 owner 校验。
- 历史验收证据缺口：Task 4 早期一次 seed 失败时未输出临时库名，自动回收未报错，但无法恢复精确名称。此后 helper 在初始化前注册清理并输出回收名；实现/修复阶段 35 个已记录临时库及主控 8 个临时库均报告删除，不据此伪造早期缺失记录。
- Task 12 执行命令偏差：实现者一次在 ConfigHub 注入环境运行 worker/agenthost/service race 时漏掉约定的 `-run 'Readonly|Background|Batch|AgentCalendarRead'`。当时有临时库运行，未强杀，正常结束 exit 0，报告 14 个本次专用库经原始身份核验回收。主控只读核查该范围的数据库测试入口均为本期 `agenttest.Open`，未发现既有数据库、共享角色或 bootstrap 操作；这缩小了已知影响，但不把漏筛选命令改称合规。之后实现者及主控分别执行了正确筛选的串行专项 race，不能用后续通过抹去原偏差。
- Task 12 工作报告跟踪偏差：实现者用 `git add -f` 把被忽略的 `task-12-report.md` 放入 `8820d71`；主控也发现 Task 10 报告在早前提交中已被跟踪。`8c280c1` 仅对这两个本期 Agent 工作报告取消索引跟踪，本地文件保留，`git ls-files .superpowers` 已为空；未改产品代码或重写历史。后续禁止 force-add 本地工作报告。
- Task 12 历史输出缺口：实现者旧 session 15796/52231 的完整 verbose stdout、部分 focused 红绿命令原始 transcript 及逐库原始行未保存。报告保留已知命令、结果、36 个唯一库名及报告的核验回收结果，明确区分 checkpoint 摘录和原始输出。主控后续 session 33697 的完整专项输出含 14 个创建/核验回收一一匹配记录，已另行保存；它是新的验证证据，不冒充恢复的旧日志。两处首轮失败是测试预期/测时问题（停机与重投文案不同、调用前测时误计），已与真实产品缺陷的 RED 分开记录。
- 验证工具链调整：仓库原有 jsdom `30.0.1` 要求 Node `^22.22.2 || ^24.15.0 || >=26.0.0`，原计划/现有 CI 的 24.7.0 和本机 25.2.1 均不在支持范围。已使用临时 Node 24.15.0 验证；Task 15 将 CI 相关 Node 版本统一到 24.15.0，此处尚未修改 CI。
- Go Windows 本机为 1.26.2，目标仍为 1.25；Windows 无 C 编译器。2026-09-07 发现 WSL 既有 Go 1.25.0/gcc，完整 agentruntime 与 testdb 包的离线 race 测试已通过；其他包的 race 验证仍按各任务记录。上述进度不代表数据库、跨宿主或真实厂商验收完成。
- `297bb13` 上主控复验：19 项 Node 检查、231 项 Web Agent 测试、完整 Web 37 个文件/352 项测试、7 个 Go Foundation 包、typecheck、生成漂移检查、Web build 均通过；构建保留既有的大分块提示。实际架构扫描与生产禁用路由的六个子测试也通过。
- 最终审查待复核的低优先级项：五个新增 Golden 文件排版、Go 20ms deadline 测试的调度抖动风险、`maxUtf8Bytes` 的 Go `json.Number` 配置暂不接受等值 `4.0`/`4e0` 写法（内置整数 `4096` 不受影响）、Provider 去重测试需补同一原始 payload（含结算后重提）以独立证明同内容去重。Adapter idle 测试需等待实际 body.Close 后断言以消除调度相关失败。Task 6 的一次超时保留为根因未证实的验收异常。不据此扩大本轮实现范围。

## Task 1: Protocol 2.0 与完整模型轮次

**Files:**

- Create: `contracts/agent/v2/protocol.schema.json`
- Modify: `scripts/generate-agent-contracts.mjs`、`scripts/agent-contracts.test.mjs`
- Modify: `apps/api/internal/agentprotocol/validate.go`、`apps/web/src/agent/protocol/validate.ts`
- Modify: `apps/api/internal/agentruntime/reducer.go`、`apps/web/src/agent/runtime/reducer.ts`
- Modify: `contracts/agent/conformance/*.json`、双端 conformance/reducer 测试、Provider Mock、Skill/worker/profile/driver 测试内的版本与事件序列
- Generate: `apps/api/internal/agentprotocol/{generated_types.go,protocol.schema.json}`、`apps/web/src/agent/generated/`

**Interfaces:** 保留现有 `advance(state,input)` / `Advance(state,input)`；v2 用已有 `pendingToolCall` 暂存模型调用，只有 completed(tool_use) 才产出 resolve_tool。新增共享定义 `ProviderEnvelope{protocolVersion,runId,turnId,sequence,event}`；sequence 为本轮从 1 开始的正整数。DTO `ProviderEvent` 保留四种 type。

- [x] **1. 写未完成轮次不能执行 Tool 的失败测试。** 在 TS reducer 测试中使用以下输入，并在 Go 添加相同断言：

```ts
const initial = createRuntimeState({
  runId: "r-v2", executionMode: "foreground",
  capabilitySnapshot: { runtimeVersion: "2.0.0", executionMode: "foreground",
    toolIds: ["dayorder.calendar.read"], skills: [], scope: { domains: ["calendar"] } },
  budget: { maxSteps: 8, maxTokens: 16000, maxDurationMs: 120000,
    maxWorkers: 1, maxConcurrency: 1, maxRepeatedToolCalls: 2 },
});
const started = advance(initial, { type: "user_message", text: "查询日程" });
const called = advance(started.state, { type: "provider_event", providerEvent: {
  type: "tool_call", call: { id: "c1", name: "dayorder.calendar.read", input: {} },
} });
expect(called.effects).toEqual([]);
expect(called.state.phase).toBe("model_streaming");
const ended = advance(called.state, { type: "provider_event", providerEvent: {
  type: "completed", stopReason: "tool_use",
  usage: { inputTokens: 10, outputTokens: 5, totalTokens: 15 },
} });
expect(ended.state.usage.totalTokens).toBe(15);
expect(ended.effects.map(e => e.type)).toEqual(["resolve_tool"]);
```

- [x] **2. 运行红测试。** `npm run test --workspace @dayorder/web -- src/agent/runtime/reducer.test.ts`。预期当前 1.0 校验拒绝 2.0；升级 DTO 后仍会因立即 resolve_tool 而失败，不能将只缺版本的失败当作行为覆盖。
- [x] **3. 新建 v2 Schema 并切换生成器。** 将协议常量改为 2.0、runtimeVersion 改为 2.0.0、`$id` 指向 v2，保留 Skill SemVer 泛型。加入 Envelope 的严格 Schema：

```json
{"type":"object","additionalProperties":false,
 "required":["protocolVersion","runId","turnId","sequence","event"],
 "properties":{"protocolVersion":{"const":"2.0"},"runId":{"type":"string","minLength":1},
 "turnId":{"type":"string","minLength":1},"sequence":{"type":"integer","minimum":1},
 "event":{"$ref":"#/$defs/ProviderEvent"}}}
```

运行 `npm run agent:generate`，更新校验器的 Schema ID/definition 列表。生成器不删除 v1 源。测试仍拒绝旧版本和未知字段。
- [x] **4. 修正两个 reducer。** 把旧 `onToolCall` 的历史提交、重复调用计数和 resolve 效果移动到完成函数；接收调用仅快照暂存：

```ts
if (state.pendingToolCall) return protocolFailure(state, "multiple tool calls in one turn");
const next = nextState(state);
next.phase = "model_streaming";
next.pendingToolCall = structuredClone(event.call!);
return finish(next, []);
```

completed 分支先累加 Usage/检查 maxTokens；tool_use 要求 pending 存在，再将 assistantDraft 与调用合并写历史并进入 tool_pending；end_turn 要求无 pending。错误、max_tokens、取消清理暂存，不发执行效果。一次 Input 只能使 sequence 增加一次，不能嵌套调用会二次增序的旧 helper。
- [x] **5. 迁移 Golden Fixtures 与全部组件输入。** 保留六个原始场景，新增 `tool-turn-usage.json`、`tool-call-incomplete.json`、`multiple-tool-calls.json`、`tool-turn-budget-exhausted.json`、`orphan-tool-use.json`。Mock 的每次调用后添加 completed(tool_use)；更新硬编码 Fixture 清单。修正脚本中“每次 transition 恰有一个 effect”“usage 始终不变”的旧断言：Tool 暂存允许空 effect，Usage 仅在 completed 累加。期望值来自独立规则和人工核对，不能调用待测 reducer 自动生成答案。
- [x] **6. 验证并提交。** 运行 `npm run test:agent-foundation`、`npm run typecheck`、`npm run agent:generate:check`；确认 getter/Go 同步 Binding/guard 风险测试仍存在。提交消息 `feat(agent): version complete model turns as protocol v2`。

## Task 2: Driver 剩余 deadline 与可区分的取消原因

**Files:**

- Create: `apps/web/src/agent/runtime/stop.ts`、`stop.test.ts`、`apps/api/internal/agentruntime/stop.go`、`stop_test.go`
- Modify: 双端 Driver、Driver 测试

**Interfaces:** Go `DriverConfig` 新增 `Deadline time.Time`（零值维持旧相对计时）；TS `RuntimeHost` 新增 `deadlineAtMs?: number`。TS `RuntimeStopReason={agentStop:"user"|"timeout"|"interrupted"}`；Go `StopCause{Kind string}` 实现 error，Kind 同值。`stopInput(reason)` / `StopInput(cause error)` 产生 RuntimeInput；普通无原因 caller 取消仍视为 user，内部 Run deadline 为 timeout。显式结构化原因优先；原生 DeadlineExceeded（含包装）/DOMException TimeoutError 为 timeout，context.Canceled/DOMException AbortError 为 user，未知显式原因/kind 为 interrupted，不透传错误文本；完整映射见 Spec §10 实施补充。

- [x] **1. 写分类失败测试。**

```ts
it.each([
  ["user", "cancel", undefined],
  ["timeout", "runtime_error", "timeout"],
  ["interrupted", "runtime_error", "internal_error"],
] as const)("maps %s", (kind, type, code) => {
  const input = stopInput({ agentStop: kind });
  expect(input.type).toBe(type);
  expect(input.error?.code).toBe(code);
});
```

Go 使用 table test 调用 `StopInput(StopCause{Kind: kind})` 验证同一三行。另用现有可取消 Binding fixture + fake timers/开始信号验证：已过绝对 deadline 时不调用 Provider；剩余 20ms 不被预算 120s 覆盖；Tool 先开始再验证 Run deadline 优先。
- [x] **2. 运行红测试。** `npm run test --workspace @dayorder/web -- src/agent/runtime`、`go test ./apps/api/internal/agentruntime -run 'Stop|Deadline' -count=1`，应在新 helper/绝对 deadline 分类处失败。
- [x] **3. 实现分类函数和实际计时上限。**

```ts
const remaining = host.deadlineAtMs === undefined ? state.budget.maxDurationMs
  : Math.max(0, host.deadlineAtMs - Date.now());
const duration = Math.min(state.budget.maxDurationMs, remaining);
```

Go 用 `min(now+budget, config.Deadline)` 构造内部 deadline context。已耗尽先 feed runtime_error/timeout，不启动操作。caller cause 分类先于默认 cancelled；保留已有明确用户取消优先级。两个 Driver 的 budget 快照不变。
- [x] **4. 验证取消链路与旧风险回归。** Go 对非协作代码不再添加 Tool goroutine；TS 不绕过 structuredClone 快照。Provider iterator return/body close 只清理资源，不替换终止原因。运行 `npm run test:agent-foundation`；Go deadline 用例 `-count=25`；能使用 C 编译器的环境再运行 `go test -race ./apps/api/internal/agentruntime`。
- [x] **5. 提交。** `feat(agent): honor host deadlines and cancellation causes`。

## Task 3: 产品 Skill、日程 ToolSpec 与 HTTP DTO

**Files:**

- Create: `skills/calendar-overview/SKILL.md`、`contracts/agent/tools/calendar-read.json`
- Create: `apps/api/internal/agentassets/assets.go`、`assets_test.go`、`apps/web/src/agent/assets/builtin.ts`、`builtin.test.ts`
- Create (generated): `apps/api/internal/agentassets/generated/`、`apps/web/src/agent/generated/builtin/`
- Modify: v2 Schema、生成器、协议 definition 列表、`scripts/agent-contracts.test.mjs`、package.json 的生成漂移检查路径

**Interfaces:** Go `agentassets.CalendarReadSpec() (agentprotocol.ToolSpec,error)`、`CalendarOverviewBundle() agentskill.SkillBundleInput`；TS `calendarReadSpec(): ToolSpec`、`calendarOverviewBundle(): SkillBundleInput`。资产只暴露副本。新增以下 Schema definitions 并生成 DTO：

```ts
type CalendarReadInput = { start: string; end: string; cursor?: string; limit?: number };
type CalendarReadData = { events: Array<{ id: string; title: string; startAt: string;
  endAt: string; timezone: string; kind: string; version: number }>;
  window: { start: string; end: string }; hasMore: boolean; nextCursor: string | null };
type ReadonlyRunStart = { intent: string; executionMode: "foreground" | "background";
  scope: AgentScope; timezone: string; modelProfile: string };
type ReadonlyRunView = { protocolVersion: "2.0"; runId: string; executionMode: "foreground" | "background";
  status: "ready" | "analyzing" | "completed" | "failed" | "stopped"; version: number;
  capabilitySnapshot: CapabilitySnapshot; budget: Budget; modelProfile: string;
  serverNow: string; deadlineAt: string; usage: Usage; usageComplete: boolean;
  resultOrigin: "client_reported" | "server_runtime"; summary?: string; error?: AgentError };
type ReadonlyRunFinish = { phase: "completed" | "failed" | "cancelled"; summary: string;
  error?: AgentError; steps: Array<{ title: string; detail: string }> };
type CalendarReadRequest = { callId: string; input: CalendarReadInput };
```

以生成器的 Go 命名为准核对 ID 字段（例如 RunID），不要另造手写 wire DTO。finish steps 最多 16，title 240 字符、detail 2000、summary 8000、intent 2000；阶段与 error 组合另作语义校验，不信任客户端 Usage。

实施细化：TS `createSchemaValidator()` 与 Go `NewSchemaCompiler() (*jsonschema.Compiler,error)` 共用于协议及实际 Driver 的动态 Tool 校验；Node 契约脚本复用 Web 配置。标准 uuid/date-time 与自有 `maxUtf8Bytes` 都必须实际断言。测试包括真实 Skill 解析、输入/输出边界、复制隔离和实际 Driver 调用次数，不只断言常量。Go 的生成 `DateTime` 是具名 string 类型，使用已有 string 变量时需显式转换；finish steps 在 TS 生成为数组，16 项上限由 Schema 校验。厂商投影要求交接至 Task 8，详见 Spec §5.1。

- [x] **1. 写资产和 Schema 失败测试。**

```ts
const spec = calendarReadSpec();
expect(spec.id).toBe("dayorder.calendar.read");
expect(spec.sideEffect).toBe("read");
expect(spec.executionTargets).toEqual(["client", "server"]);
expect(spec.timeoutMs).toBe(10000);
const profile = await parseSkillBundle(calendarOverviewBundle());
expect(profile.manifest["allowed-tools"]).toEqual([spec.id]);
expect(profile.manifest["min-runtime-version"]).toBe("2.0.0");
```

Go 用现有 `agentskill.ParseBundle(SkillBundleInput)` 验证同一内容；脚本按现有 digest 算法比较两份生成资产。
- [x] **2. 运行红测试。** `npm run test --workspace @dayorder/web -- src/agent/assets`、`go test ./apps/api/internal/agentassets`，预期资产入口不存在。
- [x] **3. 写入只读 Skill 与 ToolSpec。**

```yaml
---
name: calendar-overview
description: Read and summarize DayOrder calendar events within the authorized window.
version: 1.0.0
allowed-tools: [dayorder.calendar.read]
execution-target: either
background-allowed: true
user-invocable: true
disable-model-invocation: false
min-runtime-version: 2.0.0
risk-level: low
---
```

正文逐条写入 Spec §4 四条约束，明确分页未结束不能宣称查全。输入 Schema required=start/end、additionalProperties=false、limit 1–50、cursor 长度/UTF-8 字节双重限制；输出只包含 Spec 列明字段，UUID/time/version 校验。跨时间窗关系在 Service 校验，不依赖 JSON Schema 的 format。
- [x] **4. 同步资产与 DTO。** 生成器复用拒绝 symlink 的拷贝逻辑，产品内置与 invalid Fixture 分开；Go 使用 embed，Web 使用构建导入。修改 `agent:generate:check` 覆盖两个 builtin 输出目录。运行生成、双端 assets/skill/protocol 测试和漂移检查。
- [x] **5. 提交。** `feat(agent): add builtin calendar overview contracts`。

## Task 4A: ConfigHub 临时数据库前置接入（2026-09-07 用户批准）

**范围：** 用户批准通过 ConfigHub 读取数据库配置，在同一实例创建验收专用临时数据库，并仅删除本次创建的临时库。既有 `dayorder-test`、生产 `dayorder`、其他数据库和共享角色均不得修改。Docker 保留为默认/CI 后端，不再是本地验收的唯一前置条件。先完成此步骤，再开始原 Task 4。

**Files:** 新增 `apps/api/internal/testdb/isolated.go`、`isolated_test.go`、`confighub.go`、`confighub_test.go`、`confighub_integration_test.go`；仅对 `postgres.go` 的资源句柄增加必要的私有所有权/清理支持。不要改变既有 `Start` / `StartForTest` 的 Docker 行为，不把整套旧数据库测试自动切换到共享实例。

**Interfaces:** 新增 `testdb.StartIsolated(context.Context) (*Postgres,error)`、`StartIsolatedForTest(testing.TB) *Postgres`、`(*Postgres).Close(context.Context) error`、`(*Postgres).DatabaseName() string`。`DAYORDER_AGENT_TEST_DB_SOURCE` 只允许空值/`docker`/`confighub`：空值仍为 Docker；显式来源失败不得回退其他实例；独立 Host 的 Start 返回错误，不使用 testing skip。普通未指定来源的测试包装可沿用 Docker 不可用时 skip，但其结果不计为验收。

ConfigHub 模式仅从注入的七个 `db_*` 字段构造 TLS 连接，复用现有 `config.LoadConfigHubDatabaseSource`；不读取/接受 `DATABASE_URL`、`WORKER_DATABASE_URL`、`MIGRATION_DATABASE_URL` 或用户指定库名，不修改生产配置解析器。原配置名为 `shier/prod`，不代表目标是生产数据库。

- [x] **1. 先写安全性失败测试。** 覆盖来源选择、缺字段/非法值、敏感错误脱敏、临时名称校验和清理身份不匹配时无 DROP；测试必须调用实际边界，不仅比较源代码字符串。
- [x] **2. 实现限域的创建与回收。** 临时名由程序生成，格式固定为 `dayorder_agent_it_` 加 32 个小写十六进制字符（随机 UUID），不能由调用方指定或接管已存在的库。管理员仅连接维护库 `postgres` 做有界只读预检与本次 CREATE/DROP；确认 TLS、服务器版本、CREATEDB 能力和既有三角色为受限角色。不得调用 `dbbootstrap.Run` 或任何 CREATE/ALTER/DROP ROLE、全局 GRANT。
- [x] **3. 配置新库并保留迁移自由度。** 管理员只在新库设置连接/建 Schema 权限，Migrator 创建其拥有的 `dayorder` Schema；URL 仍为既有三角色的独立密码且 sslmode=require。不自动应用 migration，便于原 Task 4 分别测试空库及 000008→000009 升级。预检和创建/回收连接及时关闭，避免占用共享角色连接配额；测试串行，后续 Migrator 池最多 1 个连接。
- [x] **4. 验证清理边界。** 清理依据句柄内不可由公开 URL 字段改写的创建记录，校验临时名称、成功创建事实、数据库 OID 和 owner，再仅删除该库；碰撞不接管、不删除已有库。创建中途失败也要有界回收；错误必须暴露“清理未完成”及可安全报告的临时名，不输出 DSN/密码。Close 可重复调用，失败允许对同一身份重试，不扫描前缀批量删除。必要时仅强制结束该临时库连接；不得影响其他库或共享角色。

异常恢复补充：已收到 CREATE 成功确认但随后身份查询失败时，使用新的有界 context 做身份核实和安全回收；CREATE 回复丢失等结果不确定的情况不能仅凭同名库存在就认领/删除，应明确报告“清理未完成”、安全的精确临时名及结果不确定。清理诊断经过多层脱敏仍须保留状态与临时名，不透传原始驱动错误。

- [x] **5. 真实验证并提交。** 显式 ConfigHub 模式下执行 `TestConfigHubTemporaryDatabaseLifecycle`：三个真实角色连接到相同新库、TLS 与受限属性正确、Migrator 能初始化 Schema、Close 后精确库名不存在。记录实际 PostgreSQL 版本、创建和删除的临时名。先跑不触网的安全测试，再运行这一个真实生命周期测试，不执行旧全量数据库用例。缺配置/权限/连接失败明确失败，不以 skip 完成。提交 `test(agent): support owned confighub temporary databases`。

本机 CLI 位于 `C:/Users/yeshaopeng/AppData/Local/ConfigHub/bin/confighub.exe`，配置文件在原工作区根目录；用 `confighub run --project shier --env prod -- <受控子进程>` 注入后通过 `go -C <工作树> test ... -p=1 -parallel=1 -count=1` 运行。子进程只输出脱敏结果；不得导出配置文件、打印原始 ConfigHub 输出或运行同时初始化正式库的 bootstrap。

## Task 4: 执行记录与 operation 的 PostgreSQL 存储

**Files:**

- Create: `apps/api/migrations/000009_agent_readonly_execution.up.sql`
- Create: `apps/api/internal/agentexecution/types.go`、`store.go`、`apps/api/internal/db/query/agent_execution.sql`
- Create: `apps/api/internal/postgres/agent_execution_repository.go`、`agent_execution_integration_test.go`
- Create: `apps/api/internal/agenttest/database.go`（本任务的真实 DB Fixture helper）
- Modify/Generate: migration 测试、`apps/api/internal/db/gen/`
- Modify: `apps/api/internal/testdb/postgres_test.go` 的总表数/RLS 表数期望（新增两表后为 31/29）；保持该旧 Fixture 为 Docker-only。

新增 integration 测试使用外部 `package postgres_test`，避免后续 `agenttest/services.go` 构造真实仓库时形成测试 import cycle；迁移版本常量与嵌入检查同步升级。

**Interfaces:** `agentexecution.Record{Run model.AgentRun; Execution Execution}`。`Execution` 字段：UserID/RunID/Token（uuid.UUID）、Mode（ExecutionMode）、ProtocolVersion/RuntimeVersion/ModelProfile/Timezone/ResultOrigin（string）、Capabilities（CapabilitySnapshot）、Budget（Budget）、Deadline（time.Time）、KnownUsage（Usage）、ReservedTokens（int）、UsageComplete（bool）。`Operation` 字段：UserID/RunID（UUID）、Kind/ID/State（string）、Hash（[32]byte）、Attempts/ReservedTokens（int）、Usage（Usage）、UsageComplete（bool）、ErrorCode（string）、StartedAt/FinishedAt（time.Time）。

`agentexecution.Store` 定义：

```go
type Store interface {
    LockAccount(context.Context, database.Tx, uuid.UUID) error
    Create(context.Context, database.Tx, Record) error
    Get(context.Context, database.Tx, uuid.UUID, uuid.UUID, bool) (Record, error)
    Active(context.Context, database.Tx, uuid.UUID) ([]Record, error)
    CountCreatedSince(context.Context, database.Tx, uuid.UUID, time.Time) (int, error)
    Save(context.Context, database.Tx, Record, int64, uuid.UUID) error
    Operations(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]Operation, error)
    PutOperation(context.Context, database.Tx, Operation, string) error
    AddRefs(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.AgentSourceRefDraft) error
    AddSteps(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.AgentStepDraft) error
}
```

`Get` 最后一参为 forUpdate；`Save` 最后二参为 expected run version/token，只改控制字段，不改冻结快照。`PutOperation` 最后一参为 expected state，空值表示 INSERT；冲突返回 model.ErrConflict。`postgres.NewAgentExecutionRepository()` 返回此 Store。

接口补充：execution token 属于可变执行权控制，不属于冻结快照。`Save` 从 `Record.Execution.Token` 保存新 token，以独立的 expected token/version 参数核验旧状态；两种角色都必须验证轮换后旧 token 失效且冲突不产生部分修改。operation 的 `StartedAt` 保留首次开始时间，更新只改变允许的控制字段和完成时间。

- [x] **1. 写 migration/RLS 红测试。** helper `agenttest.Open(t testing.TB) *Database` 返回 `{Fixture *testdb.Postgres; API,Worker,Migrator *pgxpool.Pool; UserA,UserB,DeviceA,DeviceB uuid.UUID}`，内部 StartIsolatedForTest/Up/开池/两组合成用户+设备，注册 Cleanup（先关闭池，再 Fixture.Close）。Migrator 池最多 1 个连接。首次断言：

```go
func TestReadonlyExecutionTablesUseRLS(t *testing.T) {
    f := agenttest.Open(t)
    ctx := context.Background()
    for _, name := range []string{"agent_run_executions", "agent_run_operations"} {
        var enabled bool
        err := f.Migrator.QueryRow(ctx,
            `SELECT relrowsecurity FROM pg_class WHERE oid=to_regclass($1)`, "dayorder."+name).Scan(&enabled)
        if err != nil || !enabled { t.Fatalf("%s RLS: %v, %v", name, enabled, err) }
    }
}
```

- [x] **2. 运行红测试。** `go test ./apps/api/internal/postgres -run ReadonlyExecution -p=1 -parallel=1 -count=1 -v`，使用可用 Docker 或显式 ConfigHub 临时库，因缺表失败；缺少所选后端则记录未执行，不用 skip 证明完成。ConfigHub 注入仅限本期专用测试，不对未经审查的旧全量集成用例开放共享实例权限。
- [x] **3. 新建两表。** executions 以 `(user_id,run_id)` 主键/FK 到 agent_runs；operations 以 `(user_id,run_id,kind,operation_id)` 主键/FK 到 executions。JSONB 使用 object 约束；kind 仅 provider_turn/calendar_read，state 仅 running/completed/failed/unknown，hash 长度 32，次数非负。开启 RLS：

```sql
ALTER TABLE dayorder.agent_run_executions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dayorder.agent_run_executions
USING (user_id = dayorder.current_user_id())
WITH CHECK (user_id = dayorder.current_user_id());
ALTER TABLE dayorder.agent_run_operations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dayorder.agent_run_operations
USING (user_id = dayorder.current_user_id())
WITH CHECK (user_id = dayorder.current_user_id());
GRANT SELECT, INSERT ON dayorder.agent_run_executions, dayorder.agent_run_operations
TO dayorder_api, dayorder_worker;
```

UPDATE 仅授权可变控制列，冻结 Scope/Profile/deadline 列不给 API/Worker UPDATE；operations 仅状态/次数/用量/完成时间可改，hash/key 不可改。不授予 DELETE。锁顺序 account → run → execution → operation；LockAccount 用 `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`（参数为可信 userID 字符串），不使用需要 UPDATE 权限的 user_settings FOR UPDATE。哈希碰撞只增加串行等待，不改变授权；账户锁仅在短事务中持有。
- [x] **4. 实现 SQL/Repository 和权限测试。** 查询显式 user_id；Save 同时 CAS run.version/token，更新行数非 1 返回 conflict；SourceRef 使用现有实体+版本唯一键 `ON CONFLICT DO NOTHING`。API/Worker 角色分别验证跨用户读写、无 UserContext、错误 FK、operation 重复 CAS、旧 token 完成 Run、冻结字段 UPDATE 全部被拒绝。既测全新库，也测已有 000008 数据的升级。
- [x] **5. 验证并提交。** `npm run db:generate`、上述 integration 测试、`go test ./apps/api/internal/migrations ./apps/api/migrations`、`npm run db:generate:check`。提交 `feat(agent): persist readonly executions and operation ledger`。

## Task 5: 只读 Run 生命周期与可信 Actor

**Files:**

- Create: `apps/api/internal/service/agent_readonly_run.go`、`agent_readonly_run_test.go`、`agent_readonly_run_integration_test.go`
- Create: `apps/api/internal/agentexecution/policy.go`、`policy_test.go`、`apps/api/internal/agenttest/services.go`

**Interfaces:** `agentexecution.Actor{UserID,Token uuid.UUID; Mode agentprotocol.ExecutionMode}` 仅由 Host 构造。`service.NewAgentReadonlyService(AgentReadonlyConfig) (*AgentReadonlyService,error)`；Config 字段为 Store（Task 4 Store）、Transactor（现有 UserTransactor）、Commands（*CommandService）、SyncWriter/AuditWriter（现有 CommandSyncWriter/CommandAuditWriter）、Capabilities（map[agentprotocol.ExecutionMode]agentprotocol.CapabilitySnapshot）、Profiles（[]string）、Now（func() time.Time）、Budget（agentprotocol.Budget，全零时取 Spec 缺省，否则所有值须合法且不超过缺省上限；仅宿主可注入更短测试预算）。方法：

```go
Create(context.Context, MutationContext, agentprotocol.ReadonlyRunStart) (agentprotocol.ReadonlyRunView, error)
Get(context.Context, uuid.UUID, uuid.UUID) (agentprotocol.ReadonlyRunView, error)
Cancel(context.Context, MutationContext, uuid.UUID, int64) (agentprotocol.ReadonlyRunView, error)
Finish(context.Context, MutationContext, uuid.UUID, int64, agentprotocol.ReadonlyRunFinish) (agentprotocol.ReadonlyRunView, error)
Claim(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, bool) (agentexecution.Record, error)
Complete(context.Context, agentexecution.Actor, uuid.UUID, agentprotocol.RuntimeState, []model.AgentStepDraft) error
Fail(context.Context, agentexecution.Actor, uuid.UUID, agentprotocol.AgentError) error
```

Claim 参数为 userID/runID/newToken/redelivered；未开始则取得执行权，已终态返回原记录，已有执行权且是可靠重投则保存 execution_interrupted 失败、不再次运行，非重投竞争返回 conflict。Complete 是后台内部终态事务，客户端不能调用；Fail 是 Gateway/Host 内部可信失败入口，允许两种 mode，但不覆盖已存在终态。`agentexecution.ValidateScope(AgentScope) error`、`RunOutbox(mode agentprotocol.ExecutionMode,runID uuid.UUID) []model.OutboxDraft` 是本任务的纯规则函数。

Actor 初始化约定：前台 execution token 固定从 uuid.Nil 开始，可信 HTTP Host 从 Session 与 foreground mode 构造 nil-token Actor，不把 token 加入公开 DTO。后台 uuid.Nil 表示未领取；Claim 拒绝 foreground 与 nil newToken，领取后后台操作/完成必须使用精确匹配的非 nil token。未领取 Run 的过期收敛不等于授予执行权。

用量权威约定：Complete 仅投影 Runtime 终态，不用 RuntimeState.Usage 覆盖持久化 KnownUsage、ReservedTokens 或 UsageComplete。Task 6/9 的可信记账先于 completed 发布；Runtime 已确认用量可能少于包含重试/未知尝试的服务端账目，完成不能抹除差异或把未知用量标成完整。

- [x] **1. 写模式隔离红测试。**

```go
func TestReadonlyForegroundDoesNotEnqueue(t *testing.T) {
    id := uuid.New()
    if got := agentexecution.RunOutbox("foreground", id); len(got) != 0 {
        t.Fatalf("foreground outbox = %#v", got)
    }
    got := agentexecution.RunOutbox("background", id)
    if len(got) != 1 || got[0].EventType != "agent.readonly.run.requested" {
        t.Fatalf("background outbox = %#v", got)
    }
}
```

新增表驱动 Scope 测试：缺时间、from>=to、超过 31 天、空/重复/其他域、非 nil entityIds（包括空数组）失败；RFC3339 不同时区同一时间规范化后相等。
- [x] **2. 运行红测试。** `go test ./apps/api/internal/agentexecution ./apps/api/internal/service -run Readonly -count=1`。
- [x] **3. 实现 Create 事务。** 在 executeResourceCommand 的回调中 LockAccount，先收敛过期活动 Run，再检查活动数/过去一分钟创建数，固定 read、生成快照、deadline=now+配置预算（缺省 120s），Store.Create 并返回 Sync/Audit/按模式 Outbox：

```go
drafts := agentexecution.RunOutbox(input.ExecutionMode, runID)
result := CommandResult{Status: 201, Body: body, Outbox: drafts,
    Changes: []model.SyncChangeDraft{{EntityType: "agent_run", EntityID: runID, Operation: "create", EntityVersion: 1}},
    Audits: []model.AuditDraft{{Action: "agent.readonly.create", Entities: []model.AuditEntity{{EntityType: "agent_run", EntityID: runID}}}},
}
```

body 是生成 ReadonlyRunView 的 JSON；冻结对象先复制，不能复用可修改配置 map。Commands 在幂等 replay 时先返回旧响应，不能把 replay 算成新创建。
- [x] **4. 实现 Get/Cancel/Finish/Claim/Complete。** Get 拒绝旧记录缺 execution；过期非终态以 timeout 收敛。Cancel/Finish 要求 expectedVersion，重复相同 mutation 回放，新的 mutation 对已经终态返回 conflict 并可 Get 原结果；不能覆盖终态。Finish 仅 foreground、client_reported，不接受预算/用量/引用；已知 Provider 错误/deadline 优先。Complete 在 user transaction 写受控步骤、终态、Sync/Audit，重复终态返回 nil，旧 token 冲突；不运行模型。Claim 遵守第 9 节中断恢复策略。
- [x] **5. 加真实事务测试 helper 并验证。** `agenttest.Services{Runs *service.AgentReadonlyService; Calendar *service.CalendarService; Transactor *database.Transactor}`，`NewServices(t,f,role)` 用 API 或 Worker pool、现有 Idempotency/Sync/Audit/Command/Calendar 构造器及实际 assets 构造服务；新 service 测试统一使用 `package service_test` 避免 import cycle。断言 Create replay 只有一个 Run/事件，两个并发创建只有一个成功，10 次速率上限，前台/后台 finish 不能混用，取消后 finish 不覆盖，所有事务均有 Sync/Audit。
- [x] **6. 绿测试并提交。** `go test ./apps/api/internal/agentexecution ./apps/api/internal/service -run Readonly -count=1 -v`。提交 `feat(agent): add readonly run lifecycle and actor policy`。

## Task 6: 服务端 operation 去重、Token 预留与执行权

**Files:** Create `apps/api/internal/service/agent_readonly_operations.go`、`agent_readonly_operations_test.go`、`agent_readonly_operations_integration_test.go`；本任务已定义的协议错误包装放在 `apps/api/internal/agentexecution/error.go`，对应窄测试为 `error_test.go`。

依赖修正：`agent_readonly_run.go` 的 Create 显式初始化 UsageComplete=true（尚无模型尝试时零用量完整），增加实际初始化测试；此后完整性由 operation 账本维护。

规范化复用：新增无依赖的 `apps/api/internal/canonicaljson/json.go`、`json_test.go`，仅暴露 Value/Bytes。迁移既有 `agentruntime/canonical_json.go` 算法并保留其私有委托函数，Service 直接使用共享包；不复制另一套算法、不让 Service 依赖 Runtime。此次提取不改变既有规范化规则，须保留并验证 Runtime 指纹/Golden 回归及 Go1.25 race。

**Interfaces:** 在 Task 5 Service 新增：

```go
Authorize(context.Context, agentexecution.Actor, uuid.UUID) (agentexecution.Record, error)
BeginOperation(context.Context, agentexecution.Actor, uuid.UUID, string, string, []byte, int) (agentexecution.Operation, error)
RetryOperation(context.Context, agentexecution.Actor, agentexecution.Operation, int) (agentexecution.Operation, error)
EndOperation(context.Context, agentexecution.Actor, agentexecution.Operation, agentprotocol.Usage, bool, string) error
```

Begin 参数为 runID/kind/id/canonicalPayload/reservedTokens；End 最后二参为 usageComplete/errorCode。canonicalPayload 先规范 JSON 再 SHA256，不能信任客户端 hash。内部 auth 错误保留 service/model 错误供 HTTP 映射，协议错误用 `agentexecution.Error{Agent agentprotocol.AgentError}`（实现 error）。

- [x] **1. 写重复 Turn 与不同内容冲突测试。** 使用 Task 5 helper 创建 foreground Run，构造 Actor；同一个 `turn-1` 连续 Begin 两次，第二次必须 model.ErrConflict，且数据库只有一条 operation；不同 body 同 ID 同样失败（同一原始 payload 的独立覆盖作为低优先级审查项保留，见实施进度）：

```go
for _, payload := range [][]byte{[]byte(`{"text":"a"}`), []byte(`{"text":"b"}`)} {
    _, err := runs.BeginOperation(ctx, actor, runID, "provider_turn", "turn-1", payload, 2048)
    if !errors.Is(err, model.ErrConflict) { t.Fatalf("duplicate error = %v", err) }
}
```

前置第一次 Begin、ctx/runs/actor/runID 均在该测试中由 NewServices/Create/UUID parse 明确构造。
- [x] **2. 运行红测试。** `go test ./apps/api/internal/service -run 'ReadonlyOperation|ReadonlyBudget' -count=1`。
- [x] **3. 实现事务中的限额和 CAS。** Authorize 验证 user/mode/token/status/deadline；在同一 run 锁下读取 ledger：provider_turn 不超过 9，任一时刻最多一个 running Provider；calendar_read 不超过 8 个不同 ID。重复日程同 hash 可以开始新 attempt，结果不是快照重放；不同 hash 冲突，SourceRef 不重复。单 Run 日程总 attempt 再限制 32，防止重复请求绕过限额。

```go
if record.Execution.KnownUsage.TotalTokens+record.Execution.ReservedTokens+reserve > record.Execution.Budget.MaxTokens {
    return agentexecution.Operation{}, &agentexecution.Error{Agent: agentprotocol.AgentError{
        Code: "validation_failed", Message: "run token budget exceeded", Retryable: false,
    }}
}
```

RetryOperation 最大 attempts=2，未知上一 attempt 用量保留预留；End 收敛 state，用量只能由可信 Gateway 调用累计，重复 End 不累计两次。断流 incomplete 标 unknown；无依据时不能把 reserved 释放为零。

重试结算约定：RetryOperation 只重试尚未最终结算的同一固定请求/选项，每次预留额必须相同，改变预留额则拒绝；不重新打开已结算的 Provider Turn。Operation.ReservedTokens 始终表示持久化的未决总预留，End 根据持久化尝试次数及固定单次预留结算，不信任仅存在于返回句柄中的“当前尝试预留”。未知旧尝试在后次成功后仍保留预留及不完整标记。Task 9 重试不得重算/改变已冻结的请求预算；可变预算重试不在本期。

模型用量只属于 provider_turn：calendar_read 的 Begin 必须传入零预留，End 必须传入零模型 Usage；非零值拒绝且不修改账本。日程重读通过 Begin 登记新尝试，不能调用仅用于 Provider 的 RetryOperation。不引入收费 Tool 的另一套预算。

具体调用顺序：Begin 返回第一次尝试句柄 → End(已知用量,false,错误码) 保存 unknown/incomplete → Retry(原句柄,相同预留) → End(最终尝试)。Retry 读取真实持久化状态，只允许前一次尝试为 unknown/incomplete，拒绝仍 running 或已 completed/failed；调用方无需也不得伪造更新后的 State。Task 9 仍须先判定未发布事件、可重试错误、剩余时间及次数，不把该内部结算顺序作为重新执行整个 Run 的许可。
- [x] **4. 运行并发/超限矩阵。** 9/10 轮、8/9 日程 ID、32/33 日程 attempt、重试两次、并发 Begin、旧 token、取消后 Begin、重复 End、unknown 保留预留都做真实事务断言。运行 service 专项和 `go test -race ./apps/api/internal/service -run Readonly`。
- [x] **5. 提交。** `feat(agent): enforce run operation and usage budgets`。

## Task 7: 日程 Application Service 与后台 Binding

**Files:**

- Create: `apps/api/internal/service/agent_calendar_read.go`、`agent_calendar_read_test.go`、`agent_calendar_read_integration_test.go`
- Create: `apps/api/internal/agentbinding/calendar.go`、`calendar_test.go`

**Interfaces:** `NewAgentCalendarReadService(runs *AgentReadonlyService, calendar *CalendarService) (*AgentCalendarReadService,error)`；`Read(ctx, actor, runID, callID, input CalendarReadInput) (ToolResult,error)`；在 Run Service 新增 `RecordCalendarRefs(ctx,actor,runID,refs []model.AgentSourceRefDraft) error`，复用其 Store/事务。`agentbinding.NewCalendar(actor,runID,reader) (agenttool.Binding,error)`，reader 是上述 Read 方法接口。构造时处理 CalendarReadSpec 的资产校验错误，Spec() 仅返回已验证资产的隔离副本，不吞掉装配失败或推迟到首次 Invoke。

- [x] **1. 写真实 Scope 失败测试。** 用 agenttest.NewServices 建 Run/日程，再调用 reader.Read；扩展开始时间一秒应返回 permission_denied，普通账户 CalendarService 仍能读取该事件。使用下列输入矩阵生成测试：

```go
cases := []struct { name, start, end, code string }{
    {"inside", "2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", ""},
    {"before", "2026-09-04T23:59:59Z", "2026-09-06T00:00:00Z", "permission_denied"},
    {"reversed", "2026-09-06T00:00:00Z", "2026-09-05T00:00:00Z", "validation_failed"},
}
```

- [x] **2. 运行红测试。** `go test ./apps/api/internal/service ./apps/api/internal/agentbinding -run Calendar -count=1`。
- [x] **3. 实现读取与投影。** Schema/字节校验 → Authorize → UTC 时间关系/Run window → BeginOperation(calendar_read) → CalendarService.List（非长事务）→ 输出最小字段 → Schema/64KiB 校验 → RecordCalendarRefs → EndOperation。引用最后发布前再次验证 token/状态；任何步骤失败不发布成功。operation 开始后的结算使用独立、最多五秒的元数据 context；取消前后均不能跳过可信收尾，结算失败仍保留原始取消/deadline 的错误身份，并脱敏保留结算失败原因。独立 context 只保护元数据写入，不延长日程读取或成功发布授权。

```go
page, err := service.calendar.List(ctx, actor.UserID, &start, &end, cursor, limit)
if err != nil { return agentprotocol.ToolResult{}, err }
refs := make([]model.AgentSourceRefDraft, 0, len(page.Events))
for _, event := range page.Events {
    refs = append(refs, model.AgentSourceRefDraft{EntityType: "calendar_event",
        EntityID: event.ID, EntityVersion: event.Version, LabelSnapshot: event.Title})
}
```

cursor 由原 CalendarService 解码；输出 nextCursor 无后续页时显式 null。超限返回 validation_failed，不静默截断；参数错误不触发 DB 读取。Binding 只转换 input/actor/IDs，调用 Read，返回可信 ToolResult；不导入 postgres 包。
- [x] **4. 覆盖业务边界。** 事件恰好在 start 结束/end 开始、跨时间窗、UTC 同义输入、空列表、同 start 的 ID 排序、两页、篡改/跨用户 cursor、超长结果、读取取消、重复 call 不重复引用、两种真实数据库角色。回读 event.version 不变、无日程 Sync 变更，只有 Agent 元数据。
- [x] **5. 绿测试并提交。** 上述命令加 `go test ./apps/api/internal/service ./apps/api/internal/agentbinding -count=1`。提交 `feat(agent): add scoped calendar service tool binding`。

## Task 8: DeepSeek Adapter、历史转换与确定性 Fake

**Files:**

- Create: `apps/api/internal/agentprovider/adapter.go`、`deepseek.go`、`deepseek_stream.go`、`history.go`、`fake.go` 及各自 `_test.go`
- Create: `apps/api/internal/agentprovider/testdata/deepseek/{tool-turn,text-turn,multiple-calls,missing-usage}.sse`

**Interfaces:** 保留原 StreamProvider；新增仅服务端的 Adapter：

```go
type TurnOptions struct { Model string; MaxOutputTokens int }
type Adapter interface {
    Stream(context.Context, agentprotocol.ModelTurnRequest, TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error]
}
type DeepSeekConfig struct { Endpoint, APIKey string; AllowLoopbackHTTP bool; Client *http.Client }
type FakeConfig struct { Window agentprotocol.CalendarReadInput; RecoverToolTimeout bool; Fault string }
```

`NewDeepSeek(DeepSeekConfig) (*DeepSeek,error)`、`NewFake(FakeConfig) (*Fake,error)` 实现 Adapter；`ProviderError{Code agentprotocol.ErrorCode; Status int; RetryAfter time.Duration; Retryable bool; KnownUsage *agentprotocol.Usage}` 实现 error（Error() 只输出安全类别）。`KnownUsage` 标记 `json:"-"`，仅在服务端传递失败前已经收到且验证合法的实际用量快照，nil 表示未观察到；不修改 ProviderEvent 或 HTTP DTO。缺 DONE、后续协议错误或取消仍失败，Gateway 按 incomplete 结算且保留未知预留，不能据此发布 completed；重复尾帧不累加用量。`EncodeDeepSeekRequest(ModelTurnRequest,TurnOptions) ([]byte,error)` 不携带密钥；`ParseDeepSeekStream(ctx,io.Reader) iter.Seq2[ProviderEvent,error]` 只负责已受网络取消保护的流解析。

官方依据（2026-09-05 只读核对，未调用模型）：[Chat Completion](https://api-docs.deepseek.com/api/create-chat-completion)、[Tool Calls](https://api-docs.deepseek.com/guides/tool_calls)。当前模型列表包含 deepseek-v4-flash；思考模式缺省 enabled，需显式 disabled；Usage 在 `[DONE]` 前的尾帧，尾帧 choices 可能同时有 finish_reason，不能仅接受 choices=[]。文档未列出 parallel_tool_calls，本 Adapter 不发送未经确认的该参数，仍强制拒绝多调用。

共享前置验证接口：`ValidateHistory(request agentprotocol.ModelTurnRequest) error`，供 Gateway 在网络调用前验证。它与 Encode/Fake 复用私有转换路径，验证消息上限、有限工具/别名、单调用配对/唯一 ID/无未解决调用及原始 ToolSpec 参数 Schema；不暴露厂商消息类型。用户 system 消息拒绝、冻结 ToolSpec/Profile 比较和总请求限制仍由 Gateway 负责，此 helper 允许服务端生成的 system 约束。

- [x] **1. 写真实格式分片红测试。** 使用 strings.NewReader/每次只返回一个字节的 reader 包装，收集 ParseDeepSeekStream 输出，断言顺序为 tool_call/completed、Usage=15：

```text
data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"dayorder_calendar_read","arguments":"{\"start\":\"2026-09-05T00:00:00Z\","}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"end\":\"2026-09-06T00:00:00Z\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]

```

同时验证 usage-only 尾帧、缺 Usage/缺 DONE/两次 finish/多 choices/多 calls/截断 JSON 均无成功 completed，不执行 Tool。
- [x] **2. 运行红测试。** `go test ./apps/api/internal/agentprovider -run 'DeepSeek|History|Fake' -count=1`。
- [x] **3. 实现请求/历史转换。** 固定别名表为 skill_list→skill_list、skill_load→skill_load、dayorder.calendar.read→dayorder_calendar_read；反向查表拒绝未知名/冲突。逐条验证 assistant/tool 配对、孤立 result、重复 callId、未解决 call；ToolResult 作为 JSON 文本发送，不把数据升级成 system 指令。序列化请求核心：

```go
payload := map[string]any{
    "model": options.Model, "messages": messages, "tools": tools,
    "stream": true, "stream_options": map[string]any{"include_usage": true},
    "thinking": map[string]any{"type": "disabled"}, "max_tokens": options.MaxOutputTokens,
}
return json.Marshal(payload)
```

messages/tools 为本文件定义的厂商私有类型和经验证的别名，禁止引入 Web SDK。发给厂商的参数 Schema 移除 Task 3 引入的 DayOrder 自有 `maxUtf8Bytes` 及私有 vocabulary 元数据，保留标准 type/required/properties/format/limits/description；增加实际序列化请求测试，不实现通用 Schema 清洗器。tool_call arguments 拼完后按完整原始 ToolSpec inputSchema 校验，不用投影 Schema 放宽本地校验；任何未知 tool/多调用返回 protocol_incompatible。
- [x] **4. 实现受限 HTTP/SSE。** endpoint 配置需 HTTPS、无 userinfo/fragment；本地 http 只有 AllowLoopbackHTTP=true 且解析为 loopback 才允许。禁止重定向。请求 context 同时控制连接/读 body，15 秒空闲 timer 取消请求并关闭 body，每个有效读取重置 timer；总 deadline 由 Gateway 限制。解析设置帧/累计字节/深度边界、合法 UTF-8、一个 terminal；stop→end_turn、tool_calls→tool_use、length→max_tokens，其余受控失败。非思考请求出现非空 reasoning_content 视为不支持，不能混作用户文本。
- [x] **5. 实现 Fake 的输入依赖。** 按已经解决的 ToolResult 数选择：0 发 skill_list；1 校验可见 calendar-overview 后 skill_load；2 校验 activeToolIds 后 calendar.read；3 校验日程结果并由实际 events 标题/数量生成文本。固定每轮 Usage=15。RecoverToolTimeout 模式收到 timeout 才生成“未能完成查询”的安全文本；不允许错序输入仍成功。HTTP error 测试覆盖 429/Retry-After（秒与日期）、500、401、取消及畸形帧；日志不得包含 key/原始 body。
- [x] **6. 绿测试并提交。** `go test ./apps/api/internal/agentprovider -count=1`、`go vet ./apps/api/internal/agentprovider`。提交 `feat(agent): add deepseek streaming adapter and scripted fake`。此时只有 Adapter 测试通过，不代表真实厂商冒烟通过。

## Task 9: 服务端 Gateway、系统约束与重试

**Files:** Create `apps/api/internal/agentgateway/gateway.go`、`policy.go`、`registry.go`、`gateway_test.go`、`gateway_integration_test.go`。

**Interfaces:**

```go
type Profile struct { ID, Model string; Adapter agentprovider.Adapter }
type Config struct { Runs *service.AgentReadonlyService; Profiles []Profile; Tools []agentprotocol.ToolSpec }
```

`agentgateway.New(Config) (*Gateway,error)`；`Gateway.ForRun(actor,runID) agentprovider.StreamProvider`；`Gateway.Prepare(ctx,actor,request) (*Turn,error)` 完成可在 Header 前拒绝的校验/Begin；`Turn.Events(ctx) iter.Seq2[ProviderEvent,error]` 只可消费一次；`Gateway.Cancel(runID uuid.UUID,cause error)` 取消活动调用。ForRun.Stream 组合 Prepare/Events，不绕过任何校验。

- [x] **1. 写服务端拒绝伪造 Tools 测试。** 使用 Task 5 的真实 Runs，Profile 配置 Fake；将请求中日程 Tool 改为 sideEffect=reversible_write，Prepare 必须拒绝，operation 和 Fake 调用数都为零。另测 URL-like Profile、错误 runId/用户/模式、用户 system message、过期 Run。

```go
request.Tools[0].SideEffect = "reversible_write"
_, err := gateway.Prepare(ctx, actor, request)
if err == nil { t.Fatal("forged ToolSpec accepted") }
```

request 从 Task 1 合法 ModelTurnRequest Fixture 构造并使用该测试 Create 返回的 RunID/资产 ToolSpec。
- [x] **2. 运行红测试。** `go test ./apps/api/internal/agentgateway -count=1`。
- [x] **3. 实现 Prepare。** 认证 Actor→Authorize→Profile 固定匹配→严格 Schema/消息数/字节/深度→历史配对→从快照+assets 重建完整有效 ToolSpec 并规范比较→BeginOperation。请求 digest 在插入服务端 system 约束后计算；系统说明只包含 Skill descriptor、授权窗口和只读约束，不提前塞入完整 Skill 正文。

输入 Token 预留用序列化后的 UTF-8 字节数作为保守上界，加 512 协议余量、加 maxOutputTokens；该上界是预算估计，不是实际 tokenizer。超剩余预算拒绝，不截断授权说明。模型 context 上限由固定 Profile 限制（本期请求输入最多 16384 预估 Tokens），不依赖用户声明。
- [x] **4. 实现有界 Events。** 总时限取 min(45s,剩余 Run)；登记 cancel；消费 Adapter 事件直到 terminal；每发布一个事件设置 published。retry 判定：

```go
func allowRetry(err *agentprovider.ProviderError, published bool, attempt int, remaining time.Duration) bool {
    return err != nil && err.Retryable && !published && attempt < 2 && remaining > err.RetryAfter
}
```

退避取 Retry-After（最多 5 秒）或 250ms；Retry-After 大于允许等待时失败，不提前无视要求重试。重试先 RetryOperation，受 Run 预留限制；已发布后失败不重试。结束前 EndOperation，Usage 入账成功才发布 completed。断开/取消时用最多 5 秒收尾 context 保存已知用量与 incomplete；不得延长网络调用。最终 Provider 失败写 Run 受控失败，不能被后续 foreground finish 改成成功。

异常时读取服务端内部 `ProviderError.KnownUsage`（若非 nil）作为本次尝试已知用量，以 `EndOperation(handle,knownUsage,false,errorCode)` 结算；保留不完整性及未知预留。合法 Usage 后缺 DONE/后续报错的测试必须证明已知用量未丢失、没有成功 completed、没有 Tool 执行，不能只测试未知用量为零的断流。
- [x] **5. 验证重试/去重矩阵。** httptest Adapter 首次 429、第二次成功：两次尝试、一个 Turn；首次 text 后断流：一次尝试、failed；同 turn 两并发请求：一次厂商调用；取消→清理 registry；missing Usage→failed/incomplete，超预算→无 Tool 执行。调用日志检查不存在 prompt、key、Cookie。
- [x] **6. 提交。** `feat(agent): enforce provider gateway policy and bounded retries`。

实施与复盘边界（Task 9 复审后）：取消或正常终态收尾建立同一个最多五秒的绝对时间窗，End / Authorize / Fail 分别截止于 T+3 / T+4 / T+5 秒，为终态保存预留时间。取消时活动消费者显式交出真实 Usage/结果后由唯一协调者结算；未消费的 Turn 独立清理。到 End 截止仍没有返回结果时不替活动消费者写零用量结算，保留未结算 operation、预留额度及 incomplete，再尝试保存受控终态并有界释放槽。该截止不延长网络调用，不承诺强停任意不协作代码或持久化任意迟到的 Usage。End 中途发生的显式取消/deadline，在 Fail 前重新判定主原因；清理错误仅附脱敏标记。数据库仍不可用时返回 `persistence=failed`，不能宣称终态已持久化；后续宿主仍须依据真实持久化结果决定是否确认消费。HTTP 断连只表示 execution_interrupted；显式取消需先由 Service 保存 stopped，再通知 Gateway。

## Task 10: 独立集成 Router 与标准 SSE Endpoint

**Files:**

- Create: `apps/api/internal/httpapi/agent_integration_router.go`、`agent_integration_handlers.go`、`agent_integration_stream.go`、`agent_integration_test.go`
- Modify: `apps/api/internal/httpapi/router.go`（提取私有构造逻辑，生产行为不变）、必要的 `middleware.go` ResponseController 兼容及测试

**Interfaces:** `AgentIntegrationOptions{Environment config.Environment; Runs *service.AgentReadonlyService; Calendar *service.AgentCalendarReadService; Gateway *agentgateway.Gateway}`；`NewAgentIntegrationRouter(base RouterOptions, agent AgentIntegrationOptions) (http.Handler,error)`。只允许 development/test；原 NewRouter 不获得任何新 Agent enable 字段。

- [x] **1. 写生产拒绝与无 Session 红测试。**

```go
func TestAgentIntegrationRouterRejectsProduction(t *testing.T) {
    _, err := NewAgentIntegrationRouter(RouterOptions{}, AgentIntegrationOptions{Environment: config.Production})
    if err == nil { t.Fatal("production agent integration router accepted") }
}
```

沿用当前 router 测试的 stub account/session 进行未登录/跨 Origin 测试；真实 Session 在 Task 14/15 再验收。
- [x] **2. 运行红测试。** `go test ./apps/api/internal/httpapi -run AgentIntegration -count=1`。
- [x] **3. 分离生产与集成路由装配。** NewRouter 调用私有 builder，不注册新链路；新构造器验证环境后，在同一个 mux/middleware 注册 Spec §8.1 六类路径。原 `/agent-runs` 与 `/agent-changes` 仍走 agentUnavailable。生产 `/api/v1/agent/runs` 路径由 disabled handler 明确返回 AGENT_NOT_AVAILABLE，不靠路由遗漏绕过测试。

创建/取消/finish 使用 authenticateRequest + mutationContext + expectedVersion（后两者）；Tool/Turn 验证 Session、合法且归属用户的 X-Device-ID、Origin、UUID 和 callId/turnId 长度 1–128。新 POST 只接受 JSON，拒绝跨源缺乏可信 Origin 的浏览器请求；测试 CLI 显式发送同源 Origin。伪造 device 不能只通过 UUID 语法就获准。
- [x] **4. 实现流写出。** Prepare 完成后再发送 SSE Header；禁缓冲、每帧 Flush；Envelope sequence 本轮从 1 递增：

```go
response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
response.Header().Set("Cache-Control", "no-store")
response.Header().Set("X-Accel-Buffering", "no")
envelope := agentprotocol.ProviderEnvelope{ProtocolVersion: "2.0", RunID: request.RunID,
    TurnID: request.TurnID, Sequence: sequence, Event: event}
payload, err := json.Marshal(envelope)
if err != nil { return }
if _, err = fmt.Fprintf(response, "data: %s\n\n", payload); err != nil { return }
if err = http.NewResponseController(response).Flush(); err != nil { return }
```

writer middleware 增加 `Unwrap() http.ResponseWriter` 以支持 Flush/write deadline。每次写设置不超过 15 秒/Run deadline 的上限；客户端断开取消当前 Turn。开始前标准 HTTP error，开始后规范 error terminal，不输出原始错误。心跳仅 SSE comment，不能增加 Envelope sequence。
- [x] **5. 补齐协议测试并提交。** 401、跨用户 404、安全错误映射、恶意 Origin、无设备、过大/过深 JSON、wrong mode、Prepare 失败无 SSE Header、真实网络收到首帧即 Flush、断流 cancel、同 turn conflict、App/旧路由 guard 仍关闭。运行 `go test ./apps/api/internal/httpapi -count=1` 和 `npm run test:architecture`；提交 `feat(agent): expose readonly integration http and sse routes`。

实施与复盘边界（Task 10 复审后）：ResponseController 经 middleware 保留实际 FlushError 与写 deadline；HTTP 日程读取取请求取消、Run 绝对 deadline 与 canonical Tool10秒超时的最早边界。Gateway 已结算并结束后，最终 HTTP 写/Flush 仍可能失败，因此未成功送达终止帧时取消请求 context，并通过现有 Runs.Fail 作最多1秒的元数据终态收敛；它不依据 UsageComplete 判断执行所有权，不改变已知用量/未知预留，不覆盖已有终态，数据库不可用时不承诺持久化成功。对累计 SSE 上限，非终止帧入场前按实际身份/下一 sequence 为所有受控错误类型预留足够终止帧空间，仍保持单帧64KiB、累计1MiB。真实连接断开不承诺错误帧可送达；连接可写的本地上限失败必须保留规范 error terminal。

## Task 11: 前台 HTTP Client、Tool Binding 与 Run Host

**Files:**

- Create: `apps/web/src/agent/api/client.ts`、`client.test.ts`
- Create: `apps/web/src/agent/api/uuid.ts`（Agent 内部共享安全 UUID，不修改普通 API/领域 ID）
- Create: `apps/web/src/agent/provider/http.ts`、`sse.ts`、`http.test.ts`、`sse.test.ts`
- Create: `apps/web/src/agent/tool/calendar-http.ts`、`calendar-http.test.ts`
- Create: `apps/web/src/agent/host/foreground.ts`、`foreground.test.ts`
- Modify: `apps/web/src/api/http.ts`（导出复用安全错误解析，不改变普通 JSON 行为）

**Interfaces:** `createAgentClient({baseURL,deviceId,fetchImpl?})` 返回 `create(input,signal)`、`get(runId,signal)`、`cancel(runId,version,signal)`、`finish(runId,version,result,signal)`、`calendar(runId,callId,input,signal)`，返回 Task 3 DTO/ToolResult；每次 mutation 生成稳定 UUID 并在该次重试保持不变。`createHttpProvider(config): ProviderGateway`；`parseProviderSSE(body,identity,signal): AsyncIterable<ProviderEvent>`；identity={runId,turnId}。`createCalendarHttpBinding(client): ToolBinding`；`runForeground(input,client,provider,signal): Promise<{run:ReadonlyRunView;trace:RuntimeTrace}>`。

- [x] **1. 写 SSE 分片/缺 terminal 红测试。**

```ts
const body = new ReadableStream<Uint8Array>({
  start(controller) {
    const raw = new TextEncoder().encode('data: {"protocolVersion":"2.0","runId":"r","turnId":"t","sequence":1,"event":{"type":"text_delta","text":"日程"}}\n\n');
    for (const byte of raw) controller.enqueue(Uint8Array.of(byte));
    controller.close();
  },
});
await expect((async () => {
  for await (const event of parseProviderSSE(body, { runId: "r", turnId: "t" }, new AbortController().signal)) {
    expect(event.text).toBe("日程");
  }
})()).rejects.toThrow(/completion|terminal/);
```

- [x] **2. 运行红测试。** `npm run test --workspace @dayorder/web -- src/agent/provider src/agent/tool/calendar-http.test.ts src/agent/host`。
- [x] **3. 实现 HTTP/SSE。** fetch 使用 credentials=include、cache=no-store、signal、JSON Header、X-Request-ID/X-Device-ID；创建/finish/cancel 加 Idempotency-Key，后两者 If-Match。SSE 使用流式 fatal TextDecoder，多行 data 用换行连接，支持 LF/CRLF/BOM/注释；逐帧校验 JSON/Envelope/递增 sequence/上下文 ID，限帧和累计字节；EOF 无 terminal 为失败，不自动重连；finally cancel/release reader。不复用 response.json() 去读流。

HTTP Tool 错误映射固定：400/415/422/428→validation_failed；401/403/404→permission_denied；409→version_conflict；429/5xx/非取消网络失败→tool_failed。Host 区分 Abort reason，不把主动 abort 变成 tool_failed。
- [x] **4. 装配 foreground Host。** create→验证版本/mode/digest→构造 Runtime/Skill Registry/3 个 Binding→driveToCompletion→get 最新版本→finish。使用从创建请求开始测量的 round-trip 扣除剩余时间，避免客户端时钟差：

```ts
const serverRemaining = Date.parse(run.deadlineAt) - Date.parse(run.serverNow);
const localDeadlineAtMs = Date.now() + Math.max(0, serverRemaining - elapsedRequestMs);
```

完整初始化：`createRuntimeState({runId,executionMode:"foreground",capabilitySnapshot,budget})`；createSkillToolBindings 接收相同 snapshot/registry/policy；ApprovalBroker 固定 deny（不应被调用）。初始 user_message.text 使用已提交 intent。Run phase 精确转换为 finish.phase，受控摘要来自最终 assistant 文本，不包含 Prompt；不上传完整 Trace。
- [x] **5. 验证取消与结束冲突。** signal user abort 时本地立即取消，并用独立最多 5 秒 signal 尝试 cancel API；cancel/finish 同 mutation 只重发原请求，不重开 Run。409 后 get 已有终态，不覆盖。页面卸载不新建后台任务，不承诺 best-effort 网络一定送达。覆盖 digest mismatch、非法 Event、Auth/Scope 错误、Tool timeout 恢复、终态 cancel 赢过迟到 finish。
- [x] **6. 绿测试并提交。** `npm run test --workspace @dayorder/web -- src/agent`、`npm run typecheck`、`npm run test:architecture`。提交 `feat(agent): connect foreground readonly runtime over http`。

Task 11 实施裁定与复盘边界：

- `createHttpProvider` 与 Client 使用同形 `{baseURL,deviceId,fetchImpl?}` 配置；`baseURL` 包含 `/api/v1`，两者再追加 `/agent/runs`。原计划未明确 Provider 配置字段，此裁定复用既有边界；若不符合后续调用方需求，代价是配置类型和调用点调整。
- 本期不新增 mutation 自动重试。每次方法调用固定一个 body/version/UUID 并只调用一次 fetch；如未来重发同一逻辑 mutation，必须保持原身份和内容，另一次方法调用是新 mutation。不以浏览器会重放 POST 作为保证。原计划未规定重试触发、次数或退避；若后续需要自动恢复，需补重试策略/API，当前传输失败可能需要显式恢复。409 后 GET 和有界显式取消仍执行。
- `api/uuid.ts` 仅导出 `createAgentUUID(): string`，两个 Agent transport 共用 secure randomUUID/getRandomValues；没有安全随机源时在 fetch 前失败。该内部提取消除重复逻辑，不增加外部配置、不改普通 ID；若模块边界需调整，代价仅是小范围文件/import 重组。
- SSE 只计 UTF-8 data（含多行拼接 LF），每次保留 data field 前约束 pending frame 和累计额度；不能等空行/EOF 才拒绝无终止的多行流。独立 wire-line guard 约束未结束行，但 framing/comments 不占 data 配额。
- 显式取消覆盖最终 GET、finish 及冲突 GET，复用一个懒创建的最多五秒清理 signal；已持久化终态和服务端权威 Usage 优先，不因前台 trace 或不完整用量覆盖。此处单测/构建不替代 Task 14–16 的真实 Session、双宿主与远程 Provider 验收。

## Task 12: 后台 Run Host 与独立 Outbox Handler

**Files:**

- Create: `apps/api/internal/agenthost/background.go`、`background_test.go`
- Create: `apps/api/internal/worker/agent_readonly.go`、`agent_readonly_test.go`
- Modify: `apps/api/internal/worker/runner.go`、`runner_test.go`（可选 batch size，生产缺省不变）
- Modify: `apps/api/internal/service/agent_readonly_run.go`、`agent_readonly_run_test.go`、`apps/api/internal/agenttest/services.go`（修正完整冻结 Tool 集，见下述实施裁定；`agent_calendar_read_test.go` 使用同一构造 helper，保留覆盖，无需人为改动）
- Modify: `apps/api/internal/service/agent_readonly_run_integration_test.go`（完成态 Fixture 对齐 v2 已提交消息，验证真实摘要投影）

**Interfaces:** `worker.RunnerOptions{BatchSize int}`；新增 `NewRunnerWithOptions(repository,handlers,options) (*Runner,error)`，原 NewRunner 委托 BatchSize=25。`agenthost.Config{Runs *service.AgentReadonlyService; Calendar *service.AgentCalendarReadService; Gateway *agentgateway.Gateway}`；`NewBackground(Config) (*Background,error)`；`Background.Process(ctx,event model.OutboxEvent) error`；`worker.NewReadonlyAgentHandler(processor interface{Process(context.Context,model.OutboxEvent) error}) (*ReadonlyAgentHandler,error)`。

实施裁定（Task 12 首轮 RED 后发现的跨任务缺口）：Spec §4 的完整最大 Tool 集为 `dayorder.calendar.read`、`skill_list`、`skill_load`。此前 Service 构造器错误地仅准 calendar，而 Go/TS Driver 都按 frozen grant 过滤，Gateway 又要求三个规范 ToolSpec，导致真实接线不一致；Task 11 单测 Fixture 的三项快照不能证明真实服务端输出。Task 12 在可信 Service 构造边界验证精确三项集合（拒绝缺少、额外及重复，保留防御性复制），更新上述构造 Fixture。Create 返回、实际执行及 Complete 校验全程使用同一冻结快照，不通过 Host 临时追加再剥离 meta IDs 绕过校验；Skill allowed-tools 仍仅 calendar，不增加业务能力。若此裁定需推翻，代价是内部配置/Fixture 重做或另行明确 meta-tool 合约；不授权迁移既有库、新公共 API 或生产开放。补双模式真实 Service 冻结/拒绝/copy 红绿测试和 Host→Gateway→Fake list/load/read 闭环证据后再完成本任务。

第二项接线修正：v2 reducer 在 end_turn 把最终 assistant 文本写入 Messages 并清空 AssistantDraft，而此前后台完成投影仅读 Draft，真实 Run 可能 completed 却无摘要。本任务修正 Service 的完成态投影，从已提交的最后 assistant 消息 text 块取摘要，排除其他角色/Tool Payload，遵守现有 8000 字符上限；不把草稿塞回 State、不改冻结校验或用量账目。用真实 Host/Fake 的空摘要 RED→最终文本落库 GREEN，以及消息优先级/角色排除/Unicode 上限测试验证，Service 完成态 Fixture 改为真实 v2 形状。若此裁定需推翻，代价是内部投影/Fixture 重做，不增加 API、Schema 或迁移。

- [x] **1. 写领取数量与终态不重跑红测试。** 使用现有 runner repository stub 记录 Claim 的 limit，WithOptions=1 必须只领取 1；另默认仍为 25。背景 Host 测试由 Task 5 Create(background) 取得真实 event，第一次 Process 后重新 Process 同 event，Fake Adapter 调用数不增加：

```go
before := calls.Load()
if err := host.Process(ctx, event); err != nil { t.Fatal(err) }
if calls.Load() != before { t.Fatal("terminal run executed again") }
```

calls 是本测试包装 Adapter.Stream 的 atomic.Int64，包装实现仍委托真实 Fake；不是用无条件成功 Processor 替代 Host。
- [x] **2. 运行红测试。** `go test ./apps/api/internal/worker ./apps/api/internal/agenthost -run 'Readonly|Batch' -count=1`。
- [x] **3. 实现 Handler/Host。** 先校验 event type/user/aggregate/payload.runId，再 Claim。终态 nil；可执行时用 Run frozen 配置、Task 2 Deadline、同资产/3 个 Binding、ForRun Gateway 构造 Go Driver。事件 LockToken 作为执行 token；ModelTurn 初始输入是持久化 intent，不能信任事件自带 Prompt。Runtime terminal 用最多 5 秒独立 context Complete；成功 nil，让 Runner Confirm。
- [x] **4. 实现取消监测与中断恢复。**

```go
runCtx, stop := context.WithCancelCause(ctx)
defer stop(nil)
ticker := time.NewTicker(250 * time.Millisecond)
defer ticker.Stop()
```

监测 goroutine 每 tick Authorize，看到 stopped 用 StopCause{Kind:"user"}，token 丢失/停机用 interrupted；必须有 done channel 并在 Process 返回前等待监测器退出。Driver 同步 Tool，观测器不得持有业务事务。首次 Handler 基础设施失败可返回 error；已登记 executing 的重投按 Claim 保存 execution_interrupted，不重启模型。deadline 已过优先保存 timeout。预算+收尾>=5min 的配置拒绝。
- [x] **5. 故障注入验证并提交。** 终态 commit 失败在同进程收尾窗口重试原结果；Complete outbox 失败重投只确认；cancel 后业务无迟到结果；stale token 不可写；进程中断 operation unknown 不重放；Handler 业务 failed 返回 nil、基础设施失败返回 error。运行 worker/agenthost/service 专项 uncached + race，提交 `feat(agent): integrate bounded readonly outbox worker host`。

完成实现补充：上方 context 代码是原始结构草图；实际 Host 用 `context.WithoutCancel(ctx)` 保留上下文值，并以单独父取消桥接显式传递 interrupted，避免停机的裸 context.Canceled 被 Runtime 兼容逻辑误认为用户取消。父取消立即传入正在阻塞的 Authorize/Get，两个监测 goroutine 都在 Process 返回前 join；不延长业务 deadline。独立审查未发现阻断问题。取消到依赖实际退出的一秒验收由下一任务提供专门观测证据，元数据落库的五秒窗口不可混入此测量。

## Task 13: 受控观测、取消时延与脱敏

**Files:**

- Create: `apps/api/internal/agentexecution/observation.go`、`apps/api/internal/observability/agent.go`、`agent_test.go`
- Modify: `observability/metrics.go`、Run/Gateway/Background 的 Config 与关键事件点、Calendar Service 可选 observer setter

**Interfaces:** `agentexecution.Observation{Kind,Mode,ToolID,ModelProfile,Outcome,ErrorCode string; Duration,QueueWait,CancelLatency time.Duration; Usage agentprotocol.Usage; UsageComplete bool; Attempts int}`；`Observer interface{ObserveAgent(Observation)}`。`observability.Metrics.ObserveAgent` 实现接口，nil observer 安全 no-op。各 Config 新增可选 Observer；Calendar 的 `SetObserver(Observer)` 仅用于构造装配、启动后不再修改。字段不包含 Prompt、日程正文、Cookie 或高基数 ID。

Task 13 实施前裁定（补齐观测编码与日志注入，不改变执行语义）：Kind 固定为 run/provider/tool/background_slot，outcome 为 started/completed/failed/cancelled，未知输入归 unknown；Profile 白名单含现有 readonly-default 与 Task 14 的 readonly-fake/readonly-deepseek，错误码仅协议枚举及 none/unknown。指标以 `dayorder_agent_` 为前缀，使用 operations_total、runs_total、operation_duration_seconds、run_duration_seconds、queue_wait_seconds、cancel_latency_seconds、usage_tokens_total、usage_unknown_total、retries_total、active_background_slots；不增加货币成本指标。Run 只在新增终态事务成功后观测，Provider 按单次实际尝试累计已知/未知用量和重试，不重复累加 Run 累计快照；后台实际执行才取得 slot，所有返回路径 defer 释放，终态重投不计新执行，排队时长只在准入时记录一次。Run/Gateway/Background Config 增加可选 `Logger *slog.Logger`（nil 不记录），Calendar 复用其 Run Service logger；不改全局 slog。日志显式构建受控 Run/turn/call ID、版本/digest、标准结果和数值字段，Observation 仍不含 ID 或私有正文。若需推翻，代价为内部指标/日志装配与测试重做，不涉及协议、数据库 Schema、公开 Endpoint 或业务权限。

跨任务验收已在 Task 13 关闭：Task 12 的十秒 whole-Process 等待仅证明取消源、传播、join 与无迟到发布；本任务另从真实 Runs.Cancel 入口测量协作 Provider/Calendar 实际退出并验证 ≤1 秒，含持久化与轮询，不混入最多五秒的终态收尾。后续整体验收继续保留该断言，不以 whole-Process 时长替代。

取消观测窄接口裁定：新增内部 `AgentReadonlyService.CancellationLatency(ctx, actor, runID, dependencyExitedAt) (time.Duration, bool, error)`，仅按同一用户/精确执行 token 读取已保存 stopped/cancelled 的 FinishedAt。先在 Adapter 迭代或 Calendar.List 实际返回时捕获退出时间，再于现有有界元数据收尾 context 中查询并相减；不增新五秒窗口、不改变业务结果/取消路径/HTTP DTO。缺失、stale 或读取失败时不输出样本，不能伪造零或退回 monitor 发现时刻。该指标明确表示“已保存取消时间→依赖退出”，包含之后的轮询传播但不声称覆盖存储时间戳之前的请求处理；另以真实测试从 Runs.Cancel 入口计时，验证含持久化/轮询的完整 ≤1 秒。只有明确识别的本地停机/超时可用本地信号时间。若此裁定需推翻，代价为内部 helper/观测测试重做，不涉及迁移、HTTP 或业务权限。

- [x] **1. 写指标白名单红测试。**

```go
metrics := observability.NewMetrics("agent-integration", nil, nil)
metrics.ObserveAgent(agentexecution.Observation{Kind: "tool", Mode: "background",
    ToolID: "dayorder.calendar.read", Outcome: "failed", ErrorCode: "timeout"})
response := httptest.NewRecorder()
metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
if !strings.Contains(response.Body.String(), "dayorder_agent_operations_total") {
    t.Fatal("agent metrics missing")
}
if strings.Contains(response.Body.String(), "runId") { t.Fatal("high cardinality metric label") }
```

- [x] **2. 运行红测试。** `go test ./apps/api/internal/observability -run Agent -count=1`。
- [x] **3. 实现指标与日志。** 注册 operations/run outcome counter、duration/queue wait/cancel latency histogram、usage tokens/unknown usage/retry counter、active background slots gauge 和 GoCollector goroutine 指标。mode/tool/profile/outcome/code 均固定白名单，未知值归 unknown；ID 只进受控结构化日志。slog 属性显式构建：

```go
logger.Info("agent operation completed", "runId", runID, "turnId", turnID,
    "profile", profileID, "errorCode", code, "durationMs", elapsed.Milliseconds())
```

不记录 request/response 对象或 err.Error() 原文。成本没有价格配置就不输出货币数值；Usage incomplete 时不报零成本。结束/error/defer 都释放执行槽，监测真实取消请求到依赖结束时间。
- [x] **4. 验证并提交。** 注入含 canary key/Cookie/日程标题的错误，收集完整日志并断言不存在 canary；超时/取消/重试计数与 operation 表相符，Repeated Observe 不把 ID 展开成新标签。运行 observability/gateway/host/service 测试，提交 `feat(agent): add readonly runtime observability`。

## Task 14: 独占集成宿主与真实认证 Fixture

**Files:**

- Create: `apps/api/internal/agentintegration/host.go`、`seed.go`、`control.go`、`host_test.go`
- Create: `apps/api/cmd/agent-integration/main.go`
- Create: `apps/web/tests/agent-integration/harness/index.html`、`main.ts`
- Modify: `apps/api/internal/agenttest/database.go`、`services.go`（抽取不依赖 testing.TB 的构造器供宿主复用）

**Interfaces:** `agentintegration.Config{Environment config.Environment; RealProvider bool; ProviderKey,ProviderModel string; AllowedOrigins []string}`；`Start(ctx,Config) (*Host,error)`；`Host.APIURL() string`、`Host.Close(ctx) error`。main 仅解析自身 flags/env，不调用生产 Config.Load、不接受外部 DATABASE_URL。脚本先选定 Vite 的 loopback 端口，通过 `--harness-origin` 传给宿主；AllowedOrigins 仅接受明确的 loopback HTTP origin，不配置通配符。

命令入口交接约束：密钥不设 CLI 参数或 flag 默认值；解析成功且选择 real 模式后才读取 Task 16 专用的 `DAYORDER_AGENT_TEST_PROVIDER_KEY`，可选模型为 `DAYORDER_AGENT_TEST_MODEL`。Fake/help/参数错误路径不得查询或输出密钥。stdin 由编排器拥有，收到 stop、EOF 或读取错误都进入有界 Host.Close；读取错误和清理失败受控返回非零。stdout ready 写入失败也不能吞掉清理失败及精确临时库诊断。用行为回归验证上述边界，不因此执行真实模型或额外真实库测试。

Task 4 helper 补 `NewDatabase(ctx,*testdb.Postgres) (*Database,error)` / `Database.Close(ctx) error`；Task 5 helper 补 `BuildServices(db *Database, role config.DatabaseRole, options ServicesOptions) (*Services,error)`；原 Open/NewServices 是 testing.TB 包装。Host 通过 Task 4A 的 StartIsolated 创建并拥有 Fixture，Database 仅拥有池；关闭所有池后再调用 Fixture.Close，不直接访问可为空的 Container。Open 包装在测试中按同样顺序注册 Cleanup。

构造选项裁定：现有 NewServices 写死 readonly-default 且 Command/Sync/Audit 为局部变量，无法表达集成宿主的 Profile、共享观测和固定故障配置。新增仅内部测试/集成的 `ServicesOptions{Profiles []string; Budget agentprotocol.Budget; Store agentexecution.Store; Observer agentexecution.Observer; Logger *slog.Logger}`；空值保留既有默认 Profile/预算/真实 PostgreSQL Store，原 NewServices 用空选项委托、保持按值返回。Host 显式传入唯一 readonly-fake 或 readonly-deepseek，角色只接受现有 API/Worker 枚举。Services 可额外暴露已构造的 Commands/Sync/Audit，复用而不复制整段装配代码。Budget/Store 仅供既定故障场景构造；Store 包装仍委托真实数据库并保留事务、Actor/token/Scope 校验，不修改已冻结预算、不接受诊断端任意配置。若需推翻，代价是内部构造器/调用点重做，不涉及 Schema、HTTP、生产开关或账号权限。

Task 14 Tool 超时隔离补充裁定：上述内部 ServicesOptions 再增加唯一字段 `CalendarStore service.CalendarStore`，nil 仍使用真实 PostgreSQL CalendarRepository；复用现有 NewCalendarService，不在核心 AgentCalendarReadService 增加 BeforeDependency 测试回调/Options 构造器。集成私有 CalendarStore 包装仅在 ListEvents 的私有 context Run 标记、已登记故障 Run 和实际 owner 全部相符时协作等待取消并返回错误，其余委托真实存储。HTTP 仅规范日程 Tool 路径、Worker 私有 Handler 包装分别传递此标记，原认证/Scope/设备/事件一致性/Actor/token 校验全部保留，context 标记不能成为授权依据。移除全表 ACCESS EXCLUSIVE 锁、唯一 Migrator 连接占用及锁计时 goroutine，覆盖无标记/其他 Run/其他 owner/取消/真实跨账号读取不受影响。若需推翻，代价仅为内部构造选项、私有包装和测试重做，不改 Schema、共享角色或生产服务 API。

Task 14 首次审查修复裁定：内部 ServicesOptions 增加 `Beginner database.Beginner`，nil 保持原角色池构造，否则复用现有 NewTransactor/WithUser/三次可重试事务逻辑；集成私有 Beginner/Tx 包装真实角色池，不改核心 Service/database/HTTP API。故障预留绑定到事务，在实际 Commit 前发布精确 Run 的临时映射，防止已提交 Outbox 的 Worker 抢先执行；成功提交才消耗，明确回滚（含既有可重试 40001/40P01）恢复。其他提交错误按结果不确定处理，故障隔离在原账号/Run，后续创建或重新注入受控拒绝，不转给其他 Run、不假定回滚，直至宿主正常关闭。预留按身份/代次保护，不能跨数据库调用持有全局故障锁。Outbox 固定故障的查询、租约修改错误及零行均返回受控错误；确认租约修改成功才消耗一次性状态，实际 Retry 成功才释放中断状态。若需推翻，代价是内部构造器/私有包装和测试重做；不确定提交可能需关闭并重建独占测试宿主，不改变业务恢复或共享角色。审查同时要求每次 harness 初始化重试和在途轮询请求都受实际截止信号约束，不能只在请求前检查时钟。

- [x] **1. 写环境/地址红测试。**

```go
func TestAgentIntegrationHostRejectsProductionBeforeDatabase(t *testing.T) {
    _, err := agentintegration.Start(context.Background(), agentintegration.Config{Environment: config.Production})
    if err == nil { t.Fatal("production integration host accepted") }
}
```

再测所选数据库后端缺配置/不可用直接返回 error（不是 t.Skip），不回退另一实例；Fake 模式不读取 ProviderKey；启动后地址只能是 loopback，Shutdown 后 DB/Worker/HTTP 全退出。production 拒绝必须发生在读取数据库配置和创建临时库之前。
- [x] **2. 运行红测试。** `go test ./apps/api/internal/agentintegration -count=1 -v`。
- [x] **3. 实现 Host 构建。** StartIsolated 创建 Docker 容器或 ConfigHub 独占临时库→migrations→API/Worker 池→合成用户设置/设备/日程→真实 SessionService/AccountService→新 Router→Task 12 Worker。七个数据库配置字段仅传给 Go 宿主，使用后清理环境并将清理所需凭据留在私有资源句柄中；既有数据库/角色不修改。使用真实 auth.HashPassword，浏览器通过正常 `/auth/login` 获得 HttpOnly Cookie，不用 stub Authenticate 证明 RLS。Seed 固定窗口 2026-09-05T00:00:00Z 到 2026-09-06T00:00:00Z，两个账号各自不同标题/ID。

Fake 配置与 Seed 时间窗相同，Profile ID 为 `readonly-fake`；真实模式为 `readonly-deepseek`，模型默认 `deepseek-v4-flash`、thinking disabled。后台只注册 agent.readonly.run.requested，batch=1。生产命令入口完全不改。
- [x] **4. 暴露只在该宿主存在的诊断。** `GET /__test/config` 返回合成账号登录信息、deviceId、Profile、窗口，不含模型密钥/数据库 URL；`POST /__test/fault` 只接受固定故障枚举（tool_timeout/run_timeout/provider_429/provider_disconnect/complete_once/commit_once/interrupted）。诊断和 fault 同样要求 loopback、合法 Origin，除 config 外要求真实测试 Session；不得装入生产 Router。故障控制不接受任意 SQL、URL 或代码。

`GET /__test/runs/{runId}` 仅对所属账号返回以下诊断形状，供 Task 15 直接断言：

```ts
type TestRunState = {
  status: string; errorCode: string | null; summary: string;
  outboxCount: number; outboxStatus: string | null; deliveries: number;
  providerCalls: number; calendarCalls: number; attempts: number[];
  sourceRefs: Array<{ entityId: string; entityVersion: number }>;
};
```

集成 Worker 循环调用 RunOnce，基础设施错误有界退避后继续，不能直接复用遇 error 就退出的 Runner.Run。complete_once 注入首次确认失败，并仅在测试 fixture 内使该事件租约过期以触发重投；不等待生产 5 分钟，也不改变生产 staleAfter。commit_once 仅使第一次终态提交失败；interrupted 在登记 operation 后模拟执行者退出、保留执行权及未知结果，然后可靠重投。run_timeout 在新建 Run 时注入较短合法预算及受控慢 Provider，不修改已冻结 deadline。
诊断 GET 允许浏览器同源请求省略 Origin，并继续遵守 Session/CORS 约束；带 Origin 时必须匹配允许列表。所有诊断 POST 必须提供可信 Origin，不能把 GET 的宽松规则用于 fault 操作。

Task 14 实施裁定：migration 000004 只给 API 角色 Outbox INSERT 权限，Worker 通过安全函数操作队列，不能为诊断扩大共享角色权限。仅独占集成诊断允许在真实 Session 和 API Runs.Get 归属/RLS 校验成功后，使用宿主私有 Migrator 池查询 Outbox/operation/SourceRef 运行元数据；每条查询必须同时限定 user_id 与 run_id/aggregate_id，并实际测试其他账号访问诊断返回 404。此特例不进入生产，不是业务执行的 RLS 证明；Run/Calendar 的执行仍使用真实 API/Worker 角色。若需推翻，代价是诊断及隔离测试重做，错误实现可能泄露合成测试账号的元数据，不扩展生产能力。

- [x] **5. 实现最小浏览器 harness。** 仅三个按钮“前台运行/后台运行/取消”和 textContent 输出，不接生产 App，不进行视觉改版。显式走 Vite `/api` proxy；对 `/__test` 同样代理到宿主。harness 导出以下自动化入口：

```ts
interface AgentHarness {
  runForeground(): Promise<{ run: ReadonlyRunView; trace: RuntimeTrace }>;
  runBackground(): Promise<ReadonlyRunView>;
  cancel(): void;
}
declare global { interface Window { agentTest: AgentHarness } }
```

内部从 config 取得合成身份，用真实 login fetch、Task 11 client/host。后台 create 后 get 轮询有截止时间；关闭页面不取消 background。输出用户/模型文本只用 textContent，禁止 innerHTML。
- [x] **6. 生命周期验证与提交。** main 通过 stdout 输出一条 `{type:"ready",apiURL}`，日志走 stderr；stdin 接收到 `stop` 时取消、关闭 HTTP/Worker 和所有池，再 Fixture.Close 终止容器或删除核验身份后的本次临时库。不打印凭据；清理失败应报告非成功退出和精确临时名，不能声称已回收。运行 Host 测试和 `go build ./apps/api/cmd/agent-integration`（输出到临时目录，不遗留根目录二进制）；提交 `test(agent): add isolated authenticated integration host`。

## Task 15: 双宿主整体测试、浏览器验证与 CI 门禁

分页同源对照补充裁定：现有Service fixture新增同B账号一个前台HTTP Run，在后台Service两页读取并正常终态收尾后，通过同库真实Session/现有Router读取相同两页，比较完整CalendarReadData、cursor、50实体字段/顺序和SourceRefs。总计划19 Runs=Service B2+Host A10/B7，两个库严格串行，不新增endpoint/模型请求、不放宽限额。Go HTTP对照与Host浏览器TS Binding覆盖分别记录，不混称；本裁定只批准实现，不授权第三次live。完整同源对照仍待执行通过。

429默认预算验收裁定（优先于下方旧completed示例）：保留原input-dependent Fake完整Tool流程及16000总预算/未知用量预留，不添加提前end_turn分支。真实资产/ToolSpec/Runtime请求离线测得首轮raw5189、预留7749；429后该未知预留保留，成功重试已知用量15，次轮需8314而只剩8236，故整个Run应因预算拒绝failed，不保证completed。断言实际仅一次重试、已知/不完整Usage、无Calendar/业务变更、Outbox processed；精确Run/Trace错误码须沿真实Driver规范化链验证，不能直接套用Gateway admission的validation_failed。允许agentgateway包test-only预算/Driver回归，不修改产品策略；保留原始数值、命令及输出供主控复跑。Spec的“在剩余预算内最多重试一次”优先于Plan原先必定completed的假设；若裁定有误，代价是验收预期/覆盖重做并可能误报拒绝原因，不得放宽服务端预算。此裁定不授权第三次live。

前台显式取消窄修复裁定：离线行为 RED 已证明 caller Abort 先断开模型 transport，未改 HTTP 将断流落库为 failed/internal_error，随后 foreground GET 见终态而不再提交 cancel。允许修改 `apps/web/src/agent/host/foreground.ts` 及 `foreground.test.ts`，在本次显式用户取消的持久化收尾完成/失败/超时后再中止 transport；仍复用单次≤5秒独立收尾，timeout/interrupted立即传播，finally释放listener。必须验证cancel响应之前Provider已返回cancelled/error、Calendar已成功/失败的竞态，阻止晚结果触发新轮次/错误终态；不能重写执行后的Trace或把收尾上限充当1秒依赖退出证据。覆盖cancel失败/耗时上限，保留HTTP断连≠用户取消、服务端终态优先和原Runtime协议。此项为已批准取消语义的bugfix，不扩展服务端接口；若裁定有误，代价是前台编排/测试重做，取消可能继续竞态或迟至收尾上限，不能放宽服务端安全策略。仍需独立审查/真实门禁，不授权第三次live。

第二轮真实验收后的生命周期证据补充：仅 integration 私有 Host 在 StartIsolated 成功返回后立即记录本次精确库名/来源；正常 Close 与初始化失败清理共用 helper，只有实际 Fixture.Close 返回 nil 后输出 guarded cleanup 成功，失败保留受控失败及原错误，不能打印成功。版本由已创建本人 fixture 的现有 Migrator pool 在有限 context 下执行固定 current_setting('server_version') 取得，不新增 testdb getter/维护连接，不输出 DSN、密码、Cookie 或完整 Trace。补离线行为红绿回归；后续 exec/write_stdin 内外层预算都足够或分段续取，不补造第二轮已截断内容。此范围不改变生产 HTTP/Runtime/Service；若裁定有误，代价是私有日志/测试重做及清理状态仍须明确未知，不扩大共享数据库/角色操作，也不授权第三次 live。

实际验收拓扑裁定：同一编排入口允许两个严格串行独占fixture，任一时刻最多一个活动库。第一个由专用编译Go test binary执行精确Service Binding用例，真实API创建/Worker Claim与Binding、B账号1 Run；正常核验身份回收后才启动第二个Host fixture，其外部HTTP用例与浏览器合计A10/B7 Runs。仍保留真实账户限额。两个Go目标均验证恰好一次PASS/无SKIP，不把空filter或协作停止当验收成功。Service test binary用stdin协作stop、最多165秒收尾等待；Host保留35秒stop等待；失败/强杀仍明确清理未知，不按名称恢复。目标Go stdout有界实时转发，取消期间也保留原始guarded-cleanup尾部。拓扑如需推翻，代价是验收装配重做及一个额外临时库生命周期，不改变生产接口/角色/RLS或双端行为要求。

Playwright1.63隐私实现补充：关闭trace/screenshot/video后仍会自动把失败页面的ariaSnapshot写入error-context；harness会显示完整运行结果，因此编排的Playwright child还须固定 `PLAYWRIGHT_NO_COPY_PROMPT=1` 并有spawn参数回归。使用锁定版本已有的关闭条件，不修改依赖包或删减真实Trace；失败比较只报告受控差异路径。

浏览器取消验收细化：分别在受控Provider等待和Calendar等待期间，对前台/后台发起真实用户取消（共四个Run），先看到实际依赖进入，再从取消发起测量到依赖退出≤1秒；不能用operation账本终态或最多5秒收尾代替退出。允许integration私有Adapter迭代与CalendarStore.ListEvents边界按Run维护活动计数，defer在实际返回后减计数、归零即删除，通过原有认证/归属诊断增加activeDependencies；不增加核心回调/生产指标接口。后台页面Promise取消时按当前harness契约受控reject，单独检查持久化stopped/cancelled及后台真实Trace，不伪造resolve。Playwright trace/HAR/storageState/视频关闭，不把真实Cookie、登录body或完整Trace写文件；一致性比较保留实际数据，失败只输出差异路径/受控字段，避免全量Prompt/标题日志。

固定分页数据裁定：仅在合成账号B追加50条日程，窗口为 `2026-09-07T00:00:00Z` 至 `2026-09-08T00:00:00Z`，只读config增加pagingWindow；原 `2026-09-05`—`2026-09-06` A/B各一条保持不变。使用合法240字符、JSON需要转义的合成标题（例如 `<`），以limit25取得恰好两页并验证顺序/cursor/version，以limit50触发真实完整ToolResult的65536字节上限。该数据只用于合成fixture setup与只读边界测试，不调用真实模型，不修改Schema/角色。若需推翻，代价是固定seed和边界用例重做；错误窗口可能污染基线，因此保留原Task14一条事件断言。

真实 Fixture 边界补充：保留两个基线账号在原固定窗口各一条日程（Task14 跨账号隔离已有明确断言）；分页与64KiB输出超限应使用额外、不相交的固定合成数据窗口或经裁定的固定变体，不能改坏基线。limit=51是参数超限，不替代真实序列化结果超限；测试数据遵守VARCHAR240等实际数据库约束。用户取消必须单独验证前后台，不得复用 interrupted/run_timeout 当作取消。需要超过账号每分钟10次创建时，有界串行时间分隔或另建声明过的独占fixture，不能关限额或删场景。

会话到期测试专用入口裁定：只在独占集成宿主新增固定 `POST /__test/session/expire`，先通过真实Session、已知合成账号及合法Origin校验，body为空/严格空对象，不接受user/session ID。私有Migrator仅固定SQL更新当前AuthenticatedSession的精确user_id/session_id并验证一行；为满足expires_at>created_at，允许仅将该合成Session的created_at和expires_at同时置为有序的过去时间。不能改Cookie为假值、用撤销冒充到期、影响另一Session/账号或修改共享权限/核心SessionService。随后真实Router必须拒绝仍由浏览器发送的原HttpOnly Cookie。若此方案需推翻，代价是固定测试入口/用例重做；授权或范围实现错误可能使合成账号会话失效，入口绝不进入生产。

Trace recorder 具体边界：最多64条已完成 Driver 的 JSON 快照，每条≤4MiB、总量≤16MiB；生成副本后只存私有 JSON，不在通用日志/文件/报告写完整 Trace。诊断字段为 `trace:{status:"ready",value:{state,inputs,effects,modelTurns}}`；执行未返回为 pending，终态未执行 Driver 为 missing；快照/条数或总量/单条大小失败明确为 error 与 `trace_snapshot_failed` / `trace_capacity_exceeded` / `trace_size_exceeded`。不得截断、驱逐既有 ready、另建无界错误 map，容量耗尽可用固定 Host 级错误标志；观测失败不改变原 Process error 或队列行为。真实 Session 和 API Runs.Get 归属校验先于私有读取，资源随正常 Host.Close 释放。上限如不足，应让专项明确失败并复盘，不以删掉输入/效果绕过。

Task15 执行 Trace 裁定：实际 Background.Process 已从 Driver.Run 得到完整 Trace，但原方法只保留 State，现有持久化记录不足以还原 skill_list/load 与输入/效果顺序。新增内部 `(*Background).ProcessWithTrace(ctx,event) (*agentruntime.Trace,error)`，原 Process 委托并保持同一错误；单一实现体保留 Claim、校验、monitor join、终态收尾/协调、slot 释放。未运行 Driver 返回 nil trace；运行后即使持久化失败也返回其真实 Trace，但只能在既有完成/协调结束后交给调用方。Config 不加 TraceSink 回调，避免可变 State 在完成前被外部代码改写。仅 integration 私有 processor 包装于真实 ReadonlyAgentHandler 下，取得现成 Trace 后制作有界 JSON 快照，并原样返回执行 error，不改变队列确认；所属账号的已认证诊断可返回该实际 Trace，缺失/容量失败必须明确，不能截断或重建为成功。新增修改范围仅 `agenthost/background.go`、其窄回归测试及 integration 私有诊断/测试，Runtime/Service/生产 HTTP/Worker API 不改。比较只规范化临时 Run/call/turn 身份、宿主模式和控制时间元数据，保留日程时间窗/实体 ID/version、Scope、digest、事件顺序和错误。若需推翻，代价是内部返回接口、私有诊断与一致性测试重做；错误实现可能增加测试内存或泄露合成账号 Trace，因此必须有界并验证归属，不授权生产使用。

**Files:**

- Create: `apps/api/internal/agentintegration/conformance_test.go`、`failure_test.go`
- Create: `apps/web/tests/agent-integration/readonly.spec.ts`、`failures.spec.ts`、`playwright.config.ts`
- Create: `scripts/agent-integration.mjs`、`agent-integration.test.mjs`
- Modify: 根 package.json/package-lock.json、`.github/workflows/ci.yml`、`scripts/lib/agent-architecture-rules.mjs`、对应 guard tests、`scripts/validate-architecture.mjs`
- Create: `docs/testing/agent-phase2a-acceptance.md`

**Interfaces:** 脚本 `node scripts/agent-integration.mjs` 是唯一 Fake 整体入口，默认 Docker；`--database-source confighub` 显式选择授权的临时库模式并将来源传至 Go 子进程的 `DAYORDER_AGENT_TEST_DB_SOURCE`，不接受任意 URL/库名。保留 CI 的 `--require-docker`，与 confighub 组合时直接拒绝。package alias `test:agent-integration` 不硬编码 require-docker，以便本地追加数据库来源。Playwright config 从受控环境变量 `DAYORDER_AGENT_HARNESS_URL` 取地址，禁止默认访问生产。Fake 模式显式排除 real-provider.spec.ts；real 模式只运行该文件。脚本测试使用注入的 spawn/cleanup 函数，实际集成不可 stub 网络/数据库。

- [ ] **1. 写浏览器红测试。**

```ts
test("foreground runs skill and calendar tool without outbox", async ({ page }) => {
  await page.goto("/tests/agent-integration/harness/");
  const result = await page.evaluate(() => window.agentTest.runForeground());
  expect(result.run.status).toBe("completed");
  expect(result.trace.effects.filter(e => e.type === "execute_tool").map(e => e.toolCall?.name))
    .toEqual(["skill_list", "skill_load", "dayorder.calendar.read"]);
  expect(result.run.usage.totalTokens).toBe(60);
});
```

补充通过 `/__test/runs/{runId}` 断言 foreground Outbox=0、sourceRefs 对应真实版本、没有业务实体改动。后台在另一个页面创建后关闭该页面，由独立 browser context 查询同一账号 Run，最终 completed 且 Outbox processed。
- [ ] **2. 添加测试依赖并运行红测试。** `npm install --save-dev --save-exact @playwright/test@1.63.0`；`npx playwright install chromium`；运行新脚本，应在缺失实现/断言处失败，不能因没有实际匹配测试而返回成功。Vitest 配置排除 `tests/agent-integration/**`，避免将 Playwright 用例作为 Vitest 执行。
- [ ] **3. 实现脚本编排。** 先校验所选数据库来源，Docker 模式检查 Docker、ConfigHub 模式检查七个注入字段，缺依赖/配置 exit!=0；mkdtemp 创建专用目录→编译 integration binary→spawn binary→等待 ready JSON→启动 Vite 指定 proxy/strict port→等待 harness readiness→Go integration+Playwright。数据库 db_* 字段与专用 Provider key 仅定向传给需要的 Go 子进程，Vite/Playwright/browser 环境清除这些字段及各原生数据库 URL；用实际 spawn 参数断言证明没有泄漏。所有进程与 Fixture 都有退出/超时处理，finally 发送 stop，等待关闭，再有界终止残留子进程；不能把强杀宿主当作远程临时库已清理。删除仅限该脚本创建且已验证前缀/绝对路径的临时目录，数据库清理由 Fixture 的精确身份校验负责，不能清理工作区或既有用户数据库。禁止调用 `config:db:bootstrap` / `config:dev:*`；ConfigHub CLI 只注入测试数据库字段，不获取模型凭据。
- [ ] **4. 加跨端比较与故障矩阵。** 使用同一 Seed/Fake，比较输入/效果顺序、ToolResult/Usage/终态、错误；只规范化 runId/callId/绝对时间/executionMode 等已声明宿主差异，不排序事件或删掉错误。

```ts
for (const fault of ["tool_timeout", "run_timeout", "provider_disconnect", "provider_429", "complete_once", "commit_once", "interrupted"]) {
  test(`${fault} preserves the readonly boundary`, async ({ page }) => {
    await page.goto("/tests/agent-integration/harness/");
    await page.waitForFunction(() => Boolean(window.agentTest));
    const headers = { Origin: new URL(page.url()).origin };
    const injected = await page.request.post("/__test/fault", { headers, data: { fault } });
    expect(injected.ok()).toBe(true);
    const run = await page.evaluate(() => window.agentTest.runBackground());
    const read = async () => {
      const response = await page.request.get(`/__test/runs/${run.runId}`, { headers });
      expect(response.ok()).toBe(true);
      return await response.json() as TestRunState;
    };
    await expect.poll(async () => (await read()).outboxStatus).toBe("processed");
    const observed = await read();
    const failed = ["run_timeout", "provider_disconnect", "provider_429", "interrupted"].includes(fault);
    expect(observed.status).toBe(failed ? "failed" : "completed");
    if (fault === "tool_timeout") {
      expect(observed.calendarCalls).toBe(1);
      expect(observed.summary?.includes("未能完成查询")).toBe(true);
    }
    if (fault === "provider_disconnect") {
      expect(observed.providerCalls).toBe(1);
      expect(observed.errorCode).toBe("provider_unavailable");
    }
    if (fault === "run_timeout") expect(observed.errorCode).toBe("timeout");
    if (fault === "provider_429") {
      expect(observed.errorCode).toBe("provider_unavailable");
      expect(observed.providerCalls).toBe(2); // attempts, not persisted Turn count
      expect(observed.attempts).toEqual([2]);
      expect(observed.usage.totalTokens).toBe(15);
      expect(observed.usageComplete).toBe(false);
      expect(observed.calendarCalls).toBe(0);
    }
    if (["complete_once", "commit_once"].includes(fault)) expect(observed.providerCalls).toBe(4);
    if (fault === "complete_once") expect(observed.deliveries).toBeGreaterThanOrEqual(2);
    if (fault === "interrupted") {
      expect(observed.errorCode).toBe("internal_error");
      expect(observed.providerCalls).toBeLessThanOrEqual(1);
    }
  });
}
```

TestRunState 使用 Task 14 定义，读取真实 operation/Outbox 与计数，不在诊断 handler 中按 fault 名伪造期望结果。故障按下一次 Run 隔离，并在测试结束清除；每个测试用新账号或等待上一 Run 终态，避免账户并发/速率限制污染场景。

覆盖未登录/过期 Session、恶意 Origin、伪造 device、双用户隔离、越窗、entityIds、篡改 cursor/Profile/ToolSpec、两页与超限、流中止/UTF-8/sequence、1 秒取消、deadline、重复 finish、unknown Usage。前台正常/Tool timeout/Run timeout/取消与后台均比较，不仅比成功场景。
- [ ] **5. 加 guard 与 CI。** AST/Go 扫描守护现有生产入口不导入 agentintegration/不调用新构造器；新增反例使用别名、多行、字符串/注释伪造，不退回关键词扫描。新 `agent-integration` job 使用仓库已有固定 SHA 的 checkout/setup-node/setup-go，Node 24.15.0/Go 1.25，npm ci、Playwright chromium、`npm run test:agent-integration`；同时将现有 CI 相关 Node 24.7.0 升到已验证且受 jsdom 支持的 24.15.0。不注入 Provider 密钥；Docker/浏览器缺失失败。
- [ ] **6. 最终自动化验证并提交。**

```text
npm run agent:generate:check
npm run db:generate:check
npm run test:agent-foundation
npm run test:agent-integration
npm run typecheck
npm run test:web
npm run test:api
npm run test:architecture
npm run build
go vet ./apps/api/...
go test -race ./apps/api/internal/agentgateway ./apps/api/internal/agenthost ./apps/api/internal/service ./apps/api/internal/worker
```

验收报告记录 commit、实际命令/退出状态、数据库来源/实际 PostgreSQL 版本/临时资源回收、浏览器环境、未执行项；不得用旧测试计数替代本期结果。CI 保持 Docker 且不注入 ConfigHub 凭据；本地显式 ConfigHub 仅运行本期专用用例，旧全量数据库用例不自动切换共享实例。提交 `test(agent): gate readonly cross-host integration in ci`。

## Task 16: 真实 DeepSeek 冒烟与最终范围核对

**Files:** Modify `scripts/agent-integration.mjs`、`agent-integration.test.mjs`、`docs/testing/agent-phase2a-acceptance.md`；必要时新增仅 real 模式执行的 `apps/web/tests/agent-integration/real-provider.spec.ts`。

**Interfaces:** 命令 `node scripts/agent-integration.mjs --require-docker --real-provider --confirm-synthetic`；本地授权的临时库模式将 `--require-docker` 替换为 `--database-source confighub`。模型部分只读用户专门提供的 `DAYORDER_AGENT_TEST_PROVIDER_KEY` 与可选 `DAYORDER_AGENT_TEST_MODEL`，默认 deepseek-v4-flash；数据库模式的批准不授权从 ConfigHub 或任何现有 `.env` 猜测模型凭据。没有 --confirm-synthetic 或 key 时在创建任何真实模型请求前失败。

- [ ] **1. 写模式门禁红测试。** 脚本导出 `validateRunMode(args,env)`，测试不包含密钥值：

```js
assert.throws(() => validateRunMode(["--real-provider"], {}), /confirm-synthetic/);
assert.throws(() => validateRunMode(["--real-provider", "--confirm-synthetic"], {}), /test provider key/);
const mode = validateRunMode(["--require-docker"], {});
assert.equal(mode.realProvider, false);
```

- [ ] **2. 运行红测试。** `node --test scripts/agent-integration.test.mjs`。
- [ ] **3. 实现显式 real 模式并运行本地门禁绿测试。** 真实 key 只传给 Go child 的定向环境，不传 Vite/browser/命令行/报告；从父进程转发环境时为 Vite 删除 Provider key，Fake 模式不读 key。real 模式不运行 Fake 固定文字/Usage=60 断言，断言实际发生至少一个日程 Tool、轮次 Usage 完整、返回实体属于合成账号、前后台各自终态正常。两次 Run 总调用量受已有预算上限限制，不重试整个验收。
- [ ] **4. 在具备专用测试凭据时运行真实冒烟。** 只运行上述显式命令，记录前台/后台 Model/协议版本、实际 Tool 与 Usage、结果，不记录凭据或完整 Prompt。若缺凭据，停止真实厂商步骤并向用户准确说明缺项；不得把它勾选完成，也不得称 Phase 2A 全部验收完成。
- [ ] **5. 核对并提交验收文档。** 验证 Spec 各项映射到以下覆盖表，确认没有生产开关/写 Tool/计划调度/子 Agent/Native 偷跑。提交 `docs(agent): record phase2a acceptance evidence`（只有真实执行的项目写通过）。不自动 push、merge 或开启生产。

## Spec 覆盖表与完成定义

| Spec | 实施任务 |
| --- | --- |
| §1–3 分工、集成隔离、生产关闭 | 1、10、12、14、15 |
| §4 内置 Skill、digest、冻结能力 | 3、5、11、12 |
| §5 ToolSpec、Scope、双 Binding | 3、6、7、10、11、15 |
| §6 完整 Turn、历史关联、单调用 | 1、2、8、9、15 |
| §7 Gateway、SSE、重试、预算 | 6、8、9、10、11、16 |
| §8 Run、信任边界、持久化 | 4、5、6、10、11 |
| §9 后台触发、执行权、中断/终态 | 5、6、12、14、15 |
| §10 超时与取消 | 2、7、9、11、12、15 |
| §11 观测、安全关闭 | 13、14、15 |
| §12 自动化与真实厂商验收 | 14、15、16 |
| §13 分期、不扩范围 | 所有任务，最终核对 16 |

Task 1–15 通过只能报告“自动化集成已通过”。Task 16 实际远程冒烟也通过、验收记录完整，才能报告 Phase 2A 完成。checkbox 仅在对应实现、测试和审查完成后勾选；实施进度与未执行条件以上方检查点为准，不将规划或跳过测试当成执行证据。
