# Agent 完整模型会话上下文

完整模型会话让你通过回复 bot 的某条回答，接着那一条回答的模型历史继续对话。历史包含工具调用与结果，而不只是 Telegram 上可见的问答。回复同一条旧回答可以产生不同分支，各分支共享祖先，不混入彼此后续的对话。

`context_mode: chat` / `reply_chain` 决定未加载、未接受或禁用完整会话时的上下文来源。后者从 Telegram 回复链构建可见消息上下文，不等于恢复模型的完整工具会话，详见 [reply-chain 文档](agent_reply_sessions.md)。

## 配置与续聊方式

```yaml
agent_v3:
  session:
    enable: true
    directory: data/agent-sessions
    ttl: "24h"
    context_overflow:
      strategy: rebuild
      max_tokens: 200000
      # patterns: ["maximum context length", "too many tokens"] # 可选；覆盖 provider 400 错误正文的识别正则，省略使用内置列表。
agents:
  - name: example
    session:
      save_context: true
      load_context: false
      context_overflow:
        max_tokens: 32768 # 可选覆盖；省略继承全局值，按自己的模型预留输出与工具增长余量。
    # 保留现有 model、agent、trigger、filters、format 等配置。
```

全局 `agent_v3.session.enable` 缺省为 **true**。显式 false 关闭完整会话的加载、保存、阈值检查及实际 reply 的 save/load 强制覆盖，沿用原 `context_mode`；不会删除已有归档，也不关闭 raw turns、summary、memory 等独立存储。

`enable` 接受 YAML 布尔值；环境变量和 YAML 字符串按 `strconv.ParseBool` 解析，支持 `true` / `false`、`TRUE` / `FALSE`、`True` / `False`、`1` / `0`、`t` / `f`、`T` / `F`，不接受任意大小写混写。YAML 数字（例如未加引号的 `1`）、其他错误类型和显式 `null` 均拒绝；关闭 session 也不会跳过显式配置的合法性校验。

全局 session 启用且可用时，每个 agent 的 `session.save_context` 缺省为 **true**，显式 false 可关闭非 reply 调用的新会话归档；`session.load_context` 缺省为 **false**，独立控制是否加载已保存的完整会话。只有**实际 reply 分支触发**的本次调用强制 save/load 为 **true/true**，即使两项都显式 false，也不会修改共享 agent 配置。

带回复消息的 command 或 regex 仍是非 reply。同一条 trigger 同时配置 command/regex/reply 时，以本次实际选中的分支为准。现有 command 优先、regex 先于 reply、首个匹配项优先及 caption 处理不变；全局白名单、agent filters 和 agent 启用条件仍适用。

**直接回复 bot 自己的消息**（被回复消息的 sender 是本 bot，且在同一 chat）总是续接该条回答的会话，并强制本次 save/load 为 true/true，与本次是由 command、regex 还是 reply 分支选中无关。这个判断按消息形状决定，不改变 `main.go` 的分发顺序；被回复的 bot 消息若不是已归档节点（`ErrMiss`），仍静默回退原 `context_mode`，并以回退上下文保存新 root。回复其他成员的消息、或回复另一 chat 的 bot 消息，不触发这条规则，仍按下表的开关处理。

默认设置适合“每次命令独立提问，回复回答时续聊”。若希望普通命令或 regex 也自动接着该 agent 的上一轮对话，设置 `load_context: true`。per-agent 的这两个开关不能禁止实际 reply 或回复 bot 消息的续聊；全局 `session.enable: false` 可以关闭完整会话，但也不是停止所有聊天数据保存的隐私总开关。

全局 `agent_v3.session.directory` 缺省为 `data/agent-sessions`，相对路径以进程启动工作目录为基准；`agent_v3.session.ttl` 缺省为 `24h`。目录和 TTL 的空字符串按缺省处理。非空 TTL 必须是严格为正的 Go duration 字符串，例如 `"24h"`、`"1h30m"`；不支持 `"1d"`，非法、溢出、零和负值都拒绝，不静默替换成默认值。部署前确认目录可写，并具备下文所述的锁和原子文件操作能力。

未启用 Agent v3、没有实际启用的 v3 agent，或显式关闭 session 时，不创建 session store 或启动它的维护任务。有效配置下，目录、锁探测等 session 存储初始化失败只记录 Warn 并局部降级到原 `context_mode`，不因这一可选能力阻断其他 bot 功能；不保证 Redis 整体故障时原路径仍可用。非法配置仍拒绝，不属于这一降级。

## 如何选择历史

下表适用于全局 session 启用且可用时；加载候选还需通过下文的完整输入接受检查。

