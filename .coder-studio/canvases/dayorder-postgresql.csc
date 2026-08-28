{
  "version": 1,
  "kind": "architecture_canvas",
  "title": "DayOrder PostgreSQL 关系模型",
  "document": {
    "summary": "DayOrder 从整份 AppData 快照改造为 PostgreSQL 资源级关系模型；users 是数据隔离根，业务实体独立存储，设置保留 JSONB。",
    "diagram": {
      "dsl": "mermaid",
      "source": "flowchart TB\nUser[users 用户账户]\nSession[sessions 登录会话]\nSettings[user_settings 用户设置 JSONB]\nGoal[goals 目标]\nMilestone[goal_milestones 里程碑]\nTask[tasks 任务]\nEvent[calendar_events 日程]\nRecord[records 快速记录]\nNote[notes 笔记]\nReview[daily_reviews 每日复盘]\nAgentRun[agent_runs Agent运行]\nAgentDetail[agent_steps 和 agent_changes]\nAudit[audit_events 审计日志]\nUser -->|一对多| Session\nUser -->|一对一| Settings\nUser -->|租户隔离| Goal\nUser -->|租户隔离| Task\nUser -->|租户隔离| Event\nUser -->|租户隔离| Record\nUser -->|租户隔离| Note\nUser -->|租户隔离| Review\nUser -->|租户隔离| AgentRun\nUser -->|租户隔离| Audit\nGoal -->|一对多| Milestone\nGoal -->|可选归属| Task\nGoal -->|可选关联| Event\nRecord -->|可转化来源| Task\nAgentRun -->|一对多| AgentDetail\nAgentDetail -->|确认后变更| Task\nAgentDetail -->|确认后变更| Event\nAgentDetail -->|确认后变更| Note\nAgentDetail -->|产生| Audit"
    },
    "annotations": [
      {
        "title": "不是每种类型都建表",
        "body": "具有独立生命周期、需要查询索引或需要外键约束的业务实体建表；纯值对象可作为列或受控 JSONB。"
      },
      {
        "title": "用户隔离",
        "body": "所有用户拥有的业务表都包含 user_id，并使用复合约束防止跨用户关联；后续可启用 PostgreSQL Row Level Security。"
      },
      {
        "title": "JSONB 边界",
        "body": "JSONB 主要用于 user_settings、权限配置和少量不参与关键查询的扩展元数据，不再保存整份 AppData。"
      },
      {
        "title": "同步方式",
        "body": "前端从 PUT /state 整包覆盖改为资源级 CRUD 与离线操作队列；每次只同步发生变化的实体。"
      }
    ]
  }
}
