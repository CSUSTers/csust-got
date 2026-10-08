# 部署检查清单

一页纸：给升级 dev 镜像的运维，和改了部署面的 PR 作者。规则本身在 `AGENTS.md`「部署文档随功能走」，PR 勾选项在 `.github/PULL_REQUEST_TEMPLATE.md`。

## 运维：升级新 dev 镜像前

逐项对照 `git log <当前版本>..<目标版本> -- config.yaml docs/ docker-compose.yml Dockerfile`，以及各 PR 描述里的「部署影响」段。

| 检查项 | 看哪里 | 不做会怎样 |
| --- | --- | --- |
| 新增 / 改名 / 删除的配置键 | 目标版本 `config.yaml` 与各 `docs/<feature>.md`「部署与配置」段的 diff | 新键按默认值运行（行为可能变化）；被删或改名的旧键启动时以 warning 记入日志，功能静默失效 |
| 新增挂载 | `docker-compose.yml` `volumes`、`Dockerfile` `VOLUME`、功能文档 | 数据落在容器可写层，重建容器即丢；bind 挂载 `create_host_path: false` 时宿主目录不存在直接启动失败 |
| 新增 / 必填环境变量 | `docker-compose.yml` `environment`（`${VAR:?...}` 为必填）、功能文档 | 必填项缺失 Compose 拒绝启动；可选项缺失按默认值运行 |
| 时区（TZ）依赖 | 功能文档是否声明依赖本地时间（cron、定时清理、日期格式化） | 镜像默认 `TZ=Asia/Shanghai`，Compose 用 `BOT_TZ` 覆盖；与 Redis 中已存的时间戳口径不一致会导致任务错时或重复触发 |
| Redis key 布局与策略 | 功能文档中的 key 前缀、TTL；`redis.conf` 的 `maxmemory-policy`（当前 `allkeys-lfu`）与 `save` 规则 | key 布局变更需要迁移或双读，否则旧数据不可见；`allkeys-lfu` 下无 TTL 的 key 也可能被淘汰，不要把它当持久存储 |
| 后台任务 | 功能文档中的队列 / 定时任务说明 | 多实例共用一个 Redis scope 时重复执行；需要共享卷的任务见对应文档的锁要求 |
| 自动更新器（Watchtower 等） | 该部署是否对 `csust/csust-got:latest` 启用了自动拉取 | 新镜像会在无人值守时上线，上面所有项都没人检查。要么关掉 bot 服务的自动更新、钉住 tag，要么确认每个合入 dev 的 PR 都带了部署文档 |
| 回滚路径 | 目标版本每个「部署与配置」段的「回滚方式」 | 不知道回滚后旧版本能否读新 key 布局 / 新挂载内的数据 |

升级后看启动日志：有未知配置键 warning，先对照文档确认是文档漏写还是键已删除，再决定是否清理 `config.yaml`。

## PR 作者：改了部署面要做什么

1. 在对应 `docs/<feature>.md` 增加或更新「部署与配置」段，使用下方模板。
2. 同步更新仓库内的 `config.yaml` 示例（新键带默认值或注释）。
3. 若涉及挂载 / 环境变量，同步更新 `docker-compose.yml`（必要时 `Dockerfile`）。
4. 在 PR 描述勾选模板里的部署检查项，并在「部署影响」段写明升级前要做什么、怎么回滚。
5. 改键名或删键时，在本地用旧 `config.yaml` 启动一次，确认 warning 日志指向正确的键。

## 「部署与配置」段模板

复制到 `docs/<feature>.md`。每一行一个键 / 挂载 / 变量；没有的列写「无」，不要留空。

```markdown
## 部署与配置

| 键名 / 路径 | 类型 | 默认值 | 不配置时的行为 | 需要挂载 / 环境变量 / TZ |
| --- | --- | --- | --- | --- |
| `feature.enabled` | 配置项 | `false` | 功能关闭，不注册 handler | 无 |
| `feature.directory` | 配置项 | `./data/feature` | 使用默认目录 | 需要挂载：多实例须为同一共享卷 |
| `/app/data/feature` | 挂载 | 无 | 数据写入容器可写层，重建即丢 | 需要挂载 |
| `FEATURE_TOKEN` | 环境变量 | 无（必填） | 启动失败 | 需要环境变量 |

Redis key：`<prefix>:feature:c<chatID>`，TTL `24h`；无 TTL 的 key 请注明依赖 `maxmemory-policy`。

时区：是否依赖 `TZ`，依赖的话影响哪些行为。

### 对已有部署的迁移步骤

1. 升级前：
2. 升级后：

### 回滚方式

- 回滚到 `<版本>` 后，新 key / 新目录内的数据如何处理（可忽略 / 需手动清理 / 旧版本会误读）。
```