| 实际触发 / 开关 | 加载规则 | 保存规则 |
| --- | --- | --- |
| 任意触发，且当前消息直接回复本 bot 的消息 | 只查当前 scope 下被回复的那条 bot 消息；允许同 chat 跨 agent、跨成员分叉 | 强制保存 |
| reply（被回复消息缺少 sender 信息时） | 同上，按被回复消息 ID 精确选择 | 强制保存 |
| command / regex，回复其他成员或无回复，load=true | 同 scope、同 agent 最近**成功提交**节点；最近按 Redis 提交顺序，不按请求开始顺序；该节点之后的一轮提交失败时，它及更早节点退出最近选择（见「保存内容与调用边界」） | 由 save 开关决定 |
| command / regex，回复其他成员或无回复，load=false | 不读完整会话，使用原有 `context_mode` 上下文构建 | save=true 时创建**新 DAG**，不续接最近节点 |

例如，先用 agent A 得到回答 M，再由 agent B 的实际 reply 触发回复 M：B 接着 M 的祖先历史回答，并在 M 下建立新分支。另一位成员再次回复 M 会建立另一条分支，不会读到 B 分支后续的对话。回复 M 的消息即使先匹配了 command 或 regex，只要 M 是本 bot 发出的，同样续接 M；只有回复非 bot 消息时才按非 reply 开关处理。

reply 查不到目标节点时**不改查最近节点**。无节点、祖先链损坏、文件读失败、Redis 失败、版本不支持或无法完整验证时，整体回退现有 `context_mode` 构建，不使用半条历史。没有成功完整加载就没有父节点；需要保存时只能以实际回退上下文创建新 DAG 的根节点。回退自身失败时沿用既有错误行为，“session 失败可回退”不承诺 Redis 整体故障时仍可成功回答或成功归档。

通过接受检查并成功恢复时只回放完整对话历史，再追加一次本轮输入，不重复叠加旧 raw turns、summary、无关群历史或 Telegram 回复链。**只有 system 消息不回放**：每轮都重新构建**当前 agent** 的 system / stable prefix 与工具权限，跨 agent 回复同样如此。除 system 外的归档消息——包括历史 memory snapshot、`reply_chain` 的模板/时间 addition、首轮 loop 指令和 runtime guidance、工具调用与结果——按原始顺序**原样回放**，保证本轮模型输入的前缀与上一轮最后一次模型输入逐字节一致，让 provider 的 prompt cache 能命中归档前缀。代价是过期的 datetime 文本、旧 memory 文本和旧 guidance 文本会留在历史里；它们是历史证据，不是新指令。本轮把当前 memory 与回放历史中最后一条 snapshot 的正文比较：相同则不追加；不同则追加 `<group_memory_snapshot supersedes="earlier">`，header 声明它取代此前所有 snapshot；memory 已为空而历史里仍有 snapshot 时追加 `<group_memory_snapshot cleared="true">` 标记。追加的消息位于回放之后、本轮输入之前，并归档进本节点，之后同样原样回放（详见 `docs/agent_memory.md`）。

**删除记忆**不走追加路径。每个节点记录提交时的群 memory epoch（节点 JSON 的 `memory_epoch`），`/memory forget` 删除条目时推进 epoch。加载时所选节点的 epoch 与当前不一致即视为未命中：整体回退原 `context_mode`（只含当前 memory snapshot），以新 root 保存，记 Debug 日志，不刷新旧 DAG 的活跃时间，行为与超限 rebuild 相同。记忆删除后，该群所有会话链从下一轮起重建为新根；旧 DAG 按 TTL 回收；首次调用缓存 miss 一次。

历史工具调用只是数据，不重执行，不恢复 Runtime 环境或权限，也不消耗本轮工具预算。唯一的例外是输出格式：若当前 agent 启用了 rich 且回放历史中包含 `load_skill(rich-message)` 的调用，则本轮直接视为 rich 已激活，让续聊保持同一输出格式；这不会恢复技能的环境变量或其他权限。无论是否激活，`<telegram_rich_message>` 标签都不会原样发到 Telegram：未授权或未启用 rich 时发送 envelope 的纯文本回退，解析失败时剥去标签后发送内部文本。

仅在完整会话被接受的路径中补充当前消息的外部直接引用：如果已在所选祖先链中，省略重复引用；不在该链中的引用仍保留正文、链接实体及现有图片能力支持的媒体，不导入它的整条旁支历史。回退或未使用完整会话时，仍尊重原模板作者是否插入 reply 的选择，不强制补引用。

`reply_chain` 的模板/时间 addition 与 memory snapshot 在归档中属于 history（根节点为 Bootstrap 或 Delta，子节点为 Delta），后续轮次原样 replay；只有 system 是 Frame。prompt cache key 为 `csust:<bot>:<chat>:<model>:v<N>`，不含 agent 名。

## 完整输入阈值与一次重建

