{
  "version": 1,
  "kind": "architecture_canvas",
  "title": "DayOrder 单机生产架构",
  "document": {
    "summary": "DayOrder 首期单台云服务器 Docker Compose 生产拓扑；公网只暴露 Caddy，PostgreSQL、API 与 Worker 位于内部网络，备份必须异地保存。",
    "diagram": {
      "dsl": "mermaid",
      "source": "flowchart TB\nUser[公网用户浏览器]\nCaddy[Caddy TLS终止 静态Web 反向代理]\nAPI[Go API 资源接口 认证 同步]\nWorker[Go Worker 提醒 Agent Outbox]\nPostgres[PostgreSQL 主数据库]\nBackup[pgBackRest 备份与WAL归档]\nObjectStorage[异地S3兼容对象存储]\nMetrics[Prometheus与外部可用性监控]\nUser -->|HTTPS 443| Caddy\nCaddy -->|静态资源| User\nCaddy -->|/api/v1| API\nAPI -->|事务与RLS| Postgres\nWorker -->|领取Outbox任务| Postgres\nPostgres -->|全量 增量 WAL| Backup\nBackup -->|加密上传| ObjectStorage\nAPI -->|指标与结构化日志| Metrics\nWorker -->|队列延迟与失败指标| Metrics\nPostgres -->|连接与查询指标| Metrics"
    },
    "annotations": [
      {
        "title": "公网边界",
        "body": "服务器只开放 80/443 和受限 SSH；PostgreSQL 不映射宿主机公网端口。"
      },
      {
        "title": "单点故障",
        "body": "这是生产级单机架构而非高可用架构；服务器或云可用区故障会中断服务，恢复依赖异地备份。"
      },
      {
        "title": "可靠异步任务",
        "body": "API 在业务事务中写入 Outbox，Worker 使用 PostgreSQL 锁领取任务；首期无需引入 Redis。"
      },
      {
        "title": "恢复能力",
        "body": "数据库卷不等于备份；使用 pgBackRest 和连续 WAL 归档到异地对象存储，并定期执行恢复演练。"
      }
    ]
  }
}
