# Agent 群记忆（memory）与上下文缓存写入策略

群记忆由 `/memory add|list|forget` 和用户消息里的显式「记住：…」写入，渲染成 `<group_memory_snapshot>` 注入模型输入（不在 system prompt 中）。

## 写入策略

| `write_policy` | 管理员 | 非管理员 |
| --- | --- | --- |
| `explicit_or_admin`（默认） | 可写 / 查看 / 删除 | 一律拒绝，回复「只有管理员可以写入群记忆。」 |
| `explicit_quota` | 不限 | 每人最多 `max_entries_per_user` 条；可 `list` 自己的条目、`forget` 自己添加的条目 |

- 私聊中所有人视为管理员；群内管理员判定为「可限制成员」权限。
- 容量：所有条目按 `- 内容` 行累计，超过 `snapshot_max_tokens × 4` 字符即视为写满。写满后新写入被**拒绝并明确回复**
  （命令与「记住：」两条路径都一样），不再静默成功。
- 配额与容量检查和写入在同一个 Redis 事务里完成（`orm.AgentV3AddMemoryChecked`：WATCH `memory:active` 及全部条目 key，
  检查通过后 MULTI 写入，冲突时最多重试 5 次）。并发写入不会突破 `max_entries_per_user` 或容量；重试耗尽返回
  `ErrAgentV3StateConflict`，用户看到「写入 memory 失败，请稍后重试」。
- snapshot 超出预算时（例如升级前已经写满）**丢弃最旧条目**、保留最新条目，并在开头标注 `[earlier memory omitted: N entries]`。
- memory 项与 snapshot **不再设置 TTL**：升级后首次写入 / 删除会把现有 memory key 转为持久 key。

## 会话中的 memory snapshot 只追加、不改写

session 命中时归档里的历史 `<group_memory_snapshot>` 会原样回放（保证 prompt cache 前缀对齐），不会被删除或改写。本轮用**当前 memory**
与回放历史中**最后一条** snapshot 的正文比较：

| 情况 | 追加的消息 |
| --- | --- |
| 历史中没有 snapshot，且 memory 非空 | 普通 `<group_memory_snapshot>` |
| 正文相同 | 不追加 |
| 正文不同，memory 非空 | `<group_memory_snapshot supersedes="earlier">`，header 声明它取代此前所有 snapshot |
| memory 已清空（例如 `/memory forget` 删光） | `<group_memory_snapshot cleared="true">群记忆已清空，忽略此前所有记忆快照。</group_memory_snapshot>` |

追加的消息位于回放之后、本轮输入之前，并作为 history 归档进本节点，后续轮次同样原样回放；下一次比较以这条新 snapshot 为准。

## 会话命中且发布成功时不再重复写入

当某轮成功加载了完整会话 DAG，**并且**本轮节点发布成功（`tc.Session.parent != nil && tc.Session.committed`），这一轮**不再**追加 raw turns，
也不再重建 rolling summary，只保存 Telegram 回复消息（`SaveResponse`）。raw turns / summary 只是会话不可用时的回退上下文，session hit 的内容已经在 DAG 中。
若加载了父节点但 commit 失败（Redis / 文件 / 10 秒超时），这一轮仍走 raw turns + summary 的回退保存，避免该轮在两边都丢失。
后果：在 reply 续聊之后再用 `load_context=false` 的命令触发时，回退上下文看不到成功发布的 session 轮次。

## 已删除的 Redis key

| key | 说明 |
| --- | --- |
| `<prefix>:agentv3:<bot>:tg:<chatID>:prefix:<version>:messages` | 从未被读取，不再写入；旧 key 按原 TTL（默认 30d）自然过期 |
| `<prefix>:agentv3:<bot>:tg:<chatID>:memory:snapshot:<version>` | 从未被读取，不再写入；旧 key 按原 TTL 自然过期 |

仍在使用：`…:prefix:current:<agent>:<model>`、`…:memory:item:<id>`、`…:memory:active`、`…:memory:snapshot:current`。

## 部署与配置

| 键名 / 路径 | 类型 | 默认值 | 不配置时的行为 | 需要挂载 / 环境变量 / TZ |
| --- | --- | --- | --- | --- |
| `agent_v3.memory.write_policy` | 配置项 | `explicit_or_admin` | 仅管理员可写；其它取值回退为默认并记 warning | 无 |
| `agent_v3.memory.max_entries_per_user` | 配置项 | `20` | 仅 `explicit_quota` 下生效 | 无 |
| `agent_v3.memory.snapshot_max_tokens` | 配置项 | `2000` | 约 8000 字符容量；写满拒绝，snapshot 保留最新 | 无 |

Redis key：`<prefix>:agentv3:<bot>:tg:<chatID>:memory:*`，**无 TTL**。当前 `redis.conf` 为 `allkeys-lfu`，无 TTL 的 key 在内存不足时仍可能被淘汰，不要把它当成持久数据库；需要保留请定期 `/memory list` 导出或开启 RDB/AOF。

时区：不依赖 `TZ`。

### 对已有部署的迁移步骤

1. 升级前：无需操作。若希望普通成员也能写记忆，在 `config.yaml` 设置 `write_policy: explicit_quota`。
2. 升级后：以前任何人发「记住：…」都会写入记忆，现在默认只有管理员可写，非管理员会收到一条拒绝回复；请告知群成员。
3. 已经超过容量的群，下次写入 / 删除触发重建时 snapshot 会改为保留最新条目。

### 回滚方式

- 回滚到旧版本后：`max_entries_per_user` 被忽略，`explicit_quota` 会被重置为 `explicit_or_admin` 并记 warning；
  memory key 仍保持无 TTL（旧版本的重建会重新给它们加上 `context_cache.redis_ttl`）；旧版本会重新开始写 `prefix:<v>:messages` 与 `memory:snapshot:<v>` key，无需手动处理。