`agent_v3.session.context_overflow` 随 session 启用。省略该对象或其中字段时，`strategy` 默认 `rebuild`，全局 `max_tokens` 默认 **200000**，此默认值不变。每个 agent 可用 `agents[].session.context_overflow.max_tokens` 覆盖阈值；省略时继承全局值，不提供 per-agent 策略或独立启用开关。当前唯一支持的策略是 `rebuild`，其他策略及显式空策略非法；显式阈值必须是正整数，零、负数、小数、布尔、容器类型、空值及整数溢出都拒绝，环境变量覆盖也必须为正整数字符串。即使 session 被禁用，显式非法配置仍不被忽略。

上例的 **32768** 只是配置示例，不是自动识别的模型窗口。应按自己模型的实际容量设置阈值，并为输出和后续工具结果增长留出余量；模型的输出上限不是上下文上限，不能用 output max 代替 context max。

判断对象是本轮**首次实际模型请求的完整候选输入**，不只已加载的历史：包括当前 system / stable prefix、memory、模板、本轮输入及引用、已加载历史（含推理、工具调用参数及结果）、首轮 loop 指令和 guidance，以及实际绑定的 tool definitions / schema。在确认加载并刷新旧 DAG 活跃时间**之前**估算；只有估算值**严格大于**阈值才因超限拒绝，恰好 200000 不因阈值拒绝。

超限时，整个候选退回现有 `context_mode`，保留原当前消息和回复关系，按原路径构建回退上下文，不使用半条模型历史。最终完整交付且本次允许保存时，以实际回退基线建立**新 root / 新 DAG**，不复连旧 parent；`save=false` 不会因为 rebuild 自动变成 true，实际 reply 原有强制保存规则除外。旧档案和映射不因此改写或删除，旧 DAG 继续按原闲置 TTL 等待每日 GC；超限拒绝不刷新它的最后活跃时间，被接受的成功 load-only 仍刷新。

没有加载候选、session 不可用或处于后台/子 agent 调用时，不为阈值额外加载历史或重开 DAG。回退输入本身也可能超阈值，此时只执行一次既有模式，不递归重建、不新增截断；原模式已有的预算、summary 和错误处理保持不变。“一次”指一次顶层调用，仍允许原有模型重试和多轮工具循环。无法安全估算的未知输入记录 Warn 并整体回退，不能以零 token 当作可接受。

### 估算方法与局限

方法名称为 `text-runs-media-budget-v1`，不使用 provider 的精确 tokenizer，也不是 200k 字节限制：

- 每个模型可见文本字段独立计算：空文本为 **0**；每个最大连续 ASCII run（含空白、标点）按 `ceil(字符数 / 4)` 计；CJK 和其他非 ASCII 字符均按每个 Unicode rune **2** 计。字段边界或非 ASCII rune 会结束 ASCII run；emoji / 组合字符按 rune 而非视觉上的字符计。例如 `abcd中ef` 估算为 `1 + 2 + 1 = 4`。
- 额外封装预算为每条 message **16**、每个实际绑定 tool definition **16**、每个 request **32**。历史 usage / trace 不是历史正文，不把过去的 PromptTokens 逐轮累加。
- 仅对已识别的**结构化媒体 part**使用固定预算：image **4096**、audio **8192**、video **16384**、file **8192**，每个 part 计一次。它们的 URL、data URI、raw base64 或 file ID 不另按传输字符数计；等价传输表示预算相同。独立 caption、转写和描述仍按文本计，不为估算下载、解码媒体或查询 metadata。
- 正文、reasoning、tool arguments / results、可见 JSON 和 schema 中的真实文本正常计数，JSON/schema 的键、标量和结构也按其文本表示计；即使文本包含 URL、base64、`data:image/...` 或 `image_url` 字段，也不从字符串形状猜成结构化媒体。未知或不支持的输入不能安全估算时整体回退；如计数提前封顶，诊断中的数值只是已超过阈值的下界，不是精确总数。

这些规则可能高估或低估 provider 的实际 tokens；固定媒体预算尤其不是任意长度音视频或文档的上界。检查只在本轮首次模型请求前决定一次，不计未来模型输出，也不保证模型/工具生成的新内容不会继续超过 provider 窗口。已经调用模型或工具后不因本阈值回滚重跑，避免重复外部副作用。

已接受加载后，若**本轮首次模型调用**（尚未执行任何工具轮次）失败于 provider context-length 错误，会持久记录针对 **selectedNode + 当前 agent/model identity** 的拒绝标记。下一次在同一标记 key 下，该候选不再被选中或 Confirm；本次不自动重跑模型或工具，首次已接受加载刷新的 LastActive 也不回滚。工具轮次之后才出现的同类错误来自本轮自身增长，不标记父节点，失败的本轮也不会提交或回放。“首次模型调用”由本轮 SessionCapture 记录的模型响应数证明；`save_context: false` + `load_context: true` 的 load-only 轮次同样建立 capture，只用于这一判断，不会因此提交节点。没有 capture 时无法证明，不标记父节点，只记 Debug 日志。标记不改写旧 JSONL 或不可变历史，不屏蔽其他 agent/model，并随整 DAG 回收一并清除。

