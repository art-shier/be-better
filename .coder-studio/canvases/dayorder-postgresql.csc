{
  "version": 1,
  "kind": "architecture_canvas",
  "title": "DayOrder PostgreSQL 关系模型",
  "document": {
    "summary": "DayOrder 面向公开多用户个人服务的单机 Docker Compose 架构：React/IndexedDB 通过 Caddy 访问 Go API，PostgreSQL 关系表与 RLS 隔离用户，Worker 消费事务 Outbox，pgBackRest 和 Prometheus 提供恢复与可观测性。",
    "diagram": {
      "dsl": "mermaid",
      "source": "flowchart LR\nUser[浏览器用户]\nCaddy[Caddy TLS 与 SPA 入口]\nWeb[React 客户端与 IndexedDB]\nAPI[Go API 资源事务与增量同步]\nWorker[Go Worker 邮件 提醒 Agent]\nPG[PostgreSQL 17 关系表与 RLS]\nOutbox[事务 Outbox 与幂等队列]\nBackup[pgBackRest 加密备份与 WAL]\nMetrics[Prometheus 指标与告警]\nUser -->|HTTPS 80 和 443| Caddy\nCaddy -->|静态资源| Web\nWeb -->|Cookie 资源 CRUD 同步游标| Caddy\nCaddy -->|API 与健康检查| API\nAPI -->|带用户上下文的事务| PG\nAPI -->|同事务写入| Outbox\nOutbox -->|表内持久化| PG\nWorker -->|领取 重试 完成| Outbox\nWorker -->|RLS 数据读取与确认写入| PG\nPG -->|物理备份与 WAL 归档| Backup\nAPI -->|HTTP 与连接池指标| Metrics\nWorker -->|Outbox 与后台任务指标| Metrics\nBackup -->|备份和恢复指标| Metrics"
    },
    "annotations": [
      {
        "title": "首版业务边界",
        "body": "这是公开多用户的个人服务，每个账户只管理自己的目标和任务；首版没有组织、团队、成员共享或协同编辑。"
      },
      {
        "title": "客户端数据模型",
        "body": "游客数据只在 localStorage；登录账户的 entities、mutations、syncMeta 和 accounts 按 accountId 存入 IndexedDB。同步按实体 Mutation 和游标增量收敛，不再每 500 ms 上传整份 JSON。"
      },
      {
        "title": "PostgreSQL 关系域",
        "body": "身份域、目标/任务/日程、记录/笔记/复盘、Agent/审计、同步/幂等分别使用普通列、外键和关联表。JSONB 只用于设置、Agent scope/patch、审计快照和 Outbox payload。"
      },
      {
        "title": "强制租户隔离",
        "body": "所有用户资源包含 user_id，跨表引用带 user_id 复合外键；API/Worker 连接角色不具备 BYPASSRLS，并在事务内设置 dayorder.user_id。PostgreSQL RLS 是应用过滤之外的最后隔离层。"
      },
      {
        "title": "可靠后台任务",
        "body": "API 在业务事务内写 Outbox；Worker 使用锁令牌、SKIP LOCKED、幂等重试和 dead 状态消费邮件、提醒与 Agent 任务。生产 Agent Provider 必须使用 HTTPS。"
      },
      {
        "title": "单机企业运行基线",
        "body": "PostgreSQL 不发布主机端口，data 网络为 internal；容器非 root、只读根文件系统并移除 capabilities。pgBackRest 加密备份与 WAL 归档目标 RPO 不超过 5 分钟、RTO 不超过 60 分钟；Prometheus 仅绑定本机。"
      }
    ]
  }
}
