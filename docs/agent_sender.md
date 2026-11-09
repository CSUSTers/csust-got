# Agent 当前发送者

群聊里谁在提问，模型需要知道。旧版本中，`chat` 模式只给模型看到上下文消息的发送者（`<MESSAGE_CONTEXT>` 和 `<reply_to_message>` 带 `username`/`showname`），但没有触发本轮的那条消息的发送者，而上下文窗口（`message_context`）恰好不含当前消息。模型只能猜，常常把问题安到窗口里最后发言的人、或被回复的人头上；历史 turns 和滚动摘要也只记 `user`，上一轮的认错会在下一轮回放中被继续沿用。

## 行为

- **动态后缀**：每轮用户消息的 `<dynamic_suffix>` 末尾（紧贴 `</dynamic_suffix>` 之前）自动加一行 `<current_sender id="…" username="…" showname="…" />`（属性做 HTML 转义）。不需要改模板就生效。后台任务（cron）和没有发送者的消息不加。
- **防伪造**：用户可控的文本（正文、模板渲染出的上下文与被回复消息、图片说明、文件名等）里出现的 `<current_sender` / `</current_sender`（不区分大小写）会被改写成 `&lt;current_sender`，所以本轮消息里只有系统生成的那一条是真标签。实现上先放一个每轮随机的占位符，整条消息转义完再替换成真标签，用户无法预测占位符。历史 turn 回放的正文同样会转义。
- **匿名管理员 / 频道身份**：消息带 `sender_chat` 时（匿名管理员、以频道身份发言、关联频道转发），以该 chat 的 `id`/`username`/`title` 作为发送者，而不是 Telegram 填的占位 bot（`GroupAnonymousBot` 等）。
- **模板字段**：`prompt_template` 新增 `{{ .SenderUsername }}` 与 `{{ .SenderShowName }}`，例如 `<USER_INPUT from="{{ .SenderShowName | html }}">`。字段是原始文本，`text/template` 不会自动转义，放进标签属性时务必加 `| html`，否则显示名里的 `"` `>` 能伪造标签。这两个字段属于动态字段：写进 `system_prompt` 会像 `.Input` 一样在构建本轮时报错（`system_prompt references dynamic field`），用户收到该 agent 的 `error_message`。
- **历史 turns**：保存用户轮次时记下发送者标签 `显示名 (@username, id <id>)`（缺项时省略对应部分）。标签始终带 id，有人把显示名改成别人的名字冒充，也会因 id 不同而区分开；标签里的 `[` `]` 和换行会被替换，防止伪造轮次边界。回放时在内容（正文与图片引用合并后）前加 `[标签]: `，只有图片的轮次也带标签，与 `<MESSAGE_CONTEXT>` 的回退格式一致。
- **滚动摘要**：用户行写作 `- user [标签]: …`，图片行写作 `- user [标签] image: …`；assistant 行不变。
- **完整会话**：续聊时回放的是历史轮次的完整模型输入，因此每条历史用户消息各带一个 `<current_sender>`，最新一条用户消息里的才是本轮提问者。建议在 `system_prompt` 里写明（示例见 `config.yaml`）。
- `reply_chain` 模式不在本次范围：它的模板只允许 `DateTime`/`CurrentDateCN`/`BotUsername`（其余字段由 `validateReplySessionTemplate` 拒绝），链上消息自带 `sender_id`。

## 部署与配置

| 键名 / 路径 | 类型 | 默认值 | 不配置时的行为 | 需要挂载 / 环境变量 / TZ |
| --- | --- | --- | --- | --- |
| `agents[].prompt_template` 中的 `.SenderUsername` / `.SenderShowName` | 模板字段 | 无 | 不引用就不渲染；`<current_sender>` 仍会出现在动态后缀里 | 无 |
| `<prefix>:agentv3:<bot>:tg:<chat>:hot:raw_turns` 与 `…:turns` 元素的 `sender` 字段 | Redis 值字段 | 省略（`omitempty`） | 旧轮次没有该字段，按原样回放，不加前缀 | 无 |
| `<prefix>:agentv3:<bot>:tg:<chat>:summary:current` | Redis 值格式 | 无 | 用户行多出 `[标签]` | 无 |
| `agent_v3.session.directory` 下的会话归档 | 文件内容 | 无 | 归档的模型输入里含 `<current_sender>` | 沿用已有挂载，无新增 |

- 无新增配置键、挂载或环境变量；无新增 Redis key，TTL 不变。
- 数据敏感性：发送者的 Telegram id、username、显示名会写入上面两条 Redis 列表（`:turns` 保留的条数多于 `:hot:raw_turns`）、滚动摘要和磁盘上的会话归档。之前这些位置只有消息正文；同样的信息本来也在 `message_full` 消息缓存里。
- 时区：不依赖 `TZ`。

### 对已有部署的迁移步骤

1. 升级前：无需操作。
2. 升级后：可选地在 `prompt_template` 引用发送者字段，并在 `system_prompt` 中说明 `<current_sender>`。
3. prompt cache 可能失效一次：只有带 `sender` 的新轮次进入摘要、摘要内容真的变了时，摘要 version 才递增；若按示例改了 `system_prompt`，stable prefix 会重新生成。属正常现象。
4. 已经认错过人的群：旧轮次和摘要里仍是认错后的回答，会被回放并影响后续几轮。
   - **回退历史路径**（未命中完整会话时用 raw turns + 摘要）：要同时删除该群的 `…:hot:raw_turns`、`…:turns` 和 `…:summary:current`。只删前一个和摘要不够，摘要会从 `…:turns` 重建。删除会丢掉短期多轮上下文，不影响群记忆 memory。
   - **完整会话路径**（`agent_v3.session` 开启并命中续聊）：旧答案来自 `agent_v3.session.directory` 下的会话归档，删上面三个 key 清不掉；会话闲置超过 `agent_v3.session.ttl`（缺省 `24h`）后由每日回收清理，见 `docs/agent_session_context.md`「闲置 TTL 与每日回收」。

### 回滚方式

- 回滚到旧版本后，turn 里多出的 `sender` 字段会被 JSON 反序列化忽略，不影响读取；新一轮的动态后缀不再带 `<current_sender>`。
- 已写入的数据不会被清理：摘要里的 `[标签]` 作为普通文本保留到下次重建；会话归档里升级期间的历史轮次仍带 `<current_sender>`，续聊时会原样回放，直到会话过期或被回收。旧版本不认识这个标签，只当普通文本，不影响运行。
- 若模板引用了 `.SenderUsername` / `.SenderShowName`，回滚前要先删掉，否则旧版本渲染模板会报错（`can't evaluate field`）。