context-length 错误的识别规则：OpenAI 风格 API 错误（eino-ext 或 go-openai 的 `APIError`）的 `code == "context_length_exceeded"` 直接命中；否则要求 HTTP 400（或数值 code 400）且错误正文匹配正则列表之一。内置列表（大小写不敏感）为 `maximum context length`、`context length`、`context_length`、`input length`、`too many tokens`、`exceeds? the (model'?s )?(maximum )?(context|token)`、`上下文.*(长度|超)`、`超出.*长度`、`Range of input length`，可覆盖 vLLM 风格 `{"object":"error","code":400,"message":"This model's maximum context length is ..."}`（没有 `error` 包装、以 `RequestError` 形式出现）和 DeepSeek 的 `invalid_request_error`。`agent_v3.session.context_overflow.patterns` 可替换该列表，元素必须是合法正则，空列表沿用内置值；一般的 400 参数错误不命中。trace 的 error 字段会记录截断到 300 字符的 provider 原始错误，便于定位，而用户可见错误仍只显示安全文案。**`context_overflow` 本身只提供 rebuild，不截断、不改写旧档案。** 可选的后台压缩是独立功能，见下一节 `session.compact`，默认关闭。

## 后台压缩到新 root（`session.compact`）

`agent_v3.session.compact` 默认关闭。开启后，每次**成功提交**一个节点，会用当前 agent 的估算器（`text-runs-media-budget-v1`，含实际绑定的 tool schema）估算**该节点下一轮回放的输入**：本轮完整模型输入加上本轮新增的工具调用、结果与最终回答。估算值严格大于 `threshold_tokens` 时，向后台队列登记一个压缩任务；回复发送与归档提交都不等待它。

设计前提不变：**绝不改写已有节点或已发送的历史**。压缩只会创建一个**新 root / 新 DAG**，它的首次模型调用是一次性的 prompt cache miss；旧 DAG 原样保留，`LastActive` 不被压缩任务刷新，按闲置 TTL 等待每日回收。

### 任务流程

1. **读取**：以只读方式沿父链读取被压缩节点的完整回放（root 的 Bootstrap 加各节点 Delta），不建立租约、不刷新活跃时间。
2. **切分**：最近 `keep_recent_turns`（默认 2）个节点的 Delta 为「recent」，其余（root Bootstrap 与更早的 Delta）为「old」。old 为空（例如链长不超过保留轮数且 root 没有 Bootstrap）时跳过，记 Debug 日志。
3. **摘要**：用 `compact.model` 调用一次便宜模型；未配置时回退该 agent 的 `format.progress_summary.model`，两者都没有则跳过。提示词要求保留用户原话、结论、全部来源 URL、关键工具结果摘录与当前任务状态，输出不超过 `summary_max_chars`（默认 6000，按 rune 硬截断）。送入摘要模型的转写对每条消息做头尾截断、不包含媒体载荷，整体过长时丢弃最旧消息并标注。
4. **写新 root**：Bootstrap = 一条 `<session_summary>…</session_summary>` user 消息 + 除最后一轮外的 recent Delta 原样；Delta = 最后一轮的 Delta 原样；`Complete=true`，agent 为提交该节点的 agent。走正常的 Reserve / WriteAtomic / Publish 路径，受同样的 codec 校验。
5. **重定向映射**：Publish 事务在同一个 `WATCH`/`MULTI` 中，把被压缩节点的已交付消息 ID 中**仍指向该节点**的那些改指向新 root；已被别的发布改走的 ID 不动。没有任何 ID 仍指向源节点时发布失败（`ErrStale`），按正常补偿清理文件和 intent（Reserve 已建立的空 DAG 留给每日回收）。只有源节点当时就是该 agent 的 latest 时，新 root 才同时成为 latest；否则更新的提交保持优先。新节点元数据带 `redirected_from` 指向源节点，便于诊断。
6. **之后的回复**：回复被压缩的那条 bot 消息会加载新 root，模型输入 = 当前 system + 摘要 + 保留轮次原样 + 本轮输入；回复更早的 bot 消息仍加载旧 DAG 对应节点。摘要之前的旧历史对新分支不可见，只在摘要文本中保留。

