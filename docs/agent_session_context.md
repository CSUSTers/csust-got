# Agent 完整模型会话上下文

完整模型会话让你通过回复 bot 的某条回答，接着那一条回答的模型历史继续对话。历史包含工具调用与结果，而不只是 Telegram 上可见的问答。回复同一条旧回答可以产生不同分支，各分支共享祖先，不混入彼此后续的对话。

`context_mode: chat` / `reply_chain` 决定未加载完整会话时的上下文来源。后者从 Telegram 回复链构建可见消息上下文，不等于恢复模型的完整工具会话，详见 [reply-chain 文档](agent_reply_sessions.md)。

## 配置与续聊方式

```yaml
agent_v3:
  session:
    directory: data/agent-sessions
    ttl: "24h"
agents:
  - name: example
    session:
      save_context: true
      load_context: false
    # 保留现有 model、agent、trigger、filters、format 等配置。
```

每个 agent 的 `session.save_context` 缺省为 **true**，显式 false 可关闭非 reply 调用的新会话归档；`session.load_context` 缺省为 **false**，独立控制是否加载已保存的完整会话。只有**实际 reply 分支触发**的本次调用强制 save/load 为 **true/true**，即使两项都显式 false，也不会修改共享 agent 配置。

带回复消息的 command 或 regex 仍是非 reply。同一条 trigger 同时配置 command/regex/reply 时，以本次实际选中的分支为准。现有 command 优先、regex 先于 reply、首个匹配项优先及 caption 处理不变；全局白名单、agent filters 和 agent 启用条件仍适用。

默认设置适合“每次命令独立提问，回复回答时续聊”。若希望普通命令或 regex 也自动接着该 agent 的上一轮对话，设置 `load_context: true`。这两个开关不能禁止实际 reply 续聊，也不是停止所有聊天数据保存的隐私总开关。

全局 `agent_v3.session.directory` 缺省为 `data/agent-sessions`，相对路径以进程启动工作目录为基准；`agent_v3.session.ttl` 缺省为 `24h`。目录和 TTL 的空字符串按缺省处理。非空 TTL 必须是严格为正的 Go duration 字符串，例如 `"24h"`、`"1h30m"`；不支持 `"1d"`，非法、溢出、零和负值都拒绝，不静默替换成默认值。部署前确认目录可写，并具备下文所述的锁和原子文件操作能力。

## 如何选择历史

| 实际触发 / 开关 | 加载规则 | 保存规则 |
| --- | --- | --- |
| reply | 只查当前 scope 下被回复的那条 bot 消息；允许同 chat 跨 agent、跨成员分叉 | 强制保存 |
| command / regex，load=true | 同 scope、同 agent 最近**成功提交**节点；最近按 Redis 提交顺序，不按请求开始顺序 | 由 save 开关决定 |
| command / regex，load=false | 不读完整会话，使用原有 `context_mode` 上下文构建 | save=true 时创建**新 DAG**，不续接最近节点 |

例如，先用 agent A 得到回答 M，再由 agent B 的实际 reply 触发回复 M：B 接着 M 的祖先历史回答，并在 M 下建立新分支。另一位成员再次回复 M 会建立另一条分支，不会读到 B 分支后续的对话。若回复 M 的消息先匹配了 command 或 regex，则按非 reply 开关处理，而不是自动选择 M。

reply 查不到目标节点时**不改查最近节点**。无节点、祖先链损坏、文件读失败、Redis 失败、版本不支持或无法完整验证时，整体回退现有 `context_mode` 构建，不使用半条历史。没有成功完整加载就没有父节点；需要保存时只能以实际回退上下文创建新 DAG 的根节点。回退自身失败时沿用既有错误行为，“session 失败可回退”不承诺 Redis 整体故障时仍可成功回答或成功归档。

成功恢复时只回放完整对话历史，再追加一次本轮输入，不重复叠加旧 raw turns、summary、无关群历史或 Telegram 回复链。即便同 agent，也重新构建**当前 agent** 的 system / stable prefix、当前 memory、模板附加指令及工具权限；跨 agent 回复同样如此。旧 system、memory 等 frame 可归档供审计，但不作为下一轮提示 replay。历史工具调用只是数据，不重执行，不恢复旧技能、Runtime 环境或权限，也不消耗本轮工具预算。

## 保存内容与调用边界

归档保存完整成功的**顶层模型轮次**，包括 user、assistant tool calls、匹配的 tool responses、最终 assistant、推理和多模态字段；不是 Telegram 可见文本问答对。每个成功轮次写入独立的**增量 JSONL** 文件，后续节点不重复保存整条祖先链；根节点保留实际起始上下文。恢复时沿父链重建历史，单个增量文件不等于完整可恢复会话。

只有完整模型轮次且最终回答实际发送到 Telegram 成功、具有有效消息 ID 时才保存并发布节点。流式占位消息或中途更新成功不代表最终交付成功。半截流、错误提示、未完成工具链、取消或最终发送失败不得成为可回复节点。归档失败不重新运行模型、不重复发送回答，也不阻止旧兼容保存路径。

