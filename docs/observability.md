# 日志、Trace 与关停

Bot 进程的文件日志、Agent v3 trace JSONL 的落盘方式，以及收到退出信号后的关停顺序。

## 文件日志轮转

`log_file_dir` 非空时，日志同时写到 stderr 与 `<log_file_dir>/got.log`（zap 内部错误写 `got_err.log`）。两个文件都由 lumberjack 按大小与天数轮转，旧文件命名为 `got-<时间戳>.log`，默认 gzip 压缩。stderr 输出保持不变；对 stderr 调用 `Sync` 时返回的 `EINVAL` / `ENOTTY` / `EBADF` 会被忽略，不再在退出时打印 `Logger Sync failed`。进程退出时 `log.Close()` 会关闭这两个文件句柄（Windows 上未关闭的句柄会阻止删除或移动日志目录）。

## Trace JSONL 异步写入

`agent_v3.observability.enable` 为 true 时，每轮 trace 的 JSON 会投递到进程内的有界队列，由单个写入 goroutine 顺序追加到 `jsonl_path`，并由 lumberjack 按大小轮转。队列满时该条记录被丢弃并计数；丢弃会在 warning 日志中带 `dropped_total` 字段，进程退出时若有丢弃也会汇总打印一条 warning。目录权限保持 `0700`，文件（含轮转出的备份）保持 `0600`。

退出时 `agentv3.Close()` 先关闭队列并等待写入 goroutine 把剩余记录刷盘（最长 5s），再关闭 session 服务与其他资源。

## 关停顺序

收到 `SIGINT` / `SIGTERM` 后：

1. `bot.Stop()` 停止拉取更新；已经在处理的消息 handler 继续运行。
2. 主进程等待所有进行中的交互式 agent 轮次结束，最长 `agent_v3.shutdown_grace`；超时则记录 warning 并继续。
3. `agentv3.Close()`：刷 trace 队列 → 关闭 session 服务 → 停止 cron → 关闭 MCP 连接。
4. `log.Close()`：`Sync` 后关闭 `got.log` / `got_err.log` 的文件句柄。

容器编排的 `stop_grace_period`（Compose）或 `terminationGracePeriodSeconds`（k8s）应大于 `shutdown_grace` 加几秒，否则进程会在 drain 完成前被 `SIGKILL`。

## 部署与配置

| 键名 / 路径 | 类型 | 默认值 | 不配置时的行为 | 需要挂载 / 环境变量 / TZ |
| --- | --- | --- | --- | --- |
| `log.max_size_mb` | 配置项 | `100` | 单个日志文件写满 100 MB 后轮转 | 无 |
| `log.max_backups` | 配置项 | `7` | 最多保留 7 个轮转文件，多余的最旧先删 | 无 |
| `log.max_age_days` | 配置项 | `14` | 超过 14 天的轮转文件删除 | 无 |
| `log.compress` | 配置项 | `true` | 轮转文件 gzip 压缩 | 无 |
| `agent_v3.observability.trace_max_size_mb` | 配置项 | `50` | trace JSONL 写满 50 MB 后轮转 | 无 |
| `agent_v3.observability.trace_max_backups` | 配置项 | `5` | 最多保留 5 个轮转 trace 文件 | 无 |
| `agent_v3.observability.trace_queue` | 配置项 | `256` | 异步写入队列容量 256 条，满则丢弃并计数 | 无 |
| `agent_v3.shutdown_grace` | 配置项 | `60s` | 退出信号后最多等 60s 让进行中的轮次完成 | 无；编排层 stop grace 需大于该值 |
| `<log_file_dir>/` | 挂载 | `logs` | 日志与 trace 在容器可写层，重建即丢 | 需要挂载（已有） |

轮转文件名使用本地时间（`LocalTime: true`），依赖进程 `TZ`，只影响备份文件名，不影响内容。

Redis key：无新增。

### 对已有部署的迁移步骤

1. 升级前：确认 `logs/` 挂载有足够空间容纳 `max_backups × max_size_mb`（默认约 700 MB 日志 + 250 MB trace，压缩后更少）。现有超大的 `got.log` 不会被切分，会在下一次达到阈值时整体轮转为备份；需要立即释放空间可先手动截断。
2. 升级后：观察启动日志是否有未知配置键 warning；若关停时出现 `in-flight agent turns did not finish within grace`，适当调大 `shutdown_grace` 或编排层 stop grace。

### 回滚方式

- 回滚后旧版本不认识上述新键，启动时按 warning 记录并忽略；轮转出的 `got-*.log(.gz)`、`agentv3-traces-*.jsonl` 备份文件旧版本不会读取也不会清理，可手动删除。