任务尚未运行、被跳过或失败时，现有的加载时 `context_overflow` rebuild 仍是兜底。队列有界（16 个任务）、单 worker、同一 DAG 同一时刻只保留一个任务；队列满或同 DAG 已在排队时直接丢弃本次登记（Warn / 无日志），下一次提交会重新估算。worker 随 session service 一起启停，关闭时等待进行中的任务结束；单个任务上限 3 分钟。

成功时记录 Info（`agentv3: session compacted into new root`，含新旧 DAG/节点 ID、重定向的消息 ID、估算 token）；跳过记录 Debug；读取、摘要、写入失败记录 Warn（`agentv3: session compaction failed; load-time rebuild remains the fallback`）。日志不打印会话正文或摘要内容。

### 部署与配置

| 键名 / 路径 | 类型 | 默认值 | 不配置时的行为 | 需要挂载 / 环境变量 / TZ |
| --- | --- | --- | --- | --- |
| `agent_v3.session.compact.enable` | 配置项 | `false` | 不创建 worker，提交后不估算、不登记任务；其余 session 行为不变 | 无 |
| `agent_v3.session.compact.threshold_tokens` | 配置项 | 有效 `context_overflow.max_tokens` 的 60%（全局 200000 时为 120000；agent 覆盖时按该 agent 的值算） | 使用上述比例；显式值必须为正整数 | 无 |
| `agent_v3.session.compact.keep_recent_turns` | 配置项 | `2` | 最近 2 个节点原样保留；显式值必须为正整数 | 无 |
| `agent_v3.session.compact.summary_max_chars` | 配置项 | `6000` | 摘要按 6000 个 rune 截断；显式值必须为正整数 | 无 |
| `agent_v3.session.compact.model` | 配置项（`Model` 对象） | 无 | 回退该 agent 的 `format.progress_summary.model`；都没有则跳过压缩 | 无；模型 API key 与现有 model 配置相同方式提供 |
| `agent_v3.session.context_overflow.patterns` | 配置项（正则列表） | 内置列表（覆盖 vLLM `maximum context length`、DeepSeek、Qwen 等） | 使用内置列表；空列表同样沿用内置值，非法正则启动时拒绝 | 无；迁移无需操作，回滚删除该键即可 |

Redis key：复用 session 的分区布局（新 DAG 的 meta/nodes/intents/leases，scope 的 `messages`/`latest`/`runs`/`sequence`），节点 JSON 新增可选字段 `redirected_from`，压缩 root 的 `memory_epoch` 继承被压缩节点；`compact` 本身没有新增 key 前缀或 TTL。文件：新 root 的 JSONL 写入同一 `session.directory`，受同样的锁与原子发布要求。

每日 GC 新增 Redis key `<prefix>:agentv3:session:{<namespace>}:last_collection`（session 布局的全局区，无 TTL），保存最近一次成功每日回收的当地日期。升级后该 key 不存在，所以升级后**第一次在当地 02:00 之后启动**时会补跑一次完整 session GC：扫描所有 scope 的全部 DAG，每个 scope 受 `CollectTimeout`（默认 2m）限制；失败不写标记，每小时重试一次。此后恢复每日 02:00 节奏。回滚：旧版本忽略该 key，删除它也无副作用（下次新版本启动只会再补跑一次回收）。会话节点 JSON 的 `memory_epoch` 字段与群内 `…:memory:epoch` key 见 `docs/agent_memory.md`，不是本节新增。

时区：不依赖 `TZ`；旧 DAG 的回收仍由每日 02:00 的 session GC 处理。

### 对已有部署的迁移步骤

1. 升级前：无需迁移。旧版本写入的 DAG/JSONL 不变，未配置 `compact` 时代码路径与升级前一致。
2. 升级后：如要启用，先配置便宜模型（`compact.model` 或 agent 的 `progress_summary.model`），再把 `enable` 设为 `true`；观察 Info/Warn 日志确认摘要模型可用。阈值应低于 `context_overflow.max_tokens` 并为后续工具增长留余量。

### 回滚方式

- 回滚到不含本功能的版本后，`compact` 键会作为未知配置键在启动时记 warning，可删除。已生成的压缩 root 是普通 root 节点，旧版本可以正常加载、续接；旧版本忽略 `redirected_from` 字段。被重定向的消息 ID 继续指向压缩 root（旧版本无法再从这些 ID 回到被压缩的旧节点，但旧 DAG 本身仍在，直到 TTL 回收）。不需要清理 Redis 或文件。

## 保存内容与调用边界

归档保存完整成功的**顶层模型轮次**，包括 user、assistant tool calls、匹配的 tool responses、最终 assistant、推理和多模态字段；不是 Telegram 可见文本问答对。每个成功轮次写入独立的**增量 JSONL** 文件，后续节点不重复保存整条祖先链；根节点保留实际起始上下文。恢复时沿父链重建历史，单个增量文件不等于完整可恢复会话。Redis 分区不改变 JSONL 的统一读写路径；`schema.ParamsOneOf`、`schema.ToolInfo` 及 `schema.Message.MultiContent` 等正式 SDK 类型和字段，不是过时的 feature 数据。