delegate/subagent **不接入交互 session**。子调用内部不加载或保存 DAG、不建立 session 租约，也不继承父会话的顶层捕获器；其私有内层 history 不进入顶层归档。顶层模型发出的 delegate 工具请求及其返回结果，仍作为**普通顶层工具消息**随完整成功的顶层轮次保存，不展开子调用内部 history。后台 cron 同样不加载或保存交互 session。

## 闲置 TTL 与每日回收

`agent_v3.session.ttl` 是**整 DAG 的闲置 TTL**，不是逐节点或逐文件年龄，也不是 Redis `EXPIRE`。成功完整加载和成功提交都更新整 DAG 的最后活跃时间；仅请求开始、失败加载或租约续期不算用户活跃。load=true/save=false 的成功读取也会刷新活跃时间。

只有每天在进程本地时区的 **02:00** 才检查过期的 active DAG，并回收整个 DAG，连同文件、节点、消息映射、最近节点记录及生命周期记录。它不使用 `agent_v3.cron.timezone`，也不依赖 cron runner 是否启用；部署者应设置进程/容器的本地 TZ。每日重新计算当地 02:00，不以固定 24h ticker 替代；DST 重复日期至多运行一次，02:00 不存在时使用当天跳时后的首个有效时刻。

TTL 表示回收资格，不保证到第 24h 秒立即物理删除。每日检查可能额外保留不足一天，故障或活动调用可能继续延后。已到期但尚未进入 deleting 的 DAG 可以由成功加载重新激活；有效租约保护慢模型正在使用的父链。已进入 deleting 的 DAG 不再复活，读取回退。DAG 元数据和清理清单不能靠 Redis 自然 TTL 消失；崩溃或部分失败会留下可重试的 intent / deleting 状态。

启动及短周期恢复仅继续 Redis 已登记的 intent / deleting 未完成工作，不提前扫描过期 active DAG。没有通用孤儿文件扫描，陌生文件不会因为年龄或缺少 Redis 索引而自动删除。

## 存储范围与备份

会话 scope 由 **Redis prefix + bot + platform + chat** 决定，**不按 user 隔离**。agent 名称用于非 reply 的最近节点选择，不阻止同一 chat 的跨 agent 回复。群内不同成员可能通过回复 bot 消息继续同一段历史，因此工具结果、媒体和先前成员提供的敏感信息也可能进入其他成员的续聊。

Redis 负责节点和父边、消息到节点的映射、提交顺序以及 DAG 活跃和删除状态。当前仓库以每个 scope 的 state JSON 配合 `WATCH` / `MULTI` 更新这些记录，不是 ZSET 索引方案，也不承诺 ZSET 的性能。具体 key 拼写属于存储实现，不是固定的外部契约；不要直接编辑这些 key 来迁移或删除会话。

备份或迁移要保留整条祖先链与对应 Redis 元数据。Redis 元数据需可靠持久化，避免任意 eviction；**不保证 Redis 丢失后自动从磁盘重建**。文件与 Redis 不是跨系统 ACID，未确认发布状态的文件应保留，待发布状态确认或已登记恢复工作处理。无法确认 Redis 引用状态时，不得扫盘删除“无索引文件”。

## 数据敏感性与部署约束

- JSONL 可能包含群聊内容、工具结果、推理、system frame 和媒体/data URI。默认保存是 true；建议归档文件使用 `0600`，目录只允许 bot 服务账号访问，Windows 使用等效 ACL，并限制备份权限。不要向静态文件服务或远端 Runtime 工具暴露目录，日志不得打印会话全文、令牌、环境秘密或媒体载荷。
- session 开关只控制**完整模型会话**，不关闭现有 `SaveResponse`、raw turns、summary、Telegram 消息缓存、memory 和 trace。DAG 回收不承诺擦除这些独立数据，memory forget 也不保证擦除历史归档。
- 不同实例使用同一 Redis session scope 时，必须挂载**同一共享数据卷（sharedVolume）**，且该卷对所有参与者提供可靠跨进程锁与同目录原子硬链接发布能力。建议将相同的 sharedVolume 挂载到各实例配置的 `directory`，并确认跨主机锁支持。各机器独立本地盘却共用同一个 Redis scope 不满足要求；单进程 mutex 不能代替跨进程锁。
- 文件路径受固定根目录与安全 ID 约束，不能通过路径穿越、symlink / Windows reparse point 逃逸。回收仅按可信清单精确删除；未知文件保留，不递归删除整个 data、bot、chat 或锁目录。
- 关闭保存或回滚不会立即清空目录。迁移 directory、改变 bot 用户名或 Redis key prefix 前要处理旧 namespace 的 DAG/intents；不能仅改路径就假定旧节点仍可恢复。不同 Redis prefix 共卷时仍需独立 storage namespace，防止相互回收。
- 归档保留当时提供的多模态载荷/引用，不新增自动外部下载；外部媒体 URL 失效仍是风险。完整历史可能超过模型上下文容量，不能以静默裁剪或摘要冒充完整恢复。
