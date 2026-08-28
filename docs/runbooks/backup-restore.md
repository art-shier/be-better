# PostgreSQL 备份与恢复演练

生产数据库使用 pgBackRest：每天完整备份、工作日差异备份、每小时增量备份，并持续归档 WAL。`archive_timeout = 5min` 把无写入时的 WAL 切换上限控制为五分钟；目标是 RPO ≤ 5 分钟、RTO ≤ 60 分钟。

备份仓库位于独立 Docker Volume，并使用 AES-256-CBC 加密。这能防止普通文件泄露，但不能替代异地副本。生产必须把 pgBackRest 仓库持续复制到另一可用区或对象存储；单机磁盘损坏时，同机 Volume 不算备份。

## 计划任务

在宿主机计划任务中使用以下命令。先确认 `.env.production` 和 secret 权限正确：

```powershell
# 每天一次完整备份
pwsh -File deploy/scripts/backup-check.ps1 -RunBackup -BackupType full

# 其他工作日差异备份
pwsh -File deploy/scripts/backup-check.ps1 -RunBackup -BackupType diff

# 每小时增量备份
pwsh -File deploy/scripts/backup-check.ps1 -RunBackup -BackupType incr

# 每 15 分钟只检查仓库、归档与最新备份年龄
pwsh -File deploy/scripts/backup-check.ps1
```

脚本调用 `pgbackrest check`、解析最新完成备份，并原子写入 `deploy/metrics/dayorder_backup.prom`。Node Exporter textfile collector把结果交给 Prometheus。检查失败会写 `dayorder_backup_check_success 0` 并以非零状态退出。

每次备份后还要把仓库同步到异地位置，并在目标端校验对象数量、加密和不可变保留策略。不要只根据“上传成功”判断备份可恢复。

## 月度恢复演练

恢复脚本只允许删除固定名称 `dayorder-postgres-restore` 的隔离 Volume，不会接触生产 `postgres_data`：

```powershell
pwsh -File deploy/scripts/restore-drill.ps1 -Force
```

需要检查恢复后的数据时可保留隔离 Volume：

```powershell
pwsh -File deploy/scripts/restore-drill.ps1 -Force -KeepRestoredVolume
```

若用户删除流程产生了待重放的删除清单，先由两人复核 SQL，再传入：

```powershell
pwsh -File deploy/scripts/restore-drill.ps1 -Force -DeletionManifest C:\secure\deletions.sql
```

演练流程：

1. 停止并删除旧的隔离恢复容器。
2. 精确删除并重建 `dayorder-postgres-restore`。
3. 从最新完整/差异/增量链恢复，并重放归档 WAL。
4. 启动隔离 PostgreSQL，等待健康检查。
5. 可选重放账号删除清单。
6. 验证 migration 版本、RLS 标志和 Outbox 聚合函数。
7. 记录耗时到 `dayorder_restore.prom`；超过 60 分钟直接失败。
8. 默认清除隔离容器和 Volume。

阶段 8 的安全验收还会在恢复库上执行两用户隔离与核心资源 API 测试。月度演练报告需记录备份标签、恢复目标时间、最新 WAL、实际 RPO/RTO、验证结果和执行人。

## 生产恢复

生产灾难恢复必须先冻结写入并保留故障卷，不得直接在原卷上试错。创建新 Volume 完成恢复和验收，确认目标时间点及用户删除清单均正确后，再把 API/Worker 切换到新库。切换后保留旧卷直到业务验收与备份完成。

以下情况必须停止恢复并升级处理：备份链校验失败、加密密钥不匹配、WAL 缺口、migration 脏版本、RLS 未启用、删除清单失败或恢复结果晚于允许的业务时间点。