只有完整模型轮次且最终回答实际发送到 Telegram 成功、具有有效消息 ID 时才保存并发布节点。流式占位消息或中途更新成功不代表最终交付成功。半截流、错误提示、未完成工具链、取消或最终发送失败不得成为可回复节点。归档失败不重新运行模型、不重复发送回答，也不阻止仍独立运行的 `SaveResponse`、raw-turn 和 summary 保存机制。交付之后的提交、`SaveResponse` 与 raw-turn / summary 保存都使用独立的 10 秒期限（`context.WithoutCancel` 派生），不受此时可能已过期的本轮 context 影响。最终回答只部分交付（见 `docs/agent_delivery.md` §1.3）时，归档在最终回答之后**追加**一条框架写入的 assistant 消息 `<delivery_note>回答共 N 段，仅前 K 段成功发送；用户只看到了前 K 段。</delivery_note>`，不改写已发送的回答；用 assistant 角色是因为归档要求 Delta 以无工具调用的 assistant 消息结尾。raw-turn 回退保存只写入已交付段的原文拼接。`commitAgentV3Session` 返回节点是否发布成功并记录在本轮状态上：只有**加载了父节点且发布成功**的轮次才跳过 raw-turn / summary 的回退保存；父节点已加载但发布失败（Redis、文件或 10 秒超时）的轮次仍写入回退上下文。若该父节点是按 `load_context: true` 的最近节点选中的，发布失败后还会把它、同 agent 更早的节点，以及后台压缩期间从它发布、`redirected_from` 指向它的压缩 root 移出最近节点索引（节点和消息映射不变，回复这些回答仍能续聊；真正更新的其他提交保留），使下一次非 reply 调用未命中、走包含这一轮 raw turn 的回退上下文并保存新 root；否则下一次又会加载同一父节点、跳过 raw turns，这一轮就永远到不了模型。

delegate/subagent **不接入交互 session**。任何工具启动的嵌套 agent 调用都不继承顶层捕获器，不以工具名称判断；子调用内部不加载或保存 DAG、不建立 session 租约，其私有内层 history 不进入顶层归档。顶层模型发出的工具请求及其返回结果，仍作为**普通顶层工具消息**随完整成功的顶层轮次保存，不展开子调用内部 history。后台 cron 同样不加载或保存交互 session。

Load 的存储阶段使用有界短 timeout；准备回调遵循 caller / Service lifetime 的 deadline，不共用存储阶段已消耗的 10 秒预算。整个 prepare 期间持有 pin 保护，完成后重新 fresh confirm；`save=false` 也保留 pin 和成功加载续活。租约续期遇到瞬时错误（Redis 抖动、锁等待超时）不会立刻放弃：只要距上次成功续期的时间仍小于 `LeaseDuration - RenewInterval/2`，就在下一个 tick 重试；只有 fence / corrupt / closed 或即将越过租约期限才终止。用于租约丢失时保存新 root 的完整 replay baseline 只在需要保存时生成，并在锁外生成；公开的 load messages 与私有 baseline 保持隔离，不让调用方修改公开消息污染保存基线。真正因租约丢失而以新 root 提交时记录 Warn（含 scope、父 DAG/节点 ID）并累加 `session.ForkedRootCommits()` 计数，便于观测。

媒体读取同样受调用取消控制：相册每个 poll 批量 `MGET`，读取资源在 operation 内复用，不为每个 ID 新建 client。photo 请求复用 bot/proxy transport 并携带请求 context，不为每张 photo 新增固定 10 秒 deadline，也不修改共享 bot/client 或传入 context；日志不打印媒体 URL 或 token。这些约束不代表已测得固定峰值资源数量。

## 闲置 TTL 与每日回收

`agent_v3.session.ttl` 是**整 DAG 的闲置 TTL**，不是逐节点或逐文件年龄，也不是 Redis `EXPIRE`。被接受的成功完整加载和成功提交都更新整 DAG 的最后活跃时间；仅请求开始、失败加载、超限/估算失败拒绝或租约续期不算用户活跃。load=true/save=false 的被接受成功读取也会刷新活跃时间。

每天在进程本地时区的 **02:00** 检查过期的 active DAG，并回收整个 DAG，连同文件、节点、消息映射、最近节点记录及生命周期记录。它不使用 `agent_v3.cron.timezone`，也不依赖 cron runner 是否启用；部署者应设置进程/容器的本地 TZ。每日重新计算当地 02:00，不以固定 24h ticker 替代；DST 重复日期至多运行一次，02:00 不存在时使用当天跳时后的首个有效时刻。成功完成的回收把当地日期写入 Redis 全局区的 last-collection 标记；进程若在当天 02:00 之后启动且标记不是今天（或从未记录），会在启动恢复之后**补跑一次** Collect，避免长期错过每日回收。每分钟的恢复循环仍只处理 pending/deleting，不回收普通 active DAG。Collect 失败时不写标记，并在 1 小时后（不晚于下一个当地 02:00）重试，成功后恢复每日 02:00 节奏。

每个 scope 的回收不再整段持有文件锁：intent 恢复、claim/读取 deleting 清单、以及**每个 DAG 的文件删除**各自独立加锁，期间的 Load / Commit 可以穿插进行。Collect 对每个 scope 使用独立的 `CollectTimeout`（默认 2 分钟），与 10 秒的普通操作超时分开；Recover 仍沿用 10 秒预算。

TTL 表示回收资格，不保证到第 24h 秒立即物理删除。每日检查可能额外保留不足一天，故障或活动调用可能继续延后。已到期但尚未进入 deleting 的 DAG 可以由成功加载重新激活；有效租约保护慢模型正在使用的父链。已进入 deleting 的 DAG 不再复活，读取回退。DAG 元数据和清理清单不能靠 Redis 自然 TTL 消失；崩溃或部分失败会留下可重试的 intent / deleting 状态。

启动及每分钟恢复通过 scope 的 pending/deleting DAG SET 索引，仅扫描 Redis 已登记的未完成工作，不每分钟查询所有普通 active DAG，也不提前回收它们。索引生命周期与 Reserve / Finish / claim 在同一事务中维护；每日回收仍全扫描 DAG 检查闲置 TTL。没有通用孤儿文件扫描，陌生文件不会因为年龄或缺少 Redis 索引而自动删除。

## 存储范围与备份

会话 scope 由 **Redis prefix + bot + platform + chat** 决定，**不按 user 隔离**。agent 名称用于非 reply 的最近节点选择，不阻止同一 chat 的跨 agent 回复。群内不同成员可能通过回复 bot 消息继续同一段历史，因此工具结果、媒体和先前成员提供的敏感信息也可能进入其他成员的续聊。私聊具有自己的 chat scope，不会自动带入群内的完整模型历史。

Redis 按 DAG 分区保存状态：每个 DAG 使用小型 meta JSON，以及独立的 nodes、leases、intents hashes。scope 维护 run/message 索引、提交 sequence、每个 agent 的 latest 有序索引和 DAG 成员集合，全局 catalog 登记 scope。`WATCH` / `MULTI` 协调节点、租约、发布与索引更新，不再把整 scope 的历史集合放进一个 JSON 重写。具体 key 拼写属于存储实现，不是固定的外部契约；不要直接编辑这些 key 来迁移或删除会话。

续租读取目标 DAG 的 meta 与指定 lease 字段，只更新该租约的 deadline，不读取或重写历史节点，载荷不随历史节点数增长，也不刷新 LastActive。known catalog 中已有匹配 field 时不重复 `HSET`，仍通过 `WATCH` 保护 scope 归属及 last-DAG / new-root guard。但 `WATCH` 的竞争单位是 **key** 而非 hash field：同 DAG 的 leases hash 仍可能竞争；并发提交写 scope 的 run/message/sequence 等索引 key 也仍可能冲突，首次 scope 注册及最后清理仍可跨 scope 竞争。服务层文件系统 scope 锁的临界区仍串行执行，多 key 分区还带来额外的小 Redis RPC，不能据此宣称整个服务是 O(1) 或没有争用。

完整 Load 一次 `HGETALL` 读取**目标 DAG** 的 nodes，在内存按父边构建选中祖先链，再回放祖先 JSONL；Redis 节点读取和内存占用随目标 DAG 节点数线性增长，文件回放随祖先历史量增长，不是每个祖先单独一次 Redis RPC。恢复索引避免每分钟扫描普通 active DAG，但非空工作/index 载荷、租约、intent 及待清理节点/文件的处理仍是线性成本；每日 GC 仍扫描 DAG 集合，竞争和重试还会增加成本。JSONL 的媒体载荷不等同于 Redis 元数据大小；恢复不是整个服务 O(1)，当前没有新的生产加速倍数证明。

当前 Redis reader 只识别新分区布局，**不读取旧 whole-scope JSON**；没有迁移器、dual-write 或旧 feature reader。布局替换不会自动迁移、清空或删除实际旧 Redis keys/JSONL，也不通过扫盘重建索引；缺少新布局元数据不构成删除旧文件或未知数据的授权。

备份或迁移要保留整条祖先链的 **JSONL + 对应 Redis 元数据**。Redis 元数据需可靠持久化，避免任意 eviction；**不保证 Redis 丢失后自动从磁盘重建**。文件与 Redis 不是跨系统 ACID，未确认发布状态的文件应保留，待发布状态确认或已登记恢复工作处理。无法确认 Redis 引用状态时，不得扫盘删除“无索引文件”。

## 容器目录与时区

[Dockerfile](../Dockerfile) 保留 `tzdata`，工作目录为 `/app`，默认 `TZ=Asia/Shanghai`，默认 session 路径因此是 `/app/data/agent-sessions`。运行时可覆盖 `TZ`；每日 02:00 使用这个进程时区，**不是** `agent_v3.cron.timezone`，后者只控制 cron，两者独立配置。

[docker-compose.yml](../docker-compose.yml) 中 bot 将独立 host 目录 `./agent-sessions` bind 到 `/app/data/agent-sessions`，不共用 Redis 的 `./data:/data`，也不向 Runtime 挂载归档。bot 的环境设置为 `TZ=${BOT_TZ:-Asia/Shanghai}`，可在 Compose 环境或 `.env` 中设置 `BOT_TZ` 覆盖；其他部署方式直接传入容器 `TZ`。

- 首次启动前，在 Compose 文件所在目录预建 `agent-sessions`，将其所有者/ACL 设为实际 bot 服务账号，限制其他账号访问并确认可写。挂载使用 `create_host_path: false`，目录不存在时部署失败，避免隐式创建权限宽松的敏感目录。
- 已有部署先停止所有旧写者，备份原容器/卷中**确切的** `/app/data/agent-sessions` 及一致 Redis 元数据，再将文件迁移到新挂载后启动。新 mount 会遮住容器旧路径，不能因此认定旧档案已丢失或安全创建空档案；本次不自动移动或删除生产数据。自定义 `session.directory` 时必须同步调整挂载 target，默认挂载不会自动跟随自定义路径。
- Dockerfile 的 `VOLUME` 只声明默认目录。未显式指定 host mount / named volume 时通常创建匿名卷；重新创建容器**不保证自动复用同一匿名卷**，更不提供多实例共享或备份。重新部署需显式复用同一 host 路径或 named volume；单机 bind mount 不证明跨主机共享锁可用。
- `.gitignore` 覆盖默认 `data/` 和 Compose 的 `/agent-sessions/`，但不保护已跟踪文件，也不控制 Docker build context。仓库根 `.dockerignore` 已排除 `/data/` 与 `/agent-sessions/`，防止这两个目录进入默认本地构建上下文；自定义归档路径仍须显式加入 `.dockerignore` 及相应 Git 忽略规则。这不是对所有数据或已提交内容的自动保护，归档仍不得提交。

## 数据敏感性与部署约束

- JSONL 可能包含群聊内容、工具结果、推理、system frame 和媒体/data URI。默认保存是 true；建议归档文件使用 `0600`，目录只允许 bot 服务账号访问，Windows 使用等效 ACL，并限制备份权限。不要向静态文件服务或远端 Runtime 工具暴露目录，日志不得打印会话全文、令牌、环境秘密或媒体载荷。
- session 开关只控制**完整模型会话**，不关闭现有 `SaveResponse`、raw turns、summary、Telegram 消息缓存、memory 和 trace。DAG 回收不承诺擦除这些独立数据。memory forget 不擦除历史归档，但会让记录了旧 memory epoch 的会话链不再被续接和回放给 provider；旧归档文件直到 DAG 按 TTL 回收前仍含已删除的记忆正文。
- 不同实例使用同一 Redis session scope 时，必须挂载**同一共享数据卷（sharedVolume）**，且该卷对所有参与者提供可靠跨进程锁与同目录原子硬链接发布能力。建议将相同的 sharedVolume 挂载到各实例配置的 `directory`，并确认跨主机锁支持。各机器独立本地盘却共用同一个 Redis scope 不满足要求；单进程 mutex 不能代替跨进程锁。
- 文件路径受固定根目录与安全 ID 约束，不能通过路径穿越、symlink / Windows reparse point 逃逸。回收仅按可信清单精确删除；未知文件保留，不递归删除整个 data、bot、chat 或锁目录。
- 关闭保存或回滚不会立即清空目录。迁移 directory、改变 bot 用户名或 Redis key prefix 前要处理旧 namespace 的 DAG/intents；不能仅改路径就假定旧节点仍可恢复。不同 Redis prefix 共卷时仍需独立 storage namespace，防止相互回收。
- 归档保留当时提供的多模态载荷/引用，不新增自动外部下载；外部媒体 URL 失效仍是风险。通过近似阈值的完整输入和回退输入仍可能超过 provider 容量，不能把 rebuild 描述成对旧档案的截断或窗口保证。
